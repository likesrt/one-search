package provider

import (
	"context"
	"net/http"
	"strings"

	"github.com/one-search/one-search/backend/internal/model"
)

const (
	// keenableKeyedEndpoint 是带 key 的搜索端点，鉴权走 X-API-Key 头。
	keenableKeyedEndpoint = "/v1/search"
	// keenablePublicEndpoint 是 Keenable 的专用无 key 端点。
	// 它与 keyed 端点不是「同一个端点少发一个头」：匿名调用必须改打这条路径，
	// 直接对 /v1/search 省略鉴权头只会拿到 401 Missing authentication。
	keenablePublicEndpoint = "/v1/search/public"
	// keenableAnonymousTitle 是匿名调用必须携带的应用标识头 X-Keenable-Title 的取值。
	//
	// 该头是「应用标识」（上游用于限流归因与来源识别），不是凭据，因此写死而不做成配置项：
	// 取值与 UPSTREAM_USER_AGENT 的默认值同名，便于上游认出流量来自本网关。
	// 实测缺失该头时 public 端点返回 400 Missing app identifier。
	keenableAnonymousTitle = "OneSearchRelay"
)

// KeenableProvider 是 Keenable 搜索渠道适配器。
// 按 key 是否为空走两条路径：key 非空打 POST /v1/search（X-API-Key 鉴权），
// key 为空打 POST /v1/search/public 并带 X-Keenable-Title 应用标识（详见 keenableEndpoint / keenableApplyAuth）。
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

// SupportsAnonymousKey 报告 Keenable 在无密钥时能否正常调用，恒为 true。
//
// 依据是实测：POST /v1/search/public 带 X-Keenable-Title 返回 200，响应结构与 keyed 路径一致。
// 代价是该路径按 IP 限流（1000 次/小时、10 次/秒）且为共享池，额度不受本网关控制，
// 因此管理台提示只说明「支持无密钥调用」，不暗示额度充足。
// 返回 true 只影响管理台提示，不改变 Search 的放行逻辑。
func (p *KeenableProvider) SupportsAnonymousKey() bool {
	return true
}

// Search 调用 Keenable 的搜索接口。
// 请求体由 keenableBody 组装，两条路径共用同一份请求体（仅端点与鉴权头不同）。
// 端点由 keenableEndpoint 决定：key 为空走 /v1/search/public，非空走 /v1/search；
// 请求头由 keenableApplyAuth 决定：key 为空发 X-Keenable-Title，非空发 X-API-Key。
// 返回结果按名次折算 score；上游不返回 usage 字段，因此 Usage 为空切片，
// 由编排层按「requests:1」兜底计次（不会报错）。
// 副作用：发起一次真实 HTTP 请求，Response.Body 由 decodeResponse 关闭。
func (p *KeenableProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	request, err := p.newJSONRequest(ctx, http.MethodPost, keenableEndpoint(key), keenableBody(req))
	if err != nil {
		return model.ProviderResponse{}, err
	}
	keenableApplyAuth(request, key)
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

// keenableEndpoint 返回该次调用应使用的端点路径：密钥为空的条目走匿名端点，否则走带 key 的端点。
//
// 判据用 strings.TrimSpace(key.Value) == "" 而不是严格等于空串，是为了与 security.MaskSecret 的
// 空值口径（纯空白同样视为空）保持一致：脱敏串为空时管理台会把该条目显示成「匿名」，
// 若这里只认严格空串，一条纯空白的密钥就会「显示为匿名、实际走带 key 路径并发空鉴权头」，
// 两边对同一份数据给出互相矛盾的结论。TrimSpace 只是把「空白」并入「空」，
// 对任何真实密钥（首尾不带空白，也不可能只有空白）没有行为差异。
//
// 注意 `#` 完整端点模式下 HTTPProvider.requestURL 会忽略这里返回的路径，
// 因此中转站用户仍可用完整端点覆盖；但那条路径不会自动带上匿名所需的 X-Keenable-Title，
// 这是有意保留的既有语义，不在这里特判。返回值恒为两个端点常量之一，无副作用。
func keenableEndpoint(key model.APIKey) string {
	if strings.TrimSpace(key.Value) == "" {
		return keenablePublicEndpoint
	}
	return keenableKeyedEndpoint
}

// keenableApplyAuth 按密钥是否为空写入对应的请求头。
//
// 密钥非空：写 X-API-Key（上游也接受 Authorization: Bearer，这里固定用前者）。
// 密钥为空：**不发 X-API-Key**，改发 X-Keenable-Title 应用标识。之所以必须跳过空头，
// 是因为实测带空 X-API-Key 头与完全不带鉴权同样被上游判为 401（空串不算「有鉴权」）。
// 空值判据与 keenableEndpoint 完全相同（TrimSpace 后为空），两处必须同源，
// 否则会出现「打了 public 端点却带 X-API-Key」这类自相矛盾的组合。
// 副作用：就地修改 request.Header；request 必须非 nil（调用方已完成错误检查）。
func keenableApplyAuth(request *http.Request, key model.APIKey) {
	if strings.TrimSpace(key.Value) == "" {
		request.Header.Set("X-Keenable-Title", keenableAnonymousTitle)
		return
	}
	request.Header.Set("X-API-Key", key.Value)
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
