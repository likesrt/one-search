/**
 * 章节：兼容接口
 *
 * 覆盖范围：四个第三方形态端点的开关、字段映射、Options 键名、被忽略的字段，
 * 以及与原生接口的行为差异（白名单、错误形状、缺少渠道级诊断）。
 *
 * 代码依据：
 * - `backend/internal/api/handlers.go`（`tavilySearch`/`serperSearch`/`openAISearch`）
 * - `backend/internal/api/deepseek_compat.go`（`deepSeekSearch`/`deepSeekFetch` 与兼容错误对象）
 * - `backend/internal/compat/schema.go`、`mapper.go`（字段映射与被丢弃的字段）
 * - `backend/internal/model/search.go`（`CompatFormat` 常量）
 */

import type { DocChapter } from './types'

export const compatApiChapter: DocChapter = {
  id: 'doc-compat-api',
  title: '兼容接口',
  sections: [
    {
      id: 'compat-overview',
      title: '四个端点与开关',
      blocks: [
        {
          type: 'table',
          columns: ['端点', '对标形态', '开关（系统设置 → 兼容接口）', '出厂默认'],
          rows: [
            ['`POST /v1/compat/tavily/search`', 'Tavily Search', 'Tavily / `compat_tavily_enabled`', '开'],
            ['`POST /v1/compat/serper/search`', 'Serper.dev', 'Serper / `compat_serper_enabled`', '开'],
            ['`POST /v1/compat/openai/responses-search`', 'OpenAI Responses 的 web 搜索结果形态', 'OpenAI / `compat_openai_enabled`', '开'],
            ['`POST /v1/tools/search`、`POST /v1/tools/fetch`', 'DeepSeek 官方 Search / Fetch', 'DeepSeek / `compat_deepseek_enabled`', '开']
          ]
        },
        {
          type: 'paragraph',
          text: '四个端点共用 `/v1` 前缀下的令牌鉴权（`Authorization: Bearer` 或 `X-API-Key`）。搜索端点内部调用同一套搜索编排，DeepSeek 的 fetch 端点复用原生抓取链路，因此渠道、路由、重试、缓存、日志与用量统计与原生接口一致。`compat_format` 会记入检索日志并参与搜索缓存键，因此各搜索端点独立缓存，互不串味。'
        },
        {
          type: 'callout',
          tone: 'danger',
          title: 'Tavily / Serper / OpenAI 三个端点不执行令牌的「允许请求渠道」',
          text: '原生 `/v1/search`、MCP 的 `tools/call` 与 DeepSeek 兼容端点会在编排前调用 `applyTokenProviders`，把请求渠道收敛到令牌白名单、越权时返回 403；而 Tavily / Serper / OpenAI 三个兼容端点**没有任何等价逻辑**。给一个只允许 `exa` 的令牌，用这三个端点请求 `brave` 一样会被执行。需要渠道级收敛时，请只开放原生接口、MCP 或 DeepSeek 兼容端点。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '兼容响应里没有渠道级诊断信息',
          text: '四个端点的成功响应都只映射「结果」部分，**不含 `providers` 与 `meta`**。当所有渠道都失败时，Tavily / Serper / OpenAI 端点返回 HTTP 200 加空结果集；DeepSeek 端点会返回 500，但仍看不到具体错误类型或渠道状态。需要诊断请到「请求日志」页按 `request_id` 查看，或改用原生接口。'
        },
        {
          type: 'paragraph',
          text: '参数校验也不完全一致：原生端点会在 `query` 为空时返回 400 `query is required`；Tavily / Serper / OpenAI 三个端点不做这项校验，空查询会直接透传到上游渠道；DeepSeek 端点按官方契约校验 `query`、`count`、`ori_question` 与 `url`。'
        }
      ]
    },
    {
      id: 'compat-tavily',
      title: 'Tavily 兼容端点',
      blocks: [
        {
          type: 'code',
          lang: 'bash',
          title: '请求示例',
          content: `curl -sS http://localhost:5173/v1/compat/tavily/search \\
  -H "Authorization: Bearer osr_xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "query": "latest web search APIs",
    "max_results": 8,
    "search_depth": "advanced",
    "topic": "news",
    "days": 7,
    "include_raw_content": true,
    "include_domains": ["github.com"]
  }'`
        },
        { type: 'heading', text: '请求字段映射', level: 4 },
        {
          type: 'table',
          columns: ['兼容字段', '映射到原生', '说明'],
          rows: [
            ['`query`', '`query`', '搜索词'],
            ['`max_results`', '`limit`', '该字段是否出现在请求体里会影响「显式 limit」判定'],
            ['`providers`', '`providers`', '**非 Tavily 原生字段**，本服务附加的扩展；出现即视为显式指定渠道'],
            ['`mode`', '`mode`', '**扩展字段**，取值同上'],
            ['`cache`', '`cache`', '**扩展字段**，`default`/`refresh`/`bypass`'],
            ['`include_raw_content`', '`include_raw`', '透传为上游的 `include_raw_content` 并回填 `raw`'],
            ['`search_depth`', '`options.search_depth`', '透传给 Tavily 渠道'],
            ['`topic`', '`options.topic`', '透传给 Tavily 渠道'],
            ['`days`', '`options.days`', 'Tavily 渠道会折算成 `time_range`：≤1 天 → day，≤7 → week，≤31 → month，其余 → year'],
            ['`include_domains`', '`options.include_domains`', '透传'],
            ['`exclude_domains`', '`options.exclude_domains`', '透传'],
            ['`include_answer`', '—', '**被忽略**，响应里 `answer` 恒不出现'],
            ['`include_images`', '—', '**被忽略**，本服务不返回图片结果']
          ]
        },
        { type: 'heading', text: '响应映射', level: 4 },
        {
          type: 'table',
          columns: ['响应字段', '来源'],
          rows: [
            ['`query`', '请求里的 `query` 原样回显'],
            ['`results[].title` / `url`', '合并结果的标题与链接'],
            ['`results[].content`', '优先 `snippet`，为空时回退 `content`。因此**默认请求下仍有值**——它取的是摘要，不是正文'],
            ['`results[].raw_content`', '直接映射 `content`（正文）。**默认请求下为空**：正文由原生接口的 `include_content` 控制，而 Tavily 兼容层的 `include_raw_content` 只透传给上游渠道、不会打开本地正文输出。需要该字段请改用 `/v1/search` 并传 `include_content: true`'],
            ['`results[].score`', '合并结果的分数'],
            ['`response_time`', '`meta.latency_ms` 换算成秒'],
            ['`request_id`', '`meta.request_id`，可用于到请求日志里查详情']
          ]
        }
      ]
    },
    {
      id: 'compat-serper',
      title: 'Serper 兼容端点',
      blocks: [
        {
          type: 'code',
          lang: 'bash',
          title: '请求示例',
          content: `curl -sS http://localhost:5173/v1/compat/serper/search \\
  -H "Authorization: Bearer osr_xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "q": "latest web search APIs",
    "num": 10,
    "tbs": "qdr:w",
    "gl": "us",
    "hl": "en"
  }'`
        },
        { type: 'heading', text: '请求字段映射', level: 4 },
        {
          type: 'table',
          columns: ['兼容字段', '映射到原生', '说明'],
          rows: [
            ['`q`', '`query`', '搜索词'],
            ['`num`', '`limit`', '出现即视为显式 limit'],
            ['`tbs`', '`freshness` **且** `options.tbs`', '同时写入两处，保证不管路由到哪家渠道都能生效'],
            ['`page`', '`options.page`', '仅 Serper 渠道读取'],
            ['`gl`', '`options.gl`', '仅 Serper 渠道读取（`country` 亦可）'],
            ['`hl`', '`options.hl`', '仅 Serper 渠道读取（`locale`/`language` 亦可）'],
            ['`providers`', '`providers`', '**扩展字段**'],
            ['`mode`', '`mode`', '**扩展字段**'],
            ['`cache`', '`cache`', '**扩展字段**']
          ]
        },
        { type: 'heading', text: '响应映射', level: 4 },
        {
          type: 'table',
          columns: ['响应字段', '来源'],
          rows: [
            ['`searchParameters`', '固定回填 `{ q, num, type: "search" }`，不做真实回显校验'],
            ['`organic[]`', '合并结果逐条映射：`title`、`link`、`snippet`；`position` 按合并后的顺序从 1 重新编号'],
            ['`credits`', '**不是真实额度消耗**，取本次实际发起过的渠道数量 `meta.providers_queried` 的长度'],
            ['`requestId`', '`meta.request_id`']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '`tbs` 不会被翻译',
          text: '兼容层把 `tbs` 原样写进 `freshness`。落到 Serper 渠道时它同样原样进上游的 `tbs`（Good），但落到其它渠道时会按各自规则解释：Tavily 只认 `qdr:d/w/m/y` 这类值，Brave 只认 `pd/pw/pm/py`。跨渠道使用请用各渠道的通用写法，或直接调原生接口配 `options`。'
        }
      ]
    },
    {
      id: 'compat-openai',
      title: 'OpenAI 兼容端点',
      blocks: [
        {
          type: 'code',
          lang: 'bash',
          title: '请求示例',
          content: `curl -sS http://localhost:5173/v1/compat/openai/responses-search \\
  -H "Authorization: Bearer osr_xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "input": "latest web search APIs",
    "limit": 5,
    "providers": ["exa", "tavily"]
  }'`
        },
        { type: 'heading', text: '请求字段映射', level: 4 },
        {
          type: 'table',
          columns: ['兼容字段', '映射到原生', '说明'],
          rows: [
            ['`query`', '`query`', '优先使用；裁剪空白后为空时才看 `input`'],
            ['`input`', '`query`', '`query` 缺失或为空白时的回退搜索词'],
            ['`limit`', '`limit`', '出现即视为显式 limit'],
            ['`providers`', '`providers`', '**扩展字段**'],
            ['`mode`', '`mode`', '**扩展字段**'],
            ['`cache`', '`cache`', '**扩展字段**']
          ]
        },
        { type: 'heading', text: '响应映射', level: 4 },
        {
          type: 'table',
          columns: ['响应字段', '来源'],
          rows: [
            ['`id`', '`meta.request_id`'],
            ['`object` / `status`', '固定为 `"response"` / `"completed"`，**不反映真实成败**'],
            ['`search_results`', '原生的 `results` 数组，字段完全一致（这是唯一保留结构化结果的兼容端点）'],
            ['`output[0]`', '`{ "type": "web_search_call", "status": "completed" }` 占位项'],
            ['`output[1]`', '`{ "type": "message", "content": [{ "type": "output_text", "text": "..." }] }`，文本由每条结果拼成 `序号. 标题 - 链接` 换行再接摘要，条目之间空一行']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '`status` 与 `isError` 都不代表真实结果',
          text: '`status` 恒为 `completed`，即使所有渠道都失败也一样。判断是否有结果请看 `search_results` 是否为空。'
        }
      ]
    },
    {
      id: 'compat-deepseek',
      title: 'DeepSeek 兼容端点',
      blocks: [
        {
          type: 'paragraph',
          text: '按 DeepSeek 官方 `POST /v1/tools/search` 与 `POST /v1/tools/fetch` 的字段契约暴露同一网关能力，路径与官方逐字一致，已在用 DeepSeek 搜索/抓取的客户端只需把 Base URL 指向本服务（`https://api.deepseek.com` → 网关地址）。这不是新增上游渠道：内部仍走同一套编排，DeepSeek 官方渠道只是被路由到的渠道之一。'
        },
        {
          type: 'code',
          lang: 'bash',
          title: '搜索请求示例',
          content: `curl -sS http://localhost:5173/v1/tools/search \\
  -H "Authorization: Bearer osr_xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "query": "DeepSeek V4 模型 发布",
    "count": 5,
    "need_content": false
  }'`
        },
        { type: 'heading', text: '搜索请求字段', level: 4 },
        {
          type: 'table',
          columns: ['字段', '类型', '映射到原生', '说明'],
          rows: [
            ['`query`', 'string（必填）', '`query`', '1～128 个 Unicode 字符，不能全为空白；支持 `site:`、`-site:`、引号等搜索语法'],
            ['`count`', 'integer', '`limit`', '1～50，缺省 10；无论是否传值都按显式 limit 处理，不会被渠道的 `request_result_limit` 改写'],
            ['`ori_question`', 'string / null', '`options.ori_question`', '1～128 字符；只有 DeepSeek 官方渠道读取，其它渠道忽略。空串与 null 都视为未提供'],
            ['`localization`', 'string', '`options.localization`', '用于优先返回本地化结果的 IP；原样透传给 DeepSeek 官方渠道'],
            ['`need_content`', 'boolean', '`include_content`', '缺省 false；为 false 时响应里的 `content` 恒为空字符串'],
            ['`providers` / `mode` / `cache`', '—', '同名原生字段', '**扩展字段**，官方客户端不会传；出现时按原生语义生效']
          ]
        },
        { type: 'heading', text: '搜索响应映射', level: 4 },
        {
          type: 'table',
          columns: ['响应字段', '来源', '说明'],
          rows: [
            ['`results[]`', '原生的 `results`', '无结果时是空数组；所有渠道都失败时返回 500 而不是空数组'],
            ['`rerank_score`', '原生 `score` × 3', '原生分数统一为 0..1，这里还原为官方的 0..3 并夹紧、保留三位小数'],
            ['`title` / `url` / `snippet`', '同名字段', '摘要长度沿用原生 `snippet_limit` 口径'],
            ['`summary`', '`need_content=true` 时取正文，否则取 `snippet`', '本服务没有独立的「600～800 字长摘要」数据源，因此长摘要会退化为摘要或正文'],
            ['`content`', '原生 `content`', '`need_content=false` 时恒为空；为 true 时按本服务硬顶 50000 字节返回（官方没有长度参数）'],
            ['`site_name` / `site_icon`', '恒为 `null`', '本服务不采集站点名与图标，字段始终出现'],
            ['`date`', '原生 `published_at`', 'UTC 的 RFC 3339；未知时 `null`'],
            ['`authority`', '恒为 `-1`', '本服务不评估权威性，`-1` 表示未知']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '响应只含 `results`',
          text: '官方契约里响应顶层只有 `results`；本服务的 `providers`、`meta`、`request_id` 都不会出现。需要按 request id 排错时，请改用原生接口或到「请求日志」页查看。'
        },
        {
          type: 'code',
          lang: 'bash',
          title: '抓取请求示例',
          content: `curl -sS http://localhost:5173/v1/tools/fetch \\
  -H "Authorization: Bearer osr_xxx" \\
  -H "Content-Type: application/json" \\
  -d '{"url":"https://example.com/article"}'`
        },
        { type: 'heading', text: '抓取字段与响应', level: 4 },
        {
          type: 'table',
          columns: ['字段', '类型', '说明'],
          rows: [
            ['请求 `url`', 'string（必填）', '不超过 8192 字符；必须是带 host 的 HTTP/HTTPS 地址，不能携带用户名或密码'],
            ['响应 `title`', 'string', '本服务不采集页面标题，恒为空字符串（官方允许缺失时为空串）'],
            ['响应 `content`', 'string', 'Markdown 正文；按抓取硬上限 50000 字符返回，超长时末尾会带截断续读提示']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '抓取复用全局抓取配置',
          text: '`/v1/tools/fetch` 与原生 `/v1/fetch` 共用「网页抓取」的开关、代理、SSRF 护栏、超时、Tavily 回退与缓存。抓取功能整体关闭时该端点返回 404；目标被 SSRF 护栏拦截返回 403；下游超时返回 504；正文为空返回 404。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '错误响应是 OpenAI 兼容对象',
          text: '成功响应与错误响应都按官方契约输出：错误形如 `{"error":{"message":"...","type":"invalid_request_error","param":null,"code":null}}`，`param` 恒为 `null`。鉴权与限流错误也由 DeepSeek 专用中间件输出同一形状；这是四个兼容端点里唯一做到这一点的。其余三个端点仍使用网关的 `{message,status}` 错误格式。'
        }
      ]
    }
  ]
}
