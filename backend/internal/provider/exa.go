package provider

import (
	"context"
	"net/http"

	"github.com/one-search/one-search/backend/internal/model"
)

type ExaProvider struct {
	*HTTPProvider
}

func NewExaProvider(cfg Config) *ExaProvider {
	cfg.Name = model.ProviderExa
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.exa.ai"
	}
	return &ExaProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 Exa 的 POST /search，并把结果归一化为统一的搜索结果。
//
// 参数：req.Query 为检索原文；req.Limit 直接作为 numResults 透传（<=0 时为 10，无上限裁剪）；
// req.Freshness 与 req.Options 都不读取（上游请求体里没有对应位置）。
// key.Value 作为 Bearer 凭据发送。
//
// 返回值：归一化结果、usage 计量与上游原始响应；上游非 2xx 或响应体非法时返回 *Error。
//
// 边界条件：请求体的 contents 只在需要正文（include_content）或原始条目（include_raw）时才要求
// text，highlights 恒要求 —— 摘要取自 highlights，省掉它会让默认路径的结果没有摘要；
// 摘要与正文的截断口径由 resultOptionsFrom 统一决定。
//
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponse 关闭。
func (p *ExaProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	opts := resultOptionsFrom(req, DefaultSnippetLimit)
	// text 是全文（单条可达数万字符），只有调用方要正文或原始条目时才向上游索取：
	// Exa 按请求的 contents 计费与出流量，无条件带上等于把成本花在默认被丢弃的字段上。
	contents := map[string]interface{}{"highlights": true}
	if opts.IncludeContent || opts.IncludeRaw {
		contents["text"] = true
	}
	body := map[string]interface{}{
		"query":      req.Query,
		"numResults": limit,
		"type":       "neural",
		"contents":   contents,
	}
	request, err := p.newJSONRequest(ctx, http.MethodPost, "/search", body)
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
	results := normalizeExaResults(payload, opts)
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderExa, payload), Raw: payload}, nil
}

// normalizeExaResults 把 Exa 响应归一化为统一的搜索结果。
// 字段映射：URL 缺失的条目丢弃；摘要优先 highlights[0]，回退 text / summary；正文取 text。
// 截断口径与是否填充正文/原始条目全部由 opts 决定（见 resultOptionsFrom）。
// Score 取上游 score，发布时间取 publishedDate / published_at，两者缺失时分别为 0 与 nil。
// 返回值为新切片；不修改 payload。
func normalizeExaResults(payload map[string]interface{}, opts resultOptions) []model.SearchResult {
	items := resultArray(payload, "results")
	results := make([]model.SearchResult, 0, len(items))
	for _, rawItem := range items {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		url := stringValue(item, "url")
		if url == "" {
			continue
		}
		snippet := firstStringFromArray(item, "highlights")
		if snippet == "" {
			snippet = stringValue(item, "text", "summary")
		}
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     truncate(snippet, opts.SnippetCap),
			Provider:    model.ProviderExa,
			Providers:   []string{model.ProviderExa},
			Score:       floatValue(item, "score"),
			PublishedAt: parseTimeValue(stringValue(item, "publishedDate", "published_at")),
		}
		if opts.IncludeContent {
			result.Content = truncate(stringValue(item, "text"), opts.ContentCap)
		}
		if opts.IncludeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}
