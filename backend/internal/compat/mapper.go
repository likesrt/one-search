package compat

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/one-search/one-search/backend/internal/fetch"
	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
)

func TavilyToNative(req TavilySearchRequest) model.SearchRequest {
	options := map[string]interface{}{}
	if req.SearchDepth != "" {
		options["search_depth"] = req.SearchDepth
	}
	if req.Topic != "" {
		options["topic"] = req.Topic
	}
	if req.Days > 0 {
		options["days"] = req.Days
	}
	if len(req.IncludeDomains) > 0 {
		options["include_domains"] = req.IncludeDomains
	}
	if len(req.ExcludeDomains) > 0 {
		options["exclude_domains"] = req.ExcludeDomains
	}
	return model.SearchRequest{
		Query:        req.Query,
		Providers:    req.Providers,
		Mode:         model.SearchMode(req.Mode),
		Limit:        req.MaxResults,
		IncludeRaw:   req.IncludeRawContent,
		Cache:        model.CachePolicy(req.Cache),
		CompatFormat: model.CompatFormatTavily,
		Options:      options,
	}
}

func TavilyFromNative(query string, response model.SearchResponse) TavilySearchResponse {
	results := make([]TavilyResult, 0, len(response.Results))
	for _, item := range response.Results {
		results = append(results, TavilyResult{
			Title:      item.Title,
			URL:        item.URL,
			Content:    firstNonEmpty(item.Snippet, item.Content),
			RawContent: item.Content,
			Score:      item.Score,
		})
	}
	return TavilySearchResponse{
		Query:        query,
		Results:      results,
		ResponseTime: float64(response.Meta.LatencyMS) / 1000,
		RequestID:    response.Meta.RequestID,
	}
}

func SerperToNative(req SerperSearchRequest) model.SearchRequest {
	options := map[string]interface{}{}
	if req.Page > 0 {
		options["page"] = req.Page
	}
	if req.TBS != "" {
		options["tbs"] = req.TBS
	}
	if req.Gl != "" {
		options["gl"] = req.Gl
	}
	if req.Hl != "" {
		options["hl"] = req.Hl
	}
	return model.SearchRequest{
		Query:        req.Q,
		Providers:    req.Providers,
		Mode:         model.SearchMode(req.Mode),
		Limit:        req.Num,
		Freshness:    req.TBS,
		Cache:        model.CachePolicy(req.Cache),
		CompatFormat: model.CompatFormatSerper,
		Options:      options,
	}
}

func SerperFromNative(req SerperSearchRequest, response model.SearchResponse) SerperSearchResponse {
	organic := make([]SerperOrganicResult, 0, len(response.Results))
	for index, item := range response.Results {
		organic = append(organic, SerperOrganicResult{
			Title:    item.Title,
			Link:     item.URL,
			Snippet:  item.Snippet,
			Position: index + 1,
		})
	}
	return SerperSearchResponse{
		SearchParameters: map[string]interface{}{"q": req.Q, "num": req.Num, "type": "search"},
		Organic:          organic,
		Credits:          len(response.Meta.ProvidersQueried),
		RequestID:        response.Meta.RequestID,
	}
}

func OpenAIToNative(req OpenAISearchRequest) model.SearchRequest {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		query = strings.TrimSpace(req.Input)
	}
	return model.SearchRequest{
		Query:        query,
		Providers:    req.Providers,
		Mode:         model.SearchMode(req.Mode),
		Limit:        req.Limit,
		Cache:        model.CachePolicy(req.Cache),
		CompatFormat: model.CompatFormatOpenAI,
	}
}

func OpenAIFromNative(response model.SearchResponse) OpenAISearchResponse {
	lines := make([]string, 0, len(response.Results))
	for index, item := range response.Results {
		lines = append(lines, fmt.Sprintf("%d. %s - %s\n%s", index+1, item.Title, item.URL, firstNonEmpty(item.Snippet, item.Content)))
	}
	return OpenAISearchResponse{
		ID:            response.Meta.RequestID,
		Object:        "response",
		Status:        "completed",
		SearchResults: response.Results,
		Output: []map[string]interface{}{
			{"type": "web_search_call", "status": "completed"},
			{"type": "message", "content": []map[string]string{{"type": "output_text", "text": strings.Join(lines, "\n\n")}}},
		},
	}
}

