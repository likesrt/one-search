#!/bin/sh
set -eu

log() {
  printf '%s\n' "$*"
}

# nginx 配置模板与渲染产物。模板里的 __XXX__ 占位符由 render_nginx_config 替换。
# 模板放在 /etc/nginx 下而非 /app，是为了与 nginx 自身的配置就近，便于排错时对照。
NGINX_TEMPLATE="${NGINX_TEMPLATE:-/etc/nginx/nginx.conf.template}"
NGINX_CONF_OUT="${NGINX_CONF_OUT:-/etc/nginx/http.d/default.conf}"
# nginx -t 的包装配置：把渲染结果 include 进一个最小的 http 上下文，
# 这样不依赖运行中的主配置也能校验站点配置的语法。
# 该文件由 Dockerfile 在构建期生成，路径必须与那里保持一致。
NGINX_TEST_WRAPPER="${NGINX_TEST_WRAPPER:-/etc/nginx/nginx-test.conf}"

escape_sql_literal() {
  printf "%s" "$1" | sed "s/'/''/g"
}

escape_sql_ident() {
  printf '"%s"' "$(printf "%s" "$1" | sed 's/"/""/g')"
}

normalize_container_proxy_env() {
  normalize_one_proxy HTTP_PROXY
  normalize_one_proxy HTTPS_PROXY
  normalize_one_proxy ALL_PROXY
  normalize_one_proxy http_proxy
  normalize_one_proxy https_proxy
  normalize_one_proxy all_proxy
}

normalize_one_proxy() {
  name="$1"
  value=$(eval "printf '%s' \"\${$name:-}\"")
  if [ -z "$value" ]; then
    return
  fi
  value=$(printf '%s' "$value" | sed 's#//127\.0\.0\.1:#//host.docker.internal:#; s#//localhost:#//host.docker.internal:#')
  eval "export $name=\"$value\""
}

psql_admin() {
  su-exec postgres psql -U "$POSTGRES_USER" -d postgres -v ON_ERROR_STOP=1 "$@"
}

psql_admin_scalar() {
  su-exec postgres psql -U "$POSTGRES_USER" -d postgres -tAc "$1" | tr -d '[:space:]'
}

cleanup() {
  trap - INT TERM EXIT
  for pid in ${nginx_pid:-} ${backend_pid:-} ${postgres_pid:-}; do
    if [ -n "${pid:-}" ] && kill -0 "$pid" 2>/dev/null; then
      kill "$pid" 2>/dev/null || true
    fi
  done
}

wait_for_backend() {
  attempts=0
  while ! curl --noproxy '*' -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1; do
    if ! kill -0 "$backend_pid" 2>/dev/null; then
      log "backend exited before becoming healthy"
      exit 1
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 120 ]; then
      log "backend did not become healthy in time"
      exit 1
    fi
    sleep 1
  done
}

start_postgres() {
  log "starting postgres"
  su-exec postgres postgres \
    -D "$PGDATA" \
    -p 5432 \
    -c listen_addresses=127.0.0.1 \
    -c logging_collector=off \
    -c log_destination=stderr \
    -c client_min_messages=warning \
    >/proc/1/fd/1 2>&1 &
  postgres_pid=$!

  until pg_isready -U "$POSTGRES_USER" >/dev/null 2>&1; do
    if ! kill -0 "$postgres_pid" 2>/dev/null; then
      log "postgres exited during startup"
      exit 1
    fi
    sleep 1
  done
  log "postgres is ready"
}

ensure_database() {
  app_db_lit=$(escape_sql_literal "$POSTGRES_DB")
  app_pass_lit=$(escape_sql_literal "$POSTGRES_PASSWORD")
  app_db_ident=$(escape_sql_ident "$POSTGRES_DB")
  app_user_ident=$(escape_sql_ident "$POSTGRES_USER")

  log "ensuring database and role"
  psql_admin -c "ALTER ROLE $app_user_ident WITH LOGIN PASSWORD '$app_pass_lit';"

  if [ "$(psql_admin_scalar "SELECT 1 FROM pg_database WHERE datname = '$app_db_lit'")" != "1" ]; then
    psql_admin -c "CREATE DATABASE $app_db_ident OWNER $app_user_ident;"
  else
    psql_admin -c "ALTER DATABASE $app_db_ident OWNER TO $app_user_ident;"
  fi
}

