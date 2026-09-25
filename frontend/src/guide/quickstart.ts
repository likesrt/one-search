/**
 * 章节：快速开始
 *
 * 覆盖范围：把 One Search 从零跑通的最短路径——启用渠道 → 添加上游 Key → 测试 →
 * 创建接口令牌 → 发第一次请求 →（可选）接入 MCP 客户端。
 *
 * 代码依据：
 * - `backend/internal/api/handlers.go`（路由挂载与鉴权中间件）
 * - `backend/internal/db/store.go`（RuntimeSettings / CreateAPIToken 默认值）
 * - `frontend/src/views/ProvidersView.vue`、`TokensView.vue`（管理台操作与按钮文案）
 */

import type { DocChapter } from './types'

export const quickstartChapter: DocChapter = {
  id: 'doc-quickstart',
  title: '快速开始',
  sections: [
    {
      id: 'qs-entry',
      title: '先认清三类调用入口',
      blocks: [
        {
          type: 'paragraph',
          text: '管理台只负责配置与调试；真正的搜索调用走下面三类入口。三者共用同一套渠道、缓存、日志、用量统计与令牌限制。'
        },
        {
          type: 'table',
          columns: ['入口', '路径', '用途'],
          rows: [
            ['原生搜索', '`POST /v1/search`', '字段最全（`mode`/`limit`/`freshness`/`dedupe`/`rerank`/`cache`/`include_raw`/`options`），新接入优先用它'],
            ['网页抓取', '`GET|POST /v1/fetch`', '抓取指定 URL 的正文，HTML 转紧凑 Markdown 并按字符数截断，可续读；与搜索链路独立'],
            ['兼容接口', '`POST /v1/compat/tavily/search`、`/v1/compat/serper/search`、`/v1/compat/openai/responses-search`', '让已在用 Tavily / Serper / OpenAI 形态的客户端少改代码即可切过来'],
            ['MCP', '`POST /mcp`（默认路径，可用 `MCP_PATH` 改）', '给 MCP 客户端（Codex / Claude Desktop / Cursor / LobeHub 等）暴露 `search` 与 `fetch` 两个工具']
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '鉴权开关',
          text: '默认 `api_auth_required = true`，三类入口都要求 `Authorization: Bearer <令牌>` 或 `X-API-Key: <令牌>`。可在「系统设置 → 运行与接口 → 接口令牌鉴权」关闭，关闭后允许匿名调用（仅建议本地调试）。'
        }
      ]
    },
    {
      id: 'qs-provider',
      title: '第一步：启用渠道并添加上游 Key',
      blocks: [
        {
          type: 'steps',
          items: [
            {
              title: '进入「平台管理」',
              text: '八家渠道（Exa / You.com / Jina / Tavily / Firecrawl / Serper / Brave Search / Keenable）以卡片形式列出。卡片右上角的开关控制渠道是否参与搜索，关掉的渠道即使被请求指定也会被跳过。'
            },
            {
              title: '点击卡片进入配置弹窗',
              text: '弹窗内分「密钥 / 计费 / 运行 / 高级」四个 Tab。先在「密钥」Tab 点标题右侧的加号，填入该渠道的 API Key。Exa 还可以额外填「Exa 管理密钥」，用于查询官方用量。'
            },
            {
              title: '确认渠道已启用且至少有 1 把可用密钥',
              text: '卡片上的「绑定密钥」显示「可用密钥数 / 密钥总数」。可用数为 0 时，渠道会被判定为不可用，「搜索调试」页也不会把它列进可选平台。'
            },
            {
              title: '点「测试密钥」做一次真实搜索',
              text: '密钥行右侧的刷新图标会真的调用上游。成功会弹出「返回 N 条结果，耗时 X ms」；失败会显示具体错误原因，并同时更新该密钥的成功/失败统计与状态。'
            },
            {
              title: '按需调整「运行」Tab',
              text: '「请求结果数」默认 10、「换 key 重试」默认 3、「渠道并发」默认 0（不限）。保持默认即可先跑通，后续再按渠道限流情况细化。'
            }
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '注意「请求结果数」是强制覆盖',
          text: '渠道级 `request_result_limit` 一旦配置为大于 0 的值，**每次请求**打往该渠道时都会用它替换请求里的 `limit`，与调用方是否显式传了 `limit` 无关。在弹窗里保存过「运行」Tab 就会写入默认值 10，届时调用方传 `limit: 30` 也只会拿到该渠道的 10 条（最终合并结果数另受 `limit` 约束）。'
        }
      ]
    },
    {
      id: 'qs-token',
      title: '第二步：创建接口令牌',
      blocks: [
        {
          type: 'steps',
          items: [
            {
              title: '进入「接口令牌」，点右上角加号',
              text: '填写名称、RPM、日额度、月额度。额度填 0 表示不限。'
            },
            {
              title: '按需限制「允许请求渠道」',
              text: '不选择表示全部渠道。选择后，调用方请求未授权的渠道会直接得到 403。注意：该限制**只对 `/v1/search` 与 MCP 生效，三个兼容端点不受它约束**，详见「兼容接口」与「凭据与配额」章节。'
            },
            {
              title: '立即复制明文令牌',
              text: '保存成功后页面顶部会出现绿色提示条，其中 `osr_` 开头的明文只显示这一次，之后列表里只保留前缀。'
            }
          ]
        },
        {
          type: 'code',
          lang: 'text',
          title: '三种凭据前缀',
          content: `osr_xxx   接口令牌（在「接口令牌」页创建，受 RPM / 日额度 / 月额度 / 允许渠道约束）
oak_xxx   管理员 API Key（在「系统设置 → 安全」生成，拥有完整管理权限，不受上述约束）
adm_xxx   管理台登录会话令牌（登录后自动签发，存在浏览器 sessionStorage，仅用于管理接口）`
        },
        {
          type: 'callout',
          tone: 'danger',
          title: '明文只出现一次',
          text: '`osr_` 与 `oak_` 的明文都只在创建/轮换的响应里返回一次，服务端只保存哈希与密文。丢了只能重新生成；轮换管理员 API Key 会让旧 Key 立即失效。'
        }
      ]
    },
    {
      id: 'qs-first-call',
      title: '第三步：发一次搜索',
      blocks: [
        {
          type: 'code',
          lang: 'bash',
          title: '原生搜索接口',
          content: `curl -sS http://localhost:5173/v1/search \\
  -H "Authorization: Bearer osr_xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "query": "latest web search APIs",
    "mode": "parallel",
    "limit": 5,
    "dedupe": true,
    "cache": "default"
  }'`
        },
        {
          type: 'paragraph',
          text: '响应固定是 `{ results, providers, meta }` 三段：`results` 是合并去重后的结果，`providers` 是每个渠道的调用摘要（含状态、错误类型、耗时、条数），`meta` 含 `request_id`、`mode`、`latency_ms`、`total_results`、`deduped_results`、`cache_hit`、`cache_key`、`providers_queried`。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '全部渠道失败时 HTTP 仍是 200',
          text: '搜索链路不会因为渠道报错而返回 5xx，失败体现在 `providers[].status = "error"`、`providers[].error_type` 与 `meta` 上。只有请求体非法、缺少 `query` 或鉴权失败才返回 4xx/5xx。客户端必须检查响应体，不能只看状态码。'
        }
      ]
    },
    {
      id: 'qs-mcp',
      title: '第四步：接入 MCP 客户端（可选）',
      blocks: [
        {
          type: 'steps',
          items: [
            {
              title: '确认服务端已启用 MCP',
              text: 'MCP 默认关闭。需要在部署环境设置 `MCP_ENABLED=true`（可选 `MCP_PATH`，默认 `/mcp`），重启服务后 `GET /mcp` 应返回含 `"tools": ["search", "fetch"]` 的 JSON（在管理台「网页抓取」页关闭抓取后只有 `search`）。'
            },
            {
              title: '在客户端填入 URL 与令牌',
              text: 'URL 填 `http://localhost:5173/mcp`（换成客户端实际能访问到的地址），令牌用 `Authorization: Bearer osr_xxx` 或 `X-API-Key: osr_xxx`。'
            },
            {
              title: '用客户端自带的连接测试确认',
              text: 'Codex 里输入 `/mcp` 应看到 `one_search` 与 `search`、`fetch` 两个工具；其它客户端应能看到工具列表。拿不到工具列表时先看「MCP 配置 → 排错清单」。'
            }
          ]
        },
        {
          type: 'callout',
          tone: 'info',
          title: '别忘了地址可达性',
          text: '`initialize`、`ping`、`tools/list` 这类方法**不需要鉴权**就能返回，因此「能连上但搜不出结果」通常是令牌问题；而「连都连不上」通常是地址问题——特别是容器里的客户端，`localhost` 指向它自己。'
        }
      ]
    }
  ]
}
