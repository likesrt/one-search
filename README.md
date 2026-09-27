# One Search

自托管 Web Search API 中转 / 聚合网关。

统一接入 Exa、You.com、Jina、Tavily、Firecrawl、Serper、Brave、Keenable，以及文档检索渠道 **Context7**（检索开源库/框架的权威文档与可运行代码示例，定位与其它通用网页搜索渠道不同，**不在默认渠道列表内**，需显式指定 `providers: ["context7"]`），提供：

- 统一搜索接口 `POST /v1/search`（`parallel` / `fallback` / `single`）
- 网页抓取接口 `GET|POST /v1/fetch`（HTML 转 Markdown、按字符数截断、可续读）
- Tavily / Serper / OpenAI 兼容接口
- Web 管理台：Provider、Key、Token、调试、网页抓取、日志、用量、审计
- 可选 MCP（`search` + `fetch` 工具）

预览图
<p>
  <img src="./docs/images/搜索调试.png" alt="搜索调试" width="132" />
  <img src="./docs/images/仪表盘.png" alt="仪表盘" width="120" />
</p>

## 快速部署

需要 Docker 24+ / Compose v2，以及至少一个上游搜索 API Key。

```bash
git clone https://github.com/CncCbz/one-search.git
cd one-search
cp .env.example .env
```

编辑 `.env`，填写标「必填」的三项（其余项都有出厂默认值，可按注释调整）：

```dotenv
POSTGRES_PASSWORD=强密码
ADMIN_PASSWORD=管理员密码
ENCRYPTION_KEY=至少32字符   # openssl rand -base64 32
```

```bash
docker compose up --build -d
curl http://localhost:5173/healthz
```

打开 <http://localhost:5173>，用管理员账号登录。端口由 `.env` 的 `HOST_PORT` 决定
（容器内固定 80）。

## 首次配置

1. **平台管理**：启用要用的 Provider  
2. **Key 管理**：添加上游 API Key  
3. **搜索调试**：验证可用性  
4. **API 令牌**：创建业务用的 `osr_...` Token  

## 使用

```bash
curl -X POST http://localhost:5173/v1/search \
  -H "Authorization: Bearer osr_你的令牌" \
  -H "Content-Type: application/json" \
  -d '{
    "query": "golang web search",
    "mode": "fallback",
    "limit": 5,
    "providers": ["brave", "tavily"]
  }'
```

也可用 `X-API-Key: osr_xxx`。

| 路径 | 说明 |
| --- | --- |
| `/` | 管理台 |
| `/healthz` | 健康检查 |
| `/v1/search` | 统一搜索 |
| `/v1/fetch` | 网页抓取（`GET` 调试用 / `POST` 完整能力，含 Tavily 回退与本地缓存） |
| `/v1/compat/tavily/search` | Tavily 兼容 |
| `/v1/compat/serper/search` | Serper 兼容 |
| `/v1/compat/openai/responses-search` | OpenAI 兼容 |
| `/mcp` | MCP（默认开启） |

完整接口见 [docs/admin-api-key.md](docs/admin-api-key.md)、[docs/mcp.md](docs/mcp.md)。

## MCP 配置

`.env.example` 默认 `MCP_ENABLED=true`，端点：

```text
http://localhost:5173/mcp
```

先在管理台创建 `osr_...` Token（`API_AUTH_REQUIRED=true` 时必须）。自检：

```bash
curl http://localhost:5173/mcp
# 应返回 enabled:true、tools:["search","fetch"]（抓取功能在管理台关闭时只有 ["search"]）
```

### Codex

```bash
export ONE_SEARCH_API_TOKEN=osr_xxx
```

写入 `~/.codex/config.toml`：

```toml
[mcp_servers.one_search]
url = "http://localhost:5173/mcp"
bearer_token_env_var = "ONE_SEARCH_API_TOKEN"
enabled = true
tool_timeout_sec = 60
enabled_tools = ["search", "fetch"]
```

启动 Codex 后输入 `/mcp`，应能看到 `one_search` / `search` 与 `one_search` / `fetch`。
`fetch` 需要抓取功能处于启用状态（管理台「网页抓取」页，默认启用）。

### Claude Desktop / 通用 HTTP MCP

多数客户端填：

```json
{
  "url": "http://localhost:5173/mcp",
  "headers": {
    "Authorization": "Bearer osr_xxx"
  }
}
```

更多参数、错误码、排错见 [docs/mcp.md](docs/mcp.md)。

## 配置说明

所有环境变量都在 `.env` 里，每一项的默认值与含义直接写在
[`.env.example`](.env.example) 的注释中（compose 只是把这个文件整体读进容器，
不再持有第二份默认值）。

部署时至少要改的是这三项：

| 变量 | 说明 |
| --- | --- |
| `POSTGRES_PASSWORD` | **必填**，数据库密码 |
| `ADMIN_PASSWORD` | **必填**，首次启动创建的管理员密码 |
| `ENCRYPTION_KEY` | **必填**，≥32 字符，加解密上游 Key 与凭据明文 |

最常改的还有：

| 变量 | 默认 | 说明 |
| --- | --- | --- |
| `HOST_PORT` | `5173` | 宿主机端口，容器内固定 80 |
| `ADMIN_USERNAME` | `admin` | 首次管理员用户名 |
| `API_AUTH_REQUIRED` | `true` | `/v1/*`、MCP 是否强制 Token |
| `MCP_ENABLED` | `true` | 是否开启 MCP |
| `MCP_PATH` | `/mcp` | MCP 路径 |
| `CORS_ALLOWED_ORIGINS` | `http://localhost:5173,http://localhost:8080` | CORS 白名单 |

