/**
 * 章节：渠道配置详解
 *
 * 覆盖范围：九家渠道的默认地址、认证头、端点与路径、结果数上限、支持的 Options 键、
 * 正文与 usage 的来源、官方额度查询方式与易错点；渠道级 settings 键全集；
 * key 级 base_url 覆盖与 `#` 完整端点语法；key 级代理三态；官方额度查询支持矩阵；
 * 空密钥（匿名）条目的语义与支持匿名的渠道差异。
 *
 * 代码依据：
 * - `backend/internal/provider/exa.go`、`you.go`、`jina.go`、`tavily.go`、`firecrawl.go`、
 *   `serper.go`、`brave.go`、`keenable.go`、`context7.go`（各自拼装的请求体/查询参数与结果归一化，
 *   以及 `SupportsAnonymousKey` 的取值与 keenable 的匿名端点/请求头分支）
 * - `backend/internal/provider/helpers.go`（`requestLimit` 的 fallback/max、`usageMeasurements`、
 *   `optionString`/`optionInt`/`optionStringSlice`）
 * - `backend/internal/provider/errors.go`（`ClassifyHTTPError` 的错误类型判定）
 * - `backend/internal/search/orchestrator.go`（`providerResultLimits`、`resolveBaseURL`、
 *   `autoRefreshOfficialQuota`、`quotaRefreshInterval`）
 * - `backend/internal/search/quota.go`（各渠道官方额度端点与口径）
 * - `backend/internal/db/store.go`（`UpdateProviderKey` 的 COALESCE 语义、`validateProviderKeyBaseURL`）
 *
 * 注意：`context7` 是文档检索渠道，**不在默认渠道列表内**（默认路由仍是前八家通用搜索渠道），
 * 只有显式在 `providers` 里指定时才会参与。
 *
 * 本节只描述口径，不重复抄写单价数字——内置价目表在 Go 侧与前端各有一份副本，
 * 要查当前生效值请看「平台管理 → 计费」Tab 与「仪表盘 → 成本估算」。
 */

import type { DocChapter } from './types'

