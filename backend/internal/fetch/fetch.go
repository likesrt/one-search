// Package fetch 提供「抓取任意 URL → 转紧凑 Markdown → 按字符数截断并可续读」的抓取执行体。
//
// 本包由项目内既有的独立抓取工具移植而来，只保留执行核心，不含 HTTP 路由与 MCP 协议部分：
// 路由、鉴权、日志与配置读写都在 internal/api 与 internal/db 里完成，配置通过 Config 注入，
// 因此本包不读数据库、不感知调用方身份。
//
// 面向服务端场景的设计取舍：
//   - 默认拦截内网地址以防 SSRF（Config.AllowPrivate 可放开），判定在 TCP 拨号层而非仅校验 URL
//     字符串，以覆盖「重定向到内网」与「DNS rebinding」两种绕过；
//   - 响应体读取量、总超时、重定向次数都有上限，对外服务不能假定对端返回的体积与速度合理；
//   - 截断按 Unicode 码点计，不会把多字节字符切成两半。
//
// 已知能力边界：本包不执行 JavaScript，纯客户端渲染（CSR）的 SPA 站点与 Cloudflare 之类的
// 主动质询页只能拿到空内容或质询页本身；也不检查 robots.txt，合规由调用方自行保证。
package fetch

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultMaxLength 是默认返回的字符数上限。
	// 取值偏小是有意的：多数页面读开头即可判断是否有用，而模型上下文昂贵；
	// 确需更多内容时由调用方提高 max_length，或用 start_index 分段续读。
	defaultMaxLength = 5000
	// maximumMaxLength 是单次返回的硬上限，防止一次调用挤占过多上下文。
	// 取 50000 字符：足以覆盖长文与技术文档的完整正文（按中英混排约折合
	// 1.5 万至 2.5 万 token），同时仍留有上限兜底。
	maximumMaxLength = 50000
	// defaultUserAgent 是默认 UA。沿用浏览器标识，因为不少站点会拒绝空 UA
	// 或非浏览器 UA 的请求，用爬虫标识会被直接拦截。
	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	// defaultMaxResponseBytes 限制响应体读取量，防止超大响应耗尽内存。
	defaultMaxResponseBytes = 10 << 20 // 10 MiB
	// defaultTimeout 是单次抓取的总体超时（含重定向链）
	defaultTimeout = 30 * time.Second
	// maxTimeout 是超时配置的上界。
	// 与 deploy/nginx.conf 的 proxy_read_timeout（65s）对齐：再长会先被反代断开，
	// 调用方只会看到 504，反而丢失真实错误。
	maxTimeout = 60 * time.Second
	// defaultMaxRedirects 是允许跟随的最大重定向次数
	defaultMaxRedirects = 10
	// dialTimeout 是单次 TCP 建连超时
	dialTimeout = 10 * time.Second
)

// ToolName 是暴露给模型的 MCP 工具名，同时也是续读提示中回指的调用名。
const ToolName = "fetch"

// Config 是抓取执行体的外部配置，由调用方从数据库配置注入，本包不读库。
type Config struct {
	// ProxyURL 是已规范化的代理地址（调用方负责补 scheme 与容器内地址改写），空串表示直连
	ProxyURL string
	// AllowPrivate 为 true 时不再拦截内网与环回目标，仅应在完全可信的部署中开启
	AllowPrivate bool
	// Timeout 是单次抓取的总体超时，非正时取 defaultTimeout，超过 maxTimeout 时按上界收敛
	Timeout time.Duration
	// MaxResponseBytes 限制单次响应体的读取上限，非正时取 defaultMaxResponseBytes
	MaxResponseBytes int64
	// UserAgent 是默认 User-Agent，可被请求级 headers 覆盖，空串时取 defaultUserAgent
	UserAgent string
}

// Result 是单次抓取的原生结果，REST 与管理台试抓共用这一结构。
//
// 注意 StatusCode 是上游返回的状态码：抓到 4xx/5xx 页面本身仍是一次成功的抓取
// （自定义 API 常把错误详情放在响应体里，只报状态码无法定位问题），
// 因此调用方必须读 StatusCode 而不是只看 HTTP 层是否出错。
type Result struct {
	// URL 是本次抓取的最终地址（跟随重定向后仍为原始请求地址）
	URL string `json:"url"`
	// Method 是实际使用的出站方法，仅 GET 或 POST
	Method string `json:"method"`
	// StatusCode 是上游响应状态码，可能为 4xx/5xx
	StatusCode int `json:"status_code"`
	// ContentType 是上游响应的 Content-Type 原始值，未提供时为空串
	ContentType string `json:"content_type"`
	// Content 是归一化并按 max_length 截断后的内容（含续读提示）
	Content string `json:"content"`
	// Truncated 表示内容是否因超出 max_length 而被截断
	Truncated bool `json:"truncated"`
	// NextStartIndex 是续读起点，仅在 Truncated 为 true 时有意义；未截断时等于 TotalLength
	NextStartIndex int `json:"next_start_index"`
	// TotalLength 是归一化后完整内容的字符数（以 rune 计）
	TotalLength int `json:"total_length"`
}

