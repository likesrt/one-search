/**
 * 章节：路由与重试
 *
 * 覆盖范围：三种 `mode` 的精确语义、渠道级路由策略、Key 级选择策略、
 * 换 key 重试流程与默认可重试错误集、权重/重试次数夹紧、超时叠加规则、错误类型分类。
 *
 * 代码依据：
 * - `backend/internal/search/orchestrator.go`（`executeSearch`、`searchParallel`、`searchFallback`、
 *   `shouldContinueFallback`、`searchSingle`、`callProvider`、`shouldRetryWithNextKey`、
 *   `routeProviders`、`weightedProviderOrder`、`filterEnabledProviders`、
 *   `effectiveRequestTimeoutMS`、`providerKeyRetryCounts`）
 * - `backend/internal/keypool/manager.go`（`orderKeys`、`usesPosition`、`weightedKeyOrder`、两趟排除逻辑）
 * - `backend/internal/db/store.go`（`ListAvailableProviderKeys` 过滤条件、`clampWeight`、`RecordKeyResult`）
 * - `backend/internal/provider/errors.go`（`ClassifyHTTPError`）
 */

import type { DocChapter } from './types'

export const routingChapter: DocChapter = {
  id: 'doc-routing',
  title: '路由与重试',
  sections: [
    {
      id: 'routing-mode',
      title: '三种搜索模式',
      blocks: [
        {
          type: 'table',
          columns: ['`mode`', '执行方式', '遇到的失败怎么处理', '写缓存条件'],
          rows: [
            ['`parallel`（默认）', '并发请求所有目标渠道，按渠道在请求里的顺序收集结果', '某个渠道失败不影响其它渠道；失败只体现在 `providers[]` 里', '**任何渠道报错就不写缓存**'],
            ['`fallback`', '按顺序一次只调一个渠道', '该渠道报错、或返回 0 条结果，都继续试下一个；一旦某个渠道返回了至少 1 条结果就**立即停止**', '只要有任意一个渠道成功就写'],
            ['`single`', '只调用渠道列表里的**第一个**渠道', '不换渠道，失败就是整体失败', '该渠道成功就写']
          ]
        },
        {
          type: 'list',
          items: [
            '未识别的 `mode` 字符串按 `parallel` 处理——不会有报错提示，只是行为等同默认。',
            '`fallback` 的「停止条件」是**拿到非空结果**，而不是「调用成功」：上游返回 200 但零结果时仍会继续往下一个渠道试。',
            '三种模式下的结果排序与 `limit` 截断规则相同：先按分数降序、同分按标题升序，最后截断。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '`fallback` 不区分错误类型',
          text: '代码里对 `auth`、`quota_exhausted`、`rate_limited`、`timeout`、`upstream`、`no_key`、`invalid_response` 以及未知类型，一律继续尝试下一个渠道。也就是说，即使第一个渠道是「密钥无效」这种不该重试的错误，`fallback` 也会照常换渠道。'
        }
      ]
    },
    {
      id: 'routing-provider',
      title: '渠道级路由策略',
      blocks: [
        {
          type: 'paragraph',
          text: '策略来自「系统设置 → 搜索默认值 → 平台路由策略」（`provider_routing_strategy`），**只在请求没有显式传 `providers` 时生效**。请求体里出现 `providers` 字段（哪怕是空数组以外的任何内容）就会跳过排序，完全按调用方给的顺序执行。渠道数少于 2 时也不排序。'
        },
        {
          type: 'table',
          columns: ['取值（UI 文案）', '排序规则'],
          rows: [
            ['`priority`（优先级优先）', '按渠道 `priority` 升序；相同则按渠道名字典序升序'],
            ['`weighted`（权重优先）', '按渠道 `weight` 降序；相同则按 `priority` 升序'],
            ['`random`（随机）', '随机打乱'],
            ['`weighted_random`（按权重随机）', '按权重无放回地逐个抽取（`weight <= 0` 视为 1）'],
            ['`available_keys`（可用 Key 优先）', '按可用密钥数降序；相同则按 `priority` 升序'],
            ['`fixed`（固定顺序）', '**不改变顺序**']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '`fixed` 在代码里没有专门分支',
          text: '实现是一个 `switch`，只对上面除 `fixed` 外的五个取值做了排序，其余取值（含 `fixed` 与任何拼错的值）都走到 `default` 分支原样返回。所以「固定顺序」的实际语义就是「不干预调用方给的顺序」，写一个不存在的策略名不会报错，效果与 `fixed` 相同。'
        },
        {
          type: 'paragraph',
          text: '排序之后还有一道**渠道启用过滤**：已停用的渠道会被从列表里剔除——这一步对显式传入 `providers` 的请求同样生效，因此向已停用的渠道发请求不会拿到它的结果，也不会产生该渠道的调用记录。'
        }
      ]
    },
    {
      id: 'routing-key',
      title: 'Key 级选择策略',
      blocks: [
        {
          type: 'paragraph',
          text: '策略来自渠道的 `key_routing_strategy`（平台管理 → 运行 Tab → Key 路由策略）。候选池的准入条件是：所属渠道已启用、密钥状态为 `enabled`（或 `cooling` 且冷却时间已过）、且未超出该密钥的日/月额度。已删除的密钥不在池内。'
        },
        {
          type: 'table',
          columns: ['取值（UI 文案）', '排序规则', '是否轮转游标'],
          rows: [
            ['未配置 / 未知值（按权重排序轮询）', '基础顺序为 `weight` 降序、`last_used_at` 最旧的优先、`id` 升序', '**是**，每次取用后游标前移，形成轮询'],
            ['`least_used`（最少使用优先）', '按 `total_successes + total_failures` 升序；相同则 `last_used_at` 更早的优先；再相同按 `id` 升序', '否'],
            ['`random`（随机）', '随机打乱', '否'],
            ['`weighted_random`（按权重随机）', '按权重无放回抽取', '否'],
            ['`weight_priority`（权重优先）', '先随机打乱，再按权重降序**稳定**排序：高权重档位优先，同档内随机', '否']
          ]
        },
        {
          type: 'paragraph',
          text: '唯一使用轮转游标的是默认策略；其余四种每次都从头开始按排序结果取，因此「排序第一」的密钥会被持续优先使用（`weight_priority` 与 `random` 因为带随机打乱，表现为档内/全池随机）。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '「渠道并发」限制在取 key 阶段生效',
          text: '`max_concurrency` 大于 0 时，若该渠道当前正在进行的请求数已达上限，取 key 会直接失败，错误类型被归类为 `rate_limited`（消息为「所有密钥都在限流或繁忙」）。它不会等待排队，也不会自动重试其它渠道——在 `parallel` 模式下只是这个渠道这一轮失败。'
        }
      ]
    },
    {
      id: 'routing-retry',
      title: '换 Key 重试',
      blocks: [
        {
          type: 'paragraph',
          text: '单个渠道内的重试是**换一把 key 再试**，不是同 key 重发。总尝试次数 = 渠道的 `key_retry_count` + 1（默认 3，因此默认最多 4 次），该值在 0–20 之间夹紧。'
        },
        {
          type: 'steps',
          items: [
            {
              title: '取一把可用 key',
              text: '从候选池里排除本次请求已经试过的 key（第一趟），选不到时再忽略排除集重来一遍——因此只有一个 key 的渠道仍会对同一把 key 重试，而多 key 渠道会尽量换新。'
            },
            {
              title: '按当前 key 重建适配器并发起请求',
              text: '每次尝试都会用当前 key 的 `base_url` 覆盖值重建适配器，所以同一渠道的不同 key 可以指向不同中转站。'
            },
            {
              title: '回收 key 并记录结果',
              text: '无论成功失败都会写回该 key 的状态与成功/失败计数，并触发一次（受节流限制的）官方额度刷新。'
            },
            {
              title: '判断要不要换 key 再试',
              text: '只有当「还有剩余尝试次数」且「该错误类型在可重试集合里」时才继续；否则结束这个渠道。'
            }
          ]
        },
        {
          type: 'table',
          columns: ['错误类型', '默认是否换 key 重试', '典型来源'],
          rows: [
            ['`auth`', '**是**', '上游 401 / 403，且响应体不含额度相关字样'],
            ['`quota_exhausted`', '**是**', '上游 402，或 401/403/其它状态但响应体命中额度关键词'],
            ['`rate_limited`', '**是**', '上游 429，或渠道并发已达上限'],
            ['`timeout`', '**是**', '请求超时'],
            ['`upstream`', '**是**', '其它上游错误、渠道未注册'],
            ['`invalid_response`', '**否**', '响应不是合法 JSON'],
            ['`no_key`', '**否**（取不到 key 会直接结束该渠道，见下方说明）', '该渠道取不到任何可用密钥']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '配置了「可重试错误」就以配置为准',
          text: '渠道「高级」Tab 的「可重试错误」多选一旦非空，默认集合会被**整体替换**成勾选项。勾上「响应异常」就会重试 `invalid_response`；把默认五项取消勾选而只留一项，其它类型就不会再重试。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '取 key 失败不进入重试循环',
          text: '「无可用密钥」与「并发已满」发生在取 key 阶段，代码会立即结束该渠道，不消耗 `key_retry_count` 的额度。`fallback` 模式下这仍会让流程继续试下一个渠道。'
        }
      ]
    },
    {
      id: 'routing-limits',
      title: '超时、权重与错误分类',
      blocks: [
        { type: 'heading', text: '两层超时', level: 4 },
        {
          type: 'list',
          items: [
            '**单次尝试超时**：渠道 `timeout_ms` 大于 0 时，每次尝试各带一个独立的超时；未配置（或 ≤0）时回落到全局 `REQUEST_TIMEOUT_MS` 环境变量，默认 20 秒。',
            '**整次请求超时**：取「系统设置的请求超时」与「按各渠道推算出的最小需求」中的较大值。单个渠道的需求 = 渠道超时 + 1000ms；若该渠道 `key_retry_count > 0`，再加一个渠道超时（即按两次尝试预估）。都没配时兜底 20000ms。',
            '因此调大渠道超时或重试次数会同时放宽整体超时，不需要手动同步改设置。'
          ]
        },
        { type: 'heading', text: '数值夹紧与默认值', level: 4 },
        {
          type: 'table',
          columns: ['字段', '规则'],
          rows: [
            ['Key 权重 `weight`', '创建与更新时都夹紧到 **1 – 10000**（≤0 视为 1，>10000 视为 10000）；路由计算时非正值按 1 计'],
            ['`key_retry_count`', '夹紧到 **0 – 20**，缺省 3'],
            ['`max_concurrency`', '负数按 0 处理，0 表示不限'],
            ['`rpm_limit` 等额度', '0 表示不限']
          ]
        },
        { type: 'heading', text: '上游状态码如何变成错误类型', level: 4 },
        {
          type: 'table',
          columns: ['上游响应', '判定结果'],
          rows: [
            ['401 / 403', '响应体命中 `insufficientbalance`、`insufficient balance`、`quota`、`credit` 任一关键词 → `quota_exhausted`，否则 `auth`'],
            ['402', '`quota_exhausted`'],
            ['429', '`rate_limited`'],
            ['其它 4xx / 5xx', '命中上述额度关键词 → `quota_exhausted`，否则 `upstream`'],
            ['响应体不是合法 JSON', '`invalid_response`'],
            ['上下文超时', '`timeout`']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '额度关键词匹配可能误判',
          text: '判定是在**整个响应体的小写文本**里找 `quota` 或 `credit`。如果某个上游的鉴权失败信息里恰好包含这些词（例如「your plan credit」），就会被归类为 `quota_exhausted`，从而触发换 key 重试并可能把密钥状态置为 `exhausted`。排查时请以「渠道调用」面板里的原始错误文本为准。'
        }
      ]
    }
  ]
}