# nginx_value_or_default 校验单个 nginx 配置值，为空或非法时回落到默认值。
#
# 参数：$1 待校验值（可为空）；$2 默认值；$3 变量名（仅用于日志）。
# 返回：合法的配置值，保证非空。
# 为什么必须校验而不是直接透传：这些值会被拼进 nginx.conf，非法值会让 nginx 启动失败；
# 而 nginx 与后端同处一个容器，失败意味着整个容器起不来 —— 代价远大于「退回默认值继续跑」。
nginx_value_or_default() {
  # 形参不能省略实参：set -u 下缺参会直接终止，
  # 而本函数的存在意义正是「容忍缺失」，因此这里显式取空串兜底
  value="${1:-}"
  default="$2"
  name="$3"
  if [ -z "$value" ]; then
    printf '%s' "$default"
    return 0
  fi
  # nginx 合法取值：纯数字（client_max_body_size 的字节数），或数字后跟单个单位字母。
  # 尺寸单位是 k/m/g，时间单位是 s/m/h/d（两者共用 m，故合并成一组字母）。
  # 判定方式是先剥掉可能的单位字母，再要求余下部分全为数字 —— 这样 "5242880"、"5m"、
  # "130s" 通过，而 "5*1024*1024"、"abc"、"5m|s|65s|" 被拒。
  # 必须白名单式校验：这些值会被拼进 nginx 配置，非法值不仅让 nginx 起不来
  # （与后端同容器，等于整个容器起不来），还可能夹带 sed 分隔符破坏模板替换。
  core="$value"
  case "$value" in
    *[kKmMgGsShHdD]) core="${value%?}" ;;
  esac
  case "$core" in
    ''|*[!0-9]*)
      log "invalid $name='$value', falling back to $default"
      printf '%s' "$default"
      ;;
    *)
      printf '%s' "$value"
      ;;
  esac
}

# nginx_proxy_timeout_default 由后端写超时推导 nginx 的反代超时。
#
# 为什么要有这层推导：nginx 的 proxy_read_timeout 约束「后端多久没吐数据就断开」，
# 后端的 SERVER_WRITE_TIMEOUT_MS 约束「它最多花多久写响应」。二者取小值才是调用方
# 实际能等到的上限；若 nginx 更小，后端还没写完就被切断，调用方只会看到 504。
# 因此默认让 nginx 比后端宽 5s，避免边界上互相踩。
#
# 返回：形如 "130s" 的字符串；未显式配置 SERVER_WRITE_TIMEOUT_MS 时返回 65s（出厂值）。
# 无副作用。
nginx_proxy_timeout_default() {
  ms="${SERVER_WRITE_TIMEOUT_MS:-}"
  # 非数字（含空串）时不做推导：说明用户没打算调超时，或值本身有误，
  # 此时保持出厂默认，具体数值由后端自己校验
  case "$ms" in
    ''|*[!0-9]*) printf '65s'; return 0 ;;
  esac
  # 毫秒向上取整到秒，再加 5s 余量
  printf '%ss' "$(( (ms + 999) / 1000 + 5 ))"
}

# nginx_body_size_default 由后端请求体上限推导 nginx 的 client_max_body_size。
#
# 为什么要有这层推导：nginx 在外层，client_max_body_size 先于后端 bodyLimitMiddleware 生效，
# 若前者更小，调大 .env 里的 REQUEST_BODY_LIMIT_BYTES 完全看不到效果（请求先被 nginx 413）。
# 直接沿用同一数值，保证两层口径一致。
#
# 返回：字节数字符串；未显式配置 REQUEST_BODY_LIMIT_BYTES 时返回 1m（出厂值）。
# 无副作用。
nginx_body_size_default() {
  bytes="${REQUEST_BODY_LIMIT_BYTES:-}"
  case "$bytes" in
    ''|*[!0-9]*) printf '1m'; return 0 ;;
  esac
  printf '%s' "$bytes"
}

# render_nginx_config 把 nginx 配置模板渲染成最终配置。
#
# 背景：nginx 自身不读环境变量，而它的体积/超时限制与后端成对生效（见上面两个推导函数）。
# 配置若在构建期就烤进镜像，就会出现「改了 .env 却不生效」。因此在启动 nginx 前
# 做一次占位符替换，让环境变量成为唯一的配置入口。
#
# 每个占位符的取值优先级：显式指定的 NGINX_* 变量 > 从后端配置推导 > 出厂默认。
# 侧面：写出 $NGINX_CONF_OUT；渲染结果经 nginx -t 校验，失败时退回全默认值再渲染一次。
# 副作用：覆盖 $NGINX_CONF_OUT 文件。
render_nginx_config() {
  # 注意：本脚本以 set -eu 运行，未设置的变量直接引用会终止启动，
  # 因此这四项一律用 ${VAR:-} 取「可能为空」的值，再由 nginx_value_or_default 补默认
  body=$(nginx_value_or_default "${CLIENT_MAX_BODY_SIZE:-}" "$(nginx_body_size_default)" "CLIENT_MAX_BODY_SIZE")
  connect=$(nginx_value_or_default "${NGINX_PROXY_CONNECT_TIMEOUT:-}" "65s" "NGINX_PROXY_CONNECT_TIMEOUT")
  read_timeout=$(nginx_value_or_default "${NGINX_PROXY_READ_TIMEOUT:-}" "$(nginx_proxy_timeout_default)" "NGINX_PROXY_READ_TIMEOUT")
  send_timeout=$(nginx_value_or_default "${NGINX_PROXY_SEND_TIMEOUT:-}" "$(nginx_proxy_timeout_default)" "NGINX_PROXY_SEND_TIMEOUT")

  write_nginx_config "$body" "$connect" "$read_timeout" "$send_timeout"
  # 语法自检：模板被改坏或变量含特殊字符时能在这里拦住，
  # 而不是等到 nginx 启动失败、整个容器退出
  if nginx -t -c "$NGINX_TEST_WRAPPER" >/dev/null 2>&1; then
    log "nginx config ready: body=$body connect=$connect read=$read_timeout send=$send_timeout"
    return 0
  fi
  log "nginx config invalid, retrying with factory defaults"
  write_nginx_config "1m" "65s" "65s" "65s"
  if ! nginx -t -c "$NGINX_TEST_WRAPPER" >/dev/null 2>&1; then
    log "nginx config still invalid after fallback; aborting"
    exit 1
  fi
  log "nginx config ready: factory defaults"
}