// Text 返回面向模型阅读的文本。
//
// 上游返回非 2xx 时在正文前补一行状态码：MCP 工具结果只有文本通道，
// 不补这一行模型就无法判断拿到的是错误页（源工具即以该形式输出）。
// 正文为空时只返回状态码行，避免出现「HTTP 404 + 空白」这种无信息量的结果。
func (r Result) Text() string {
	if r.StatusCode >= 200 && r.StatusCode < 300 {
		return r.Content
	}
	if strings.TrimSpace(r.Content) == "" {
		return fmt.Sprintf("HTTP %d", r.StatusCode)
	}
	return fmt.Sprintf("HTTP %d\n\n%s", r.StatusCode, r.Content)
}

// Fetcher 封装抓取行为与安全策略，构造后可被多个 goroutine 并发复用。
type Fetcher struct {
	// client 是直连客户端；配置了代理时指向按该代理构造的客户端
	client *http.Client
	// timeout 是单次抓取的总体超时，用于给每次 Fetch 派生带超时的 context
	timeout time.Duration
	// maxResponseBytes 限制单次响应体的读取上限
	maxResponseBytes int64
	// userAgent 是默认 User-Agent，可被请求级 headers 覆盖
	userAgent string
	// allowPrivate 决定是否放行内网目标
	allowPrivate bool
	// proxy 是管理员配置的代理，nil 表示直连；代理地址视为可信（拨号层放行），
	// 但仍会对目标地址做字面量兜底检查，见 guard.go
	proxy *url.URL
}

// NewFetcher 构造抓取执行体。
//
// 参数 cfg 中的零值都会被替换为安全默认值：Timeout 非正取 30s（超过 60s 收敛到 60s）、
// MaxResponseBytes 非正取 10MiB、UserAgent 为空取浏览器标识。ProxyURL 会被解析，
// 解析失败时按「无代理」处理并记日志 —— 宁可直连失败，也不能带着一个不确定的代理运行。
//
// 返回的 *Fetcher 可安全并发使用；代理客户端只构造一次以保留连接池。
//
// 副作用：ProxyURL 为域名时会做一次 DNS 解析（结果用于拨号层放行名单），失败仅记日志。
func NewFetcher(cfg Config) *Fetcher {
	fetcher := &Fetcher{
		timeout:          normalizeTimeout(cfg.Timeout),
		maxResponseBytes: cfg.MaxResponseBytes,
		userAgent:        strings.TrimSpace(cfg.UserAgent),
		allowPrivate:     cfg.AllowPrivate,
	}
	if fetcher.maxResponseBytes <= 0 {
		fetcher.maxResponseBytes = defaultMaxResponseBytes
	}
	if fetcher.userAgent == "" {
		fetcher.userAgent = defaultUserAgent
	}
	proxy, err := parseProxyURL(cfg.ProxyURL)
	if err != nil {
		log.Printf("fetch: 代理地址 %q 无效，已按直连处理: %v", cfg.ProxyURL, err)
	}
	fetcher.proxy = proxy
	fetcher.client = fetcher.buildClient(proxy)
	return fetcher
}

// normalizeTimeout 收敛超时配置到 [1s, maxTimeout] 区间。
//
// 上界存在的原因是反代会先于后端超时断开（nginx proxy_read_timeout 65s），
// 配得比它更长只会让调用方拿到 504 而看不到真实错误。
func normalizeTimeout(value time.Duration) time.Duration {
	if value <= 0 {
		return defaultTimeout
	}
	if value > maxTimeout {
		return maxTimeout
	}
	return value
}

