package fetch

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Request 是单次抓取请求的规范化参数。
//
// 由 ParseRequest 完成校验后构造，不应手工填充 —— 各字段间的约束
// （如 body 与 method 的联动）只有在构造时统一校验才能保证不被绕过。
type Request struct {
	// URL 必须是 http/https 且带 host，由 parseRequestURL 校验
	URL *url.URL
	// Method 仅允许 GET 或 POST
	Method string
	// Body 仅 POST 可携带，以 UTF-8 字节发送
	Body string
	// HasBody 区分「未提供 body」与「提供了空字符串」，后者是合法输入
	HasBody bool
	// Headers 是调用方提供的请求头，同名时覆盖默认值
	Headers map[string]string
	// MaxLength 是返回内容的最大字符数（以 rune 计）
	MaxLength int
	// StartIndex 是续读起点（以 rune 计），仅 GET 可用
	StartIndex int
	// Raw 为 true 时跳过 Markdown 与 JSON 归一化，返回原始文本
	Raw bool
}

// ParseRequest 校验并规范化来自 REST 或 MCP 的原始参数。
//
// 参数 args 是 JSON 解码或查询串转换后的键值映射，键为 url、method、headers、body、
// max_length、start_index、raw（不含 proxy：代理是本功能的管理员级全局配置，
// 不接受请求级传参，否则等于把内网跳板开放给调用方）。
//
// 返回值：可交给 Fetcher.Fetch 的 Request；任一参数不合法时返回可读错误，
// 错误文案可直接回传给调用方或模型（项目 REST 层按 400 映射，MCP 层按 isError 返回）。
//
// 边界条件：缺 url、裸域名（无 scheme）、非 http(s)、method 非 GET/POST、
// body 出现在 GET 上、Content-Type 声明非 UTF-8、max_length 超出 1..50000、
// start_index 为负或在 POST 上出现，都会返回错误。
//
// 副作用：无（不发起任何网络请求，代理可用性由 NewFetcher 与拨号层负责）。
func ParseRequest(args map[string]any) (Request, error) {
	req := Request{}
	var err error
	if req.URL, err = parseRequestURL(args); err != nil {
		return Request{}, err
	}
	if req.Method, err = parseRequestMethod(args); err != nil {
		return Request{}, err
	}
	if req.Body, req.HasBody, err = parseRequestBody(args, req.Method); err != nil {
		return Request{}, err
	}
	if req.Headers, err = parseRequestMeta(args, req.HasBody); err != nil {
		return Request{}, err
	}
	if req.Raw, err = parseBoolArg(args, "raw", false); err != nil {
		return Request{}, err
	}
	if err := fillNumericArgs(&req, args); err != nil {
		return Request{}, err
	}
	return req, nil
}

// parseRequestMeta 解析请求头并补齐与 body 相关的 Content-Type。
//
// 两步合在一起是有意的：body 的编码方式与 Content-Type 相互约束，
// 分开校验会让「声明与实际不符」在两层之间漏掉。
//
// 参数 hasBody 决定是否需要 Content-Type 默认值与 charset 校验。
// 返回值：始终非 nil 的请求头映射；Content-Type 声明非 UTF-8 时返回错误。
func parseRequestMeta(args map[string]any, hasBody bool) (map[string]string, error) {
	headers := parseRequestHeaders(args)
	if err := applyContentType(headers, hasBody); err != nil {
		return nil, err
	}
	return headers, nil
}

// fillNumericArgs 解析并校验 max_length 与 start_index。
//
// 单独拆出是为了让 ParseRequest 保持在可读长度内。
// 其中 start_index 与 method 的联动是必须的：续读会重放请求，
// 而 POST 可能创建资源或二次计费，因此只允许 GET 续读。
func fillNumericArgs(req *Request, args map[string]any) error {
	maxLength, err := parseIntArg(args, "max_length", defaultMaxLength)
	if err != nil {
		return err
	}
	if maxLength < 1 || maxLength > maximumMaxLength {
		return fmt.Errorf("Invalid max_length: expected a value from 1 to %d", maximumMaxLength)
	}
	startIndex, err := parseIntArg(args, "start_index", 0)
	if err != nil {
		return err
	}
	if startIndex < 0 {
		return errors.New("Invalid start_index: expected a non-negative value")
	}
	if startIndex > 0 && req.Method != http.MethodGet {
		return fmt.Errorf("Invalid start_index: a %s response cannot be continued "+
			"because that would repeat the request; raise max_length instead", req.Method)
	}
	req.MaxLength = maxLength
	req.StartIndex = startIndex
	return nil
}

// parseRequestURL 解析并校验 url 参数。
//
// 仅接受 http 与 https，且必须带 host —— 裸域名（如 example.com）会被拒绝：
// 缺少 scheme 时无法确定目标端口与协议，静默补全 https 会掩盖调用方的笔误。
func parseRequestURL(args map[string]any) (*url.URL, error) {
	raw := strings.TrimSpace(argString(args, "url"))
	if raw == "" {
		return nil, errors.New("Invalid arguments: expected an object containing url")
	}
	target, err := url.Parse(raw)
	if err != nil || target.Host == "" ||
		(target.Scheme != "http" && target.Scheme != "https") {
		return nil, fmt.Errorf("Invalid url: %s", raw)
	}
	return target, nil
}

