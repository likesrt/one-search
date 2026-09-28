package provider

import (
	"context"
	"net/http"
	"strings"

	"github.com/one-search/one-search/backend/internal/model"
)

type FirecrawlProvider struct {
	*HTTPProvider
}

func NewFirecrawlProvider(cfg Config) *FirecrawlProvider {
	cfg.Name = model.ProviderFirecrawl
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.firecrawl.dev"
	}
	return &FirecrawlProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 Firecrawl 的 POST /v2/search，并把结果归一化为统一的搜索结果。
//
// 参数：req.Query 为检索原文；req.Limit 经 requestLimit 夹到 1..100（<=0 时为 10）；
// options 支持 tbs / country / location / include_domains（别名 includeDomains）/
// exclude_domains（别名 excludeDomains）/ timeout。
//
// 返回值：归一化结果、usage 计量与上游原始响应；上游非 2xx 或响应体非法时返回 *Error。
//
// 边界条件：scrapeOptions.formats=["markdown"] 是拿正文的前提，只在调用方要正文
// （include_content）或原始条目（include_raw）时才附带 —— 它会显著增加上游耗时与计费，
// 默认路径只返回标题/链接/摘要，没必要为被丢弃的正文付费。
//
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponse 关闭。
func (p *FirecrawlProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	opts := resultOptionsFrom(req, DefaultSnippetLimit)
	body := map[string]interface{}{
		"query":   req.Query,
		"limit":   requestLimit(req.Limit, 10, 100),
		"sources": []string{"web"},
	}
	if tbs := firecrawlTBS(req); tbs != "" {
		body["tbs"] = tbs
	}
	if country := optionString(req.Options, "country"); country != "" {
		body["country"] = country
	}
	if location := optionString(req.Options, "location"); location != "" {
		body["location"] = location
	}
	if includeDomains := optionStringSlice(req.Options, "include_domains", "includeDomains"); len(includeDomains) > 0 {
		body["includeDomains"] = includeDomains
	}
	if excludeDomains := optionStringSlice(req.Options, "exclude_domains", "excludeDomains"); len(excludeDomains) > 0 {
		body["excludeDomains"] = excludeDomains
	}
	if timeout := optionInt(req.Options, "timeout"); timeout > 0 {
		body["timeout"] = timeout
	}
	if opts.IncludeRaw || opts.IncludeContent {
		body["scrapeOptions"] = map[string]interface{}{"formats": []string{"markdown"}}
	}
	request, err := p.newJSONRequest(ctx, http.MethodPost, "/v2/search", body)
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
	results := normalizeFirecrawlResults(payload, opts)
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderFirecrawl, payload), Raw: payload}, nil
}

func firecrawlTBS(req model.SearchRequest) string {
	if value := optionString(req.Options, "tbs"); value != "" {
		return value
	}
	switch strings.ToLower(strings.TrimSpace(req.Freshness)) {
	case "hour", "qdr:h":
		return "qdr:h"
	case "day", "d", "qdr:d", "pd":
		return "qdr:d"
	case "week", "w", "qdr:w", "pw":
		return "qdr:w"
	case "month", "m", "qdr:m", "pm":
		return "qdr:m"
	case "year", "y", "qdr:y", "py":
		return "qdr:y"
	default:
		return req.Freshness
	}
}

// normalizeFirecrawlResults 把 Firecrawl 响应归一化为统一的搜索结果。
//
// 结果数组取 data（数组形态）以及 data.web / data.news（对象形态），覆盖上游两种响应形状。
// 字段映射：URL 与标题缺失时从 metadata.sourceURL / metadata.title 兜底，仍缺 URL 则丢弃；
// 摘要取 description / snippet（回退 metadata.description）；
// 正文优先 markdown，回退 html / rawHtml / description / snippet。
// 截断口径与是否填充正文/原始条目由 opts 决定；分数 = 1/position，无 position 时用 1/(序号+1)。
// 返回值为新切片；不修改 payload。
func normalizeFirecrawlResults(payload map[string]interface{}, opts resultOptions) []model.SearchResult {
	items := resultArray(payload, "data")
	if data := mapFromInterface(payload["data"]); data != nil {
		items = append(resultArray(data, "web"), resultArray(data, "news")...)
	}
	results := make([]model.SearchResult, 0, len(items))
	for index, rawItem := range items {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		metadata := mapFromInterface(item["metadata"])
		url := stringValue(item, "url")
		if url == "" && metadata != nil {
			url = stringValue(metadata, "sourceURL", "url")
		}
		if url == "" {
			continue
		}
		title := stringValue(item, "title")
		if title == "" && metadata != nil {
			title = stringValue(metadata, "title")
		}
		snippet := stringValue(item, "description", "snippet")
		if snippet == "" && metadata != nil {
			snippet = stringValue(metadata, "description")
		}
		content := stringValue(item, "markdown", "html", "rawHtml", "description", "snippet")
		position := floatValue(item, "position")
		score := 1 / float64(index+1)
		if position > 0 {
			score = 1 / position
		}
		result := model.SearchResult{
			Title:       title,
			URL:         url,
			Snippet:     truncate(snippet, opts.SnippetCap),
			Provider:    model.ProviderFirecrawl,
			Providers:   []string{model.ProviderFirecrawl},
			Score:       score,
			PublishedAt: parseTimeValue(stringValue(item, "date", "publishedDate", "published_at")),
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