// buildClient 按代理构造 HTTP 客户端，proxy 为 nil 时返回直连客户端。
//
// 两种形态共用同一份拨号护栏：直连时护栏判定目标是内网则拦截；
// 走代理时护栏放行代理主机自身的 IP（管理员配置的代理视为可信），
// 目标侧的字面量兜底检查在 doRequest 里另行完成。
func (f *Fetcher) buildClient(proxy *url.URL) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   dialTimeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if proxy == nil {
		transport.DialContext = newGuardDialer(f.allowPrivate, nil).DialContext
		return newHTTPClient(transport, f.timeout)
	}
	// 代理域名解析失败时放行名单为空：此后所有私网地址（含该代理）都会被护栏拦截，
	// 表现为抓取失败而不是静默绕过，符合「不放行不确定的地址」的策略。
	allowed := resolveProxyIPs(proxy)
	if err := applyProxyTransport(transport, proxy, f.allowPrivate, allowed); err != nil {
		log.Printf("fetch: 代理 %q 不可用，已按直连处理: %v", proxy, err)
		direct := &http.Transport{
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   dialTimeout,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     true,
			DialContext:           newGuardDialer(f.allowPrivate, nil).DialContext,
		}
		return newHTTPClient(direct, f.timeout)
	}
	return newHTTPClient(transport, f.timeout)
}

// newHTTPClient 构造带超时与重定向上限的 HTTP 客户端。
//
// 重定向上限是必需的：超长重定向链会把连接与总超时耗尽在无意义的中转上。
func newHTTPClient(transport http.RoundTripper, timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= defaultMaxRedirects {
				return fmt.Errorf("stopped after %d redirects", defaultMaxRedirects)
			}
			return nil
		},
	}
}

// Fetch 执行一次抓取。
//
// 参数 ctx 用于外部取消（客户端断开等），超时另由 Config.Timeout 兜底，二者取先到者。
// 返回的 Result 在「上游返回任何状态码」时都是有效的：4xx/5xx 页面同样算抓取成功，
// 状态码通过 Result.StatusCode 透出。
//
// 返回 error 仅表示传输层失败：连不上、超时、TLS 失败、DNS 失败、被 SSRF 护栏拦截、
// 响应体读取失败。调用方应据此映射为 502（或工具结果的 isError）。
//
// 副作用：发起真实网络请求；响应体最多读取 maxResponseBytes 字节。
func (f *Fetcher) Fetch(ctx context.Context, req Request) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	resp, err := f.doRequest(ctx, req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readBody(resp, f.maxResponseBytes)
	if err != nil {
		return Result{}, err
	}
	contentType := resp.Header.Get("Content-Type")
	text := decodeBody(body)
	if !req.Raw {
		text = contentForModel(text, strings.ToLower(contentType))
	}
	bounded := boundText(text, req)
	return f.newResult(req, resp, contentType, bounded), nil
}

// newResult 把响应与截断结果组装为对外结构。
//
// 单独拆出是为了让 Fetch 保持在可读长度内。
func (f *Fetcher) newResult(req Request, resp *http.Response, contentType string, bounded boundOutput) Result {
	return Result{
		URL:            req.URL.String(),
		Method:         req.Method,
		StatusCode:     resp.StatusCode,
		ContentType:    strings.TrimSpace(contentType),
		Content:        bounded.Content,
		Truncated:      bounded.Truncated,
		NextStartIndex: bounded.NextIndex,
		TotalLength:    bounded.Total,
	}
}

// doRequest 按请求参数发起一次出站 HTTP 请求。
//
// 请求头以默认 UA 为底、调用方提供的值覆盖，因此可以替换 User-Agent。
// POST 用 bytes.Reader 承载 body，使调用方声明的 Content-Type 原样透传。
//
// 配置了代理时先做目标地址的字面量兜底检查：走代理时本机拨号层看到的对端始终是代理，
// 无法判断最终目标，因此「目标直接写成内网 IP 或 localhost」只能在这一层拦。
// 该检查无法覆盖「域名解析到内网」与「代理自身跟随重定向到内网」。
//
// 返回 error 时已带上目标地址与原因，便于直接回传给调用方。
func (f *Fetcher) doRequest(ctx context.Context, req Request) (*http.Response, error) {
	if f.proxy != nil {
		if err := checkTargetLiteral(req.URL, f.allowPrivate); err != nil {
			return nil, err
		}
	}
	httpReq, err := f.newHTTPRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch %s: %v", req.URL, err)
	}
	resp, err := f.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch %s: %v", req.URL, err)
	}
	return resp, nil
}

// newHTTPRequest 组装出站请求，合并默认请求头与调用方请求头。
func (f *Fetcher) newHTTPRequest(ctx context.Context, req Request) (*http.Request, error) {
	var bodyReader io.Reader
	if req.Method == http.MethodPost {
		bodyReader = bytes.NewReader([]byte(req.Body))
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL.String(), bodyReader)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("User-Agent", f.userAgent)
	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}
	return httpReq, nil
}

// readBody 读取响应体，读取量以 limit 为上限。
//
// 上限的意义是防止超大响应耗尽内存：对外服务不能假定对端返回的体积合理。
// limit 非正时回退到默认上限。
func readBody(resp *http.Response, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = defaultMaxResponseBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	return data, nil
}
