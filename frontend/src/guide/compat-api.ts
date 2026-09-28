/**
 * 章节：兼容接口
 *
 * 覆盖范围：三个第三方形态端点的开关、字段映射、Options 键名、被忽略的字段，
 * 以及与原生接口的行为差异（白名单不生效、缺少渠道级诊断）。
 *
 * 代码依据：
 * - `backend/internal/api/handlers.go`（`tavilySearch`/`serperSearch`/`openAISearch`，
 *   三个函数均未调用 `applyTokenProviders`；开关关闭时返回 404）
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
      title: '三个端点与开关',
      blocks: [
        {
          type: 'table',
          columns: ['端点', '对标形态', '开关（系统设置 → 兼容接口）', '出厂默认'],
          rows: [
            ['`POST /v1/compat/tavily/search`', 'Tavily Search', 'Tavily / `compat_tavily_enabled`', '开'],
            ['`POST /v1/compat/serper/search`', 'Serper.dev', 'Serper / `compat_serper_enabled`', '开'],
            ['`POST /v1/compat/openai/responses-search`', 'OpenAI Responses 的 web 搜索结果形态', 'OpenAI / `compat_openai_enabled`', '开']
          ]
        },
        {
          type: 'paragraph',
          text: '三个端点共用 `/v1` 前缀下的令牌鉴权（`Authorization: Bearer` 或 `X-API-Key`），内部都调用同一套搜索编排，因此渠道、路由、重试、缓存、日志与用量统计与原生接口完全一致。`compat_format` 会写入 `meta` 并参与缓存键，因此三个端点各自独立缓存，互不串味。'
        },
        {
          type: 'callout',
          tone: 'danger',
          title: '令牌的「允许请求渠道」对这三个端点不生效',
          text: '原生 `/v1/search` 与 MCP 的 `tools/call` 会在编排前调用 `applyTokenProviders`，把请求渠道收敛到令牌白名单、越权时返回 403；而这三个兼容端点**没有任何等价逻辑**。给一个只允许 `exa` 的令牌，用兼容端点请求 `brave` 一样会被执行。需要渠道级收敛时，请只开放原生接口，或改用网关注入的 `providers` 字段以外的其它手段。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '兼容响应里没有渠道级诊断信息',
          text: '三个端点的响应都只映射了「结果」部分，**不含 `providers` 与 `meta`**。当所有渠道都失败时，你只会拿到 HTTP 200 加一个空结果集，看不到任何错误类型或渠道状态。需要诊断请到「请求日志」页按 `request_id` 查看，或改用原生接口。'
        },
        {
          type: 'paragraph',
          text: '另外一个差异：原生端点会在 `query` 为空时返回 400 `query is required`，兼容端点不做这项校验，空查询会直接透传到上游渠道。'
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
    }
  ]
}
