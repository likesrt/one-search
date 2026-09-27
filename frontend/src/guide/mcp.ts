/**
 * 章节：MCP 配置
 *
 * 覆盖范围：MCP 开关与路径、传输形态与鉴权范围、JSON-RPC 方法表、`search` 与 `fetch` 工具的
 * schema、`GET` 元信息、批量与通知语义、错误码，以及 Codex / mcpServers 形态客户端 /
 * LobeHub 的配置与排错。
 *
 * 代码依据：
 * - `backend/internal/api/mcp.go`（路由挂载、方法分发、工具 schema、错误码、202/405 分支）
 * - `backend/internal/config/config.go`（`MCP_ENABLED`、`MCP_PATH` 默认值）
 * - `backend/internal/api/auth.go`（`requireAPIToken` 与 `allowToken` 的复用）
 * - `docker-compose.yml`、`.env.example`（部署侧开关默认值）
 *
 * 注意：仓库内 `docs/mcp.md` 的部分描述已与代码不一致（通知状态码、transport 名称、
 * 协议版本），本页一律以代码为准。
 */

import type { DocChapter } from './types'

export const mcpChapter: DocChapter = {
  id: 'doc-mcp',
  title: 'MCP 配置',
  sections: [
    {
      id: 'mcp-enable',
      title: '开关、路径与元信息',
      blocks: [
        {
          type: 'table',
          columns: ['环境变量', '代码默认值', '说明'],
          rows: [
            ['`MCP_ENABLED`', '`false`', '关闭时不挂载任何 MCP 路由，访问对应路径会落到前端 SPA 或返回 404'],
            ['`MCP_PATH`', '`/mcp`', '以 `/` 开头；不以 `/` 开头会自动补上，留空回退 `/mcp`']
          ]
        },
        {
          type: 'paragraph',
          text: '`docker-compose.yml` 与 all-in-one 启动脚本都按 `${MCP_ENABLED:-false}` 取值，即代码默认关闭；仓库自带的 `.env.example` 里把它写成了 `true`，因此按文档初始化出来的环境是开启状态。'
        },
        {
          type: 'list',
          items: [
            '开启后会同时挂载配置路径与 `/v1/mcp`（若两者不同），并且都额外接受带尾斜杠的写法。',
            '支持 `GET`（元信息）、`POST`（JSON-RPC）、`DELETE`（固定 405）。',
            '`GET` 的响应体包含 `enabled`、`transport`（固定 `streamable-http`）、`protocol_version`（最新为 `2025-06-18`）、`supported_protocol_versions`、`endpoint`、`auth`、`tools`。',
            '若 `GET` 请求头里 `Accept` 含 `text/event-stream`（即客户端想建立 SSE 长连接），服务端直接返回 **405**，不会建立流。'
          ]
        },
        {
          type: 'code',
          lang: 'bash',
          title: '探测是否启用',
          content: `curl -s http://localhost:5173/mcp`
        },
        {
          type: 'callout',
          tone: 'info',
          title: '为什么有 405',
          text: '本实现是「HTTP 上的 JSON-RPC」：每次调用都是一次普通的 POST，服务端直接返回 `application/json`，不保持长连接。只支持 SSE 长连接的客户端会在这两类请求上失败，需要换用支持 Streamable HTTP 的客户端或加一层适配器。'
        }
      ]
    },
    {
      id: 'mcp-auth',
      title: '鉴权范围',
      blocks: [
        {
          type: 'paragraph',
          text: '鉴权按**方法**而不是按路径生效，判定规则写死在代码里：以下方法**无需令牌**即可调用——`initialize`、`ping`、`tools/list`、`resources/list`、`resources/templates/list`、`prompts/list`、`notifications/initialized`，以及空方法名。除此之外的方法（目前只有 `tools/call`）在「系统设置 → 接口令牌鉴权」打开时必须携带令牌。'
        },
        {
          type: 'table',
          columns: ['请求头', '接受的凭据'],
          rows: [
            ['`Authorization: Bearer <令牌>`', '`osr_` 接口令牌或 `oak_` 管理员 API Key'],
            ['`X-API-Key: <令牌>`', '同上']
          ]
        },
        {
          type: 'list',
          items: [
            '用 `osr_` 令牌时，`allowed_providers` 与 RPM 限制都会生效：请求未授权渠道会在工具调用结果里返回 `-32003`，RPM 超限返回 429。',
            '用 `oak_` 管理员 API Key 时，**不受 `allowed_providers` 与令牌额度约束**，且会以管理员身份记审计。',
            '关闭「接口令牌鉴权」后，`tools/call` 同样不再要求令牌。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '「能列出工具」不代表令牌正确',
          text: '`initialize` 与 `tools/list` 是免鉴权的，所以客户端显示「连接成功、发现 search 工具」只能说明地址对、MCP 已启用。真正调用搜索时才校验令牌，此处的失败会以 `-32001` 或 401 出现。'
        }
      ]
    },
    {
      id: 'mcp-methods',
      title: '支持的 JSON-RPC 方法',
      blocks: [
        {
          type: 'table',
          columns: ['方法', '是否需要令牌', '返回'],
          rows: [
            ['`initialize`', '否', '协议版本、`capabilities`（tools/resources/prompts，`listChanged` 均为 false）、`serverInfo`（`one-search-relay` / `One Search Relay` / `0.1.0`）与一段 `instructions`'],
            ['`ping`', '否', '空对象 `{}`'],
            ['`tools/list`', '否', '`{ tools: [ search, fetch ] }`；抓取功能在管理台关闭时只有 `search`'],
            ['`tools/call`', '**是**', '工具结果对象。按 `params.name` 分发到 `search` 或 `fetch`，见后续小节'],
            ['`resources/list`', '否', '`{ resources: [] }`'],
            ['`resources/templates/list`', '否', '`{ resourceTemplates: [] }`'],
            ['`prompts/list`', '否', '`{ prompts: [] }`'],
            ['其他任意方法', '—', '`-32601` `method not found`']
          ]
        },
        { type: 'heading', text: '协议版本协商', level: 4 },
        {
          type: 'paragraph',
          text: '`initialize` 会在 `params.protocolVersion` 中挑一个服务端支持的版本回显：支持 `2025-06-18`、`2025-03-26`、`2024-11-05`。没传、解析失败或传了不受支持的版本时，统一回退到 `2025-03-26`。所有响应都会带上 `Mcp-Protocol-Version: 2025-06-18` 响应头。'
        },
        { type: 'heading', text: '批量请求与通知', level: 4 },
        {
          type: 'list',
          items: [
            '请求体是 JSON 数组即为批量请求：逐条执行，只把**带 `id` 的**请求结果收集成数组返回。',
            '空数组返回 400 `-32600` `empty batch is not allowed`。',
            '批量里只要有一条需要鉴权的方法，整个请求就要求令牌。',
            '没有 `id` 的请求按通知处理：无论方法是什么都**不执行**，直接返回 **202 Accepted**（不带响应体）。批量请求里若所有条目都是通知，同样返回 202。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '通知的状态码是 202',
          text: 'MCP Streamable HTTP 规范里通知的期望状态码是 202 Accepted；本实现返回的也是 202。若你在别处看到「通知返回 204」的说法，那是过期描述。'
        }
      ]
    },
    {
      id: 'mcp-tool',
      title: 'search 工具',
      blocks: [
        {
          type: 'code',
          lang: 'json',
          title: 'tools/call 请求',
          content: `{
  "jsonrpc": "2.0",
  "id": 3,
  "method": "tools/call",
  "params": {
    "name": "search",
    "arguments": {
      "query": "latest web search APIs",
      "providers": ["exa", "tavily"],
      "mode": "parallel",
      "limit": 5,
      "freshness": "week",
      "dedupe": true,
      "cache": "default",
      "include_raw": false
    }
  }
}`
        },
        {
          type: 'table',
          columns: ['入参', '类型', '约束'],
          rows: [
            ['`query`', 'string', '**必填**，裁剪空白后不能为空，否则 `-32602` `query is required`'],
            ['`providers`', 'array', '枚举**九家**渠道：`exa` / `you` / `jina` / `tavily` / `firecrawl` / `serper` / `brave` / `keenable` / `context7`。注意**枚举九项 ≠ 默认路由九项**：不传 `providers` 时走系统默认平台，出厂仍是前八家通用搜索渠道，`context7` 必须显式指定才会参与'],
            ['`mode`', 'string', '枚举 `parallel` / `fallback` / `single`'],
            ['`limit`', 'integer', 'schema 声明 `minimum: 1`、`maximum: 50`。**建议显式传 15**（`instructions` 与 schema 描述里都写明了这点）：渠道级 `request_result_limit` 大于 0 时会按该值逐渠道取数，传得过小会让已取回并计费的结果被丢弃。（`minimum`/`maximum` 是 schema 层声明，服务端编排仍按自己的规则处理）'],
            ['`freshness`', 'string', '自由文本'],
            ['`dedupe`', 'boolean', '按 URL 去重'],
            ['`cache`', 'string', '枚举 `default` / `bypass` / `refresh`'],
            ['`include_raw`', 'boolean', '结果里是否带 `raw`']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '涉及库/框架文档的问题优先显式指定 context7',
          text: '`context7` 是文档检索渠道，返回的是开源库/框架的权威文档与可运行代码示例，与其余八家通用网页搜索定位不同。`initialize` 返回的 `instructions` 与 `providers` 的 schema 描述都引导模型：问题涉及某个库、框架、SDK 或 API 的用法时**先**显式传 `providers: ["context7"]`，若结果为空（该库未被收录）**再**用默认渠道或其它渠道补充。它只覆盖已收录的开源库文档，对通用网页问题、时事、非库类主题不适用。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: 'schema 里没有 options 和 rerank',
          text: '`search` 工具的 `inputSchema` 只声明了上表八个字段，不含原生接口的 `options`（渠道透传参数）与 `rerank`。即使把 `options` 塞进 `arguments`，虽然会被反序列化进请求结构，但客户端工具校验通常不会允许；需要渠道级透传参数时请直接调用 `/v1/search`。'
        },
        { type: 'heading', text: '工具结果', level: 4 },
        {
          type: 'list',
          items: [
            '成功时返回 `content`（一个 `text` 块，内容是缩进后的完整搜索响应 JSON）、`structuredContent`（同一份响应的结构化对象）与 `isError: false`。',
            '`structuredContent` 的结构与 `/v1/search` 的响应完全一致，因此「全部渠道失败仍返回 200」的性质在这里同样成立：工具会显示为**调用成功**，但 `results` 为空且 `providers[].status` 全为 `error`。',
            '搜索链路本身返回错误时，结果是 `{ content: [text], isError: true }`，**没有** `structuredContent`。',
            '`arguments` 里会额外被注入 `options.source = "mcp"`，因此 MCP 的缓存键与参数相同的原生请求不同，两者不会互相命中。'
          ]
        }
      ]
    },
    {
      id: 'mcp-fetch-tool',
      title: 'fetch 工具',
      blocks: [
        {
          type: 'paragraph',
          text: '第二个工具叫 `fetch`，抓取指定 URL 并把内容整理成文本：HTML 转紧凑 Markdown，JSON 压掉多余空白，最后按字符数截断。它不经过搜索编排（不带渠道、不写搜索日志、不计入搜索用量），但**共用同一套令牌鉴权**，因此令牌的 RPM、日/月额度与状态同样生效。'
        },
        {
          type: 'table',
          columns: ['入参', '类型', '约束'],
          rows: [
            ['`url`', 'string', '**必填**。必须带 `http://` 或 `https://`；裸域名被拒绝'],
            ['`method`', 'string', '`GET`（默认）或 `POST`'],
            ['`headers`', 'object', '自定义请求头，同名覆盖默认 UA'],
            ['`body`', 'string 或 JSON 值', '仅 `method=POST` 可用；对象/数组自动序列化为 JSON'],
            ['`max_length`', 'integer', '默认 5000，范围 1–50000，按 Unicode 码点计'],
            ['`start_index`', 'integer', '续读起点，仅 `method=GET` 可用'],
            ['`raw`', 'boolean', '为 `true` 时跳过转换，返回原始文本']
          ]
        },
        {
          type: 'code',
          lang: 'json',
          title: 'tools/call 请求',
          content: `{
  "jsonrpc": "2.0",
  "id": 4,
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
        { type: 'heading', text: '结果与开关', level: 4 },
        {
          type: 'list',
          items: [
            '成功时返回 `content`（一个 `text` 块，内容是抓取到的文本）与 `isError: false`；**没有** `structuredContent`，因为内容本身就是纯文本。',
            '抓到上游 4xx/5xx 页面仍算调用成功：文本以 `HTTP <状态码>` 开头，随后是响应体。判断成败要看状态码，不能只看 `isError`。',
            '参数非法、传输层失败（连不上/超时/被 SSRF 防护拦截）都返回 `{ content: [text], isError: true }`，文本即原因。',
            '抓取功能在管理台「网页抓取」页关闭时，`tools/list` 不再列出 `fetch`，直接发 `tools/call{name:"fetch"}` 也会被拒绝——工具清单不是调用授权，「隐藏」与「拒绝」用的是同一判定。',
            '代理是管理员级全局配置，**不是**工具参数：`inputSchema` 里没有 `proxy` 字段，传了也不会生效。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '别和 `fetch failed` 混淆',
          text: '下文的排错清单里有一类 `fetch failed` 报错，那是客户端（浏览器或 Node）连不上 MCP 端点时的原生网络错误，与这里讲的 `fetch` 工具无关。两者的排错方向完全不同：工具问题看「网页抓取」章节，连接问题看下方排错清单。'
        }
      ]
    },
    {
      id: 'mcp-clients',
      title: '客户端配置',
      blocks: [
        { type: 'heading', text: 'Codex（~/.codex/config.toml）', level: 4 },
        {
          type: 'code',
          lang: 'bash',
          title: '先在 shell 里导出令牌',
          content: `export ONE_SEARCH_API_TOKEN=osr_xxx`
        },
        {
          type: 'code',
          lang: 'toml',
          title: '推荐：从环境变量读令牌',
          content: `[mcp_servers.one_search]
url = "http://localhost:5173/mcp"
bearer_token_env_var = "ONE_SEARCH_API_TOKEN"
enabled = true
startup_timeout_sec = 10
tool_timeout_sec = 60
enabled_tools = ["search", "fetch"]`
        },
        {
          type: 'code',
          lang: 'toml',
          title: '临时测试：直接写请求头（不要提交到仓库）',
          content: `[mcp_servers.one_search]
url = "http://localhost:5173/mcp"
http_headers = { "X-API-Key" = "osr_xxx" }
enabled = true
tool_timeout_sec = 60`
        },
        {
          type: 'list',
          items: [
            'Codex 用 TOML 的 `[mcp_servers.<名字>]` 表，不是 JSON 的 `mcpServers`；两者混用会导致「配置了但看不到服务」。',
            '`tool_timeout_sec` 建议大于「系统设置 → 请求超时」，否则客户端先超时而服务端仍在跑。',
            '修改后重启 Codex，在 TUI 里输入 `/mcp` 应能看到 `one_search` 与 `search`、`fetch` 两个工具（抓取功能关闭时只有 `search`）。'
          ]
        },
        { type: 'heading', text: 'mcpServers 形态的客户端（Claude Desktop / Cursor 等）', level: 4 },
        {
          type: 'code',
          lang: 'json',
          title: '远程 HTTP 服务的通用写法',
          content: `{
  "mcpServers": {
    "one-search": {
      "url": "http://localhost:5173/mcp",
      "headers": {
        "Authorization": "Bearer osr_xxx"
      }
    }
  }
}`
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '这些客户端对远程 HTTP 的支持并不一致',
          text: '仓库里没有这些客户端的现成配置样例，字段名与是否支持 `url` 形态以各自官方文档为准。部分版本（尤其是较老的 Claude Desktop）只支持 stdio 服务，此时需要额外的 HTTP/Streamable HTTP 转发适配器，或者直接换用 Codex / LobeHub 这类支持远程 HTTP 的客户端。'
        },
        { type: 'heading', text: 'LobeHub / LobeChat', level: 4 },
        {
          type: 'table',
          columns: ['字段', '建议值'],
          rows: [
            ['MCP name', '`one-search`'],
            ['Connection type', '`Streamable HTTP` / `HTTP`'],
            ['Endpoint URL', '`http://<One Search 可访问地址>:5173/mcp`'],
            ['Auth type', '`API Key`（会作为 Bearer Token 发送）'],
            ['API Key', '`osr_xxx` 或 `oak_xxx`']
          ]
        },
        {
          type: 'callout',
          tone: 'danger',
          title: '最常踩的坑：localhost 指向了 LobeHub 自己',
          text: 'LobeHub 拿 Manifest 是由它的后端 / Electron 主进程 / 容器内服务去访问 MCP 地址，不是浏览器。因此：云端版必须给 One Search 一个公网可达的 HTTPS 地址；Docker 部署时 `127.0.0.1`、`localhost` 指向 LobeHub 容器自身，应改用 `host.docker.internal`（Docker Desktop）、宿主机 LAN IP（Linux Docker），或同一 compose 网络里的服务名。'
        },
        {
          type: 'paragraph',
          text: '兼容路径有四个，优先用第一个：`/mcp`、`/mcp/`、`/v1/mcp`、`/v1/mcp/`。改了 `MCP_PATH` 后记得同步反向代理配置。'
        }
      ]
    },
    {
      id: 'mcp-troubleshoot',
      title: '排错清单',
      blocks: [
        {
          type: 'table',
          columns: ['症状', '先查什么'],
          rows: [
            ['拿不到工具列表 / 获取 Manifest 失败', '在**运行客户端的那台机器或同一个容器网络里**执行 `curl -i http://<地址>/mcp`，确认返回 200 且含 `tools`。不要在浏览器所在机器上测完就下结论'],
            ['`fetch failed` 或只有 `Error POSTing to endpoint`，没有 HTTP 状态码', '属于网络层不通，不是 MCP 协议问题。把 `localhost` 换成客户端实际可达的 IP、服务名或域名'],
            ['返回 404 / 405 / 一段 HTML', '可能打到了前端 SPA 而不是后端路由。确认 `MCP_ENABLED=true`、反向代理已包含该路径，或改用 `/v1/mcp` 再试'],
            ['返回 401', '`tools/call` 需要令牌：确认 Auth type 选的是 API Key，或已手动加 `Authorization: Bearer osr_xxx`'],
            ['工具返回 `-32003`', '令牌的「允许请求渠道」不含请求的渠道，去「接口令牌」页放开或改用 `oak_` 管理员 Key'],
            ['工具调用返回 `isError: true` 但没有细节', '工具结果里的 `text` 就是错误信息；`/v1/search` 与 MCP 一样「渠道全挂也返回成功」，需要看 `structuredContent.providers`'],
            ['通知请求报错', '通知（没有 `id`）应返回 202 且空体；若客户端要求 204，属于客户端兼容性问题']
          ]
        },
        {
          type: 'code',
          lang: 'bash',
          title: '按顺序自检',
          content: `export BASE_URL=http://localhost:5173
export API_TOKEN=osr_xxx

# 1. 元信息（应 200 且 tools 含 search；抓取启用时另含 fetch）
curl -i "$BASE_URL/mcp"

# 2. 初始化（免鉴权）
curl -sS -X POST "$BASE_URL/mcp" \\
  -H 'Content-Type: application/json' \\
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"curl","version":"1.0"}}}'

# 3. 工具列表（免鉴权）
curl -sS -X POST "$BASE_URL/mcp" \\
  -H 'Content-Type: application/json' \\
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list"}'

# 4. 通知（应 202）
curl -i -X POST "$BASE_URL/mcp" \\
  -H 'Content-Type: application/json' \\
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}'

# 5. 真正调用（需要令牌）
curl -sS -X POST "$BASE_URL/mcp" \\
  -H "Authorization: Bearer $API_TOKEN" \\
  -H 'Content-Type: application/json' \\
  -d '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"search","arguments":{"query":"one search relay","limit":3}}}'`
        }
      ]
    }
  ]
}