# write_nginx_config 按给定四个值渲染模板并写出最终配置。
#
# 参数：$1 体积上限；$2 建连超时；$3 读超时；$4 写超时。
# 副作用：覆盖 $NGINX_CONF_OUT。分隔符用 | 而非 /，因为值里可能含斜杠。
write_nginx_config() {
  sed -e "s|__CLIENT_MAX_BODY_SIZE__|$1|" \
      -e "s|__PROXY_CONNECT_TIMEOUT__|$2|" \
      -e "s|__PROXY_READ_TIMEOUT__|$3|" \
      -e "s|__PROXY_SEND_TIMEOUT__|$4|" \
      "$NGINX_TEMPLATE" > "$NGINX_CONF_OUT"
}

start_backend() {
  export APP_ENV="${APP_ENV:-production}"
  export HTTP_ADDR="${HTTP_ADDR:-:8080}"
  export MIGRATIONS_DIR="${MIGRATIONS_DIR:-/app/backend/migrations}"
  export RUN_MIGRATIONS="${RUN_MIGRATIONS:-true}"
  export ADMIN_USERNAME="${ADMIN_USERNAME:-admin}"
  export ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
  : "${ENCRYPTION_KEY:?ENCRYPTION_KEY is required and must be at least 32 characters}"
  export ENCRYPTION_KEY
  export API_AUTH_REQUIRED="${API_AUTH_REQUIRED:-true}"
  export MCP_ENABLED="${MCP_ENABLED:-false}"
  export MCP_PATH="${MCP_PATH:-/mcp}"
  export CORS_ALLOWED_ORIGINS="${CORS_ALLOWED_ORIGINS:-http://localhost:5173,http://localhost:8080}"
  export UPSTREAM_USER_AGENT="${UPSTREAM_USER_AGENT:-OneSearchRelay/0.1}"
  export REQUEST_TIMEOUT_MS="${REQUEST_TIMEOUT_MS:-20000}"
  export DATABASE_URL="postgres://${POSTGRES_USER}:${POSTGRES_PASSWORD}@127.0.0.1:5432/${POSTGRES_DB}?sslmode=disable"

  log "starting backend on ${HTTP_ADDR}"
  /usr/local/bin/one-search &
  backend_pid=$!

  wait_for_backend
  log "backend is healthy"
}

start_nginx() {
  log "starting nginx"
  nginx -g 'daemon off;' &
  nginx_pid=$!
}

main() {
  : "${PGDATA:=/var/lib/postgresql/data}"
  : "${POSTGRES_DB:=one_search}"
  : "${POSTGRES_USER:=one_search}"
  : "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"

  trap 'cleanup; exit 0' INT TERM
  trap cleanup EXIT

  mkdir -p "$PGDATA" /run/postgresql
  chown -R postgres:postgres "$PGDATA" /run/postgresql

  if [ ! -s "$PGDATA/PG_VERSION" ]; then
    log "initializing postgres data directory"
    pwfile=$(mktemp)
    printf '%s\n' "$POSTGRES_PASSWORD" > "$pwfile"
    chown postgres:postgres "$pwfile"
    su-exec postgres initdb \
      -D "$PGDATA" \
      --username="$POSTGRES_USER" \
      --pwfile="$pwfile" \
      --auth-local=trust \
      --auth-host=scram-sha-256 \
      >/proc/1/fd/1 2>&1
    rm -f "$pwfile"
  fi

  normalize_container_proxy_env

  start_postgres
  ensure_database
  start_backend
  render_nginx_config
  start_nginx

  log "all-in-one stack is ready"

  while true; do
    if ! kill -0 "$postgres_pid" 2>/dev/null; then
      log "postgres stopped unexpectedly"
      exit 1
    fi
    if ! kill -0 "$backend_pid" 2>/dev/null; then
      log "backend stopped unexpectedly"
      exit 1
    fi
    if ! kill -0 "$nginx_pid" 2>/dev/null; then
      log "nginx stopped unexpectedly"
      exit 1
    fi
    sleep 5
  done
}

main "$@"
