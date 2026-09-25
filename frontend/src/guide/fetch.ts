/**
 * 章节：网页抓取
 *
 * 覆盖范围：管理台「网页抓取」页的用法、`/v1/fetch` 的参数与状态码语义、
 * MCP 的 `fetch` 工具、安全边界（SSRF 防护与代理策略）与已知能力边界。
 *
 * 代码依据：
 * - `backend/internal/fetch/`（抓取执行体：参数校验、SSRF 拨号护栏、渲染、截断）
 * - `backend/internal/api/fetch.go`（REST 与管理台端点、状态码映射、日志与审计）
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
  "content": "# Example Domain\\n\\n...",
  "truncated": true,
  "next_start_index": 3000,
  "total_length": 8421
}`
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
            ['`timeout_ms`', '`30000`', '单次抓取总超时（含重定向链），上界 `60000`']
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
            '代理主机名（如 `host.docker.internal`）在服务端启动后首次构建抓取客户端时解析一次；解析失败按「无代理」处理并记日志——宁可直连失败，也不放开一个不确定的放行名单。'
          ]
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
            '截断按 Unicode 码点进行，中文与 emoji 不会被切成两半。'
          ]
        },
        { type: 'heading', text: '能力边界', level: 4 },
        {
          type: 'list',
          items: [
            '不执行 JavaScript：纯客户端渲染（CSR）的 SPA 抓回来内容极短或为空；被 Cloudflare 等主动质询页拦截的站点只会拿到质询页本身。',
            '不保存 Cookie：需要完成登录流程的页面无法访问。显式通过 `headers` 传凭据可用。',
            '不解析 `robots.txt`：合规由使用者自行保证。',
            '不做无头浏览器：要渲染 JS 需内嵌 Chromium，体积与资源占用是另一个量级，不在本项目范围内。',
            '不缓存：每次请求都真实发起网络抓取，高频抓取同一地址会在对端产生真实流量。',
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
