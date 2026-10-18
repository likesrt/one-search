package provider

import (
	"context"
	"net/http"

	"github.com/one-search/one-search/backend/internal/model"
)

const (
	// deepseekSearchEndpoint 是 DeepSeek 官方 Search 接口。
	//
	// 只接 Search，不接同页文档里的 /v1/tools/fetch：网关的抓取链路（SSRF 护栏、Markdown
	// 转换、Tavily 回退）是独立子系统，本适配器不实现 Extract，也不引用 internal/fetch。
	deepseekSearchEndpoint = "/v1/tools/search"
)

// DeepSeekProvider 是 DeepSeek 官方网页搜索渠道适配器。
// 调用 POST /v1/tools/search，Bearer 鉴权；请求字段为 query / count / need_content /
// ori_question / localization，响应为 results[]（含 rerank_score、snippet、summary、content、date）。
type DeepSeekProvider struct {
	*HTTPProvider
}

// NewDeepSeekProvider 构建 DeepSeek 适配器。
// cfg.BaseURL 为空时回退官方地址 https://api.deepseek.com；端点由 deepseekSearchEndpoint 拼接。
// 带 `#` 前缀的完整端点语法仍由 NewHTTPProvider 统一处理（中转站可整段覆盖）。
func NewDeepSeekProvider(cfg Config) *DeepSeekProvider {
	cfg.Name = model.ProviderDeepSeek
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.deepseek.com"
	}
	return &DeepSeekProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 DeepSeek 的 POST /v1/tools/search。
//
// 参数：req.Query 原样透传（上游限制 1–128 个 Unicode 字符，超限由上游返回 400，
// 这里不做静默截断）；req.Limit 作为 count，夹到 1..50，<=0 时取 10；
// options 里的 ori_question / localization 非空时透传；req.Freshness 不读取——
// 上游没有独立时效参数，需要时用 query 语法 after:/before:。
// include_content 与 include_raw 任一为真都发送 need_content=true：前者用于填充正文，
// 后者让 raw 里的 content 也有值（与 exa/brave 的取数口径一致）。
// key.Value 作为 Authorization: Bearer 发送，渠道不支持匿名。
//
// 返回值：归一化结果、usage 计量与上游原始响应；上游非 2xx 或响应体非法时返回 *Error。
// 官方按「1,000 次成功请求 5 元」计费，且仅 200 且 results 非空才计费；空结果仍是一
// 次成功的渠道调用，但计费侧由 billing.EmptyResultsBillable 决定不再补 requests:1。
//
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponse 关闭。
func (p *DeepSeekProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	opts := resultOptionsFrom(req, DefaultSnippetLimit)
	request, err := p.newJSONRequest(ctx, http.MethodPost, deepseekSearchEndpoint, deepseekBody(req, opts))
	if err != nil {
		return model.ProviderResponse{}, err
	}
	request.Header.Set("Authorization", "Bearer "+key.Value)
	response, err := p.client.Do(request)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	payload, err := p.decodeResponse(response)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	results := normalizeDeepSeekResults(payload, opts)
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderDeepSeek, payload), Raw: payload}, nil
}

// deepseekBody 组装 Search 请求体。
// count 上限即上游上限 50；need_content 只在需要正文或原始条目时发送，避免默认会被
// 丢弃的字段占用响应体量；ori_question / localization 为空时不写键，让上游使用自己的默认值。
// 返回值恒为非 nil 的 map；未知 options 键不会进入请求体。
func deepseekBody(req model.SearchRequest, opts resultOptions) map[string]interface{} {
	body := map[string]interface{}{
		"query": req.Query,
		"count": requestLimit(req.Limit, 10, 50),
	}
	if opts.IncludeContent || opts.IncludeRaw {
		body["need_content"] = true
	}
	if question := optionString(req.Options, "ori_question", "oriQuestion"); question != "" {
		body["ori_question"] = question
	}
	if localization := optionString(req.Options, "localization"); localization != "" {
		body["localization"] = localization
	}
	return body
}

// normalizeDeepSeekResults 把 DeepSeek 响应归一化为统一的搜索结果。
// 只读 results 数组，缺 url 的条目直接丢弃（与其余适配器一致）。
// 摘要优先 snippet、空时用 summary 兜底；正文仅在 opts.IncludeContent 为真时填充，
// 上游 content 为空时同样回退 summary —— 上游把 summary 定位为「给模型读的长摘要」，
// 比直接返回空正文更有用，截断口径仍由 opts 统一决定。
// date 是 RFC 3339 的 UTC 时间；rerank_score 是 0..3 的原始分，归一化见 deepseekScore。
// 返回值为新切片；不修改 payload。
func normalizeDeepSeekResults(payload map[string]interface{}, opts resultOptions) []model.SearchResult {
	items := resultArray(payload, "results")
	results := make([]model.SearchResult, 0, len(items))
	for index, rawItem := range items {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		url := stringValue(item, "url")
		if url == "" {
			continue
		}
		summary := stringValue(item, "summary")
		snippet := stringValue(item, "snippet")
		if snippet == "" {
			snippet = summary
		}
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     truncate(snippet, opts.SnippetCap),
			Provider:    model.ProviderDeepSeek,
			Providers:   []string{model.ProviderDeepSeek},
			Score:       deepseekScore(item, index),
			PublishedAt: parseTimeValue(stringValue(item, "date")),
		}
		if opts.IncludeContent {
			content := stringValue(item, "content")
			if content == "" {
				content = summary
			}
			result.Content = truncate(content, opts.ContentCap)
		}
		if opts.IncludeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}

// deepseekScore 把上游 rerank_score（0..3）归一到 0..1。
//
// mergeResults 按 score 跨渠道排序，而其余渠道多数用 1/(名次+1)（0..1）或上游 0..1 分；
// 直接把 0..3 塞进去会让 DeepSeek 的结果无条件压过其它渠道。除以 3 保留上游质量信号，
// 同时与既有分数量纲对齐。字段缺失、非数值或 <=0 时回退按名次的 1/(index+1)。
// 返回值恒在 0..1；无副作用。
func deepseekScore(item map[string]interface{}, index int) float64 {
	score := floatValue(item, "rerank_score") / 3
	if score <= 0 {
		return 1 / float64(index+1)
	}
	if score > 1 {
		return 1
	}
	return score
}