export const providersChapter: DocChapter = {
  id: 'doc-providers',
  title: '渠道配置详解',
  sections: [
    {
      id: 'providers-matrix',
      title: '九家渠道速查表',
      blocks: [
        {
          type: 'table',
          columns: ['渠道', '默认 base_url', '认证头', '端点', '结果数上限', '是否读取 `options`'],
          rows: [
            ['exa', '`https://api.exa.ai`', '`Authorization: Bearer`', '`POST /search`', '无上限', '否'],
            ['you', '`https://ydc-index.io`', '`X-API-Key`', '`GET /v1/search`', '无上限', '否'],
            ['jina', '`https://s.jina.ai`', '`Authorization: Bearer`（可为空）', '`GET /{query}`', '**不支持**', '否'],
            ['tavily', '`https://api.tavily.com`', '`Authorization: Bearer`', '`POST /search`', '20', '是'],
            ['firecrawl', '`https://api.firecrawl.dev`', '`Authorization: Bearer`', '`POST /v2/search`', '100', '是'],
            ['serper', '`https://google.serper.dev`', '`X-API-KEY`', '`POST /search`', '100', '是'],
            ['brave', '`https://api.search.brave.com/res/v1`', '`X-Subscription-Token`', '`GET /web/search`', '20', '是'],
            ['keenable', '`https://api.keenable.ai`', '`X-API-Key`（为空时改走 `X-Keenable-Title`）', '`POST /v1/search`（为空时 `POST /v1/search/public`）', '50', '是'],
            ['context7', '`https://context7.com/api`', '`Authorization: Bearer`（可为空）', '`GET /v3/search`', '**不支持**', '是']
          ]
        },
        {
          type: 'paragraph',
          text: '「结果数上限」是渠道适配器内的硬编码上限：请求的 `limit` 超过上限时会被压到上限值；`limit` 小于等于 0 时使用各自的默认值（都是 10）。`exa` 与 `you` 不做上限裁剪，直接把 `limit` 透传给上游。`context7` 上游不接受 `limit`，由适配器在本地按 `limit` 截断。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '注意这一层与「请求结果数」的先后顺序',
          text: '渠道级 `request_result_limit`（平台管理 → 运行 Tab）会先把请求里的 `limit` 替换掉，然后适配器再应用上面这张表的上限。因此实际发出条数 = `min(request_result_limit 或请求 limit, 渠道上限)`。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '`context7` 是文档检索渠道，不在默认渠道列表内',
          text: '前八家都是通用网页搜索，`context7` 检索的是开源库/框架的权威文档与可运行代码示例。它的定位是「查库文档」，因此**出厂默认路由（`default_providers`）里没有它**，存量用户的默认搜索行为也不受影响；只有请求显式传 `providers: ["context7"]` 时它才会参与。反过来说，MCP 的 `providers` 枚举包含全部九家，枚举里有它、默认却不用它，这是有意设计而不是 bug。'
        }
      ]
    },
    {
      id: 'providers-each',
      title: '逐家详解与易错点',
      blocks: [
        { type: 'heading', text: 'exa', level: 4 },
        {
          type: 'list',
          items: [
            '请求体固定为 `{ query, numResults, type: "neural", contents: { text: true, highlights: true } }`。',
            '`type` **硬编码为 `neural`**：在渠道 settings 里配置 `type` 完全不会生效，也没有其它可传模式。',
            '不读取 `freshness`，也不读取 `options` 的任何键——这两项在请求里写了也会被安静地忽略。',
            '`numResults` 直接用请求的 `limit`（≤0 时为 10），没有上限裁剪，超大会原样发给上游。',
            '正文来源：`text` 字段；摘要优先取 `highlights[0]`，没有再回退 `text` / `summary`。',
            'usage 取自响应里的 `credits` / `total_tokens` / `cost_usd` 一类字段（沿用通用探测规则）。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: 'exa 的官方额度查询需要额外凭据',
          text: '查询 Exa 官方用量需要「Exa 管理密钥」（`x-api-key`）。没配置时该密钥按「本地计费」处理，页面会提示「未配置 Exa 管理密钥，当前使用本地计算费用模式」，且不会自动刷新官方额度。'
        },
        { type: 'heading', text: 'you', level: 4 },
        {
          type: 'list',
          items: [
            '`GET /v1/search?query=...&count=<limit>`，`count` 无上限裁剪，`limit` ≤ 0 时为 10。',
            '结果数组会依次尝试 `hits`、`organic`，再看 `results.web`、`results.news`、`results`，最后兜底 `web.results` / `web.hits`。',
            '**上游没有给 `score` 时，适配器会人工赋 `1/(序号+1)`**，所以这里看到的分数不是上游相关性分数，只是名次折算。',
            '正文优先取 `contents.markdown` / `contents.html`，否则取 `content` / `description` / `snippet`，统一截断到 4000 字符。',
            '同样不读取 `freshness` 与 `options`。'
          ]
        },
        { type: 'heading', text: 'jina', level: 4 },
        {
          type: 'list',
          items: [
            '搜索词直接拼进 **URL 路径**（`GET /{query}`，并做路径转义），不是查询参数。',
            '**不支持 `limit`**：请求里传多少都一样，返回条数由上游决定；也不读取 `freshness` 与 `options`。',
            '认证头只有 `key.Value` 非空时才发送，因此留空密钥也能尝试匿名调用（上游通常会拒绝或降级）。',
            '`score` 同样有 `1/(序号+1)` 兜底。摘要截断 1000，正文截断 4000。'
          ]
        },
        { type: 'heading', text: 'tavily', level: 4 },
        {
          type: 'list',
          items: [
            '请求体包含 `query`、`max_results`、以及**硬编码的 `include_usage: true`**（用于让上游回传用量）。',
            '`max_results = min(limit, 20)`，`limit` ≤ 0 时为 10。',
            '支持的 `options` 键：`search_depth`、`topic`、`time_range`（别名 `timeRange`）、`country`、`days`、`include_domains`（别名 `includeDomains`）、`exclude_domains`（别名 `excludeDomains`）。',
            '时间范围取值优先级：`options.time_range` → `freshness` 映射（`day/d/qdr:d/pd` → day，`week/w/qdr:w/pw` → week，`month/m/qdr:m/pm` → month，`year/y/qdr:y/py` → year）→ `options.days` 折算（≤1 天→day，≤7→week，≤31→month，其余→year）。',
            '正文优先 `raw_content`，回退 `content`。**要让 `content` 字段真的有正文，请传 `include_raw: true`**（会翻译成上游的 `include_raw_content`）。',
            '`score` 有 `1/(序号+1)` 兜底。'
          ]
        },
        { type: 'heading', text: 'firecrawl', level: 4 },
        {
          type: 'list',
          items: [
            '端点是 **`/v2/search`**（注意版本号在路径里），请求体含 `query`、`limit`、以及硬编码的 `sources: ["web"]`。',
            '`limit = min(limit, 100)`，`limit` ≤ 0 时为 10。',
            '支持的 `options` 键：`tbs`、`country`、`location`、`include_domains`（别名 `includeDomains`）、`exclude_domains`（别名 `excludeDomains`）、`timeout`（毫秒，>0 才发）。',
            '`tbs` 取值优先级：`options.tbs` → `freshness` 映射（**支持 `hour` / `qdr:h`**，映射为 `qdr:h`；`day` 系列→`qdr:d`，`week`→`qdr:w`，`month`→`qdr:m`，`year`→`qdr:y`）→ 原样透传。',
            '正文优先取 `markdown`，再回退 `html`、`rawHtml`、`description`、`snippet`。传 `include_raw: true` 会附带 `scrapeOptions.formats = ["markdown"]`，这是拿到正文的关键。',
            '分数 = `1/position`（上游给了 `position` 时）或 `1/(序号+1)`。'
          ]
        },
        { type: 'heading', text: 'serper', level: 4 },
        {
          type: 'list',
          items: [
            '请求体含 `q` 与 `num`，`num = min(limit, 100)`，`limit` ≤ 0 时为 10。',
            '支持的 `options` 键：`page`、`tbs`、`gl`（别名 `country`）、`hl`（别名 `locale`、`language`）、`location`。',
            '**`freshness` 直通上游的 `tbs`，不做任何映射**：写 `week` 不会变成 `qdr:w`，需要按 Serper 的写法自己传 `tbs: "qdr:w"`。',
            '结果取 `organic` 与 `news` 两组拼接。',
            '**`content` 与 `snippet` 同源**（都来自上游的 `snippet` / `description`），因此 Serper 没有独立的长正文。',
            '分数 = `1/position` 或 `1/(序号+1)`。'
          ]
        },
        { type: 'heading', text: 'brave', level: 4 },
        {
          type: 'list',
          items: [
            '**默认 base_url 带路径前缀 `/res/v1`**，实际请求 `GET {base_url}/web/search`。做 key 级地址覆盖时如果只写到域名，路径会重复或缺失，直接 404。',
            '`count = min(limit, 20)`，`limit` ≤ 0 时为 10。',
            '支持的 `options` 键：`freshness`、`country`、`search_lang`（别名 `searchLang`、`hl`、`language`）、`ui_lang`（别名 `uiLang`、`locale`）、`safesearch`（别名 `safe_search`、`safeSearch`）、`offset`、`page`。',
            '`freshness` 映射：`day/d/qdr:d/pd` → `pd`，`week/w/qdr:w/pw` → `pw`，`month/m/qdr:m/pm` → `pm`，`year/y/qdr:y/py` → `py`；**其它值（例如 `hour`）原样透传**，上游会拒绝并不认。',
            '`offset` 直接取 `options.offset`；没给时若 `options.page > 1` 则用 `page - 1`。它是**页序号**而不是条数偏移。',
            '**只读取响应里的 `web.results`**：响应没有 `web` 对象时直接返回空结果，不会报错。',
            '正文 = `description` 拼上 `extra_snippets`；要拿到 extra snippets 需要传 `include_raw: true`（会转成 `extra_snippets=true`）。分数固定为 `1/(序号+1)`。'
          ]
        },
        { type: 'heading', text: 'keenable', level: 4 },
        {
          type: 'list',
          items: [
            '端点按密钥是否为空二选一：密钥非空时 `POST /v1/search` + `X-API-Key`；**密钥为空（匿名条目）时改打 `POST /v1/search/public` 并带 `X-Keenable-Title: OneSearchRelay`**。两条路径的请求体完全一致，只有端点与请求头不同。',
            '匿名路径**不会发送 `X-API-Key`**（连空头都不发）：实测带空 `X-API-Key` 头与完全不带鉴权同样被上游判为 401，因此匿名分支只写应用标识头。',
            '`X-Keenable-Title` 是**应用标识而非凭据**（上游用于限流归因与来源识别），网关写死为 `OneSearchRelay`，**不提供配置项**；缺失该头时 public 端点返回 400 `Missing app identifier`。',
            '**匿名额度不可控**：keyless 限流为 1000 次/小时、10 次/秒，**按 IP 且为共享池** —— 同一 IP 上的其它流量会占用该额度，余量不受网关控制；被限流时上游返回 429，网关按 `rate_limited` 处理。**keyless 不消耗 credits**，也不返回 usage 元数据。高频或关键场景建议配一条 Key。',
            '请求体含 `query`、`max_results`（`min(limit, 50)`，`limit` ≤ 0 时为 10）与 `mode`（取 `options.mode`，缺省 `pro`，另一档是 `realtime`；其它值一律回退成 `pro`，不会原样透传给上游）。',
            '支持的 `options` 键：`mode`、`site`、`acquired_after`（别名 `acquiredAfter`）、`acquired_before`（别名 `acquiredBefore`）、`published_after`（别名 `publishedAfter`）、`published_before`（别名 `publishedBefore`）、`query_time`（别名 `queryTime`）、`snippet_max_length`（别名 `snippetMaxLength`）。除 `mode` 与 `snippet_max_length` 外都是原样透传的字符串。',
            '`snippet_max_length` 会夹紧到 180 – 10000；未配置时**不发送该键**，以免用 0 覆盖上游默认值。',
            '结果取 `results` 数组，缺 `url` 的条目直接丢弃。摘要以 `snippet` 为主、`description` 兜底（实测 `description` 可能为空串而 `snippet` 有值），正文就是该摘要截断到 4000 字符——**这条渠道没有独立的长正文**。',
            '上游不返回 `score`，分数固定为 `1/(序号+1)`（与 jina / serper 同一套兜底）；时间只认 `published_at`，`acquired_at` 仅在 `include_raw: true` 的原始条目里可见。',
            '上游响应不含 `usage` 字段，因此这条渠道只会被记 `requests: 1`，不会产生 credits / tokens / usd 计量。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: 'keenable 没有官方额度接口',
          text: '点「查询官方额度」会返回 `supported: false` 与「该渠道暂未配置官方额度查询」，不请求上游，也不参与自动刷新。内置价目表里该渠道单价为 0（官方口径是「以响应为准」，而响应不含 cost，填 0 比编一个假单价诚实）；需要成本估算时在渠道「计费」Tab 自行填写单价。'
        },
        { type: 'heading', text: 'context7', level: 4 },
        {
          type: 'paragraph',
          text: '**它是文档检索渠道，不是通用网页搜索**：检索 GitHub 仓库的结构化文档与代码片段，适合「某个库/框架/SDK 怎么用」这类问题，对时事、非库类主题、通用网页问题不适用。前八家渠道拿到的是博客、问答与营销页，这正是补它的原因。'
        },
        {
          type: 'list',
          items: [
            '端点固定 `GET /v3/search`，鉴权头 `Authorization: Bearer ctx7sk...`；**认证头只在 `key.Value` 非空时才发送**，因此留空密钥即匿名调用 —— 实测该端点不带 Authorization 同样返回 200 与正常的 `codeSnippets` / `infoSnippets`，而带**无效** key 反而返回 401 `invalid_api_key`，说明 key 是可选的配额增强而不是必需凭据。仍建议配一条 Key（可从 https://context7.com/dashboard 免费申请）以获得更高配额。',
            '**默认 base_url 是 `https://context7.com/api`**：端点 `/v3/search` 是适配器拼上去的，所以做 key 级地址覆盖时只写到 `/api`，不要连 `/v3/search` 一起写进 `base_url`。',
            '请求参数只有 `query`、`type=json` 与从 `options` 透传的 `library`（可重复，最多 4 个）/`version`/`language`。',
            '**不支持 `limit`**：上游不认这个参数，返回条数由上游决定，适配器拿到结果后在本地按请求的 `limit` 截断（`limit` ≤ 0 不截断）。这与 jina 同属一类，但截断行为在本地，不是完全不截。',
            '**不读取 `freshness`**：写进请求会被安静忽略（与 exa / you / jina 一致）；需要时间约束时靠 `version` 之类的上游参数。',
            '`library` 可传精确 ID（`/vercel/next.js`）也可传模糊名（`next.js`），**不传时上游会自己选库**——这也是模型在 `search` 工具里只给 `query` 就能用的原因。',
            '**库未命中时上游返回 404 `no_documentation_found`，网关按「空结果」处理而不是报错**：这是「这个问题没有对应库文档」的正常反馈，因此日志里不会记渠道失败，也不会把渠道状态染红。其它 404（例如 base_url 配错）照常报错。',
            '**结果按文档文件聚合**：一条结果 = 一个文档页，`Content` 里是多段代码示例按语言标注的围栏代码块拼接后的正文，`URL` 是剥掉 `#_snippet_N` 片段后的文件地址。因此 `limit` 在这里的语义是「文档页数」。',
            '分数固定为 `1/(序号+1)`（上游不返回 score，与 jina / serper / keenable 同一套兜底）；`Title` 取代码片段标题、信息类片段取面包屑；缺 URL 的条目直接丢弃。',
            '**没有官方额度接口**（`/v2/libs/metrics` 是库的访问统计而不是账户额度），也不参与自动刷新；**没有公开的按次单价**，内置价目表单价为 0。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: 'context7 没有官方额度接口',
          text: '点「查询官方额度」会返回 `supported: false` 与「该渠道暂未配置官方额度查询」，不请求上游，也不参与自动刷新。内置价目表里该渠道单价为 0（官方免费层加订阅制，没有公开的按次单价，填 0 比编一个假单价诚实，与 keenable 同口径）；需要成本估算时在渠道「计费」Tab 自行填写单价。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '它不会被默认选中',
          text: '`context7` 不在出厂默认渠道列表里，不显式指定就永远不会被调用，也不会影响存量用户的默认搜索行为。MCP 客户端侧的引导是：涉及库/框架/SDK/API 用法的问题先显式传 `providers: ["context7"]` 取权威文档，**结果为空时再用其它渠道补充**。'
        }
      ]
    },
    {
      id: 'providers-settings',
      title: '渠道级 settings 键全集',
      blocks: [
        {
          type: 'paragraph',
          text: '渠道配置里有一部分是 `providers` 表的独立字段（启用、优先级、权重、超时、基础 URL），另一部分放在 `settings` JSON 里。下面这张表列出 `settings` 的全部已知键及其读取位置。'
        },
        {
          type: 'table',
          columns: ['键', '类型', '默认', '作用与生效位置'],
          rows: [
            ['`request_result_limit`', 'int', '未配置', '大于 0 时**强制覆盖**发往该渠道的 `limit`；管理台「运行」Tab 显示默认 10'],
            ['`key_retry_count`', 'int', '3', '换 key 重试次数，0–20 夹紧；实际尝试次数 = 该值 + 1'],
            ['`max_concurrency`', 'int', '0', '0 表示不限；正数表示该渠道同时进行的请求上限，超出时取 key 会直接失败并归类为限流'],
            ['`proxy_enabled`', 'bool', 'false', '是否对该**渠道**启用代理；单条密钥可用「代理模式」覆盖（见下节）'],
            ['`proxy_url`', 'string', '空', '**渠道级**代理地址；没写协议头会自动补 `http://`，容器内会把 `127.0.0.1`/`localhost` 改写成 `host.docker.internal`'],
            ['`retry_error_types`', 'string[] 或逗号分隔字符串', '空', '限定「哪些错误类型才换 key 重试」；为空时用系统默认集合'],
            ['`key_routing_strategy`', 'string', '空（按权重排序轮询）', 'Key 选择策略，取值见「路由与重试」章节'],
            ['`price_per_request`', 'number', '内置价目表', '成本估算单价（美元/次成功调用）'],
            ['`price_per_credit`', 'number', '内置价目表', '成本估算单价（美元/credit）'],
            ['`price_per_token`', 'number', '内置价目表', '成本估算单价（美元/token）'],
            ['`default_billable_credits`', 'number', '内置价目表', '上游未返回 usage 时，一次成功搜索默认记多少 credits；0 表示不补']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '未知键会被安静忽略',
          text: '例如 exa 的 `type`、`freshness`，或拼错的 `key_retry_counts`，写进 settings 不会报错，只会没有任何效果。排查「配了没生效」时优先核对键名拼写是否与上表一致。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: 'key 级代理不在这张表里',
          text: '单条密钥的代理配置存在 `provider_keys` 表的 `proxy_mode` / `proxy_url` 两个**列**，对应管理台「编辑密钥」弹窗的「代理模式」，不是渠道 settings 键。语义见下节「key 级代理的三态覆盖」。'
        }
      ]
    },
    {
      id: 'providers-baseurl',
      title: 'base_url 的多层覆盖与代理',
      blocks: [
        {
          type: 'paragraph',
          text: '生效地址按「key 级覆盖 > 渠道级 base_url > 适配器内置默认值」三级回退。两侧都会裁剪空白，因此只包含空白的值视为「未覆盖」。'
        },
        {
          type: 'code',
          lang: 'text',
          title: '优先级',
          content: `key.base_url（非空）  →  渠道 providers.base_url（非空）  →  适配器内置默认地址`
        },
        { type: 'heading', text: 'key 级覆盖的填写与更新语义', level: 4 },
        {
          type: 'list',
          items: [
            '留空表示回退渠道默认地址；填写则必须是带 `http://` 或 `https://` 的完整地址（缺主机名会在保存时被 400 拒绝）。',
            '**PATCH 更新时，传空字符串表示清除覆盖，字段完全不传表示保持原值**——两者语义不同，管理台「编辑密钥」保存的是空串，因此在那里清空输入框等于清除覆盖。',
            'Brave 的默认地址含 `/res/v1` 前缀：指向中转站时必须把路径写全，否则 404。',
            '密钥所属的渠道不同、或同一渠道下的不同密钥指向不同中转站时，每次请求都会按当前密钥重建适配器，不存在跨密钥复用连接配置的问题。'
          ]
        },
        { type: 'heading', text: '完整端点语法：# 前缀', level: 4 },
        {
          type: 'paragraph',
          text: '适配器默认会把自己的路径拼在 `base_url` 后面（例如 tavily 拼 `/search`、firecrawl 拼 `/v2/search`）。中转站的路径如果**不只是前缀**，可以给地址加 `#` 前缀，声明「这个地址就是完整端点」，网关会原样使用、不再拼接任何路径。'
        },
        {
          type: 'code',
          lang: 'text',
          title: '拼接与完整端点',
          content: `https://api.tavily.com                    → 拼接 → https://api.tavily.com/search
https://search.604020.xyz/tavily          → 拼接 → https://search.604020.xyz/tavily/search
#https://api.tavily.com/search            → 直接用 → https://api.tavily.com/search
#https://search.604020.xyz/tavily/search  → 直接用 → https://search.604020.xyz/tavily/search`
        },
        {
          type: 'list',
          items: [
            '`#` 在 URL 里本是 fragment 分隔符，网关会先剥掉它再解析，因此 `#` 之后仍必须是以 `http://` 或 `https://` 开头的合法绝对地址，否则保存时 400。',
            '完整端点模式**不做尾部 `/` 裁剪**：你写的就是最终地址。普通模式仍会裁掉尾部的 `/`。',
            '端点自带查询串也没问题：GET 类渠道追加 `q=` 等参数时会用 `&` 连接，不会拼出两个 `?`。',
            '**Jina 不支持**：它的搜索词拼在 URL 路径里（`GET /{query}`），完整端点模式会丢掉路径，因此保存带 `#` 的 Jina 地址直接返回 400。',
            'key 级与渠道级 `base_url` 共用同一套解析，`#` 在两边都生效。'
          ]
        },
        { type: 'heading', text: '空密钥（匿名）条目', level: 4 },
        {
          type: 'paragraph',
          text: '**把密钥留空就是匿名调用**：网关不为此设渠道级开关，而是让「空密钥条目」本身承载匿名语义（`key_hint` 为空串即判据）。这样匿名调用天然复用 `provider_keys` 行上的全部既有能力 —— 权重与优先级、key 级代理三态、RPM / 日 / 月配额、key 级 `base_url` 覆盖、换 key 重试。'
        },
        {
          type: 'table',
          columns: ['渠道', '匿名时的实际行为', '`supports_anonymous_key`'],
          rows: [
            ['`context7`', '不带 `Authorization` 头打 `GET /v3/search`（实测 200，与带 key 结构一致）', '`true`'],
            ['`keenable`', '改打 `POST /v1/search/public` 并带 `X-Keenable-Title: OneSearchRelay`；不发 `X-API-Key`', '`true`'],
            ['其余七家', '照常发请求但不带凭据，上游返回 401（实测 exa / jina 如此），表现为 `auth` 失败', '`false`']
          ]
        },
        {
          type: 'list',
          items: [
            '`supports_anonymous_key` 由 `/api/admin/providers` 下发，**只用于管理台提示，不参与任何放行判断**：空密钥对所有渠道都放行。中转站等场景下，报告 `false` 的渠道也可能因自定义 `base_url` 实际可用，因此创建时只弹一次确认而不硬拦。',
            '**匿名密钥不参与自动状态机**：失败时不会被自动置为 `disabled` / `exhausted` / `cooling`，但 `total_successes` / `total_failures` 照常累加。这是为了避免「探测性的空密钥被一次 auth 失败自我停用、而用户看不出原因」。',
            '**同一渠道内匿名与真实密钥混用**不被禁止，两者同样参与权重轮询、可能被随机选中。建议一个渠道只保留一种，避免调用结果忽好忽坏。',
            '只有一把匿名密钥时，`rate_limited` 的换 key 重试会重复取到同一条，等于没有重试；这是既有行为的自然结果，不额外处理。',
            '点「查询官方额度」时匿名密钥走 default 分支返回 `supported: false`，不请求上游，无副作用。'
          ]
        },
        { type: 'heading', text: 'key 级代理的三态覆盖', level: 4 },
        {
          type: 'paragraph',
          text: '代理同样支持 key 级覆盖，但用三态而不是布尔开关：布尔无法区分「未配置」与「显式直连」，会让新建密钥默认绕过渠道级代理。'
        },
        {
          type: 'table',
          columns: ['`proxy_mode`', '生效的代理地址'],
          rows: [
            ['`inherit`（默认，缺省或非法值同此）', '渠道级：`proxy_enabled=true` 且 `proxy_url` 非空时用它，否则直连'],
            ['`direct`', '**强制直连**，忽略渠道级代理'],
            ['`custom`', '该密钥自己的 `proxy_url`；地址为空（含纯空白）时**回退渠道级代理**，而不是直连']
          ]
        },
        {
          type: 'list',
          items: [
            '搜索请求、管理台「测试密钥」与「查询官方额度」三条路径都按同一套规则解析代理，出口一致。',
            '网关**不读代理环境变量**（`HTTP_PROXY` / `HTTPS_PROXY` / `ALL_PROXY` 等），传输层被显式置为不使用环境代理；只有这里配置的代理会生效。',
            '管理台保存时切到非 `custom` 模式会把 `proxy_url` 清空，避免库里留下一条「切回 `custom` 就突然生效」的陈旧地址。'
          ]
        },
        {
          type: 'callout',
          tone: 'danger',
          title: '自定义地址后官方额度查询不再对应该密钥',
          text: '官方额度查询接口的域名是**硬编码**的官方端点，不读 key 的 `base_url`。因此给某个密钥配了中转地址后，点「查询官方额度」拿到的仍是官方账户的数据，与这条密钥实际走的通道无关；管理台会提示「使用自定义地址后，官方额度查询不再适用」，成本估算也回退到本地计费配置。代理则相反：额度查询会走该密钥解析出的 key 级代理。'
        }
      ]
    },
    {
      id: 'providers-quota',
      title: '官方额度查询矩阵',
      blocks: [
        {
          type: 'table',
          columns: ['渠道', '查询方式', '返回口径', '自动刷新'],
          rows: [
            ['exa', '`GET admin-api.exa.ai/.../{apiKeyId}/usage`，用 Exa 管理密钥作 `x-api-key`', '指定周期的用量与费用（`usd_used`），**不是账户剩余额度**', '5 分钟（未配置管理密钥时不刷新）'],
            ['you', '`GET api.you.com/v1/billing/account_balance`', '账户余额（cents，同时换算 USD）', '1 分钟'],
            ['jina', '抓取 `r.jina.ai/` 根地址，用正则解析响应文本里的 `[Balance left]`', '剩余 tokens；解析不到时标记为不支持', '1 分钟'],
            ['tavily', '`GET api.tavily.com/usage`', '当前 Key 的用量与限额，余额 = 限额 − 已用；无 key 级限额时用账户计划的 plan + paygo 合计', '1 分钟'],
            ['firecrawl', '`GET api.firecrawl.dev/v2/team/credit-usage`', '团队剩余 credits 与计费周期', '1 分钟'],
            ['serper', '**无官方余额接口**', '用默认总额度 2500 credits 减本地累计 credits 估算，页面明确标注「非官方余额」', '**不自动刷新**'],
            ['brave', '`GET api.search.brave.com/res/v1/web/search?q=...&count=1`，解析响应头 `X-RateLimit-*`', '剩余请求数（取时长最长的那个窗口）', '**不自动刷新**'],
            ['keenable', '**无官方额度接口**', '返回 `supported: false` 与「该渠道暂未配置官方额度查询」，不请求上游', '**不自动刷新**'],
            ['context7', '**无官方额度接口**', '返回 `supported: false` 与「该渠道暂未配置官方额度查询」，不请求上游（上游没有账户额度查询端点）', '**不自动刷新**']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: 'Brave 的额度查询会消耗一次真实请求',
          text: '它是靠发起一次真实搜索并读取限流响应头来估算的，因此每次查询都会占用一次成功请求配额。Serper、Brave、Keenable 与 Context7 都不参与自动刷新，只能手动点「查询官方额度」。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '查询失败时接口仍返回 200',
          text: '手动查询失败不会抛出 4xx/5xx：HTTP 状态仍是 200，错误信息在响应体的 `status: "error"` 与 `message` 字段里，管理台据此弹出黄色警告。自动化脚本不要只看状态码。'
        }
      ]
    }
  ]
}