其余运行参数（`APP_ENV`、`RUN_MIGRATIONS`、`REQUEST_TIMEOUT_MS`、
`SERVER_*_TIMEOUT_MS`、`ADMIN_SESSION_TTL_HOURS`、`ADMIN_LOGIN_*`、
`FETCH_CACHE_DIR` 等）同样列在 `.env.example` 里，一般不用改。

注意：`HTTP_ADDR` 与 `DATABASE_URL` 由容器入口脚本自行设置（后端监听 `:8080`、
连接容器内的 PostgreSQL），不需要也不应该写在 `.env` 里。

容器内 nginx 的站点配置由 `deploy/nginx.conf.template` 渲染而来：入口脚本在启动
nginx 之前把 `__XXX__` 占位符替换成环境变量值，所以 nginx 的体积与超时上限同样
通过 `.env` 控制，不需要重建镜像。默认情况下（不设任何 `NGINX_*`）它会自动跟随
后端取值，只在需要让两层不一致时才要显式设置，详见 `.env.example`。

公网请在前面加 HTTPS 反代，转发 `/`、`/api/`、`/v1/`、`/healthz`（以及 `/mcp`）。

网页抓取的配置不在 `.env` 里，而是管理台「网页抓取」页的运行期配置（存 `settings` 表，
key `fetch`）：

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `enabled` | `true` | 关闭后 `/v1/fetch` 返回 404 且 MCP 不再列出 `fetch` |
| `proxy_url` | 空 | 空 = 直连；容器部署时 `127.0.0.1` / `localhost` 会自动改写为 `host.docker.internal` |
| `allow_private` | `false` | 放行内网目标，仅限可信内网；开启等于关闭 SSRF 防护。**与 `fallback_enabled` 同时打开会把内网地址外发给 Tavily** |
| `timeout_ms` | `30000` | 单次抓取总超时，上界 `60000` |
| `fallback_enabled` | `false` | 开启后内置抓取失败/被拦截/内容过少时改用 Tavily extract，**按量消耗第三方额度** |
| `fallback_min_chars` | `80` | 可见文本低于此值触发回退（Markdown 图片与链接目标不计入） |
| `cache_ttl_seconds` | `120` | 正常内容的缓存时长，`0` = 关闭缓存 |
| `cache_error_ttl_seconds` | `120` | 上游 `401/403/429` 时的缓存时长 |
| `cache_max_bytes` | `3145728` | 单条缓存上限（3MiB），超过则不缓存该条 |
| `cache_max_total_bytes` | `268435456` | 缓存目录总量上限（256MiB），超出按 mtime 删旧 |
| `max_concurrency` | `32` | 单进程同时在飞的抓取上限，超出的排队等待 |

**回退**（`fallback_enabled`）只在以下三种情况触发：内置抓取传输层失败、
上游返回 `401/403/429`、正文可见文本少于阈值。`404` 与全部 `5xx` **不触发** ——
那是对端服务端问题，重试无意义也不该消耗第三方额度。四道闸门会拦住不该外发的请求：
被 SSRF 护栏拦截的内网目标、非 `GET` 或带 body 的请求、带自定义 `headers` 的请求
（可能是 Authorization / Cookie）、以及 `raw=true`（契约是原样返回源文本，
Markdown 无法替代）。**回退失败不会把请求变成 502**，而是返回内置抓取的结果。
回退走 Tavily 自己的 key 级与渠道级代理，抓取功能的全局 `proxy_url` 不作用于它。

> **注意**：回退的「内网目标不外发」这道闸门依赖 SSRF 护栏的拦截信号。一旦
> `allow_private` 打开，护栏不再产生该信号，此时若 `fallback_enabled` 也开着，
> 内网 URL 与页面正文会被送到 Tavily。两个开关不要同时打开。

**缓存**落在容器内 `/app/data/fetch-cache`（可用环境变量 `FETCH_CACHE_DIR` 覆盖），
不跨容器重启保留。缓存键含 `url | method | body | headers | raw`，
因此带 body 的 POST 与带认证头的请求不会互相污染。命中的请求不再打上游，
`start_index` 续读也从同一份缓存内容本地切片。清理由日志保留任务每小时执行一次：
先删过期条目，再按文件修改时间从旧到新删到总量上限以下。

## 本地开发

```bash
# DB
docker run -d --name one-search-postgres \
  -e POSTGRES_DB=one_search -e POSTGRES_USER=one_search -e POSTGRES_PASSWORD=one_search \
  -p 15432:5432 postgres:16-alpine

# 后端
cd backend
export APP_ENV=development HTTP_ADDR=:18080 \
  DATABASE_URL='postgres://one_search:one_search@localhost:15432/one_search?sslmode=disable' \
  ADMIN_PASSWORD=admin123456 \
  ENCRYPTION_KEY=local-test-encryption-key-for-runtime \
  RUN_MIGRATIONS=true MIGRATIONS_DIR=migrations
go run ./cmd/server

# 前端
cd frontend && npm install && npm run dev
# 分离运行时可设 VITE_API_BASE=http://localhost:18080
```

## 目录

```text
backend/     Go API + migrations
frontend/    Vue 管理台
deploy/      all-in-one 入口 + nginx
docs/        接口文档
```

## License

[Apache License 2.0](LICENSE) © 2026 CncCbz

## 致谢

感谢 [LINUX DO](https://linux.do) 社区。