const (
	// deepSeekMaxQueryRunes 是官方对 query / ori_question 的字符数上限（按 Unicode 码点计）。
	deepSeekMaxQueryRunes = 128
	// deepSeekDefaultCount / deepSeekMaxCount 对应官方 count 的默认值与上限。
	deepSeekDefaultCount = 10
	deepSeekMaxCount     = 50
	// deepSeekMaxURLBytes 是官方 fetch 对 url 的长度上限。
	deepSeekMaxURLBytes = 8192
)

// DeepSeekToNative 把 DeepSeek 搜索请求映射为原生请求，并按官方契约完成校验。
//
// 官方字段：query（必填、1..128 字符、不能全为空白）、count（1..50，缺省 10）、
// ori_question（可选）、localization（可选）、need_content（对应 IncludeContent）。
// providers / mode / cache 是本服务附加的扩展字段，官方客户端不传。
//
// 返回值：可直接交给编排层的请求；参数不合契约时返回可读错误（调用方按 400 返回）。
// 边界条件：ori_question 传 null 或空串都视为未提供；localization 只透传给 DeepSeek 渠道。
// 无副作用。
func DeepSeekToNative(req DeepSeekSearchRequest) (model.SearchRequest, error) {
	if err := validateDeepSeekQuery(req.Query); err != nil {
		return model.SearchRequest{}, err
	}
	count, err := deepSeekCount(req.Count)
	if err != nil {
		return model.SearchRequest{}, err
	}
	options, err := deepSeekOptions(req.OriQuestion, req.Localization)
	if err != nil {
		return model.SearchRequest{}, err
	}
	return model.SearchRequest{
		Query:          req.Query,
		Providers:      req.Providers,
		Mode:           model.SearchMode(req.Mode),
		Limit:          count,
		Cache:          model.CachePolicy(req.Cache),
		IncludeContent: req.NeedContent,
		// 官方 need_content 没有配套的长度参数，按本服务硬顶返回整页正文；
		// 沿用原生默认的 4000 字节会让 DeepSeek 客户端拿到的 content 明显短于预期。
		ContentLimit: deepSeekContentLimit(req.NeedContent),
		CompatFormat: model.CompatFormatDeepSeek,
		Options:      options,
	}, nil
}

// deepSeekContentLimit 返回 need_content 为真时的正文上限：取编排层的硬顶，
// 为 false 时返回 0（不产生任何正文）。硬顶来自 provider 包，避免在兼容层复制常量。
func deepSeekContentLimit(needContent bool) int {
	if !needContent {
		return 0
	}
	return provider.MaxContentLimit
}

// validateDeepSeekQuery 按官方规则校验 query：不能为空或全为空白，且不超过 128 个码点。
// 长度按原始字符串计算（不裁剪），避免「大量空白 + 一个字符」绕过上限。
func validateDeepSeekQuery(query string) error {
	if strings.TrimSpace(query) == "" {
		return errors.New("'query' is required")
	}
	if utf8.RuneCountInString(query) > deepSeekMaxQueryRunes {
		return errors.New("'query' must be between 1 and 128 characters")
	}
	return nil
}

// deepSeekCount 解析 count：未传取官方默认值 10，传了就必须落在 1..50。
func deepSeekCount(raw *int) (int, error) {
	if raw == nil {
		return deepSeekDefaultCount, nil
	}
	if *raw < 1 || *raw > deepSeekMaxCount {
		return 0, errors.New("'count' must be between 1 and 50")
	}
	return *raw, nil
}

