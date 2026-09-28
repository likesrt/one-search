package provider

import (
	"context"
	"net/url"
	"strconv"

	"github.com/one-search/one-search/backend/internal/model"
)

type YouProvider struct {
	*HTTPProvider
}

func NewYouProvider(cfg Config) *YouProvider {
	cfg.Name = model.ProviderYou
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://ydc-index.io"
	}
	return &YouProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 You.com 的 GET /v1/search，并把结果归一化为统一的搜索结果。
//
// 参数：req.Query 与 req.Limit 作为 query / count 透传（limit<=0 时为 10，无上限裁剪）；
// req.Freshness 与 req.Options 不读取；key.Value 作为 X-API-Key 发送（可为空）。
//
// 返回值：归一化结果、usage 计量与上游原始响应；上游非 2xx 或响应体非法时返回 *Error。
//
// 边界条件：摘要与正文的截断口径、正文是否填充由 resultOptionsFrom 统一决定；
// 上游未给出 score 时用 1/(序号+1) 兜底，因此该分数只是名次折算而非相关性分数。
//
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponse 关闭。
func (p *YouProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 10
	}
	params := url.Values{}
	params.Set("query", req.Query)
	params.Set("count", strconv.Itoa(limit))
	request, err := p.newGETRequest(ctx, "/v1/search", params)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	request.Header.Set("X-API-Key", key.Value)
	response, err := p.client.Do(request)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	payload, err := p.decodeResponse(response)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	results := normalizeYouResults(payload, resultOptionsFrom(req, DefaultSnippetLimit))
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderYou, payload), Raw: payload}, nil
}

// normalizeYouResults 把 You.com 响应归一化为统一的搜索结果。
//
// 结果数组的探测顺序：hits / organic → results.web 与 results.news → results → web.results / web.hits，
// 这是为了兼容上游不同版本/端点的响应形状。
// 字段映射：URL 取 url / link（缺失丢弃）；摘要取 snippet / description，回退 snippets[0]；
// 正文优先 contents.markdown / contents.html，回退 content / description / snippet。
// 截断口径与是否填充正文/原始条目由 opts 决定；score 缺失时用 1/(序号+1) 兜底。
// 返回值为新切片；不修改 payload。
func normalizeYouResults(payload map[string]interface{}, opts resultOptions) []model.SearchResult {
	items := resultArray(payload, "hits", "organic")
	if nested, ok := payload["results"].(map[string]interface{}); ok {
		items = append(items, resultArray(nested, "web")...)
		items = append(items, resultArray(nested, "news")...)
	} else {
		items = append(items, resultArray(payload, "results")...)
	}
	if web, ok := payload["web"].(map[string]interface{}); ok && len(items) == 0 {
		items = resultArray(web, "results", "hits")
	}
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
		snippet := stringValue(item, "snippet", "description")
		if snippet == "" {
			snippet = firstStringFromArray(item, "snippets")
		}
		content := stringValue(item, "content", "description", "snippet")
		if contents, ok := item["contents"].(map[string]interface{}); ok {
			content = stringValue(contents, "markdown", "html")
		}
		score := floatValue(item, "score")
		if score == 0 {
			score = 1 / float64(index+1)
		}
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     truncate(snippet, opts.SnippetCap),
			Provider:    model.ProviderYou,
			Providers:   []string{model.ProviderYou},
			Score:       score,
			PublishedAt: parseTimeValue(stringValue(item, "date", "publishedDate", "published_at", "page_age")),
		}
		if opts.IncludeContent {
			result.Content = truncate(content, opts.ContentCap)
		}
		if opts.IncludeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}
