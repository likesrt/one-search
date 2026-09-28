package provider

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/one-search/one-search/backend/internal/model"
)

type BraveProvider struct {
	*HTTPProvider
}

func NewBraveProvider(cfg Config) *BraveProvider {
	cfg.Name = model.ProviderBrave
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.search.brave.com/res/v1"
	}
	return &BraveProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 Brave 的 GET /web/search，并把结果归一化为统一的搜索结果。
//
// 参数：req.Query 与 req.Limit 作为 q / count 透传（经 requestLimit 夹到 1..20，<=0 时为 10）；
// options 支持 freshness / country / search_lang（别名 searchLang、hl、language）/
// ui_lang（别名 uiLang、locale）/ safesearch（别名 safe_search、safeSearch）/ offset / page；
// req.Freshness 经 braveFreshness 映射为上游取值（day→pd、week→pw、month→pm、year→py）。
// key.Value 作为 X-Subscription-Token 发送。
//
// 返回值：归一化结果、usage 计量与上游原始响应；上游非 2xx 或响应体非法时返回 *Error。
//
// 边界条件：extra_snippets=true 是拿多条摘要片段的前提，只在调用方要正文（include_content）
// 或原始条目（include_raw）时才附带 —— 它会让上游返回更多文本、增加本次调用的出流量。
//
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponse 关闭。
func (p *BraveProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	opts := resultOptionsFrom(req, DefaultSnippetLimit)
	limit := requestLimit(req.Limit, 10, 20)
	params := url.Values{}
	params.Set("q", req.Query)
	params.Set("count", strconv.Itoa(limit))
	if freshness := braveFreshness(req); freshness != "" {
		params.Set("freshness", freshness)
	}
	if country := optionString(req.Options, "country"); country != "" {
		params.Set("country", country)
	}
	if lang := optionString(req.Options, "search_lang", "searchLang", "hl", "language"); lang != "" {
		params.Set("search_lang", lang)
	}
	if uiLang := optionString(req.Options, "ui_lang", "uiLang", "locale"); uiLang != "" {
		params.Set("ui_lang", uiLang)
	}
	if safesearch := optionString(req.Options, "safesearch", "safe_search", "safeSearch"); safesearch != "" {
		params.Set("safesearch", safesearch)
	}
	if offset := braveOffset(req, limit); offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	// extra_snippets 只对 content 有意义（content = description 拼上这些片段），
	// 因此要正文或要原始条目时才向上游索取，默认路径不发，避免白拿一堆会被丢弃的文本。
	if opts.IncludeRaw || opts.IncludeContent {
		params.Set("extra_snippets", "true")
	}
	request, err := p.newGETRequest(ctx, "/web/search", params)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	request.Header.Set("X-Subscription-Token", key.Value)
	response, err := p.client.Do(request)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	payload, err := p.decodeResponse(response)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	results := normalizeBraveResults(payload, opts)
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderBrave, payload), Raw: payload}, nil
}

func braveFreshness(req model.SearchRequest) string {
	if value := optionString(req.Options, "freshness"); value != "" {
		return value
	}
	switch strings.ToLower(strings.TrimSpace(req.Freshness)) {
	case "day", "d", "qdr:d", "pd":
		return "pd"
	case "week", "w", "qdr:w", "pw":
		return "pw"
	case "month", "m", "qdr:m", "pm":
		return "pm"
	case "year", "y", "qdr:y", "py":
		return "py"
	default:
		return strings.TrimSpace(req.Freshness)
	}
}

func braveOffset(req model.SearchRequest, limit int) int {
	if offset := optionInt(req.Options, "offset"); offset > 0 {
		return offset
	}
	if page := optionInt(req.Options, "page"); page > 1 {
		return page - 1
	}
	if limit <= 0 {
		return 0
	}
	return 0
}

// normalizeBraveResults 把 Brave 响应归一化为统一的搜索结果。
//
// 只读 web.results；web 缺失时返回 nil（不是空切片）—— 上游用「没有 web 段」表示无结果，
// 与「有 web 段但 results 为空」语义不同，调用方据此区分可避免把上游异常当空结果。
// 字段映射：URL 取 url（缺失丢弃）；摘要取 description / snippet；
// 正文 = 摘要拼上 extra_snippets（上游只有请求了 extra_snippets 才给该字段）。
// 截断口径与是否填充正文/原始条目由 opts 决定；score 固定用 1/(序号+1)（上游不返回相关性分数）。
// 返回值为新切片；不修改 payload。
func normalizeBraveResults(payload map[string]interface{}, opts resultOptions) []model.SearchResult {
	web := mapFromInterface(payload["web"])
	if web == nil {
		return nil
	}
	items := resultArray(web, "results")
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
		snippet := stringValue(item, "description", "snippet")
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     truncate(snippet, opts.SnippetCap),
			Provider:    model.ProviderBrave,
			Providers:   []string{model.ProviderBrave},
			Score:       1 / float64(index+1),
			PublishedAt: parseTimeValue(stringValue(item, "age", "page_age", "published", "published_at", "date")),
		}
		if opts.IncludeContent {
			result.Content = truncate(braveContent(item, snippet), opts.ContentCap)
		}
		if opts.IncludeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}

// braveContent 拼出 Brave 的正文：摘要在前，extra_snippets 按上游顺序续在后面。
//
// 单独拆出是因为 extra_snippets 的拼接（含空片段过滤）与截断是两个关注点，
// 混在循环里会让 normalizeBraveResults 超出可读长度。
// 参数：item 为单条上游结果，snippet 为已取好的摘要（调用方已算过，避免重复取值）。
// 返回值：无 extra_snippets 时就是 snippet 本身；有则用换行分隔拼接；全为空时返回空串。
func braveContent(item map[string]interface{}, snippet string) string {
	extraSnippets := stringArrayValue(item, "extra_snippets")
	if len(extraSnippets) == 0 {
		return snippet
	}
	return strings.Join(append([]string{snippet}, extraSnippets...), "\n")
}
