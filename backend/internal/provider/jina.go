package provider

import (
	"context"
	"net/url"

	"github.com/one-search/one-search/backend/internal/model"
)

type JinaProvider struct {
	*HTTPProvider
}

func NewJinaProvider(cfg Config) *JinaProvider {
	cfg.Name = model.ProviderJina
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://s.jina.ai"
	}
	return &JinaProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 Jina Reader 的 GET /{query}，并把结果归一化为统一的搜索结果。
//
// 参数：req.Query 经 URL 路径转义后拼进路径（上游没有查询参数形式的检索入口）；
// req.Limit、req.Freshness 与 req.Options 都不读取 —— 上游不接受条数参数，返回条数由它决定。
// key.Value 非空时才发送 Authorization: Bearer（留空即匿名调用，上游通常会拒绝或降级）。
//
// 返回值：归一化结果、usage 计量与上游原始响应；上游非 2xx 或响应体非法时返回 *Error。
//
// 边界条件：摘要与正文的截断口径、正文是否填充由 resultOptionsFrom 统一决定；
// 上游未给出 score 时用 1/(序号+1) 兜底。
//
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponse 关闭。
func (p *JinaProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	request, err := p.newGETRequest(ctx, "/"+url.PathEscape(req.Query), nil)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	if key.Value != "" {
		request.Header.Set("Authorization", "Bearer "+key.Value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	payload, err := p.decodeResponse(response)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	results := normalizeJinaResults(payload, resultOptionsFrom(req, DefaultSnippetLimit))
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderJina, payload), Raw: payload}, nil
}

// normalizeJinaResults 把 Jina 响应归一化为统一的搜索结果。
// 结果数组取 data / results；URL 取 url / link（缺失丢弃）。
// 摘要取 description / snippet / content，正文取 content / text / description（两者可能同源）。
// 截断口径与是否填充正文/原始条目由 opts 决定；score 缺失时用 1/(序号+1) 兜底。
// 返回值为新切片；不修改 payload。
func normalizeJinaResults(payload map[string]interface{}, opts resultOptions) []model.SearchResult {
	items := resultArray(payload, "data", "results")
	results := make([]model.SearchResult, 0, len(items))
	for index, rawItem := range items {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		url := stringValue(item, "url", "link")
		if url == "" {
			continue
		}
		score := floatValue(item, "score")
		if score == 0 {
			score = 1 / float64(index+1)
		}
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     truncate(stringValue(item, "description", "snippet", "content"), opts.SnippetCap),
			Provider:    model.ProviderJina,
			Providers:   []string{model.ProviderJina},
			Score:       score,
			PublishedAt: parseTimeValue(stringValue(item, "published", "published_at", "date")),
		}
		if opts.IncludeContent {
			result.Content = truncate(stringValue(item, "content", "text", "description"), opts.ContentCap)
		}
		if opts.IncludeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}