// parseRequestMethod 解析 method 参数，缺省为 GET。
//
// 仅支持 GET 与 POST：PUT/PATCH/DELETE 会让模型免费获得破坏性语义，
// 而抓取工具的定位是读取，不需要写操作。
func parseRequestMethod(args map[string]any) (string, error) {
	raw, ok := args["method"]
	if !ok || raw == nil {
		return http.MethodGet, nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", errors.New("Invalid method: expected a string")
	}
	method := strings.ToUpper(strings.TrimSpace(text))
	if method != http.MethodGet && method != http.MethodPost {
		return "", fmt.Errorf("Invalid method: %s is not supported; expected GET or POST", method)
	}
	return method, nil
}

// parseRequestBody 解析 body 参数并校验其与 method 的匹配关系。
//
// 模型常把 body 传成 JSON 对象而非字符串，这里自动序列化而不是直接报错。
// 返回的 hasBody 用于区分「未提供」与「提供了空字符串」，后者是合法的。
func parseRequestBody(args map[string]any, method string) (string, bool, error) {
	raw, ok := args["body"]
	if !ok || raw == nil {
		return "", false, nil
	}
	var body string
	switch typed := raw.(type) {
	case string:
		body = typed
	case map[string]any, []any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "", false, fmt.Errorf("Invalid body: %w", err)
		}
		body = string(encoded)
	default:
		return "", false, errors.New("Invalid body: expected a string or a JSON value")
	}
	if method != http.MethodPost {
		return "", false, errors.New("Invalid body: only POST requests can carry a body")
	}
	return body, true, nil
}

// parseRequestHeaders 提取调用方提供的请求头。
//
// 非字符串值按文本形式转换，空键或空值直接忽略。
// 始终返回非 nil 映射，便于后续写入。
func parseRequestHeaders(args map[string]any) map[string]string {
	headers := map[string]string{}
	raw, ok := args["headers"].(map[string]any)
	if !ok {
		return headers
	}
	for key, value := range raw {
		if key == "" || value == nil {
			continue
		}
		headers[key] = fmt.Sprint(value)
	}
	return headers
}

// applyContentType 补齐或校验与 body 相关的 Content-Type。
//
// body 一律以 UTF-8 字节发送，因此声明了其他 charset 的 Content-Type 会被拒绝：
// 那样声明与实际字节不符，接收方会按错误编码解读而非自动转码。
// 未提供 Content-Type 时补上 application/json 作为默认值。
func applyContentType(headers map[string]string, hasBody bool) error {
	if !hasBody {
		return nil
	}
	key := findHeaderKey(headers, "content-type")
	if key == "" {
		headers["Content-Type"] = "application/json; charset=utf-8"
		return nil
	}
	declared := charsetOf(headers[key])
	if declared != "" && declared != "utf-8" && declared != "utf8" {
		return fmt.Errorf("Invalid headers: request bodies are sent as UTF-8, "+
			"so Content-Type cannot declare charset=%s", declared)
	}
	return nil
}

// findHeaderKey 以大小写不敏感方式查找请求头，返回映射中的实际键名；未命中返回空串。
func findHeaderKey(headers map[string]string, name string) string {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return key
		}
	}
	return ""
}

// argString 读取字符串型参数；类型不符时返回空串，由调用方的校验给出错误。
func argString(args map[string]any, name string) string {
	if value, ok := args[name].(string); ok {
		return value
	}
	return ""
}

// parseIntArg 解析整数参数，兼容 JSON 数字被解码为 float64 或 json.Number 的情况。
//
// 小数与超范围值一律报错，不做静默截断 —— 截断会让实际行为与调用方预期不符。
func parseIntArg(args map[string]any, name string, fallback int) (int, error) {
	raw, ok := args[name]
	if !ok || raw == nil {
		return fallback, nil
	}
	switch typed := raw.(type) {
	case int:
		return typed, nil
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) {
			return 0, fmt.Errorf("Invalid %s: expected an integer", name)
		}
		return int(typed), nil
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("Invalid %s: expected an integer", name)
		}
		return int(number), nil
	default:
		return 0, fmt.Errorf("Invalid %s: expected an integer", name)
	}
}

// parseBoolArg 解析布尔参数；类型不符时报错而非静默取默认值。
func parseBoolArg(args map[string]any, name string, fallback bool) (bool, error) {
	raw, ok := args[name]
	if !ok || raw == nil {
		return fallback, nil
	}
	value, ok := raw.(bool)
	if !ok {
		return false, fmt.Errorf("Invalid %s: expected a boolean", name)
	}
	return value, nil
}

// ArgsFromQuery 把查询串参数转换为抓取参数映射，供 REST 的 GET 形态使用。
//
// 查询串中的值一律是字符串，而数值与布尔参数需按类型传给校验逻辑，
// 因此在这里完成类型转换。转换失败即报错，不静默取默认值 ——
// 否则 ?raw=yes 这类笔误会被当成 false，调用方难以察觉。
//
// 只转换 GET 形态支持的字段：请求头与请求体不便用查询串表达，
// 且把认证头放进 URL 会留在访问日志与浏览器历史里，故仅在 POST 形态提供。
//
// 参数 values 为已解析的查询串。返回值是可直接交给 ParseRequest 的映射；
// 数值或布尔写法非法时返回错误。
func ArgsFromQuery(values url.Values) (map[string]any, error) {
	args := map[string]any{}
	for _, key := range []string{"url", "method"} {
		if value := values.Get(key); value != "" {
			args[key] = value
		}
	}
	for _, key := range []string{"max_length", "start_index"} {
		raw := values.Get(key)
		if raw == "" {
			continue
		}
		number, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("Invalid %s: expected an integer", key)
		}
		args[key] = number
	}
	if raw := values.Get("raw"); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, errors.New("Invalid raw: expected a boolean")
		}
		args["raw"] = value
	}
	return args, nil
}
