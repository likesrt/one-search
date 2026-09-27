package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/one-search/one-search/backend/internal/model"
)

type TavilyProvider struct {
	*HTTPProvider
}

func NewTavilyProvider(cfg Config) *TavilyProvider {
	cfg.Name = model.ProviderTavily
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.tavily.com"
	}
	return &TavilyProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

func (p *TavilyProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	body := map[string]interface{}{
		"query":         req.Query,
		"max_results":   requestLimit(req.Limit, 10, 20),
		"include_usage": true,
	}
	if searchDepth := optionString(req.Options, "search_depth"); searchDepth != "" {
		body["search_depth"] = searchDepth
	}
	if topic := optionString(req.Options, "topic"); topic != "" {
		body["topic"] = topic
	}
	if timeRange := tavilyTimeRange(req); timeRange != "" {
		body["time_range"] = timeRange
	}
	if country := optionString(req.Options, "country"); country != "" {
		body["country"] = country
	}
	if includeDomains := optionStringSlice(req.Options, "include_domains", "includeDomains"); len(includeDomains) > 0 {
		body["include_domains"] = includeDomains
	}
	if excludeDomains := optionStringSlice(req.Options, "exclude_domains", "excludeDomains"); len(excludeDomains) > 0 {
		body["exclude_domains"] = excludeDomains
	}
	if req.IncludeRaw {
		body["include_raw_content"] = true
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
	results := normalizeTavilyResults(payload, req.IncludeRaw)
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderTavily, payload), Raw: payload}, nil
}

// Extract 调用 Tavily 的 POST /extract 取回整页正文，供网页抓取的回退通道使用。
//
// 与 Search 的差别有三点，都是调用方需要知道的：
//   - 返回的是**整页 Markdown**（含导航与页脚），不做正文提取，因此内容质量不如内置抓取，
//     只能当兜底而不能当等价替代；
//   - 长度无上限（实测 Wikipedia 返回 577743 字符、Gutenberg 全书 1221259 字符），
//     本方法刻意**不截断**：截断点必须留给抓取侧的 max_length，否则续读拿不到后半段；
//   - 失败不计费，因此不需要为失败做冷却。
//
// 参数 targetURL 必须是完整的 http/https 地址（抓取侧已校验）；key 为 tavily 渠道的密钥，
// 允许为空值以支持匿名/中转站场景（与 Search 一致，鉴权头照发）。
//
// 返回值：正文、本次消耗的 credits（取自响应里的 usage.credits，缺失时为 0）、错误。
//
// 边界条件：HTTP 200 也可能只带 failed_results（目标站点被门禁、域名不存在等），
// 此时两个数组都要检查 —— 只看状态码会把「提取失败」误判成成功并返回空正文。
// 错误信息带上 failed_results[].error，便于调用方在日志里定位是哪一类失败。
//
// 副作用：发起真实网络请求并消耗第三方额度（成功一次计 1 个 credit 量级）。
func (p *TavilyProvider) Extract(ctx context.Context, targetURL string, key model.APIKey) (string, float64, error) {
	body := map[string]interface{}{
		"urls": []string{targetURL},
		// basic 档即可覆盖 Cloudflare 质询页这类主要场景，advanced 更贵且收益不确定
		"extract_depth": "basic",
		"format":        "markdown",
		// 不带 usage 就拿不到 credits，记账会静默变成 0
		"include_usage": true,
	}
	request, err := p.newJSONRequest(ctx, http.MethodPost, "/extract", body)
	if err != nil {
		return "", 0, err
	}
	// 与 Search 同一写法：Tavily 用 Bearer 令牌而不是自定义头
	request.Header.Set("Authorization", "Bearer "+key.Value)
	response, err := p.client.Do(request)
	if err != nil {
		return "", 0, err
	}
	payload, err := p.decodeResponse(response)
	if err != nil {
		return "", 0, err
	}
	content, err := tavilyExtractContent(payload)
	if err != nil {
		return "", 0, err
	}
	return content, usageNumber(payload, "usage.credits", "credits"), nil
}

// tavilyExtractContent 从 /extract 的响应体里取出正文。
//
// 两个数组都必须检查：results 为主结果，failed_results 记录被拒绝的目标（HTTP 200 也可能
// 只有后者）。两者都为空时按「响应里没有内容」报错，而不是返回空字符串 ——
// 空字符串会让抓取侧以为回退成功，最终把空内容当作抓取结果返回给模型。
//
// 参数 payload 为已解析的响应体。返回值：首条结果的 raw_content 与错误。
// 边界条件：results 存在但 raw_content 为空时同样报错（实测知乎/reddit 会返回空正文）。
// 本函数为纯函数，无副作用。
func tavilyExtractContent(payload map[string]interface{}) (string, error) {
	items := resultArray(payload, "results")
	for _, rawItem := range items {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		if content := stringValue(item, "raw_content", "content"); content != "" {
			return content, nil
		}
	}
	if reason := tavilyFailedResults(payload); reason != "" {
		return "", fmt.Errorf("tavily extract failed: %s", reason)
	}
	if len(items) == 0 {
		return "", fmt.Errorf("tavily extract returned no results")
	}
	return "", fmt.Errorf("tavily extract returned empty content")
}

// tavilyFailedResults 汇总 failed_results[].error 为一句可读的原因说明。
//
// 只取首条错误：抓取侧每次只传一个 URL，正常情况下不会有多条；
// 逐条拼接会让错误文案变长而信息量不增。无失败项时返回空串。
func tavilyFailedResults(payload map[string]interface{}) string {
	items := resultArray(payload, "failed_results")
	for _, rawItem := range items {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		if message := stringValue(item, "error", "message"); message != "" {
			return message
		}
	}
	return ""
}

func tavilyTimeRange(req model.SearchRequest) string {
	if value := optionString(req.Options, "time_range", "timeRange"); value != "" {
		return value
	}
	switch strings.ToLower(strings.TrimSpace(req.Freshness)) {
	case "day", "d", "qdr:d", "pd":
		return "day"
	case "week", "w", "qdr:w", "pw":
		return "week"
	case "month", "m", "qdr:m", "pm":
		return "month"
	case "year", "y", "qdr:y", "py":
		return "year"
	}
	switch days := optionInt(req.Options, "days"); {
	case days <= 0:
		return ""
	case days <= 1:
		return "day"
	case days <= 7:
		return "week"
	case days <= 31:
		return "month"
	default:
		return "year"
	}
}

func normalizeTavilyResults(payload map[string]interface{}, includeRaw bool) []model.SearchResult {
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
		content := stringValue(item, "raw_content", "content")
		snippet := stringValue(item, "content", "snippet", "description")
		if snippet == "" {
			snippet = content
		}
		score := floatValue(item, "score")
		if score == 0 {
			score = 1 / float64(index+1)
		}
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     truncate(snippet, 1000),
			Content:     truncate(content, 4000),
			Provider:    model.ProviderTavily,
			Providers:   []string{model.ProviderTavily},
			Score:       score,
			PublishedAt: parseTimeValue(stringValue(item, "published_date", "publishedDate", "date")),
		}
		if includeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}
