# AGENTS.md

本文件面向在本仓库工作的编码代理。动手前先读这里，再读目标包的文件头注释——本项目习惯把
「为什么这样设计、为什么不能改」写在包/函数注释里，很多约束只有在那里才说清楚。

## 1. 项目概览

One Search 是自托管的 Web Search API 聚合网关：

- Go 后端（chi + pgx，无 ORM），Vue 3 管理台（Element Plus + Pinia + ECharts）。
- 统一搜索 `POST /v1/search`（`parallel` / `fallback` / `single`）、网页抓取 `/v1/fetch`、
  Tavily / Serper / OpenAI 兼容接口、MCP（`search` + `fetch`）与管理台。
- 一体化镜像：nginx + PostgreSQL + Go 后端跑在同一个容器里，入口脚本负责建库、迁移、
  渲染 nginx 配置并启动各进程。
- 仓库文档、代码注释与提交信息以中文为主。

## 2. 目录地图

| 路径 | 职责 |
| --- | --- |
| `backend/cmd/server/main.go` | 全部装配：配置 → DB → 迁移 → 管理员 → 渠道注册表 → 编排器 → 路由 → 优雅退出 |
| `backend/internal/api/` | chi 路由、中间件、`/v1`、`/api/admin`、MCP（JSON-RPC）、fetch 端点、鉴权与配额 |
| `backend/internal/search/orchestrator.go` | 搜索主流程：默认值、路由、Key 重试、缓存、singleflight、去重、日志 |
| `backend/internal/provider/` | 9 个上游适配器（exa/you/jina/tavily/firecrawl/serper/brave/keenable/context7）与注册表 |
| `backend/internal/keypool/` | Key 选择策略、权重、并发闸门、冷却与用量统计 |
| `backend/internal/fetch/` | 抓取执行体：SSRF 护栏、HTML→Markdown、截断续读、缓存、Tavily 回退 |
| `backend/internal/db/` | pgx 数据访问（`store.go`，手写 SQL）与迁移执行（`migrations.go`） |
| `backend/migrations/` | 顺序编号 SQL，**每次启动全量重跑**，必须永久幂等 |
| `backend/internal/model/` | 请求/响应结构、渠道常量与默认顺序、运行期设置 |
| `backend/internal/{config,security,logging,billing,compat}/` | 配置、AES-GCM 加密、日志、计费、兼容格式映射 |
| `frontend/src/views/` | 管理台页面（`<script setup lang="ts">`） |
| `frontend/src/guide/` | 管理台「使用文档」页的结构化内容，每个章节一个模块 |
| `frontend/src/api/client.ts`, `stores/session.ts` | 前端 API 封装、管理员会话 |
| `deploy/` | all-in-one 入口脚本与 nginx 配置模板（`__XXX__` 运行时占位符） |
| `docs/` | 对外接口文档；整个目录被 gitignore，只有 `admin-api-key.md`、`mcp.md`、`images/` 已入库 |

## 3. 常用命令

```bash
# 后端（backend/）
go test ./...          # 纯单元测试，不依赖数据库
go vet ./...
go build -o /tmp/one-search ./cmd/server   # 别省略 -o：会在 backend/ 留下未忽略的 server 二进制

# 前端（frontend/）
npm install
npm run dev            # Vite，5173；/api 与 /v1 代理到 localhost:8080
npm run build          # vue-tsc --noEmit && vite build，类型检查的硬门槛

# 容器（仓库根）
docker compose up --build -d
curl http://localhost:5173/healthz
```

本地全栈（独立 Postgres、后端 `:18080`、前端）见 README 的「本地开发」。注意 Vite 代理写死
`:8080`；后端若按 README 跑在 `:18080`，需要设 `VITE_API_BASE=http://localhost:18080`。

前端没有配置测试框架或 linter，`npm run build` 的类型检查就是唯一自动门禁；`dist/` 已被忽略，
不要提交构建产物。仓库没有 Makefile，也没有 CI 配置。

## 4. 运行链路（先理解再改）

**搜索**：`api.search` 解析/校验 → `Orchestrator.Search` 读运行期设置与渠道配置 →
补默认值 → 未显式指定 `providers` 时按路由策略选渠道 → 过滤未启用渠道 → 缓存查找 →
singleflight → 逐渠道取 Key（失败按 `key_retry_count` 换 Key 重试）→ 调用适配器 →
归一化/去重/截断 → 写缓存与检索日志 → 响应。

**抓取**：`api.fetch` 鉴权 → 读 `settings` 表的 `fetch` 配置 → 缓存 → 并发闸门 →
带 SSRF 护栏的 HTTP 客户端 → HTML 转 Markdown → 按码点截断 → 可选 Tavily 回退。

**MCP**：`api/mcp.go` 实现 JSON-RPC，工具直接复用搜索编排与抓取执行体；`MCP_ENABLED`
默认在代码里是 `false`，`.env.example` 默认打开。除配置路径外还兼容挂载 `/v1/mcp`。

**管理台**：所有页面除登录外都要求会话；管理员令牌只放 `sessionStorage`（`initialToken`
会主动清掉遗留的 `localStorage` 值），不要把令牌改存到 `localStorage`。

## 5. 必须保持的不变量

### 5.1 渠道增改

新增或删除渠道必须同步以下位置，漏一处就会出现「文档有、MCP 能传、运行时报未注册」：

1. `backend/internal/provider/<name>.go` 适配器；
2. `backend/cmd/server/main.go` 的 `buildProviderRegistry` 工厂注册；
3. `backend/internal/model/search.go` 的 `DefaultProviders`（默认顺序对外可见，**只能追加**，
   `model/search_test.go` 锁定逐位顺序）；
