/**
 * 章节：管理台使用
 *
 * 覆盖范围：逐页说明管理台 8 个页面——搜索调试、网页抓取、仪表盘、平台管理、接口令牌、
 * 请求日志、审计日志、系统设置：每个控件的作用、各指标的口径、以及容易误读的地方。
 *
 * 代码依据：
 * - `frontend/src/views/PlaygroundView.vue`（筛选胶囊、统计胶囊、渠道调用面板）
 * - `frontend/src/views/FetchView.vue`（抓取调试、续读、专属配置）
 * - `frontend/src/views/DashboardView.vue`（5 档时间范围、6 个 KPI、健康分档、成本估算、日志回填）
 * - `frontend/src/views/ProvidersView.vue`、`TokensView.vue`（表单字段与按钮文案）
 * - `frontend/src/views/LogsView.vue`、`AuditLogsView.vue`（自动刷新、抽屉、风险分级）
 * - `frontend/src/views/SettingsView.vue`（5 个分区与字段）
 * - `backend/internal/db/store.go`（RuntimeSettings / FetchSettings 与 ProviderHealth 默认值/阈值）
 */

import type { DocChapter } from './types'

export const consoleChapter: DocChapter = {
  id: 'doc-console',
  title: '管理台使用',
  sections: [
    {
      id: 'console-playground',
      title: '搜索调试',
      blocks: [
        {
          type: 'paragraph',
          text: '定位是「配置完之后立刻验证一次搜索」。它调用的是管理端专用接口 `POST /api/admin/playground/search`，走的是与对外 API 完全相同的编排链路，但额外把每次渠道尝试的明细一并回填，所以能看到重试过程。'
        },
        { type: 'heading', text: '顶部状态与可用性', level: 4 },
        {
          type: 'list',
          items: [
            '标题右侧标签显示「可用」或「待配置」：只要有任意一个**已启用**且**可用密钥数 > 0** 的渠道就是「可用」。',
            '「待配置」时页面顶部出现提示条「还没有可用的搜索平台 / 启用 1 个平台并添加上游 Key 后再搜索」，右侧「去配置」按钮跳到平台管理。'
          ]
        },
        { type: 'heading', text: '筛选胶囊', level: 4 },
        {
          type: 'table',
          columns: ['控件', '取值', '默认值与说明'],
          rows: [
            ['模式', '转移（`fallback`）/ 并发（`parallel`）/ 单源（`single`）', '默认取「系统设置 → 默认模式」'],
            ['平台', '多选', '只列出已启用且有可用密钥的渠道；默认取系统设置的默认平台与该列表的交集，交集为空则全选'],
            ['条数', '`1` – `50`', '默认取「系统设置 → 汇总返回结果数」（出厂 10）']
          ]
        },
        { type: 'heading', text: '结果区', level: 4 },
        {
          type: 'list',
          items: [
            '统计胶囊一排：`N 条结果`（取 `meta.total_results`）、`去重 N`（`meta.deduped_results`）、`耗时 X`（`meta.latency_ms`，≥1000ms 显示为秒）、以及等宽的 `request_id`。',
            '每条结果：站点名（host + 路径）、标题（可点击新窗口打开）、摘要。摘要默认 3 行截断，行尾「展开」可切换显示 `content` 而不是 `snippet`，再次点击收起。',
            '结果底部标签：渠道（多来源合并时显示逗号分隔的多个渠道）与 `评分 x.xx`（整数分直接显示整数）。'
          ]
        },
        { type: 'heading', text: '右侧「渠道调用」面板', level: 4 },
        {
          type: 'paragraph',
          text: '面板标题写着「渠道调用」，右上角有个状态点：请求中为 `searching`，结束后为 `done`。每个渠道尝试是一张卡片，顶行是渠道名 + 「成功」或「失败」，第二行依次拼接以下信息（存在才显示）：'
        },
        {
          type: 'list',
          ordered: true,
          items: [
            '耗时（≥1000ms 显示为秒）',
            '`· key <别名>` 本次实际使用的密钥别名',
            '`· N 条` 该次调用返回的结果数（仅成功时显示）',
            '`· 第 N 次` 这是该渠道的第几次尝试',
            '`· 缓存`、`· 将重试`',
            '`· <错误信息>` 失败原因'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '「第 N 次」就是换 key 重试链路',
          text: '面板数据优先取 `provider_calls`（每一次换 key 尝试各占一行），因此**同一个渠道会出现多行**：`第 1 次` 失败且带「将重试」、`第 2 次` 成功，就说明第一次的密钥被换掉了。只有当响应里没有 `provider_calls`（旧数据）时才回退到 `providers`，此时每个渠道固定只有一行、显示为「第 1 次」。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '这一页只覆盖最小参数集',
          text: '搜索调试页只会发送 `query`、`mode`、`providers`、`limit` 四个字段，`cache` 恒为默认策略；页面上没有 `freshness`、`dedupe`、`rerank`、`include_raw`、`options` 的输入控件。要验证这些字段请直接调 `/v1/search`。'
        }
      ]
    },
    {
      id: 'console-fetch',
      title: '网页抓取',
      blocks: [
        {
          type: 'paragraph',
          text: '定位是「把某个 URL 的正文抓回来看一眼」，并顺带配置这项功能本身。页面分两段：上方是抓取调试，下方是专属配置。它调用管理端接口 `POST /api/admin/fetch/test`，与对外 `POST /v1/fetch` 走同一套校验与执行逻辑，因此这里试出来的行为就是对外的行为。配置里除抓取本体外，还包含 Tavily 回退兜底与本地缓存、并发上限——回退默认关闭，因为开启后会按量消耗第三方额度。'
        },
        { type: 'heading', text: '抓取调试', level: 4 },
        {
          type: 'list',
          items: [
            'URL 输入框回车或点「抓取」都会发起请求；URL 必须带 `http://` 或 `https://`，裸域名会在前端就被拦下并提示（与后端规则一致）。',
            '参数胶囊：长度（`max_length`，1–50000，默认 5000）、方法（GET/POST）、原始内容（`raw`）、起点（`start_index`）。',
            '「起点」只在方法为 GET 时出现：续读会重放请求，而 POST 可能创建资源或二次计费，服务端也会拒绝带 `start_index` 的 POST。',
            '方法切到 POST 后额外出现请求头与请求体两个输入框。请求头需填 JSON 对象（空着表示不传），请求体能被解析为 JSON 就按 JSON 值发送，否则按原始字符串发送。',
            '「重置参数」只复位参数，不清空 URL——便于改了参数后重抓同一个地址。'
          ]
        },
        { type: 'heading', text: '结果区', level: 4 },
        {
          type: 'list',
          items: [
            '标题栏显示上游状态码、`Content-Type`、总字符数与**通道**（内置抓取 / Tavily 回退）。**状态码是上游的**：抓到 404 页面同样显示结果而不是错误，因为自定义 API 常把错误详情放在响应体里。',
            '内容按 Markdown 渲染在深色代码块里。被截断时出现黄色胶囊「已截断 · 下一段从 N 开始」，并给出「续读」按钮（自动把起点设为 N 后重抓）。',
            '「续读」在方法为 POST 时会被拦下并提示提高长度后重抓。',
            '传输层失败（连不上、超时、被 SSRF 防护拦截）显示为红色横幅，文案即服务端返回的原因。'
          ]
        },
        { type: 'heading', text: '专属配置', level: 4 },
        {
          type: 'paragraph',
          text: '这些项存在独立于「系统设置」的配置里，保存后对新请求立即生效。前四项是抓取本体，中间两项是回退兜底，后五项是缓存与并发：'
        },
        {
          type: 'table',
          columns: ['字段', '默认', '要点'],
          rows: [
            ['启用网页抓取', '开', '关闭后 `/v1/fetch` 返回 404，MCP 的工具清单也不再列出 `fetch`'],
            ['抓取超时 (ms)', '30000', '上界 60000，受反向代理 65s 读取超时约束；配得更长只会先被反代断开'],
            ['抓取代理', '空', '空 = 直连。容器部署时 `127.0.0.1` / `localhost` 会被自动改写为 `host.docker.internal`。**不作用于回退通道**'],
            ['放行内网目标', '关', '打开后任何持令牌的调用方都能借本服务探测内网，页面会给出警告条'],
            ['回退兜底 (Tavily)', '关', '开启后内置抓取失败/被 401·403·429 拦截/内容过少时改用 Tavily；**按量消耗第三方额度**，故默认关闭'],
            ['触发阈值 (可见字符)', '80', '只在回退开关打开时显示。口径是可见文本（已剥离 Markdown 图片与链接目标），因此 1037 字符里 981 个是内联图片的质询页会被正确判为过少'],
            ['缓存时长 (秒)', '120', '同一 URL 窗口内直接返回缓存，不再打上游；`0` = 关闭缓存。窗口内看不到页面更新'],
            ['错误态缓存 (秒)', '120', '上游返回 `401/403/429` 时的缓存时长，避免几分钟内反复撞门禁'],
            ['单条缓存上限 (字节)', '3145728', '超过 3MiB 的结果不写缓存'],
            ['缓存总量上限 (字节)', '268435456', '目录超过 256MiB 时按文件修改时间从旧到新淘汰'],
            ['并发上限', '32', '同时在飞的抓取数，超出的排队等待；缓存命中不占名额']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '这里的改动会写审计',
          text: '保存配置写一条 `fetch.settings.update`（含新增的回退与缓存字段），试抓成功或失败都写一条 `fetch.test`（内含状态码、通道与是否截断）。而对外 `/v1/fetch` 的调用不写审计，与对外搜索保持一致，只在服务端访问日志里留下 `fetch_done` / `fetch_failed` 记录。'
        }
      ]
    },
    {
      id: 'console-dashboard',
      title: '仪表盘',
      blocks: [
        {
          type: 'paragraph',
          text: '时间范围共 5 档，选择结果记在浏览器 `localStorage` 的 `osr.dashboard.range`，下次打开沿用：'
        },
        {
          type: 'table',
          columns: ['档位', '区间', '分桶', '健康条格数'],
          rows: [
            ['近 24 小时', '往前 24 小时', '1 小时', '24'],
            ['今日', '当天 00:00 到现在', '1 小时', '当前小时数（1–24）'],
            ['近 7 天', '往前 7 天', '1 天', '7'],
            ['近 14 天（默认）', '往前 14 天', '1 天', '14'],
            ['近 30 天', '往前 30 天', '1 天', '30']
          ]
        },
        { type: 'heading', text: '6 个 KPI 卡片', level: 4 },
        {
          type: 'list',
          items: [
            '总请求 `requests_total`、成功 `requests_success`、失败 `requests_failed`、结果 `results_total`、缓存命中 `cache_hits`、平均延迟 `average_latency_ms`（保留 1 位小数）。',
            '每张卡片下方有一条迷你折线，数据来自同一时间范围的序列接口，因此趋势与数字同源。'
          ]
        },
        { type: 'heading', text: '图表区', level: 4 },
        {
          type: 'list',
          items: [
            '「请求与延迟」：柱状是每个分桶的请求数，折线是同一分桶的平均延迟（毫秒，右轴）。',
            '「平台贡献」：环形图，按各渠道请求数占比；无数据时画一个灰色的「暂无数据」。',
            '「需要关注 / 运行平稳」：把所有状态为降级、不可用、无密钥的渠道用顿号列出；全部正常时显示「近窗平台状态正常，暂无需要处理的告警。」'
          ]
        },
        { type: 'heading', text: '渠道健康', level: 4 },
        {
          type: 'paragraph',
          text: '每个渠道一张卡片，右上角是 uptime 文案：已停用的渠道显示「已停用」，没有密钥的显示「无密钥」，窗口内没有请求样本的显示「无请求样本」，其余显示 `x.xx% uptime`。下方色条每格代表一个时间桶，鼠标悬停显示「分档 · 桶宽 · 成功 N · 失败 N · 共 N」。'
        },
        {
          type: 'table',
          columns: ['每格颜色', '判定条件（该桶内）', '含义'],
          rows: [
            ['绿 · 正常', '成功率 ≥ 90%', '该段时间表现正常'],
            ['橙 · 降级', '成功率 50% – 90%', '有明显失败但仍在服务'],
            ['红 · 故障', '成功率 < 50%', '大部分请求失败'],
            ['灰 · 无请求', '该桶没有任何请求', '没有样本，不代表健康']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '两套阈值不要混淆',
          text: '色条的 90% / 50% 是**前端按桶分档**的展示规则；而后端「渠道近窗状态」（决定卡片是否被打上降级）用的是另一套：窗口内请求数 ≥ 5 且成功率 < 0.8 时，才把原本 healthy 的渠道降级。色条只反映历史请求，卡片状态还额外考虑密钥状态。'
        },
        {
          type: 'paragraph',
          text: '健康区副标题形如「近 14 天 · 1 天/段 · 14 段」。如果它末尾多出 `· 日志回填`，说明后端没有返回可用的健康序列，页面改用**请求日志 + 每次渠道调用明细在浏览器里重建**了这张图。此时样本只覆盖最近拉取到的日志条数，精度低于后端统计，属于降级展示。'
        },
        { type: 'heading', text: '成本估算', level: 4 },
        {
          type: 'list',
          items: [
            '左图按渠道汇总 USD 成本画柱状图；右表逐渠道列出 `单位 · 数量` 与对应 USD，末行是「合计」，并明确标注「估算 USD，非官方账单」。',
            '只统计**成功调用**且单价大于 0 的行；没有可估算数据时显示「暂无可估算成本（仅成功调用 + 公开单价）」。',
            '单价来源：渠道 settings 里的 `price_per_request` / `price_per_credit` / `price_per_token`，为 0 时回退后端内置公开价目表。要改单价去「平台管理 → 计费」Tab；这里不重复列出具体数字，避免与实际生效值漂移。'
          ]
        }
      ]
    },
    {
      id: 'console-providers',
      title: '平台管理',
      blocks: [
        {
          type: 'paragraph',
          text: '渠道以卡片网格展示，卡片上有：渠道名与生效地址的 host、右上角启用开关，以及四个指标——`绑定密钥`（可用数/总数）、`累计调用`、`成功`、`失败`，底部一行显示该渠道的超时毫秒数。点击卡片打开配置弹窗。'
        },
        { type: 'heading', text: '「密钥」Tab', level: 4 },
        {
          type: 'list',
          items: [
            '顶部「基础 URL」是渠道级默认地址，右侧图标可一键复制。改这里会影响该渠道所有未做 key 级覆盖的密钥。',
            '中部状态摘要三格：已启用密钥数、总调用、失败次数。',
            '下方是密钥列表，每行展示别名、密钥提示（脱敏）、权重、成功、失败、成功率，以及额度摘要。状态不是「启用」时会出现一个警示图标，悬停显示停用原因（额度不足 / 限流冷却至某时刻 / 手动停用或鉴权失败）。Exa 的密钥还会额外显示管理密钥提示或「本地计费」，做了 key 级地址覆盖的会追加 `URL <host>`。',
            '行内操作从左到右：编辑、复制、测试密钥、查询官方额度、启用/停用、删除。复制和删除都需要二次确认或真实读取密文，注意「复制」会写一条揭示密钥的审计记录。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '删除是软删除',
          text: '删除密钥后列表不再显示，但历史统计与日志仍指向它；别名唯一性也**只约束未删除的密钥**，所以删除后可以用同一个别名重新添加。'
        },
        { type: 'heading', text: '「计费」Tab', level: 4 },
        {
          type: 'table',
          columns: ['字段', 'settings 键', '说明'],
          rows: [
            ['单价 / 请求 USD', '`price_per_request`', '每次成功调用折算的美元成本'],
            ['单价 / Credit USD', '`price_per_credit`', '上游返回 credits 用量时使用'],
            ['单价 / Token USD', '`price_per_token`', '上游返回 tokens 用量时使用'],
            ['默认计费 Credits', '`default_billable_credits`', '上游未返回 usage 时，一次成功搜索默认记多少 credits；0 表示不补']
          ]
        },
        {
          type: 'paragraph',
          text: '四个字段都为 0 时回退内置公开价目表。弹窗顶部固定标注「仅用于仪表盘成本估算，不是官方账单」，样例卡显示「100 次成功请求 × 当前单价」，并列出三条生效范围：仅新请求、成功 call 才计、可随时改。'
        },
        { type: 'heading', text: '「运行」Tab', level: 4 },
        {
          type: 'table',
          columns: ['字段', 'settings 键', '范围与默认值'],
          rows: [
            ['优先级', '`priority`（字段，不是 settings）', '最小 1，用于「优先级优先」路由'],
            ['权重', '`weight`（字段）', '最小 1，用于「权重优先 / 按权重随机」路由'],
            ['请求超时', '`timeout_ms`（字段）', '最小 1000 毫秒'],
            ['请求结果数', '`request_result_limit`', '最小 1，UI 默认 10；大于 0 时**强制覆盖**请求里的 `limit`'],
            ['换 key 重试', '`key_retry_count`', '0 – 20，默认 3；实际尝试次数 = 该值 + 1'],
            ['渠道并发', '`max_concurrency`', '0 表示不限，正数表示该渠道最大并发请求数'],
            ['Key 路由策略', '`key_routing_strategy`', '按权重排序轮询 / 最少使用优先 / 随机 / 按权重随机 / 权重优先']
          ]
        },
        { type: 'heading', text: '「高级」Tab', level: 4 },
        {
          type: 'list',
          items: [
            '「可重试错误」多选：`auth` 鉴权失败、`quota_exhausted` 额度耗尽、`rate_limited` 限流、`timeout` 超时、`upstream` 上游错误、`invalid_response` 响应异常。默认勾选前五项。清空该项则回到系统默认集合。',
            '「使用代理」开关与「代理地址」（例如 `http://127.0.0.1:7897`）。这是**渠道级**代理，该渠道下所有密钥共用；单条密钥可在「编辑密钥」弹窗里用「代理模式」覆盖它（跟随渠道 / 强制直连 / 使用独立代理）。该代理同时用于搜索请求、管理台「测试密钥」与官方额度查询；容器内访问宿主机代理时地址会被自动改写为 `host.docker.internal`。'
          ]
        },
        { type: 'heading', text: '「编辑密钥」弹窗', level: 4 },
        {
          type: 'table',
          columns: ['字段', 'API 字段', '说明'],
          rows: [
            ['别名', '`alias`', '必填；同一渠道内未删除的密钥中唯一'],
            ['基础 URL（选填）', '`base_url`', '留空回退渠道默认地址；填了就覆盖，**传空串即清除覆盖**。以 `#` 开头表示该地址即完整端点，网关不再拼接自己的路径（Jina 不支持）'],
            ['代理模式', '`proxy_mode`', '`inherit` 跟随渠道级代理（默认）、`direct` 强制直连、`custom` 用该密钥自己的地址'],
            ['代理地址（仅「使用独立代理」）', '`proxy_url`', '`custom` 模式下生效；地址留空时**回退渠道级代理**而不是直连。切回其它模式时该地址会被清空'],
            ['权重（1-10000，越大越优先）', '`weight`', '超出范围会被夹紧'],
            ['Exa 管理密钥（选填，仅 exa）', '`exa_service_key`', '用于查询 Exa 官方额度；留空表示不修改现状'],
            ['每分钟限制（0 表示不限）', '`rpm_limit`', '该密钥自身的分钟级并发上限'],
            ['日额度（0 表示不限）', '`daily_quota`', '按该密钥的当日请求数计'],
            ['月额度（0 表示不限）', '`monthly_quota`', '按该密钥的当月请求数计']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '保存弹窗会写入默认值',
          text: '打开渠道弹窗时，前端会把 `request_result_limit` 补成 10、`key_retry_count` 补成 3 后一起提交。也就是说，即使你只想改一个权重，只要点了「保存」，这两个默认值也会被持久化——`request_result_limit = 10` 之后，调用方传更大的 `limit` 就会在该渠道处被压回 10。'
        }
      ]
    },
    {
      id: 'console-tokens',
      title: '接口令牌',
      blocks: [
        {
          type: 'table',
          columns: ['列', '说明'],
          rows: [
            ['名称', '令牌的可读标识，仅用于管理'],
            ['令牌', '`osr_` 前缀加省略号；完整明文可用该行操作列的「复制令牌明文」按钮随时读取'],
            ['渠道', '`allowed_providers`；为空时显示「全部」标签，否则逐个列出渠道中文名'],
            ['状态', '启用（可调用）/ 停用（调用返回 401）'],
            ['RPM', '`rate_limit_per_min`，0 表示不限'],
            ['额度', '日额度与月额度两行，0 显示为「不限」'],
            ['使用', '`usage_count` 累计被成功鉴权的次数'],
            ['操作', '编辑、复制令牌明文、启用/停用、删除']
          ]
        },
        { type: 'heading', text: '新建 / 编辑表单', level: 4 },
        {
          type: 'list',
          items: [
            '名称：新建时默认「默认客户端」。',
            '允许请求渠道：多选，占位文案「不选择表示全部渠道」。',
            '每分钟限制、日额度、月额度：0 表示不限。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '明文可随时读取',
          text: '明文以密文形式留存在服务端，因此列表行可以随时「复制令牌明文」；若提示明文未留存，说明该令牌创建于密文列引入之前，只能重建。读取动作会写入审计日志，在「审计日志」页标记为高敏。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '界面上没有 scopes',
          text: '创建令牌时后端会写入 `scopes`（默认 `["search"]`），但管理台没有提供编辑入口，且**当前服务端没有任何按 `scopes` 鉴权的逻辑**——它目前只是记录用的元数据。因此不要指望用它来做权限收敛，真正生效的是「允许请求渠道」、RPM 与额度。'
        },
        {
          type: 'callout',
          tone: 'danger',
          title: '额度耗尽后表现为「令牌无效」',
          text: '日/月额度用尽时，令牌查询条件不再匹配，调用方收到的是 401 `invalid api token`，与「令牌不存在」或「令牌被停用」完全无法区分。只有 RPM 超限才是 429。排查时请到本页核对额度与「使用」列，而不是只看错误码。'
        }
      ]
    },
    {
      id: 'console-logs',
      title: '请求日志',
      blocks: [
        {
          type: 'list',
          items: [
            '右上角「自动刷新」开关默认打开，每 10 秒拉一次；**抽屉打开期间暂停刷新**，避免你正在看详情时列表跳动。',
            '顶部四个 KPI（近窗请求 / 成功 / 失败 / 缓存命中）统计的是**当前已加载的这批日志**，不是全量累计值。',
            '筛选区：关键字框（匹配 `query`、`request_id`、`error_message`）、状态（成功 / 失败）、模式（并发 / 转移 / 单平台）、缓存（命中 / 未命中）。这些筛选都在前端本地完成。'
          ]
        },
        { type: 'heading', text: '日志卡片', level: 4 },
        {
          type: 'paragraph',
          text: '每张卡片显示搜索词、时间、缩写的 `request_id`、状态标签、模式标签、`compat_format` 标签、缓存命中标签，以及本次涉及的渠道；有错误时下方追加一行错误信息。卡片右侧是耗时（失败标红，成功但超过 2000ms 标为慢）与 `N 条结果`。'
        },
        { type: 'heading', text: '详情抽屉的三个 Tab', level: 4 },
        {
          type: 'table',
          columns: ['Tab', '内容'],
          rows: [
            ['请求参数', '搜索词、模式、渠道、结果数、缓存策略、去重、状态、延迟、格式、Request ID；有错误时下方补一个错误框'],
            ['渠道调用', '按**每次尝试**一行：别名 · `第 N 次` · 耗时 · `N 条`，并附「缓存」「将重试」标记；点标题行可展开该次调用返回的结果列表'],
            ['合并结果 / 搜索结果', '本次请求最终的合并结果。当模式是并发、且存在多于一次渠道调用、且至少一次返回了结果时，标题显示「合并结果」，否则显示「搜索结果」']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '某次调用展开后是空的',
          text: '若该次调用标记为成功、摘要也显示了条数，但展开为空，页面会提示「调用摘要显示 N 条结果，但正文未写入日志（常见于 seed/旧数据）」——这是历史数据没有落库明细，不是本次调用失败。'
        },
        {
          type: 'paragraph',
          text: '列表默认拉取「系统设置 → 请求日志近窗条数」指定的条数（默认 100，可设 1–1000）。日志本身按「日志保留天数」（默认 3 天）每小时清理一次，超过保留期的记录会被删除。'
        }
      ]
    },
    {
      id: 'console-audit',
      title: '审计日志',
      blocks: [
        {
          type: 'paragraph',
          text: '审计日志固定拉取最近 100 条，记录管理动作（谁、在什么时间、对哪个对象做了什么）。顶部四个 KPI 分别是近窗事件、登录、配置变更、高敏动作。'
        },
        { type: 'heading', text: '风险分级规则', level: 4 },
        {
          type: 'table',
          columns: ['级别', '判定规则', '当前命中的动作'],
          rows: [
            ['`hot` 高敏', '在固定清单里，**或** 动作名包含 `reveal`、`rotate`', '`provider_key.reveal`、`settings.admin_api_key.rotate`'],
            ['`warn` 变更', '在固定清单里，**或** 动作名以 `.delete`、`.create`、`.update` 结尾', '`settings.update`、`provider.update`、`provider_key.create/update/delete`、`api_token.create/update/status/delete`、`admin.login.failed`'],
            ['`ok` 常规', '其余动作', '例如 `admin.login`、`admin.logout`、`provider_key.test`、`provider_key.quota`']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '登录失败被归为「变更」',
          text: '`admin.login.failed` 落在 `warn` 级别而不是高敏，因此「配置变更」计数会把登录失败一并算进去。判断暴力破解迹象时要看具体动作名，不能只看分级。'
        },
        { type: 'heading', text: '筛选与详情', level: 4 },
        {
          type: 'list',
          items: [
            '筛选区支持关键字（同时匹配操作者、动作、`request_id`、IP、资源类型、资源 ID、动作中文名与 metadata 全文）、风险级别、资源类型，以及一排动作前缀快捷按钮（全部 / 登录 / 密钥 / 令牌 / 设置 / 渠道）。',
            '点击卡片打开抽屉，展示操作者、动作、对象、IP、时间、风险级别，以及 metadata 的格式化 JSON。卡片上最多预览 3 个标量 metadata 键值。'
          ]
        }
      ]
    },
    {
      id: 'console-settings',
      title: '系统设置',
      blocks: [
        {
          type: 'paragraph',
          text: '页面分五个分区，修改后由底部悬浮条的「保存设置」统一提交；未保存时切走会丢失改动，可用「放弃更改」回到服务端当前值。'
        },
        { type: 'heading', text: '搜索默认值', level: 4 },
        {
          type: 'table',
          columns: ['字段', 'API 字段', '出厂默认'],
          rows: [
            ['默认模式', '`default_mode`', '并发聚合（`parallel`）'],
            ['汇总返回结果数', '`default_limit`', '10（最小 1）'],
            ['默认平台', '`default_providers`', '前八家通用搜索渠道（不含 `context7`）'],
            ['结果去重', '`default_dedupe`', '开'],
            ['平台路由策略', '`provider_routing_strategy`', '固定顺序（`fixed`）']
          ]
        },
        { type: 'heading', text: '运行与接口', level: 4 },
        {
          type: 'table',
          columns: ['字段', 'API 字段', '范围与默认'],
          rows: [
            ['请求超时 (ms)', '`request_timeout_ms`', '最小 1000，默认 20000'],
            ['接口令牌鉴权', '`api_auth_required`', '默认开；关闭后允许匿名调用搜索 API'],
            ['健康统计窗口 (分钟)', '`provider_health_window_minutes`', '1 – 1440，默认 15'],
            ['日志保留天数', '`log_retention_days`', '1 – 365，默认 3'],
            ['请求日志近窗条数', '`search_logs_limit`', '1 – 1000，默认 100']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '关闭鉴权的影响面',
          text: '把「接口令牌鉴权」关掉后，`/v1/search`、三个兼容端点与 MCP 的工具调用都不再校验令牌，`allowed_providers` 与每令牌 RPM/额度也随之失效。页面会显示橙色警告条，仅建议本地调试时使用。'
        },
        { type: 'heading', text: '搜索缓存（全局）', level: 4 },
        {
          type: 'list',
          items: [
            '启用缓存 `cache_enabled`（出厂关闭）、缓存 TTL `cache_ttl_seconds`（默认 3600，0 表示不缓存）、最大缓存结果数 `cache_max_results`（默认 20，0 表示不截断）。',
            '分区副标题写明三条语义：整次搜索结果缓存、`parallel` 部分失败不写缓存、相同请求会被 singleflight 合并成一次上游调用。',
            '缓存以「整次搜索响应」为单位，请求级 `cache` 策略可覆盖全局开关（见「搜索 API」章节）。'
          ]
        },
        { type: 'heading', text: '安全', level: 4 },
        {
          type: 'list',
          items: [
            '展示当前管理员 API Key 的前缀（未生成时显示「未生成」标签），按钮为「复制明文」与「随机生成」/「重新随机生成」。',
            '「复制明文」会请求服务端解密后直接写入剪贴板，每次读取都写入审计日志（高敏动作）；未生成 Key 时不展示该按钮。',
            '点击「随机生成」后会二次确认「生成新的管理员 API Key 后，旧 Key 将立即失效」；确认后新明文显示在提示条中，可立即复制。',
            '管理员 API Key（`oak_`）可用于所有管理接口，也可直接调用搜索接口与 MCP；它不受令牌的 `allowed_providers`、RPM 与额度限制。'
          ]
        },
        { type: 'heading', text: '兼容接口', level: 4 },
        {
          type: 'paragraph',
          text: '三个开关分别控制 Tavily、Serper、OpenAI 兼容端点是否可用，默认全部打开。关闭后访问对应路径返回 404 并附带「compatibility endpoint is disabled」，可用于在不删除调用方配置的前提下临时下线其中一个入口。'
        }
      ]
    }
  ]
}
