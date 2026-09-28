package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
)

type Config struct {
	Name      string
	BaseURL   string
	UserAgent string
	Timeout   time.Duration
	ProxyURL  string
}

type HTTPProvider struct {
	name      string
	baseURL   string
	userAgent string
	client    *http.Client
	// exactEndpoint 为 true 表示 baseURL 已是完整端点（配置里带 `#` 前缀），适配器不再拼接自己的路径。
	exactEndpoint bool
}

// NewHTTPProvider 构建通用 HTTP 适配器，是所有适配器 base_url 的唯一规范化点。
// cfg.BaseURL 以 `#` 开头表示「该地址即完整端点，适配器不再拼接自己的路径」。`#` 在 URL 里是
// fragment 分隔符，必须在解析前剥掉，否则 url.Parse("#https://x/y") 会得到空 Host 与填满的 Fragment。
// exact 模式不做 TrimRight("/")：用户已声明完整端点，原样保留最忠实。
// cfg.ProxyURL 为空或纯空白时直连（transport.Proxy 显式置 nil，不读代理环境变量）。
func NewHTTPProvider(cfg Config) *HTTPProvider {
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if proxyURL := NormalizeProxyURL(cfg.ProxyURL); proxyURL != "" {
		if parsed, err := url.Parse(proxyURL); err == nil {
			transport.Proxy = http.ProxyURL(parsed)
		}
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	exactEndpoint := strings.HasPrefix(baseURL, "#")
	if exactEndpoint {
		baseURL = strings.TrimSpace(strings.TrimPrefix(baseURL, "#"))
	} else {
		baseURL = strings.TrimRight(baseURL, "/")
	}
	return &HTTPProvider{
		name:          cfg.Name,
		baseURL:       baseURL,
		userAgent:     cfg.UserAgent,
		client:        &http.Client{Timeout: timeout, Transport: transport},
		exactEndpoint: exactEndpoint,
	}
}

func (p *HTTPProvider) Name() string {
	return p.name
}

func NormalizeProxyURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	if runningInContainer() {
		value = strings.Replace(value, "//127.0.0.1:", "//host.docker.internal:", 1)
		value = strings.Replace(value, "//localhost:", "//host.docker.internal:", 1)
	}
	return value
}

func runningInContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}

func (p *HTTPProvider) HealthCheck(ctx context.Context, key model.APIKey) error {
	if strings.TrimSpace(key.Value) == "" {
		return &Error{Type: ErrorTypeNoKey, Message: "empty api key"}
	}
	return nil
}

// SupportsAnonymousKey 报告该渠道在无密钥时是否能正常调用，默认返回 false。
//
// 只有实测确认「不带凭据也能拿到正常结果」的渠道才在自己的适配器里覆写为 true，
// 因此这里用保守的默认值，新增渠道不必逐个显式声明。
//
// 该结果仅用于管理台提示，**不参与任何放行判断**：网关按设计对空密钥条目一律放行，
// 中转站等场景下本方法返回 false 的渠道也可能因自定义 base_url 而实际可用。
// 返回值恒为 false，无参数、无副作用。
func (p *HTTPProvider) SupportsAnonymousKey() bool {
	return false
}

// requestURL 拼出最终请求地址。exact 端点（base_url 带 `#` 前缀）下忽略适配器自带的路径，
// 因为用户给出的地址本身就是完整端点；其余情况沿用 base_url + endpoint 的既有语义。
func (p *HTTPProvider) requestURL(endpoint string) string {
	if p.exactEndpoint {
		return p.baseURL
	}
	return p.baseURL + endpoint
}

// newJSONRequest 组装 JSON 请求体请求（POST/PUT）。exact 端点下 endpoint 被忽略，见 requestURL。
func (p *HTTPProvider) newJSONRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Request, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, p.requestURL(endpoint), reader)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if p.userAgent != "" {
		request.Header.Set("User-Agent", p.userAgent)
	}
	return request, nil
}

