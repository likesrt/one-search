/**
 * 章节：排错 FAQ
 *
 * 覆盖范围：按「症状 → 原因 → 处理」组织的常见问题，覆盖空结果、错误码、
 * `limit` 不生效、MCP 连接、base_url 与官方额度、加密密钥、容器代理、健康条口径等。
 *
 * 代码依据：
 * - `backend/internal/api/auth.go`、`handlers.go`、`mcp.go`（各状态码的判定分支）
 * - `backend/internal/api/handlers.go`（`testKey` 失败返回 502）
 * - `backend/internal/search/orchestrator.go`（`request_result_limit` 覆盖、缓存键、singleflight）
 * - `backend/internal/search/quota.go`（官方额度端点硬编码）
 * - `backend/internal/provider/helpers.go`（`NormalizeProxyURL`、`transport.Proxy = nil`）
 * - `frontend/src/views/DashboardView.vue`（健康条分档与「日志回填」）
 */

import type { DocChapter } from './types'

export const faqChapter: DocChapter = {
  id: 'doc-faq',
  title: '排错 FAQ',
  sections: [
    {
      id: 'faq-results',
      title: '搜索结果类',
      blocks: [
        { type: 'heading', text: '搜出来是空的，但状态码是 200', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：`/v1/search` 返回 200，`results` 为空数组。',
            '**原因**：搜索链路在**所有渠道都失败**时依然返回 200，失败只体现在 `providers[].status` 与 `error_type` 上。也可能是所有渠道都成功但确实没有命中。',
            '**处理**：检查响应体的 `providers` 数组，看每个渠道的 `status`、`error_type` 与 `error`；用相同参数在「搜索调试」页复现，右侧「渠道调用」面板会显示每次尝试的明细。'
          ]
        },
        { type: 'heading', text: '兼容端点返回空结果，什么都看不到', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：Tavily / Serper / OpenAI 兼容端点返回 200 和空结果。',
            '**原因**：这三个端点的响应里**不含** `providers` 与 `meta`，渠道级诊断信息被丢弃了。',
            '**处理**：到「请求日志」页搜同一个 `request_id`（兼容响应里会带 `request_id` / `requestId` / `id`），在详情抽屉的「渠道调用」Tab 看具体失败原因；或临时改用 `/v1/search` 复现。'
          ]
        },
        { type: 'heading', text: '仪表盘健康条有一大片灰色', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：某个渠道的时间条大量是灰格。',
            '**原因**：灰格表示**那个时间桶里没有任何请求**，不是故障。成功率只在有请求的桶里计算。',
            '**处理**：结合「总请求」KPI 判断——没有流量的时段就是灰的，属正常。真正异常是红格（成功率 < 50%）或橙格（50%–90%）。'
          ]
        },
        { type: 'heading', text: '健康区副标题出现「日志回填」', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：健康区副标题形如「近 14 天 · 1 天/段 · 14 段 · 日志回填」。',
            '**原因**：后端没有返回可用的健康序列（旧版本后端，或返回的序列全是空桶），页面改用请求日志与渠道调用明细在浏览器里重建这张图。',
            '**处理**：这是降级展示，样本只覆盖最近拉取的日志条数（详情最多再取 80 条），精度低于后端统计。若长期如此，请确认后端版本与数据库迁移是否完整。'
          ]
        }
      ]
    },
    {
      id: 'faq-errors',
      title: '错误码类',
      blocks: [
        {
          type: 'table',
          columns: ['状态码', '含义', '处理'],
          rows: [
            ['400', '请求体不是合法 JSON、`query` 为空（仅原生端点校验）', '补上 `query` 或修正 JSON'],
            ['401 `api token required`', '开启了鉴权但没带令牌', '加 `Authorization: Bearer <令牌>` 或 `X-API-Key: <令牌>`'],
            ['401 `invalid api token`', '令牌不存在、已被停用，**或日/月额度已耗尽**（三者不可区分）', '到「接口令牌」页核对状态、额度与「使用」列；确认用的是 `osr_` 而不是被误当作令牌的其它串'],
            ['403', '`allowed_providers` 不允许请求的某个渠道', '放开白名单、改用 `oak_`，或去掉请求里的 `providers`。注意兼容端点不会返回 403——它的白名单不生效'],
            ['429', '该令牌的 `rate_limit_per_min` 超限', '调大 RPM，或对调用方做退避重试。注意额度耗尽**不是** 429'],
            ['502', '仅出现在管理台的「测试密钥」，表示这次真实测试失败', '响应体里仍有 `summary` 与 `results`，`summary.error` 即原因'],
            ['500', '服务内部错误（读取设置 / 渠道配置失败等）', '看服务端日志中同 `request_id` 的记录']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: 'MCP 的 401 与 -32001',
          text: 'MCP 里 token 缺失或无效返回 HTTP 401 并在 JSON-RPC 错误体里给出 `-32001`；令牌请求了未授权渠道返回 `-32003`。而 `initialize`、`tools/list` 等方法是免鉴权的，因此「能列出工具」不代表令牌没问题。'
        }
      ]
    },
    {
      id: 'faq-limit',
      title: '条数与缓存类',
      blocks: [
        { type: 'heading', text: '传了 limit，返回的条数却少很多', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：请求写 `limit: 30`，实际只拿到 10 条或 20 条。',
            '**原因**：有三层收敛。① 渠道的「请求结果数」`request_result_limit` 大于 0 时会**强制替换**请求里的 `limit`（管理台「运行」Tab 显示默认 10，保存过弹窗就会写入）；② 各家适配器还有自己的上限——tavily 与 brave 是 20、firecrawl 与 serper 是 100；③ 最终合并结果会再按 `limit` 截断。',
            '**处理**：到「平台管理 → 运行」Tab 把「请求结果数」调到与预期一致或设为 1 以上的合理值；同时确认目标渠道的上限不低于你想要的数量。'
          ]
        },
        { type: 'heading', text: '只指定了一个渠道且没传 limit，条数被改掉了', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：请求只给了 `providers: ["tavily"]`，没写 `limit`，返回条数却不是系统默认的 10。',
            '**原因**：代码在「未显式传 `limit`」且「只指定了一个渠道」时，会直接采用该渠道配置的 `request_result_limit` 作为最终条数。',
            '**处理**：这不是故障。需要确定的条数就显式传 `limit`。'
          ]
        },
        { type: 'heading', text: '带了 include_raw 却拿不到 raw', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：`include_raw: true` 的请求，结果里没有 `raw` 字段。',
            '**原因**：该字段只在渠道真的回传了原始条目时才填充，不是开关一开就一定出现。',
            '**处理**：先确认渠道本身会给原始条目（`tavily` / `firecrawl` / `brave` 会因该开关改变上游请求，其余渠道只是把已有响应原样带出）；再用 `cache: "bypass"` 排除读到旧缓存的可能。',
            '**注**：`include_raw` 已参与缓存键计算，因此不会再出现「参数不同却命中同一份缓存」的串味。'
          ]
        },
        { type: 'heading', text: '结果里没有 content（正文）', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：`results[]` 里只有 `title` / `url` / `snippet`，没有 `content`。',
            '**原因**：正文**默认不返回**（`include_content` 默认 `false`）。一条正文实测可达数万字符，默认带上会挤爆模型上下文。',
            '**处理**：需要正文时显式传 `include_content: true`，并用 `max_content_length` 控制长度。更推荐的做法是**按需抓取**：先看 `snippet` 判断哪几条值得读，再对目标 `url` 调 `/v1/fetch` 取全文（支持 `start_index` 续读，正文质量也比搜索附带的那份稳定）。',
            '**注意**：`serper` 与 `keenable` 的正文与摘要同源，打开开关也只是同一段文本按更宽的上限再截一次；`context7` 的正文是代码示例本身，需要代码示例时必须打开这个开关。'
          ]
        },
        { type: 'heading', text: '别人的请求命中了我看到的缓存', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：不同令牌的相同查询返回结果完全一致，`cache_hit` 为真。',
            '**原因**：缓存键由查询参数构成，**不含令牌或租户维度**，所有调用方共享同一份缓存。',
            '**处理**：属预期行为。需要各调用方互不干扰时，请为不同调用方使用不同的 `options` 值（它会进入缓存键），或关闭全局缓存。'
          ]
        },
        { type: 'heading', text: '同一个查询并发发起，上游只收到一次', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：日志里多条请求共用一次上游调用，延迟几乎相同。',
            '**原因**：启用缓存时，缓存键相同的并发请求会被 singleflight 合并成一次执行，所有等待者共享结果。',
            '**处理**：属预期行为。需要每次真实打上游时用 `cache: "bypass"`。'
          ]
        }
      ]
    },
    {
      id: 'faq-mcp',
      title: 'MCP 类',
      blocks: [
        { type: 'heading', text: '客户端拿不到工具列表 / 获取 Manifest 失败', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：客户端提示获取 Manifest 失败或没有任何工具。',
            '**原因**：常见三种——服务端没开 MCP（代码默认 `MCP_ENABLED=false`）、反向代理没放行该路径、或客户端的运行环境访问不到该地址。',
            '**处理**：在**运行客户端的那台机器或同一个容器网络里**执行 `curl -i http://<地址>/mcp`。返回 200 且含 `tools` 说明服务端没问题，问题在客户端网络；返回 404/405 或一段 HTML 说明打到了前端 SPA，需检查 `MCP_ENABLED` 与代理配置；还可以用 `/v1/mcp` 兼容路径再试一次。'
          ]
        },
        { type: 'heading', text: '只有 Error POSTing to endpoint，没有状态码', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：客户端只报 `fetch failed` 或 `Error POSTing to endpoint`。',
            '**原因**：网络层根本连不上，不是 MCP 协议或鉴权问题。',
            '**处理**：把 `localhost` / `127.0.0.1` 换成客户端实际可达的 IP、服务名或域名。云端客户端必须用公网可达地址；Docker Desktop 可用 `host.docker.internal`，Linux Docker 用宿主机 LAN IP，同一 compose 网络用服务名。'
          ]
        },
        { type: 'heading', text: '工具调用报 -32003', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：`tools/call` 返回 `-32003`。',
            '**原因**：所用 `osr_` 令牌的「允许请求渠道」不包含请求的渠道。',
            '**处理**：到「接口令牌」页放开白名单，或改用 `oak_` 管理员 API Key。'
          ]
        }
      ]
    },
    {
      id: 'faq-config',
      title: '配置与运维类',
      blocks: [
        { type: 'heading', text: '改了 key 的 base_url，额度查询还是官方数据', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：把某个密钥的地址改成了中转站，点「查询官方额度」拿到的仍是官方账户余额。',
            '**原因**：各家官方额度端点（exa 的 admin-api、you 的 account_balance、tavily 的 usage 等）是**硬编码的官方域名**，不读取 key 的 `base_url`。',
            '**处理**：这是预期行为，也是为什么管理台会提示「使用自定义地址后，官方额度查询不再适用」。此时该密钥按本地计费配置估算成本，不要再参考官方额度。'
          ]
        },
        { type: 'heading', text: 'Brave 覆盖地址后一直 404', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：给 Brave 的密钥配了自定义 `base_url` 后全部请求 404。',
            '**原因**：Brave 的默认地址带 `/res/v1` 路径前缀，适配器会拼 `{base_url}/web/search`。只写到域名就会漏掉前缀。',
            '**处理**：把路径写全，例如 `https://中转站域名/res/v1`。若中转站的路径不只是「前缀 + 适配器路径」（例如完整端点是 `/proxy/brave/web/search`），改用完整端点语法：地址写成 `#https://中转站域名/proxy/brave/web/search`，网关会原样使用该地址、不再拼接自己的路径。'
          ]
        },
        { type: 'heading', text: '重启后密钥解不开（ENCRYPTION_KEY）', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：重启后渠道密钥不可用，日志里出现解密失败；管理台可能重现不出明文。',
            '**原因**：密钥明文以 `ENCRYPTION_KEY` 加密存储。该变量缺失、被改动或与写入时不一致，历史密文就无法解开。',
            '**处理**：恢复原先的 `ENCRYPTION_KEY`（至少 32 字符）并重启。若确实丢失，只能为每个渠道重新录入密钥。**请把该变量纳入备份，不要随意轮换。**'
          ]
        },
        { type: 'heading', text: '容器里配了 localhost 代理但不生效', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：渠道里的「代理地址」填了 `http://127.0.0.1:7897`，容器内请求失败。',
            '**原因**：容器里的 `127.0.0.1` 指向容器自身而不是宿主机。代码在检测到运行于容器中时会把 `//127.0.0.1:` 与 `//localhost:` 自动改写成 `//host.docker.internal:`。',
            '**处理**：确认代理确实在宿主机上、且容器能解析 `host.docker.internal`。注意网关**不读代理环境变量**：容器里配的 `HTTP_PROXY`、`HTTPS_PROXY` 等一律无效（传输层被显式置为不使用代理），只有渠道或密钥配置里的代理才会生效。',
            '**渠道级与密钥级的关系**：渠道「高级」Tab 的「使用代理」是渠道级，该渠道下所有密钥共用；单条密钥可以用「代理模式」三态覆盖它——`inherit`（默认）跟随渠道级并沿用渠道地址、`direct` 强制直连（忽略渠道级）、`custom` 使用该密钥自己的地址（地址留空时仍然回退渠道级，而不是直连，避免误配置静默改变出口）。',
            '**怎么验证**：管理台的「测试密钥」与「查询官方额度」都按同一套 key 级规则解析代理，可以用它们确认某把密钥的出口；密钥列表的元信息行会标出「直连」或自定义代理的 host。'
          ]
        },
        { type: 'heading', text: '为什么有些界面上找不到 freshness / dedupe 开关', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：「搜索调试」页只能调模式、平台、条数、是否返回正文。',
            '**原因**：该页设计为最小参数集，只发送 `query`、`mode`、`providers`、`limit`、`include_content`。',
            '**处理**：需要验证 `freshness`、`dedupe`、`rerank`、`include_raw`、`snippet_limit`、`max_content_length`、`options` 时直接调用 `/v1/search`；调用结果仍会出现在「请求日志」里，可以对照详情。'
          ]
        },
        { type: 'heading', text: '上游密钥状态变成 cooling / exhausted，怎么恢复', level: 4 },
        {
          type: 'list',
          items: [
            '**症状**：密钥状态显示冷却或额度耗尽，不再被使用。',
            '**原因**：最近一次调用分别返回了 `rate_limited`（冷却 15 分钟）或 `quota_exhausted`。注意 `timeout` / `upstream` 一类错误**不会**改变状态。',
            '**处理**：`cooling` 会在 15 分钟后自动重新可用；`exhausted` 与 `disabled` 需要先解决上游额度/凭据问题，再到「平台管理」手动点启用。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '已知的两个既有缺口（本轮未修复）',
          text: '① 令牌的 `allowed_providers` 对三个兼容端点不生效；② 令牌的 `scopes` 没有任何门控逻辑。两者都属于当前实现的既有行为，本页只做如实记录，未做收敛。需要渠道级权限控制时，请只开放 `/v1/search` 与 MCP。'
        }
      ]
    }
  ]
}
