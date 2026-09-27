/**
 * 章节：网页抓取
 *
 * 覆盖范围：管理台「网页抓取」页的用法、`/v1/fetch` 的参数与状态码语义、
 * MCP 的 `fetch` 工具、安全边界（SSRF 防护与代理策略）与已知能力边界。
 *
 * 代码依据：
 * - `backend/internal/fetch/`（抓取执行体：参数校验、SSRF 拨号护栏、渲染、截断、缓存、并发闸门、回退判定）
 * - `backend/internal/provider/tavily.go`（回退通道的 `POST /extract` 适配器）
 * - `backend/internal/search/orchestrator.go`（`TavilyExtract`：回退复用 key 池与渠道代理）
 * - `backend/internal/api/fetch.go`（REST 与管理台端点、状态码映射、日志与审计、回退接线）
 * - `backend/internal/api/mcp.go`（`fetch` 工具的清单过滤与调用分发）
 * - `backend/internal/model/admin.go`（`FetchSettings` 字段与默认值）
 * - `frontend/src/views/FetchView.vue`（管理台页面行为）
 *
 * 命名提示：本工具名叫 `fetch`，与客户端侧常见的浏览器 `fetch failed` 网络报错无关，
 * 见本章「与 `fetch failed` 报错无关」小节。
 */

import type { DocChapter } from './types'