4. `backend/migrations/` 新增一条 `INSERT ... ON CONFLICT (name) DO NOTHING`；
5. `frontend/src/utils/providers.ts` 及需要的管理台展示；
6. README、`docs/admin-api-key.md`、`docs/mcp.md`、`frontend/src/guide/providers.ts`。

默认 `settings.default_providers` 不要被迁移无条件改写：只在用户从未自定义时用「上一版完整
数组」做守卫快照更新（见 `0016_add_keenable.sql`）。`context7` 是文档检索渠道，**不得**并入
默认路由，只在显式 `providers: ["context7"]` 时参与。

### 5.2 数据库迁移

- 没有迁移台账：`db.RunMigrations` 每次启动都会重跑目录下**全部** `.sql`，所以每条语句必须
  永久幂等（`IF NOT EXISTS`、`ON CONFLICT DO NOTHING`、带守卫的 `UPDATE`）。
- 迁移执行器只是按 `;` 做字符串切分。注释与字符串字面量里都不能出现分号，也不能用 `$$ ... $$`
  块，否则会被切成非法语句。已有迁移在文件头重复提醒这一点。
- 只追加新编号文件，不改已发布的迁移；不要删除历史文件。

### 5.3 安全

- 上游 Key、API Token、管理员 API Key 以 AES-GCM 密文落库（`security.Crypto`）。明文只能经
  管理台 reveal 接口返回；日志、错误、审计详情里不得出现明文或 `ENCRYPTION_KEY`。
- 密钥由 `sha256(ENCRYPTION_KEY)` 派生：**更换 ENCRYPTION_KEY 会让既有密文无法解密**，涉及
  数据迁移时必须先说明。
- 后端所有响应默认 `Cache-Control: no-store`（`securityHeadersMiddleware`），不要移除或降级为
  `no-cache`；reveal 类接口尤其依赖它。
- 抓取的 SSRF 判定在 TCP 拨号层（覆盖重定向到内网与 DNS rebinding），不要退化成只校验 URL
  字符串。Tavily 回退的四道闸门（内网目标、非 GET/带 body、自定义 headers、`raw=true`）是
  防止内网数据外发的关键；`allow_private` 与 `fallback_enabled` 不应同时打开。
- 抓取超时硬顶 60s，与 nginx 默认 `proxy_read_timeout` 65s 对齐；放宽它必须同时调整 nginx。

### 5.4 输出契约

- 搜索响应默认 **不返回正文**（`include_content=false` 是有意的破坏性变更），正文可能数万
  字符，不要为“兼容”把默认值改回去。`include_raw` 与 `include_content` 各管一头。
- 长度口径不同，别混：
  - 搜索的 `snippet_limit` / `max_content_length` 按**字节**截断，且只能收紧不能放宽；
  - 抓取的 `max_length` / `start_index` 按 **Unicode 码点**计，不会切碎多字节字符。
- 显式 `limit` 必须优先于渠道的 `request_result_limit`（近期修复），不要在默认值填充时把它
  再次覆盖。
- 修改搜索/抓取参数或行为时，同步更新 `frontend/src/guide/*.ts`（应用内文档）与 `docs/*.md`。
  两者冲突时以代码行为为准，已知差异要在 guide 里标注。

### 5.5 文档与仓库卫生

- `.env`、`data/`、`.claude/`、`docs/` 都在 gitignore 里；`data/` 是 Postgres 数据目录，
  绝不要提交或删除。新增 `docs/` 下的文件若确需入库，得 `git add -f`（`docs/plans/` 是本地
  设计草稿，默认不入库）。
- README 面向部署者，`docs/admin-api-key.md`、`docs/mcp.md` 是对外接口契约，管理台
  「使用文档」页由 `frontend/src/guide/` 驱动——行为变更三处都要考虑。

## 6. 测试与提交

- 测试与源码同目录、表驱动为主；API 测试用 `httptest` + 假 Store/KeyPool，不连数据库。
  改行为要补对应用例，安全相关的拒绝路径（未授权、内网目标、回退闸门）尤其不能只测成功路径。
- 现有代码刻意控制函数长度，过长时拆成有名字的辅助函数（多处注释说明了这一点）。
- 提交信息用 Conventional Commits，scope 可省略，标题与正文用中文，例如
  `fix(search): 显式 limit 优先于渠道 request_result_limit`。一个提交只做一件事。
- 收尾前至少跑：`cd backend && go test ./... && go vet ./...`，以及
  `cd frontend && npm run build`（改了前端时）。

## 7. 容易踩的坑

- **Vite 代理**指向 `:8080`，README 的本地后端却在 `:18080`；不设 `VITE_API_BASE` 会 502。
- **MCP 双路径**：配置的 `MCP_PATH` 之外总会再挂 `/v1/mcp`，改路由时别只改一处。
- **迁移分号**：一句注释里的分号就能让启动失败，报错形态还是难懂的 SQL 语法错误。
- **go.mod 声明 `go 1.18`**：Docker 构建用 Go 1.22，本地可能更新；不要无意引入更高版本才有的
  语法/依赖，也不要随手升级 `go` 指令。
- **前端 CSP**：nginx 模板与后端都下发 `connect-src 'self'`，新增跨域 API 或连接方式时要同时
  改部署模板（开发态 Vite 不走 nginx，行为可能不一致）。
- 仓库里 `frontend/node_modules` 和 `frontend/dist` 可能已存在，不代表依赖或产物是最新的。