// newGETRequest 组装 GET 请求并在 params 非空时追加查询串。
// exact 端点自带 query 时必须用 `&` 连接，否则会拼出两个 `?` 的非法 URL。
func (p *HTTPProvider) newGETRequest(ctx context.Context, endpoint string, params url.Values) (*http.Request, error) {
	requestURL := p.requestURL(endpoint)
	if len(params) > 0 {
		separator := "?"
		if strings.Contains(requestURL, "?") {
			separator = "&"
		}
		requestURL += separator + params.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if p.userAgent != "" {
		request.Header.Set("User-Agent", p.userAgent)
	}
	return request, nil
}

// decodeResponse 读取并解析上游响应体，把 >=400 的状态码统一转成 *Error。
// 参数 response 必须非 nil 且 Body 可读（本函数负责关闭 Body）。
// 返回值：2xx 且 JSON 可解析时为 payload；否则 payload 为 nil 并返回 *Error。
// 边界条件：响应体超过 8MB 时只读取前 8MB（防上游返回超大页面拖垮进程）；
// 2xx 但 JSON 非法返回 ErrorTypeInvalidResponse。
// 需要按状态码分流（而不是一律当错误）的适配器请改用 decodeResponseWithStatus。
func (p *HTTPProvider) decodeResponse(response *http.Response) (map[string]interface{}, error) {
	payload, _, err := p.decodeResponseWithStatus(response)
	return payload, err
}

// decodeResponseWithStatus 与 decodeResponse 行为完全一致，但额外把 HTTP 状态码返回给调用方，
// 供需要按状态码分流的上游使用（例如 Context7 的 404 no_documentation_found 属正常空结果）。
// 参数 response 必须非 nil 且 Body 可读（本函数负责关闭 Body）。
// 返回值：payload 在非 2xx、响应体读取失败或 JSON 非法时为 nil；status 恒为 response.StatusCode；
// err 为 body 读取错误、ClassifyHTTPError 的结果或 ErrorTypeInvalidResponse。
// 之所以把状态码一并返回，是因为 decodeResponse 会把所有 >=400 折叠成 *Error 而丢失原始码，
// 适配器无法据此区分「正常的无结果」与「真故障」。
func (p *HTTPProvider) decodeResponseWithStatus(response *http.Response) (map[string]interface{}, int, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		return nil, response.StatusCode, err
	}
	if response.StatusCode >= 400 {
		return nil, response.StatusCode, ClassifyHTTPError(response.StatusCode, string(body))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, response.StatusCode, &Error{Type: ErrorTypeInvalidResponse, StatusCode: response.StatusCode, Message: err.Error()}
	}
	return payload, response.StatusCode, nil
}

func stringValue(item map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			switch typed := value.(type) {
			case string:
				return typed
			case float64:
				return strings.TrimRight(strings.TrimRight(jsonNumber(typed), "0"), ".")
			}
		}
	}
	return ""
}

func floatValue(item map[string]interface{}, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := item[key]; ok {
			switch typed := value.(type) {
			case float64:
				return typed
			case int:
				return float64(typed)
			case string:
				if parsed, err := strconv.ParseFloat(typed, 64); err == nil {
					return parsed
				}
			}
		}
	}
	return 0
}

func firstStringFromArray(item map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		values, ok := item[key].([]interface{})
		if !ok || len(values) == 0 {
			continue
		}
		if first, ok := values[0].(string); ok {
			return first
		}
	}
	return ""
}

func stringArrayValue(item map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		values, ok := item[key].([]interface{})
		if !ok || len(values) == 0 {
			continue
		}
		items := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				items = append(items, text)
			}
		}
		if len(items) > 0 {
			return items
		}
	}
	return nil
}

func resultArray(payload map[string]interface{}, keys ...string) []interface{} {
	for _, key := range keys {
		if values, ok := payload[key].([]interface{}); ok {
			return values
		}
	}
	return nil
}

func mapFromInterface(value interface{}) map[string]interface{} {
	if item, ok := value.(map[string]interface{}); ok {
		return item
	}
	return nil
}

func parseTimeValue(value string) *time.Time {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	layouts := []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04:05Z"}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return &parsed
		}
	}
	return nil
}

func jsonNumber(value float64) string {
	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(value, 'f', -1, 64), "0"), ".")
}

func usageMeasurements(providerName string, payload map[string]interface{}) []model.UsageMeasurement {
	if payload == nil {
		return nil
	}
	metadata := map[string]interface{}{"provider": providerName}
	measurements := []model.UsageMeasurement{}
	if credits := usageNumber(payload, "credits", "creditsUsed", "usage.credits", "usage.total_credits"); credits > 0 {
		measurements = append(measurements, model.UsageMeasurement{Unit: "credits", Quantity: credits, Metadata: metadata})
	}
	if tokens := usageNumber(payload, "total_tokens", "tokens", "token_count", "usage.total_tokens", "usage.tokens"); tokens > 0 {
		measurements = append(measurements, model.UsageMeasurement{Unit: "tokens", Quantity: tokens, Metadata: metadata})
	}
	// 仅把明确的 USD 字段当美元；泛化 cost 可能是 credits，避免假账单。
	cost := usageNumber(payload, "cost_usd", "total_cost_usd", "usage.cost_usd", "usage.total_cost_usd")
	if cost > 0 {
		measurements = append(measurements, model.UsageMeasurement{Unit: "usd", Quantity: cost, CostUSD: float64Pointer(cost), Metadata: metadata})
	}
	return measurements
}