export const fetchChapter: DocChapter = {
  id: 'doc-fetch',
  title: '网页抓取',
  sections: [
    {
      id: 'fetch-what',
      title: '它解决什么问题',
      blocks: [
        {
          type: 'paragraph',
          text: '搜索接口只返回标题、链接与摘要。要让模型读到某个页面的完整内容，得有人把页面取回来并整理成可读文本——`fetch` 就是这一步：抓取指定 URL，把 HTML 转成紧凑 Markdown 以节省 token，JSON 响应则压掉多余空白，最后按字符数截断并支持分段续读。'
        },
        {
          type: 'table',
          columns: ['入口', '形态', '用途'],
          rows: [
            ['`GET /v1/fetch`', '参数走查询串', '调试与简单读取，只支持出站 GET'],
            ['`POST /v1/fetch`', 'JSON 请求体', '完整能力：自定义请求头、请求体、出站 POST'],
            ['MCP `fetch`', '`tools/call`', '给 MCP 客户端（Codex / Claude Desktop / LobeHub 等）使用，参数与 POST 形态一致'],
            ['管理台「网页抓取」', '页面操作', '试抓调试 + 本功能专属配置']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '与搜索完全独立',
          text: '抓取不经过搜索编排：不带渠道、不写搜索日志、不计入搜索用量统计。它复用同一套接口令牌鉴权，因此令牌的 RPM 限制、日/月额度与状态同样生效（`allowed_providers` 不影响抓取）。'
        }
      ]
    },
    {
      id: 'fetch-usage',
      title: '接口用法',
      blocks: [
        { type: 'heading', text: '参数', level: 4 },
        {
          type: 'table',
          columns: ['参数', '类型', '约束'],
          rows: [
            ['`url`', 'string', '**必填**。必须带 `http://` 或 `https://`，裸域名（`example.com`）会被拒绝——不做 scheme 静默补全，因为补错会掩盖笔误'],
            ['`method`', 'string', '`GET`（默认）或 `POST`。仅这两种，抓取不需要 `PUT`/`PATCH`/`DELETE` 的写语义'],
            ['`headers`', 'object', '自定义请求头，同名覆盖默认值（默认 UA 是 Chrome 标识，可在此替换）。仅 POST 形态提供——把认证头放进查询串会留在访问日志与浏览器历史里'],
            ['`body`', 'string 或 JSON 值', '仅 `method=POST` 可用。对象/数组自动序列化成 JSON；一律以 UTF-8 发送，声明了非 UTF-8 的 `charset` 会被拒绝'],
            ['`max_length`', 'integer', '返回内容的最大字符数（按 Unicode 码点计），默认 5000，范围 1–50000'],
            ['`start_index`', 'integer', '续读起点，取自上一次结果末尾的提示。仅 `method=GET` 可用，POST 响应无法续读'],
            ['`raw`', 'boolean', '为 `true` 时跳过 Markdown 转换与 JSON 压缩，返回原始文本']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '代理不是请求参数',
          text: '代理地址是本功能的管理员级全局配置（在「网页抓取」页配置），客户端**无法**指定。若允许请求级代理，任何持令牌的人都可把本服务当作内网跳板。'
        },
        { type: 'heading', text: '调用示例', level: 4 },
        {
          type: 'code',
          lang: 'bash',
          title: '查询串形态（调试用）',
          content: `curl -sS 'http://localhost:5173/v1/fetch?url=https://example.com&max_length=2000' \\
  -H "Authorization: Bearer osr_xxx"`
        },
        {
          type: 'code',
          lang: 'bash',
          title: '请求体形态（完整能力）',
          content: `curl -sS -X POST http://localhost:5173/v1/fetch \\
  -H "Authorization: Bearer osr_xxx" \\
  -H 'Content-Type: application/json' \\
  -d '{
    "url": "https://example.com",
    "max_length": 3000,
    "headers": { "Accept-Language": "zh-CN" }
  }'`
        },
        { type: 'heading', text: '响应结构', level: 4 },
        {
          type: 'code',
          lang: 'json',
          title: '抓取结果',
          content: `{
  "url": "https://example.com",
  "method": "GET",
  "status_code": 200,
  "content_type": "text/html; charset=UTF-8",
  "channel": "direct",
  "content": "# Example Domain\\n\\n...",
  "truncated": true,
  "next_start_index": 3000,
  "total_length": 8421
}`
        },
        {
          type: 'paragraph',
          text: '`channel` 标明内容来自哪条通道：`direct` 是内置抓取，`tavily` 是回退兜底（此时 `status_code` 恒为 `200`、`content_type` 为 `text/markdown`）。'
        },
        {
          type: 'table',
          columns: ['HTTP 状态码', '含义'],
          rows: [
            ['`404`', '抓取功能被关闭（在「网页抓取」页的开关）'],
            ['`400`', '参数非法：缺 `url`、裸域名、`method` 非 GET/POST、`max_length` 越界、在 GET 上带 body 等'],
            ['`502`', '传输层失败：连不上、超时、DNS 失败、TLS 失败，或被 SSRF 防护拦截'],
            ['`200`', '抓到内容。**包括上游返回 4xx/5xx 页面的情况**，此时 `status_code` 保留上游状态码，响应体原样保留']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '「抓到 404 页面」是成功而不是失败',
          text: '自定义 API 常把错误详情放在响应体里（`{"message":"Not Found",...}`），只报状态码无法定位问题，因此这里把「拿到响应」本身视为抓取成功，上游状态码放在 `status_code` 里。客户端必须检查 `status_code`，不能只看 HTTP 是否 200。'
        }
      ]
    },
    {
      id: 'fetch-mcp',
      title: 'MCP 工具',
      blocks: [
        {
          type: 'paragraph',
          text: '项目启用 MCP 后，`fetch` 与 `search` 并存于同一个 `/mcp` 端点。参数与 `POST /v1/fetch` 完全一致，但参数非法、传输失败与功能禁用都通过工具结果的 `isError: true` 表达（而不是 JSON-RPC 错误），这样模型能读到具体原因并自行修正后重试。'
        },
        {
          type: 'code',
          lang: 'json',
          title: 'tools/call 请求',
          content: `{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tools/call",
  "params": {
    "name": "fetch",
    "arguments": {
      "url": "https://example.com",
      "max_length": 2000
    }
  }
}`
        },
        {
          type: 'list',
          items: [
            '`tools/list` 在抓取功能启用时返回 `search` 与 `fetch` 两个工具；关闭时只返回 `search`。',
            '`GET /mcp` 元信息的 `tools` 字段与 `tools/list` 同源，不会一个有一个没有。',
            '关闭抓取后，直接发 `tools/call{name:"fetch"}` 也会被拒绝——工具清单不是调用授权，隐藏与拒绝用的是同一个判定。',
            '抓取工具**不返回 `structuredContent`**（与 `search` 不同）：内容本身就是一段文本，包装成结构化字段没有额外信息。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '与 `fetch failed` 报错无关',
          text: '工具名叫 `fetch`，而 MCP 客户端常报原生 `fetch failed`（浏览器/Node 的 `fetch` 连不上 MCP 端点）。两者没有关系：前者是本服务的一个工具，后者是「客户端到服务端」的连接问题，请查 MCP 配置章节的排错清单。'
        }
      ]
    },
    {
      id: 'fetch-settings',
      title: '全局配置',
      blocks: [
        {
          type: 'paragraph',
          text: '本功能的配置独立于「系统设置」页，存放在 `settings` 表的 `fetch` 键下，在管理台「网页抓取」页修改，保存后对新请求立即生效。'
        },
        {
          type: 'table',
          columns: ['字段', '默认', '说明'],
          rows: [
            ['`enabled`', '`true`', '关闭后 `/v1/fetch` 返回 404，MCP 的工具清单也不再列出 `fetch`'],
            ['`proxy_url`', '空', '空 = 直连。可填 `http://`、`https://`、`socks5://`。容器部署时 `127.0.0.1` / `localhost` 会被自动改写为 `host.docker.internal`'],
            ['`allow_private`', '`false`', '放行内网与环回目标。仅限完全可信的内网部署'],
            ['`timeout_ms`', '`30000`', '单次抓取总超时（含重定向链），上界 `60000`'],
            ['`fallback_enabled`', '`false`', '开启后内置抓取失败时改用 Tavily 取回正文，**按量消耗第三方额度**'],
            ['`fallback_min_chars`', '`80`', '可见文本低于此值才回退。可见文本已剥离 Markdown 图片与链接目标'],
            ['`cache_ttl_seconds`', '`120`', '同一 URL 的缓存时长，`0` = 关闭缓存'],
            ['`cache_error_ttl_seconds`', '`120`', '上游返回 `401/403/429` 时的缓存时长'],
            ['`cache_max_bytes`', '`3145728`', '单条缓存上限（3MiB），超过则不缓存该条'],
            ['`cache_max_total_bytes`', '`268435456`', '缓存目录总量上限（256MiB），超出按修改时间删旧'],
            ['`max_concurrency`', '`32`', '单进程同时在飞的抓取上限，超出的排队等待']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: 'timeout_ms 的上界来自反向代理',
          text: 'all-in-one 部署的 nginx `proxy_read_timeout` 是 65s。把抓取超时配得比这更长，请求会先被反代断开，调用方只会看到 504 而拿不到真实的抓取错误，因此上界锁在 60000。'
        },
        { type: 'heading', text: '代理与内网拦截是解耦的', level: 4 },
        {
          type: 'list',
          items: [
            '代理地址由管理员配置，**视为可信**：拨号层会放行「目标是该代理主机」的连接，因此用本地代理（`127.0.0.1:7890`）不需要关掉 SSRF 防护。',
            '**内网目标仍然被拦截**：`127.0.0.1`、`10.x`、`192.168.x`、`169.254.169.254` 等一律拒绝，除非显式打开 `allow_private`。',
            '代理主机名（如 `host.docker.internal`）在服务端启动后首次构建抓取客户端时解析一次；解析失败按「无代理」处理并记日志——宁可直连失败，也不放开一个不确定的放行名单。',
            '**抓取代理不作用于回退通道**：Tavily 走它自己的 key 级与渠道级代理配置。抓取代理是给「抓取任意 URL」这个危险动作准备的出口，不应顺带改变调用第三方 API 的出站路径。'
          ]
        }
      ]
    },
    {
      id: 'fetch-fallback-cache',
      title: '回退与缓存',
      blocks: [
        {
          type: 'paragraph',
          text: '内置抓取是纯 HTTP 请求加本地 HTML 转 Markdown，不执行 JavaScript。这对付不了两类站点：客户端渲染的 SPA（抓回来是个空壳），以及 Cloudflare 之类的主动质询页（抓回来是「Just a moment...」）。**回退**就是为这两类站点准备的兜底：内置抓取不成时，改用 Tavily 的 `extract` 接口取回整页正文。'
        },
        { type: 'heading', text: '什么时候会触发回退', level: 4 },
        {
          type: 'list',
          items: [
            '**传输层失败**：连不上、超时、DNS 失败、TLS 失败。',
            '**上游返回 `401`、`403` 或 `429`**：门禁页、质询页、限流页。',
            '**正文可见文本少于阈值**（`fallback_min_chars`，默认 80 字符）。这里比较的是**可见文本**而非原始长度：有些质询页原始一千多字符，其中绝大多数是 base64 内联图片，按原始长度看很「健康」，实际正文只有几十字。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '`404` 与全部 `5xx` 不触发回退',
          text: '对端明确说「资源不存在」或「我这边出错了」，重试没有意义，也不该为此消耗第三方额度。代价是：少数用 `404`/`410` 返回空壳的 SPA 站点救不回来。'
        },
        { type: 'heading', text: '四道闸门：这些请求不会外发', level: 4 },
        {
          type: 'list',
          items: [
            '**内网目标**（被 SSRF 防护拦截的地址）：把内网 URL 发给第三方，等于让 SSRF 护栏形同虚设。',
            '**非 `GET` 或带 body 的请求**：`extract` 只能 `GET`，重放 `POST` 可能产生副作用。',
            '**带自定义 `headers` 的请求**：其中可能是 `Authorization` 或 `Cookie`，转发出去等于泄露调用方凭据。',
            '**`raw=true` 的请求**：raw 的契约是「原样返回源文本」，而 Tavily 只出 Markdown，回退会静默改变语义。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '开启回退会消耗按量付费的额度',
          text: '这就是它默认关闭的原因。Tavily 按次计费（basic 档 1–5 个 URL 算 1 个 credit），失败与命中服务端缓存都不计费，所以失败重试不会烧钱。每次成功回退的用量会记进用量表（`unit=credits`），可在仪表盘核对。'
        },
        { type: 'heading', text: '回退失败不会让请求失败', level: 4 },
        {
          type: 'paragraph',
          text: '回退是加分项，不是必要条件。取不到可用的 Tavily 密钥、通道超时、上游报错时，返回的仍是**内置抓取的结果**（哪怕是一张 403 页面），而不是把整个请求变成 502。返回结果里的 `channel` 字段标明内容来自哪条通道：`direct`（内置抓取）或 `tavily`（回退）。回退成功时 `status_code` 为 `200`、`content_type` 为 `text/markdown`。'
        },
        { type: 'heading', text: '缓存：别把目标站点打成限流', level: 4 },
        {
          type: 'paragraph',
          text: '同一 URL 短时间内被反复抓取，既慢又容易把对端打成 `401/403/429`。缓存把结果落在服务端本地文件里，窗口内（`cache_ttl_seconds`，默认 120 秒）的重复请求直接返回上次内容，不再打上游。'
        },
        {
          type: 'list',
          items: [
            '**缓存键含 `url | method | body | headers | raw`**：带不同 body 的 POST、带认证头的请求、以及 `raw=true` 的请求各有各的缓存，不会互相污染。',
            '**续读零请求**：缓存存的是**完整内容**（不是截断后的片段），因此 `start_index` 续读直接从同一份内容切片，`total_length` 与首次完全一致。',
            '**错误态同样缓存**：上游返回 `401/403/429` 的结果用 `cache_error_ttl_seconds` 缓存，避免几分钟内反复撞同一道门禁。',
            '**传输层失败不写缓存**：一次 DNS 抖动或超时若被缓存，会让一个本来正常的 URL 在窗口内持续失败。',
            '**`cache_ttl_seconds = 0` 关闭缓存**：每次请求都真实发起网络抓取。',
            '**容量有上限**：单条超过 `cache_max_bytes` 不缓存；目录总量超过 `cache_max_total_bytes` 时按文件修改时间从旧到新淘汰。清理由服务端每小时的日志保留任务执行，与访问无关。',
            '**不跨容器重启保留**：缓存在容器内 `/app/data/fetch-cache`，重建容器即清空——缓存本就是可以随时丢弃的数据。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '缓存会让内容滞后',
          text: '窗口内页面更新看不到，这是缓存的固有代价。对时效性强的页面，把 `cache_ttl_seconds` 调小或临时设为 0。'
        },
        { type: 'heading', text: '并发上限', level: 4 },
        {
          type: 'paragraph',
          text: '`max_concurrency`（默认 32）限制单进程**同时在飞**的抓取数，超出部分排队等待。它与缓存互补：缓存挡「之后的」重复请求，并发上限挡「同时的」请求，服务端还会按缓存键把同一 URL 的并发首次抓取合并成一次上游请求。注意名额护的是**打向上游的并发**，缓存命中不占名额。'
        }
      ]
    },
    {
      id: 'fetch-limits',
      title: '安全与能力边界',
      blocks: [
        { type: 'heading', text: 'SSRF 防护在拨号层生效', level: 4 },
        {
          type: 'paragraph',
          text: '内网判定放在 TCP 拨号层（`net.Dialer.Control`），而不是只检查 URL 字符串。这样才挡得住两种绕过：一是初始 URL 合法但 30x 重定向到内网，二是 DNS rebinding（域名合法但解析到内网）——这两种情况只有实际连接的 IP 才暴露真实目标。'
        },
        {
          type: 'table',
          columns: ['地址段', '示例', '默认'],
          rows: [
            ['环回', '`127.0.0.0/8`、`::1`', '拦截'],
            ['私有网段', '`10/8`、`172.16/12`、`192.168/16`、`fc00::/7`', '拦截'],
            ['链路本地', '`169.254/16`（含云元数据 `169.254.169.254`）、`fe80::/10`', '拦截'],
            ['其他', '`0.0.0.0`、多播、CGNAT `100.64/10`、`192.0.0.0/24`', '拦截'],
            ['管理员配置的代理主机', '`127.0.0.1:7890`', '放行（仅该主机的 IP）']
          ]
        },
        {
          type: 'callout',
          tone: 'danger',
          title: 'allow_private 的安全含义',
          text: '打开它等于完全关闭内网拦截：任何持有接口令牌的人都能借本服务探测你的内网拓扑与内部接口（SSRF），云环境下还能读到实例元数据。仅在完全可信的内网部署、且确实需要抓取内部服务时开启。'
        },
        { type: 'heading', text: '资源保护', level: 4 },
        {
          type: 'list',
          items: [
            '响应体最多读取 10 MiB，超出部分丢弃——对外服务不能假定对端返回的体积合理。',
            '最多跟随 10 次重定向，避免被超长重定向链拖住连接。',
            '单次抓取有总超时兜底（默认 30s），请求体上限 1 MiB。',
            '截断按 Unicode 码点进行，中文与 emoji 不会被切成两半。',
            '`max_concurrency`（默认 32）限制同时在飞的上游抓取数，超出的请求排队等待；同一个 URL 的并发首次抓取会被合并成一次上游请求。',
            '结果缓存到本地文件（默认 120 秒），减少对同一站点的重复请求；详见「回退与缓存」小节。'
          ]
        },
        { type: 'heading', text: '能力边界', level: 4 },
        {
          type: 'list',
          items: [
            '不执行 JavaScript：纯客户端渲染（CSR）的 SPA 抓回来内容极短或为空；被 Cloudflare 等主动质询页拦截的站点只会拿到质询页本身。开启 Tavily 回退后，这类站点中的多数可由回退通道救回。',
            '不保存 Cookie：需要完成登录流程的页面无法访问。显式通过 `headers` 传凭据可用；但带自定义 `headers` 的请求不会走回退（凭据不得外发给第三方）。',
            '不解析 `robots.txt`：合规由使用者自行保证。',
            '不做无头浏览器：要渲染 JS 需内嵌 Chromium，体积与资源占用是另一个量级，不在本项目范围内。',
            '回退通道只做兜底，不做正文提取：它返回的是**整页 Markdown**（导航与页脚都在），内容质量不如内置抓取，因此只在后者失败时使用，不做交替路由。',
            '非 UTF-8 编码（GBK / Big5 等）页面会显示为乱码：标准库只内置 UTF-8 解码器。这类页面用 `raw=true` 取原始内容自行解码。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '结果为空时先用 raw=true 排查',
          text: '抓回来内容为空不是报错，而是这类站点的正常表现。用 `raw=true` 看服务实际收到了什么：空的 `<div id="app"></div>` 骨架说明是 CSR 站点；出现「Just a moment...」说明撞上了质询页。也可以先试站点的 `/llms.txt`、`/sitemap.xml`、RSS 或 JSON 接口，往往比抓 HTML 更省事。'
        }
      ]
    }
  ]
}
