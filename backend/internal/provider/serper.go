package provider

import (
	"context"
	"net/http"

	"github.com/one-search/one-search/backend/internal/model"
)

type SerperProvider struct {
	*HTTPProvider
}

func NewSerperProvider(cfg Config) *SerperProvider {
	cfg.Name = model.ProviderSerper
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://google.serper.dev"
	}
	return &SerperProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 Serper 的 POST /search，并把结果归一化为统一的搜索结果。
//
// 参数：req.Query 与 req.Limit 作为 q / num 透传（经 requestLimit 夹到 1..100，<=0 时为 10）；
// options 支持 page / tbs / gl（别名 country）/ hl（别名 locale、language）/ location；
// req.Freshness 在未给 options.tbs 时直通上游 tbs，**不做 qdr:* 映射**。
// key.Value 作为 X-API-KEY 发送。
//
// 返回值：归一化结果、usage 计量与上游原始响应；上游非 2xx 或响应体非法时返回 *Error。
//
// 边界条件：上游只给摘要不给正文，因此 content 与 snippet 同源（详见 normalizeSerperResults）；
// 摘要截断口径与 content 是否填充由 resultOptionsFrom 统一决定。
//
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponse 关闭。
func (p *SerperProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	body := map[string]interface{}{
		"q":   req.Query,
		"num": requestLimit(req.Limit, 10, 100),
	}
	if page := optionInt(req.Options, "page"); page > 0 {
		body["page"] = page
	}
	if tbs := optionString(req.Options, "tbs"); tbs != "" {
		body["tbs"] = tbs
	} else if req.Freshness != "" {
		body["tbs"] = req.Freshness
	}
	if gl := optionString(req.Options, "gl", "country"); gl != "" {
		body["gl"] = gl
	}
	if hl := optionString(req.Options, "hl", "locale", "language"); hl != "" {
		body["hl"] = hl
	}
	if location := optionString(req.Options, "location"); location != "" {
		body["location"] = location
	}
	request, err := p.newJSONRequest(ctx, http.MethodPost, "/search", body)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	request.Header.Set("X-API-KEY", key.Value)
	response, err := p.client.Do(request)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	payload, err := p.decodeResponse(response)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	results := normalizeSerperResults(payload, resultOptionsFrom(req, DefaultSnippetLimit))
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderSerper, payload), Raw: payload}, nil
}

// normalizeSerperResults 把 Serper 响应归一化为统一的搜索结果。
//
// 结果数组为 organic 与 news 的拼接（两者都可能为空）。
// 字段映射：URL 取 link / url（缺失丢弃）；Title 取 title；
// **摘要与正文同源**——上游只返回 snippet / description，没有独立的长正文，
// 因此 content 是 snippet 按更宽上限的再截断，而不是另取一个字段。
// 截断口径与是否填充正文/原始条目由 opts 决定；score 优先 1/position，无 position 时用 1/(序号+1)。
// 返回值为新切片；不修改 payload。
func normalizeSerperResults(payload map[string]interface{}, opts resultOptions) []model.SearchResult {
	items := append(resultArray(payload, "organic"), resultArray(payload, "news")...)
	results := make([]model.SearchResult, 0, len(items))
	for index, rawItem := range items {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		url := stringValue(item, "link", "url")
		if url == "" {
			continue
		}
		position := floatValue(item, "position")
		score := 1 / float64(index+1)
		if position > 0 {
			score = 1 / position
		}
		text := stringValue(item, "snippet", "description")
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     truncate(text, opts.SnippetCap),
			Provider:    model.ProviderSerper,
			Providers:   []string{model.ProviderSerper},
			Score:       score,
			PublishedAt: parseTimeValue(stringValue(item, "date", "publishedDate", "published_at")),
		}
		if opts.IncludeContent {
			result.Content = truncate(text, opts.ContentCap)
		}
		if opts.IncludeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}