func usageNumber(payload map[string]interface{}, keys ...string) float64 {
	for _, key := range keys {
		value, ok := nestedValue(payload, key)
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return typed
		case int:
			return float64(typed)
		case int64:
			return float64(typed)
		case json.Number:
			if parsed, err := typed.Float64(); err == nil {
				return parsed
			}
		case string:
			if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func nestedValue(payload map[string]interface{}, dotted string) (interface{}, bool) {
	parts := strings.Split(dotted, ".")
	var current interface{} = payload
	for _, part := range parts {
		item, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}
		current, ok = item[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func float64Pointer(value float64) *float64 {
	return &value
}

// truncate 把字符串截断到 max 字节，**按 UTF-8 字符边界回退**，保证结果始终是合法 UTF-8。
//
// 曾经直接用 value[:max] 截断：这在 max 落在多字节字符中间时会把该字符切成半个，
// 产出非法 UTF-8。上游 JSON 解码时不合法的部分会被替换为 U+FFFD（"�"），
// 于是响应末尾出现一串乱码，用户也会看到 JSON 里残留 � 转义。
// 中文摘要正好是高发场景：每个汉字 3 字节，max=1000 时 999/1000 落在字符中间。
//
// 参数 value 为待截断文本，max 为字节上限。返回值：max <= 0 或长度不超限时原样返回；
// 否则在不超过 max 的前提下回退到最近一个字符边界。**返回值可能短于 max**（最多短 3 字节）。
// 本函数为纯函数，无副作用。
func truncate(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	// 从 max 向前找第一个「非 UTF-8 续字节」的位置：续字节的高两位固定为 10，
	// 因此该位置必然是某个字符的起始字节，切在这里即保证不劈开任何字符。
	cut := max
	for cut > 0 && value[cut]&0xC0 == 0x80 {
		cut--
	}
	return value[:cut]
}

const (
	// DefaultSnippetLimit 是摘要的默认截断上限（字节）。
	DefaultSnippetLimit = 1000
	// DefaultContentLimit 是正文的默认截断上限（字节）。
	DefaultContentLimit = 4000
	// MaxContentLimit 是正文截断上限的硬顶（字节），与 fetch 的 maximumMaxLength 同量级：
	// 正文默认不返回，显式开启时也必须有个上限，否则单条正文就能吃掉整个上下文预算。
	MaxContentLimit = 50000
)

// resultOptions 是归一化结果时所需的输出开关与截断上限。
//
// 之所以做成值对象而不是继续往 normalize*Results 上加布尔参数：九个适配器共用同一套口径，
// 每加一个开关都要改九处签名，且调用点很难看出「这次到底开没开正文」。
type resultOptions struct {
	IncludeRaw     bool // 是否填充 result.Raw（上游原始条目）
	IncludeContent bool // 是否填充 result.Content（正文）
	SnippetCap     int  // 摘要截断上限（字节），已按渠道默认与请求值夹紧
	ContentCap     int  // 正文截断上限（字节）
}

// resultOptionsFrom 由请求参数与渠道默认封顶算出本次归一化的输出口径。
//
// 参数：req 为已应用默认值的搜索请求（负数长度视为未设置）；snippetCap 为该渠道自己的
// 摘要封顶（<=0 时回退 DefaultSnippetLimit）。
//
// 返回值：三个开关照抄请求，SnippetCap / ContentCap 为最终生效的字节上限。
//
// 边界条件：SnippetCap 取 min(渠道封顶, 请求值)，因此请求值只能收紧不能放宽 ——
// 允许放大就等于允许调用方绕过渠道侧的上下文保护；ContentLimit 超过 MaxContentLimit 时同样夹紧。
// IncludeContent 为 false 时 ContentCap 仍照常算出（内容压根不会被填充），避免调用点再判一次。
//
// 副作用：无。
func resultOptionsFrom(req model.SearchRequest, snippetCap int) resultOptions {
	if snippetCap <= 0 {
		snippetCap = DefaultSnippetLimit
	}
	effectiveSnippet := snippetCap
	if req.SnippetLimit > 0 && req.SnippetLimit < snippetCap {
		effectiveSnippet = req.SnippetLimit
	}
	effectiveContent := req.ContentLimit
	if effectiveContent <= 0 {
		effectiveContent = DefaultContentLimit
	}
	if effectiveContent > MaxContentLimit {
		effectiveContent = MaxContentLimit
	}
	return resultOptions{
		IncludeRaw:     req.IncludeRaw,
		IncludeContent: req.IncludeContent,
		SnippetCap:     effectiveSnippet,
		ContentCap:     effectiveContent,
	}
}

func requestLimit(limit, fallback, max int) int {
	if limit <= 0 {
		limit = fallback
	}
	if max > 0 && limit > max {
		return max
	}
	return limit
}

func optionString(options map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		value, ok := options[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			return strings.TrimSpace(typed)
		case float64:
			return jsonNumber(typed)
		case int:
			return strconv.Itoa(typed)
		}
	}
	return ""
}

func optionInt(options map[string]interface{}, keys ...string) int {
	for _, key := range keys {
		value, ok := options[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed
		case float64:
			return int(typed)
		case string:
			if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
				return parsed
			}
		}
	}
	return 0
}

func optionStringSlice(options map[string]interface{}, keys ...string) []string {
	for _, key := range keys {
		value, ok := options[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case []string:
			return typed
		case []interface{}:
			items := make([]string, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					items = append(items, strings.TrimSpace(text))
				}
			}
			if len(items) > 0 {
				return items
			}
		}
	}
	return nil
}
