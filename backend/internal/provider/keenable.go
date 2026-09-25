package provider

import (
	"context"
	"net/http"

	"github.com/one-search/one-search/backend/internal/model"
)

// KeenableProvider 是 Keenable 搜索渠道适配器。
// 只实现带 key 的 POST /v1/search（鉴权头 X-API-Key），不做无 key 的 public 端点。
type KeenableProvider struct {
	*HTTPProvider
}

// NewKeenableProvider 构建 Keenable 适配器。
// cfg.BaseURL 为空时回退官方地址 https://api.keenable.ai；
// 带 `#` 前缀的完整端点语法由 NewHTTPProvider 统一处理。
func NewKeenableProvider(cfg Config) *KeenableProvider {
	cfg.Name = model.ProviderKeenable
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.keenable.ai"
	}
	return &KeenableProvider{HTTPProvider: NewHTTPProvider(cfg)}
}

// Search 调用 Keenable 的 POST /v1/search。
// 请求体由 keenableBody 组装，鉴权走 X-API-Key 头（上游也接受 Authorization: Bearer，这里固定用前者）。
// 返回结果按名次折算 score；上游不返回 usage 字段，因此 Usage 为空切片，
// 由编排层按「requests:1」兜底计次（不会报错）。key.Value 为空时仍会发送空头，上游会以 401 拒绝。
func (p *KeenableProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	request, err := p.newJSONRequest(ctx, http.MethodPost, "/v1/search", keenableBody(req))
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
	results := normalizeKeenableResults(payload, req.IncludeRaw)
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderKeenable, payload), Raw: payload}, nil
}

// keenableOptionStrings 是「逐字透传」的字符串型 options 到上游字段名的映射：
// 键为上游字段名，值为该字段接受的 options 别名（首个是规范名）。空值不写入请求体。
var keenableOptionStrings = map[string][]string{
	"site":             {"site"},
	"acquired_after":   {"acquired_after", "acquiredAfter"},
	"acquired_before":  {"acquired_before", "acquiredBefore"},
	"published_after":  {"published_after", "publishedAfter"},
	"published_before": {"published_before", "publishedBefore"},
	"query_time":       {"query_time", "queryTime"},
}

// keenableBody 组装 Keenable 的请求体。
// max_results = min(limit, 50)，limit <= 0 时为 10（上游上限即 50，超出会被拒绝）。
// mode 取 options.mode，缺省 pro；snippet_max_length 夹紧到 180..10000，未配置时不发送该键。
func keenableBody(req model.SearchRequest) map[string]interface{} {
	body := map[string]interface{}{
		"query":       req.Query,
		"max_results": requestLimit(req.Limit, 10, 50),
		"mode":        keenableMode(req.Options),
	}
	for field, aliases := range keenableOptionStrings {
		if value := optionString(req.Options, aliases...); value != "" {
			body[field] = value
		}
	}
	if length := clampIntRange(optionInt(req.Options, "snippet_max_length", "snippetMaxLength"), 180, 10000); length > 0 {
		body["snippet_max_length"] = length
	}
	return body
}

// keenableMode 解析 options.mode，缺省 pro。
// 只认 pro / realtime 两个上游取值：其它值会让上游直接报参数错误，
// 与其把用户的笔误透传上去，不如回退到默认模式。
func keenableMode(options map[string]interface{}) string {
	switch optionString(options, "mode") {
	case "realtime":
		return "realtime"
	default:
		return "pro"
	}
}

// clampIntRange 把 value 夹紧到 [min,max]。
// value <= 0 返回 0，语义为「调用方未配置该参数」——调用点据此决定不发送该键，
// 避免用 0 覆盖上游的默认值。
func clampIntRange(value, min, max int) int {
	if value <= 0 {
		return 0
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// normalizeKeenableResults 把 Keenable 响应归一化为统一的搜索结果。
// 只读 results 数组，缺 url 的条目直接丢弃（与其余适配器一致）。
// 摘要以 snippet 为主、description 兜底：实测 description 可能为空串而 snippet 有值。
// 上游没有 score 字段，因此用 1/(序号+1) 按名次折算，与 jina / serper 的兜底口径一致。
func normalizeKeenableResults(payload map[string]interface{}, includeRaw bool) []model.SearchResult {
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
		snippet := stringValue(item, "snippet")
		if snippet == "" {
			snippet = stringValue(item, "description")
		}
		result := model.SearchResult{
			Title:       stringValue(item, "title"),
			URL:         url,
			Snippet:     snippet,
			Content:     truncate(snippet, 4000),
			Provider:    model.ProviderKeenable,
			Providers:   []string{model.ProviderKeenable},
			Score:       1 / float64(index+1),
			PublishedAt: parseTimeValue(stringValue(item, "published_at")),
		}
		if includeRaw {
			result.Raw = item
		}
		results = append(results, result)
	}
	return results
}
