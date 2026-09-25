/**
 * 章节：凭据与配额
 *
 * 覆盖范围：三种凭据前缀的权限边界、`scopes` 的现状、`allowed_providers` 的作用范围、
 * RPM 与日/月额度的判定位置与表现、上游密钥状态机与自动流转、
 * 官方额度自动刷新节流、日志保留与清理。
 *
 * 代码依据：
 * - `backend/internal/api/auth.go`（`Login` 签发 `adm_`、`requireAdmin`、`requireAPIToken`、`allowToken`）
 * - `backend/internal/api/handlers.go`（`applyTokenProviders` 仅用于原生搜索路径）
 * - `backend/internal/api/mcp.go`（MCP 工具调用同样应用白名单）
 * - `backend/internal/db/store.go`（`FindAPIToken` 的额度条件、`RecordKeyResult`、`ListAvailableProviderKeys`、
 *   `DeleteAPIToken` 的用量归并、`DeleteOldLogs` 默认 3 天）
 * - `backend/internal/search/orchestrator.go`（`refreshOfficialQuota` 节流、`autoRefreshOfficialQuota`）
 * - `backend/cmd/server/main.go`（每小时清理一次）
 */

import type { DocChapter } from './types'

export const authQuotaChapter: DocChapter = {
  id: 'doc-auth-quota',
  title: '凭据与配额',
  sections: [
    {
      id: 'auth-credentials',
      title: '三种凭据的权限边界',
      blocks: [
        {
          type: 'table',
          columns: ['前缀', '来源', '可用于', '受限维度'],
          rows: [
            ['`osr_`', '「接口令牌」页创建', '`/v1/search`、三个兼容端点、MCP `tools/call`、`/v1/providers`、`/v1/usage/summary`', 'RPM、日/月额度、`allowed_providers`（**仅原生搜索与 MCP 生效**）'],
            ['`oak_`', '「系统设置 → 安全」生成', '上述全部入口，以及所有 `/api/admin/*` 管理接口', '不受令牌额度、RPM 与 `allowed_providers` 约束'],
            ['`adm_`', '管理台登录成功后签发', '仅 `/api/admin/*`', '内存会话，默认 24 小时过期；重启服务即失效']
          ]
        },
        {
          type: 'paragraph',
          text: '管理接口的鉴权接受「有效的 `adm_` 会话」或「`oak_` 管理员 API Key」二者之一。`adm_` 会话是进程内的内存映射（不是数据库记录），所以服务重启后需要重新登录。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '`oak_` 是把万能钥匙',
          text: '用 `oak_` 调用 `/v1/search` 或 MCP 时，请求被视为管理员发起：既不校验 `allowed_providers`，也不消耗任何令牌的 RPM 与额度。它只应在服务端之间的可信调用中使用，不要下发到最终用户侧。'
        }
      ]
    },
    {
      id: 'auth-scopes',
      title: 'scopes 与 allowed_providers',
      blocks: [
        { type: 'heading', text: 'scopes 目前只是元数据', level: 4 },
        {
          type: 'paragraph',
          text: '创建令牌时后端会写入 `scopes`（缺省 `["search"]`），列表中也会返回该字段。但**当前代码里没有任何按 `scopes` 做鉴权或门控的逻辑**——它既不限制可用端点，也不影响任何行为，纯粹是记录。请不要依赖它做权限收敛；真正生效的是 `allowed_providers`、RPM 与日/月额度。管理台也没有提供编辑 `scopes` 的入口。'
        },
        { type: 'heading', text: 'allowed_providers 的实际作用范围', level: 4 },
        {
          type: 'table',
          columns: ['请求的 `providers`', '令牌白名单', '结果'],
          rows: [
            ['未指定', '空（全部）', '使用系统默认平台'],
            ['未指定', '非空', '**直接使用白名单**作为本次的渠道列表'],
            ['已指定，全部在白名单内', '非空', '按请求指定的渠道执行'],
            ['已指定，有任一不在白名单内', '非空', '**403** `api token is not allowed to request provider <name>`']
          ]
        },
        {
          type: 'callout',
          tone: 'danger',
          title: '只对 `/v1/search` 与 MCP 生效',
          text: '调用 `applyTokenProviders` 的只有两处：原生搜索处理器与 MCP 的 `tools/call`。三个兼容端点（`/v1/compat/tavily/search`、`/v1/compat/serper/search`、`/v1/compat/openai/responses-search`）**完全没有等价逻辑**，白名单对它们不起作用。这是当前实现的既有缺口：只做「如实记录」，未做收敛。需要渠道级权限控制时，请只开放原生接口与 MCP。'
        }
      ]
    },
    {
      id: 'auth-rate-quota',
      title: 'RPM 与日/月额度',
      blocks: [
        {
          type: 'table',
          columns: ['限制', '判定位置', '超限表现'],
          rows: [
            ['`rate_limit_per_min`', '进程内内存计数：以该令牌本分钟窗口内的累计次数对比上限', '**429** `api token rate limit exceeded`'],
            ['`daily_quota`', '令牌查询的 SQL 条件：当日请求数合计 < 额度才匹配到令牌', '**401** `invalid api token`'],
            ['`monthly_quota`', '同上，按自然月统计', '**401** `invalid api token`']
          ]
        },
        {
          type: 'list',
          items: [
            '额度为 0 表示不限；日/月额度统计的是**请求次数**（`requests_total`），不是结果条数或费用。',
            'RPM 计数保存在服务进程内，重启后清零；多实例部署时每个实例各自计数，实际放行量会是「实例数 × 上限」。',
            '日/月额度用尽后，令牌查询直接查不到记录，因此错误信息与「令牌不存在 / 已停用」完全相同。**只有 RPM 超限才是 429，额度耗尽不是。**',
            '删除令牌时，它会先把该令牌的用量归并到「无令牌」维度再删除记录，因此历史统计不会凭空减少。'
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '自动额度刷新有节流',
          text: '每次真实使用某个上游密钥后都会尝试刷新它的官方额度，但同一把密钥在上一次刷新「开始后」或「结束后」不足间隔时间内会被跳过，且同一时刻只允许一个刷新在途。间隔按渠道区分：exa 为 5 分钟，其余支持刷新的渠道为 1 分钟。`serper`、`brave`、`keenable` 不参与自动刷新（分别是无官方余额接口、查询本身要消耗一次真实请求、无官方额度接口），只能手动点「查询官方额度」。'
        }
      ]
    },
    {
      id: 'auth-key-status',
      title: '上游密钥状态机',
      blocks: [
        {
          type: 'paragraph',
          text: '渠道密钥的状态由每次真实调用后的结果自动改写。触发点是「一次尝试的最终结果」，包括换 key 重试过程中的每一次尝试。'
        },
        {
          type: 'table',
          columns: ['本次结果', '写入的状态', '副作用'],
          rows: [
            ['成功', '`enabled`', '`current_failures` **清零**，`total_successes` +1'],
            ['`auth`', '`disabled`', '`total_failures` +1，`current_failures` +1'],
            ['`quota_exhausted`', '`exhausted`', '同上'],
            ['`rate_limited`', '`cooling`', '额外写入 `cooldown_until = 当前时间 + 15 分钟`，`total_failures` +1'],
            ['其它错误（`timeout` / `upstream` / `invalid_response` / `no_key`）', '**保持 `enabled`**', '`total_failures` +1，`current_failures` +1，但状态不变']
          ]
        },
        {
          type: 'list',
          items: [
            '被选中参与请求的前提是「状态为 `enabled`」或「状态为 `cooling` 且冷却时间已过」。因此 `cooling` 的密钥 15 分钟后会**自动重新可用**。',
            '`disabled` 与 `exhausted` **不会自动恢复**，需要人工在「平台管理」里点启用，或等一次成功调用把它改回 `enabled`——但后者的前提是它得先被选中，而这两种状态都不会被选中。',
            '`current_failures` 只是累加，仅在成功时清零，**代码里没有任何基于失败次数的阈值判断**，它不会自动把密钥停用。管理台把它作为排查线索展示，不参与决策。'
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '超时与上游错误不会让密钥进入冷却',
          text: '`timeout`、`upstream`、`invalid_response` 这类常见抖动只会累加失败计数，密钥状态仍是 `enabled`，会被立刻再次选中。所以「密钥一直在被用但一直失败」是预期行为，需要靠渠道级的 `key_retry_count` 与「可重试错误」配置，或人工停用来止损。'
        }
      ]
    },
    {
      id: 'auth-retention',
      title: '日志保留与清理',
      blocks: [
        {
          type: 'list',
          items: [
            '「日志保留天数」默认 **3 天**（可设 1–365），同时作用于搜索请求日志与审计日志。',
            '服务启动时立即执行一次清理，之后**每小时执行一次**；同一轮还会删除已过期的搜索缓存。',
            '「请求日志近窗条数」默认 100（上限 1000），决定请求日志页一次拉取多少条，与保留天数无关。',
            '审计日志接口固定返回最近 100 条。'
          ]
        },
        {
          type: 'paragraph',
          text: '删除密钥采用软删除：密钥记录保留、状态标记为已删除，因此历史统计与日志仍能对上，但别名唯一性只约束未删除的密钥——同一个别名可以在删除后重新使用。'
        }
      ]
    }
  ]
}
