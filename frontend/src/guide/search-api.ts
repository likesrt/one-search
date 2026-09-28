/**
 * 章节：搜索 API
 *
 * 覆盖范围：原生搜索端点 `POST /v1/search` 的鉴权、请求字段全表、响应字段、
 * 缓存语义、错误码，以及 curl / Python / Node / Go 四份示例。
 *
 * 代码依据：
 * - `backend/internal/api/handlers.go`（`runSearch`：query 校验、`applyTokenProviders`、错误码）
 * - `backend/internal/api/auth.go`（`requireAPIToken`：401/429 分支）
 * - `backend/internal/model/search.go`（`SearchRequest`/`SearchResponse`/`SearchMeta` 字段与 JSON 标签）
 * - `backend/internal/search/orchestrator.go`（`applyDefaults`、`mergeResults`、`cacheKey`、
 *   `cacheTTLSeconds`、`truncateResultsForCache`、singleflight）
 */

import type { DocChapter } from './types'

export const searchApiChapter: DocChapter = {
  id: 'doc-search-api',
  title: '搜索 API',
  sections: [
    {
      id: 'api-auth',
      title: '端点与鉴权',
      blocks: [
        {
          type: 'code',
          lang: 'http',
          title: '请求',
          content: `POST /v1/search
Authorization: Bearer osr_xxx
Content-Type: application/json`
        },
        {
          type: 'paragraph',
          text: '令牌可以放在 `Authorization: Bearer <令牌>`，也可以放在 `X-API-Key: <令牌>`；两者同时存在时优先取 `Authorization`。'
        },
        {
          type: 'list',
          items: [
            '接受 `osr_` 接口令牌，也接受 `oak_` 管理员 API Key。',
            '使用 `oak_` 时请求被视为管理员发起的，**不校验 `allowed_providers`，也不消耗任何令牌的 RPM 与额度**。',
            '关闭「系统设置 → 接口令牌鉴权」后，本端点允许匿名调用。'
          ]
        },
        {
          type: 'paragraph',
          text: '同一前缀下还有三个只读端点，都需要同样的令牌：`GET /v1/providers` 返回渠道配置列表，`GET /v1/usage/summary` 返回累计用量摘要，`GET /v1/fetch` 抓取并读取一个网页（详见「网页抓取」章节）——抓取与搜索链路独立，不走渠道编排，也不写搜索日志。'
        }
      ]
    },
    {
      id: 'api-request',
      title: '请求字段',
      blocks: [
        {
          type: 'table',
          columns: ['字段', '类型', '必填', '说明'],
          rows: [
            ['`query`', 'string', '是', '搜索词。首尾空白会被裁掉；裁掉后为空返回 400 `query is required`'],
            ['`providers`', 'string[]', '否', '渠道白名单，取值 `exa`/`you`/`jina`/`tavily`/`firecrawl`/`serper`/`brave`/`keenable`/`context7`。不传时用系统默认平台，再回退内置八家。**`context7` 不在默认渠道列表内**，只有显式指定才会参与'],
            ['`mode`', 'string', '否', '`parallel` / `fallback` / `single`，默认取「默认模式」（出厂 `parallel`）'],
            ['`limit`', 'number', '否', '返回结果数上限。`<= 0` 时取「汇总返回结果数」，再回退 10'],
            ['`freshness`', 'string', '否', '时间新鲜度提示，由各渠道自行解释（见「渠道配置详解」）'],
            ['`dedupe`', 'boolean', '否', '是否按 URL 去重。缺省取「结果去重」（出厂 `true`）'],
            ['`rerank`', 'boolean', '否', '参与缓存键与请求日志，但当前实现没有改变结果排序'],
            ['`cache`', 'string', '否', '`default` / `refresh` / `bypass`，默认 `default`'],
            ['`include_raw`', 'boolean', '否', '是否在每条结果里带上游原始条目（`raw` 字段）'],
            ['`include_content`', 'boolean', '否', '是否返回每条结果的正文（`content` 字段）。**默认 `false`** —— 默认只返回 `title` / `url` / `snippet`；正文实测单条可达数万字符，会大量挤占模型上下文。要读某个页面的全文，更推荐对该 URL 调 `/v1/fetch`（带 `max_length` 与 `start_index` 续读）'],
            ['`snippet_limit`', 'number', '否', '摘要长度上限（**字节**）。不传或 `0` 时用渠道封顶（默认 1000）；正数只能收紧不能放宽，超过渠道封顶会被夹回'],
            ['`max_content_length`', 'number', '否', '正文长度上限（**字节**），仅在 `include_content: true` 时有意义。默认 4000，硬顶 50000'],
            ['`options`', 'object', '否', '渠道特定透传参数，见「渠道配置详解」']
          ]
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '`limit` 有两层「静默改写」',
          text: '第一层：显式传了 `providers` 才跳过平台的顺序调整；只指定单个渠道且**没有显式传 `limit`** 时，会直接采用该渠道的「请求结果数」作为最终条数。第二层：只要渠道配置了大于 0 的 `request_result_limit`，打往该渠道的请求就一律被改成这个值，与调用方传了什么无关。另外服务端**没有**对 `limit` 做全局上限校验，文档里常见的「上限 50」只是管理台 UI 与 MCP 工具 schema 的约束。'
        },
        {
          type: 'paragraph',
          text: '`options` 是一个自由对象，不同渠道读取不同的键：`exa` 目前不读取任何键；`you` 不读取；`jina` 不读取；`tavily` 读 `search_depth`、`topic`、`time_range`/`timeRange`、`country`、`days`、`include_domains`/`includeDomains`、`exclude_domains`/`excludeDomains`；`firecrawl` 读 `tbs`、`country`、`location`、`include_domains`、`exclude_domains`、`timeout`；`serper` 读 `page`、`tbs`、`gl`/`country`、`hl`/`locale`/`language`、`location`；`brave` 读 `freshness`、`country`、`search_lang`/`searchLang`/`hl`/`language`、`ui_lang`/`uiLang`/`locale`、`safesearch`/`safe_search`/`safeSearch`、`offset`、`page`；`keenable` 读 `mode`（`pro` 默认 / `realtime`）、`site`、`acquired_after`、`acquired_before`、`published_after`、`published_before`、`query_time`、`snippet_max_length`；`context7` 读 `library`（可传字符串或数组，最多 4 个，精确 ID 如 `/vercel/next.js` 或模糊名如 `next.js`）、`version`（需配合 `library`）、`language`（编程语言软偏好）。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '`context7` 与其它八家的定位不同',
          text: '它是**文档检索渠道**：检索开源库/框架的权威文档与可运行代码示例，按文档文件聚合成结果（一条结果 = 一个文档页）。它**不在默认渠道列表内**，不显式传 `providers: ["context7"]` 就不会被调用，存量调用方的默认行为不变。查库/SDK/框架用法时建议先单独用它，结果为空（该库未被收录）再用其它渠道补充。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '正文（`content`）默认不返回',
          text: '这是**破坏性变更**：`results[].content` 默认为空，`include_content: true` 才会返回。原因是一条正文实测可达数万字符（exa 单条 28,184 字符），一把塞进响应会挤爆调用方的模型上下文；且体量随机波动，同一个查询换一批结果就可能差一个数量级。**取正文的推荐姿势**：先用默认请求拿到 `title` + `url` + `snippet` 判断哪几条值得读，再对目标 URL 调 `/v1/fetch` 取全文——它输出紧凑 Markdown、支持 `start_index` 续读，质量比搜索附带的正文稳定。注意 `context7` 的 `content` 就是代码示例本身，关掉后只剩描述，需要代码时应对该渠道显式打开开关。'
        }
      ]
    },
    {
      id: 'api-response',
      title: '响应结构',
      blocks: [
        {
          type: 'code',
          lang: 'json',
          title: '顶层三段（默认请求，不含正文）',
          content: `{
  "results": [
    {
      "title": "...",
      "url": "https://...",
      "snippet": "...",
      "provider": "exa",
      "providers": ["exa", "tavily"],
      "score": 0.87,
      "published_at": "2026-01-02T00:00:00Z",
      "raw": { }
    }
  ],
  "providers": [
    {
      "provider": "exa",
      "key_alias": "exa-1730000000000",
      "status": "success",
      "error_type": "",
      "error": "",
      "latency_ms": 412,
      "result_count": 10,
      "cached": false
    }
  ],
  "meta": {
    "request_id": "9f2c...",
    "mode": "parallel",
    "compat_format": "native",
    "latency_ms": 690,
    "total_results": 24,
    "deduped_results": 3,
    "cache_hit": false,
    "cache_key": "3a7f...",
    "providers_queried": ["exa", "tavily", "serper"]
  }
}`
        },
        {
          type: 'table',
          columns: ['字段', '口径'],
          rows: [
            ['`results[].provider`', '首来源渠道'],
            ['`results[].providers`', '命中的全部渠道。去重时同一 URL 的多来源会被合并到这个数组里'],
            ['`results[].snippet`', '摘要，长度上限由渠道封顶或请求的 `snippet_limit` 决定（取更小者），**按字节截断**'],
            ['`results[].content`', '正文，**默认不出现**；仅在 `include_content: true` 时返回，长度受 `max_content_length` 限制。`serper` 与 `keenable` 没有独立长正文，该字段与其 `snippet` 同源'],
            ['`results[].score`', '渠道各自给出的分数，部分渠道在上游未返回分数时用 `1/(序号+1)` 兜底，**不是跨渠道可比的归一化分数**'],
            ['`providers[].status`', '`success` 或 `error`'],
            ['`providers[].error_type`', '`auth` / `quota_exhausted` / `rate_limited` / `timeout` / `upstream` / `invalid_response` / `no_key`'],
            ['`providers[].cached`', '当前实现恒为 `false`（缓存命中时整个响应直接返回，不会再走渠道调用）'],
            ['`meta.deduped_results`', '被去重掉的结果条数，不是去重后的总数'],
            ['`meta.providers_queried`', '本次实际发起过的渠道，按执行顺序'],
            ['`meta.cache_hit`', '是否直接由缓存返回；命中时 `providers[]` 也是当初写入缓存的那份'],
            ['`meta.cache_key`', '本次使用的缓存键（未启用缓存时也会计算并回填）']
          ]
        },
        {
          type: 'paragraph',
          text: '排序规则：先按 `score` 降序，分数相同时按标题升序，最后按 `limit` 截断。`published_at` 只在渠道返回了可解析的时间时才出现。'
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '全部渠道失败仍返回 HTTP 200',
          text: '当没有任何结果且**所有**渠道都报错时，接口依旧返回 200，只是 `results` 为空、`providers[].status` 全为 `error`。调用方必须检查 `providers` 数组或 `results.length`，仅凭状态码判断会误以为搜索成功。'
        }
      ]
    },
    {
      id: 'api-cache',
      title: '缓存语义',
      blocks: [
        {
          type: 'paragraph',
          text: '缓存以**整次搜索响应**为单位，不是单渠道或单条结果。总开关在「系统设置 → 搜索缓存」，出厂关闭；请求级 `cache` 只能在其基础上做减法。'
        },
        {
          type: 'table',
          columns: ['`cache` 取值', '读缓存', '写缓存', '典型用途'],
          rows: [
            ['`default`（缺省）', '读', '写', '常规调用'],
            ['`refresh`', '**不读**', '写', '强制取一次最新结果，并顺带刷新缓存'],
            ['`bypass`', '不读', '**不写**', '调试或需要完全隔离于缓存时使用']
          ]
        },
        {
          type: 'paragraph',
          text: '未识别的取值按 `default` 处理。总开关关闭时三种策略都不会读写缓存。'
        },
        {
          type: 'heading', text: '写入条件与截断', level: 4 },
        {
          type: 'list',
          items: [
            '只有整体判定为成功时才写。`parallel` 模式下只要**任一渠道报错就不写**；`fallback` / `single` 模式下只要**有任一渠道成功**就写。',
            '「最大缓存结果数」大于 0 时，超出部分会被截断后写入；命中缓存拿到的条数因此可能少于当次真实可得条数。',
            '空结果的缓存时长最多 60 秒（若配置的 TTL 更短则用配置值），避免一次上游抖动把「搜不到」固化很久。',
            '启用缓存时，**键相同的并发请求会被合并成一次上游调用**（singleflight），所有等待者拿到同一份结果。'
          ]
        },
        {
          type: 'heading', text: '缓存键包含什么', level: 4 },
        {
          type: 'code',
          lang: 'text',
          title: 'cache key = sha256 以下字段',
          content: `query
providers（排序后）
provider_limits（各渠道生效的请求结果数）
mode
limit
freshness
dedupe
rerank
compat（native / tavily / serper / openai）
options
include_raw
include_content
snippet_limit
max_content_length`
        },
        {
          type: 'callout',
          tone: 'warn',
          title: '缓存键不含调用方身份，但含全部输出开关',
          text: '键里**没有**令牌 ID / 租户维度：不同令牌、不同调用方只要参数相同就会共享同一份缓存（缓存以整次响应为单位）。但**输出开关都在键里** —— `include_raw`、`include_content`、`snippet_limit`、`max_content_length` 任一不同就会算出不同的键，因此「先不带 `include_content` 写缓存、随后带 `include_content: true` 却命中旧缓存、拿不到正文」这类串味**不会发生**。代价是同一查询按不同输出口径各存一份缓存。'
        }
      ]
    },
    {
      id: 'api-errors',
      title: '错误码',
      blocks: [
        {
          type: 'table',
          columns: ['状态码', '响应体 message', '触发条件'],
          rows: [
            ['400', '`invalid body`', '请求体读取失败'],
            ['400', '`invalid json body`', '请求体不是合法 JSON'],
            ['400', '`query is required`', '`query` 裁剪空白后为空'],
            ['401', '`api token required`', '开启了鉴权但没有带令牌'],
            ['401', '`invalid api token`', '令牌不存在、已停用、**或日/月额度已耗尽**——三者不可区分'],
            ['403', '`api token is not allowed to request provider <name>`', '请求的 `providers` 里有未在令牌白名单内的渠道'],
            ['429', '`api token rate limit exceeded`', '该令牌超过 `rate_limit_per_min`'],
            ['500', '底层错误信息', '读取运行时设置 / 渠道配置失败等内部错误']
          ]
        },
        {
          type: 'paragraph',
          text: '所有错误响应共用同一个信封：`{ "error": { "message": "...", "status": 401 } }`。上面的表格列的是 `error.message` 的取值。'
        },
        {
          type: 'callout',
          tone: 'info',
          title: '额度耗尽为什么是 401',
          text: '日/月额度的判断写在令牌查询的 SQL 条件里，额度用尽后查询直接无结果，于是与「令牌不存在」走了同一个分支。只有 RPM 超限是 429。排查 401 时请到「接口令牌」页核对额度与使用量。'
        }
      ]
    },
    {
      id: 'api-samples',
      title: '调用示例',
      blocks: [
        {
          type: 'code',
          lang: 'bash',
          title: 'curl',
          content: `curl -sS http://localhost:5173/v1/search \\
  -H "Authorization: Bearer osr_xxx" \\
  -H "Content-Type: application/json" \\
  -d '{
    "query": "latest web search APIs",
    "providers": ["exa", "tavily", "brave"],
    "mode": "fallback",
    "limit": 10,
    "freshness": "week",
    "dedupe": true,
    "cache": "default"
  }'`
        },
        {
          type: 'code',
          lang: 'python',
          title: 'Python（requests）',
          content: `import requests

resp = requests.post(
    "http://localhost:5173/v1/search",
    headers={"Authorization": "Bearer osr_xxx"},
    json={
        "query": "latest web search APIs",
        "providers": ["exa", "tavily", "brave"],
        "mode": "fallback",
        "limit": 10,
        "freshness": "week",
    },
    timeout=30,
)
resp.raise_for_status()
data = resp.json()

print(data["meta"]["request_id"], len(data["results"]))
for call in data["providers"]:
    if call["status"] != "success":
        print("失败渠道:", call["provider"], call["error_type"], call["error"])`
        },
        {
          type: 'code',
          lang: 'javascript',
          title: 'Node.js（原生 fetch）',
          content: `const resp = await fetch('http://localhost:5173/v1/search', {
  method: 'POST',
  headers: {
    Authorization: 'Bearer osr_xxx',
    'Content-Type': 'application/json'
  },
  body: JSON.stringify({
    query: 'latest web search APIs',
    providers: ['exa', 'tavily', 'brave'],
    mode: 'fallback',
    limit: 10,
    freshness: 'week'
  })
})

const data = await resp.json()
console.log(data.results.length, data.meta)
console.log(data.providers.filter((call) => call.status !== 'success'))`
        },
        {
          type: 'code',
          lang: 'go',
          title: 'Go（标准库）',
          content: `package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

func main() {
	payload, _ := json.Marshal(map[string]any{
		"query":  "latest web search APIs",
		"mode":   "fallback",
		"limit":  10,
		"dedupe": true,
	})
	req, _ := http.NewRequest(http.MethodPost, "http://localhost:5173/v1/search", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer osr_xxx")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	meta, _ := body["meta"].(map[string]any)
	fmt.Println(resp.StatusCode, meta["total_results"], meta["request_id"])
}`
        },
        {
          type: 'callout',
          tone: 'info',
          title: '示例里的地址',
          text: '以上示例统一使用 `http://localhost:5173`。页面顶部「使用当前站点地址」开关打开时，这些地址会自动替换成你当前访问的地址，可以直接复制。'
        }
      ]
    }
  ]
}