// deepSeekOptions 把可选的 ori_question / localization 写入渠道 options。
//
// ori_question 是给搜索意图的补充说明；空串与 null 同义（都视为未提供），
// 只有非空值才校验长度并透传。localization 是官方用于优先本地化结果的 IP，
// 本服务不解析其语义，原样交给 DeepSeek 渠道；其它渠道会忽略未知 options。
func deepSeekOptions(question *string, localization string) (map[string]interface{}, error) {
	options := map[string]interface{}{}
	if question != nil {
		trimmed := strings.TrimSpace(*question)
		if trimmed != "" {
			if utf8.RuneCountInString(trimmed) > deepSeekMaxQueryRunes {
				return nil, errors.New("'ori_question' must be between 1 and 128 characters")
			}
			options["ori_question"] = trimmed
		}
	}
	if trimmed := strings.TrimSpace(localization); trimmed != "" {
		options["localization"] = trimmed
	}
	return options, nil
}

// DeepSeekFromNative 把原生搜索结果映射为 DeepSeek 响应。
//
// needContent 为 false 时 content 恒为空字符串（官方约定），summary 回退到 snippet；
// 为 true 时正文来自原生 content，summary 优先复用正文，因为本服务没有独立的
// 「600..800 字长摘要」数据源。score 在编排层已统一为 0..1，这里乘回 3 还原官方量纲。
// site_name / site_icon 本服务不采集，按官方契约输出 null；authority 未知输出 -1。
//
// 返回值：results 恒为非 nil 切片（无结果时是空数组）。
// 无副作用。
func DeepSeekFromNative(response model.SearchResponse, needContent bool) DeepSeekSearchResponse {
	results := make([]DeepSeekSearchResult, 0, len(response.Results))
	for _, item := range response.Results {
		content := ""
		summary := item.Snippet
		if needContent {
			content = item.Content
			if strings.TrimSpace(content) != "" {
				summary = content
			}
		}
		results = append(results, DeepSeekSearchResult{
			RerankScore: deepSeekRerankScore(item.Score),
			Title:       item.Title,
			URL:         item.URL,
			Snippet:     item.Snippet,
			Summary:     summary,
			Content:     content,
			Date:        deepSeekDate(item.PublishedAt),
			Authority:   -1,
		})
	}
	return DeepSeekSearchResponse{Results: results}
}

// deepSeekRerankScore 把编排层统一后的 0..1 分数还原为官方的 0..3 量纲。
// NaN 与负数按 0 处理，超过 3 的按 3 夹紧；保留三位小数，避免浮点尾数外泄。
func deepSeekRerankScore(score float64) float64 {
	scaled := score * 3
	if math.IsNaN(scaled) || scaled < 0 {
		return 0
	}
	if scaled > 3 {
		return 3
	}
	return math.Round(scaled*1000) / 1000
}

// deepSeekDate 把发布时间格式化为官方要求的 UTC RFC 3339 字符串；未知时返回 nil（JSON null）。
func deepSeekDate(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
}

// DeepSeekFetchToNative 校验 DeepSeek 抓取请求并转换为 fetch.ParseRequest 所需的参数。
//
// 官方约束：url 必填、不超过 8192 字符、必须是带 host 的 HTTP/HTTPS 地址、
// 不能携带用户名或密码。官方 fetch 没有长度参数，这里取本服务的硬上限返回整页正文，
// 避免默认 5000 字符把长文截断。
//
// 返回值：url 与 max_length 参数映射；任一约束不满足时返回可读错误（调用方按 400 返回）。
// 无副作用（真正的网络请求发生在抓取执行体）。
func DeepSeekFetchToNative(req DeepSeekFetchRequest) (map[string]any, error) {
	raw := strings.TrimSpace(req.URL)
	if raw == "" {
		return nil, errors.New("'url' is required")
	}
	if len(raw) > deepSeekMaxURLBytes {
		return nil, errors.New("'url' must not exceed 8192 characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("'url' must be an HTTP or HTTPS URL with a host")
	}
	if parsed.User != nil {
		return nil, errors.New("'url' must not contain a username or password")
	}
	return map[string]any{"url": raw, "max_length": fetch.MaximumMaxLength}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
