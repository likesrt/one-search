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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"
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
	// 与 nginx 反代超时（deploy/nginx.conf.template 的 proxy_read_timeout，
	// 默认 65s、可经 NGINX_PROXY_READ_TIMEOUT 调整）对齐：再长会先被反代断开，
	// 调用方只会看到 504，反而丢失真实错误。
	// 取值仍是硬上限而非跟随环境变量：上界本身要可预期，才能保证「配了更长的超时」
	// 不会在某个部署里变成 504。放宽它需同时上调 nginx 反代超时。
	maxTimeout = 60 * time.Second
	// defaultMaxRedirects 是允许跟随的最大重定向次数
	defaultMaxRedirects = 10
	// dialTimeout 是单次 TCP 建连超时
	dialTimeout = 10 * time.Second
	// maxConnsPerHost 限制到单个主机的并发连接数。
	// 与全局并发闸门互补：闸门约束「同时在飞的抓取总数」，本项约束「其中打向同一主机的数量」，
	// 后者才是目标站点真正感受到的压力。取 16 与闸门默认值 32 配合，留出多站点并发的余量。
	maxConnsPerHost = 16
)

// ToolName 是暴露给模型的 MCP 工具名，同时也是续读提示中回指的调用名。
const ToolName = "fetch"

// ErrCacheTooLarge 表示单条抓取结果超过 cache_max_bytes 而未写入缓存。
//
// 导出而非仅记日志：调用方需要区分「缓存被刻意关闭」与「内容太大没缓存」，
// 前者不需要任何提示，后者在排查「为什么大页面每次都打上游」时是必要信息。
var ErrCacheTooLarge = errors.New("fetch result exceeds cache_max_bytes")

// Fallback 是抓取失败或内容不可用时调用的兜底通道。
//
// 定义在 fetch 包而不是直接依赖某个渠道的适配器，是为了让本包与具体第三方解耦：
// 本包只负责「何时该兜底」，用哪条通道、如何取 key 与记账都由 api 层实现。
// 返回值为：正文（完整、未截断）、本次消耗的额度、错误。
type Fallback interface {
	// Fetch 取回 targetURL 的正文。返回 error 时调用方按「无回退」处理，
	// 绝不因此把原本成功的抓取改成失败。
	Fetch(ctx context.Context, targetURL string) (string, float64, error)
}

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
	// Fallback 是内容不可用时的兜底通道，nil 表示不做回退（默认，零开销）
	Fallback Fallback
	// FallbackMinChars 是触发回退的可见文本长度阈值，非正时取 defaultFallbackMinChars
	FallbackMinChars int
	// CacheDir 是缓存目录，空串表示不缓存
	CacheDir string
	// CacheTTLSeconds 是正常内容的缓存时长，非正时视为不缓存
	CacheTTLSeconds int
	// CacheErrorTTLSeconds 是 401/403/429 结果的缓存时长，非正时回落到 CacheTTLSeconds
	CacheErrorTTLSeconds int
	// CacheMaxBytes 是单条缓存上限（字节），非正表示不限
	CacheMaxBytes int64
	// MaxConcurrency 是同时在飞的上游抓取上限，非正时取 defaultMaxConcurrency
	MaxConcurrency int
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
	// StatusCode 是上游响应状态码，可能为 4xx/5xx。
	// 内容来自回退通道（Channel 为 tavily）时恒为 200：回退正文是真实内容，
	// 报内置抓取的状态码会让 Result.Text() 在正文前补一行假的错误码。
	StatusCode int `json:"status_code"`
	// ContentType 是上游响应的 Content-Type 原始值，未提供时为空串；
	// 回退通道下固定为 text/markdown
	ContentType string `json:"content_type"`
	// Channel 标明内容来自哪条通道：direct（内置抓取）或 tavily（回退）
	Channel string `json:"channel"`
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
	// fallback 是兜底通道，nil 表示不做回退
	fallback Fallback
	// fallbackMinChars 是触发回退的可见文本长度阈值，恒为正数
	fallbackMinChars int
	// cacheDir 是缓存目录，空串表示不缓存
	cacheDir string
	// cacheTTL 与 cacheErrorTTL 是两种缓存时长（秒），语义见 cache.go 的 cacheTTLFor
	cacheTTL      int
	cacheErrorTTL int
	// cacheMaxBytes 是单条缓存上限（字节），非正表示不限
	cacheMaxBytes int64
	// gate 限流打向上游的抓取并发，缓存命中与单飞等待者不占名额
	gate *gate
	// group 按缓存键合并同一 URL 的并发首次抓取，避免「同一秒 10 个相同 URL 打 10 次上游」
	group singleflight.Group
}

// NewFetcher 构造抓取执行体。
//
// 参数 cfg 中的零值都会被替换为安全默认值：Timeout 非正取 30s（超过 60s 收敛到 60s）、
// MaxResponseBytes 非正取 10MiB、UserAgent 为空取浏览器标识、FallbackMinChars 非正取 80、
// MaxConcurrency 非正取取 32。ProxyURL 会被解析，解析失败时按「无代理」处理并记日志 ——
// 宁可直连失败，也不能带着一个不确定的代理运行。
//
// 缓存配置是「配置即开关」：CacheDir 为空或 CacheTTLSeconds 非正时不做任何缓存读写，
// 因此调用方不需要额外的布尔开关。
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
		fallback:         cfg.Fallback,
		fallbackMinChars: cfg.FallbackMinChars,
		cacheDir:         strings.TrimSpace(cfg.CacheDir),
		cacheTTL:         cfg.CacheTTLSeconds,
		cacheErrorTTL:    cfg.CacheErrorTTLSeconds,
		cacheMaxBytes:    cfg.CacheMaxBytes,
		gate:             newGate(cfg.MaxConcurrency),
	}
	if fetcher.fallbackMinChars <= 0 {
		fetcher.fallbackMinChars = defaultFallbackMinChars
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

// cacheEnabled 报告本次配置是否启用了缓存。
//
// 判定集中在一点：目录为空或 TTL 非正都视为关闭，避免读写两侧各自判断而出现
// 「写进去了但永远读不出来」这类不一致。
func (f *Fetcher) cacheEnabled() bool {
	return f.cacheDir != "" && f.cacheTTL > 0
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
//
// MaxConnsPerHost 是必需的上限：默认值 0 表示不限制，一个客户端并发抓同一站点时
// 可以无限开连接，既耗尽本机 fd，也会立刻把目标站点打成限流。
func (f *Fetcher) buildClient(proxy *url.URL) *http.Client {
	transport := &http.Transport{
		MaxIdleConns:          100,
		MaxConnsPerHost:       maxConnsPerHost,
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
			MaxConnsPerHost:       maxConnsPerHost,
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
// 流程分四步，顺序不可调换：
//  1. 查缓存（不进并发闸门，读一个本地文件很便宜）—— 命中直接按本次请求切片返回；
//  2. 未命中则按缓存键单飞：同一 URL 的并发首次抓取只打一次上游，其余等结果；
//  3. 单飞内部先取并发名额再抓，随后按触发条件与四道闸门决定是否回退，最后写缓存；
//  4. 按本次请求的 max_length / start_index 切片后返回。
//
// 单飞返回的是完整条目而非切片段，正是因为并发请求的 max_length 各不相同。
//
// 返回 error 仅表示传输层失败：连不上、超时、TLS 失败、DNS 失败、被 SSRF 护栏拦截、
// 响应体读取失败。调用方应据此映射为 502（或工具结果的 isError）。
//
// 副作用：发起真实网络请求；可能读写缓存文件；触发回退时消耗第三方额度。
func (f *Fetcher) Fetch(ctx context.Context, req Request) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	if f.cacheEnabled() {
		if entry, hit := readCache(f.cacheDir, cacheKey(req), f.cacheTTL, f.cacheErrorTTL); hit {
			return f.resultFromEntry(req, entry), nil
		}
	}
	entry, err := f.fetchShared(ctx, req)
	if err != nil {
		return Result{}, err
	}
	return f.resultFromEntry(req, entry), nil
}

// fetchShared 通过单飞组取得完整缓存条目，保证同一 URL 的并发首次抓取只打一次上游。
//
// 用 DoChan 而不是 Do：Do 让等待者复用第一个调用方的 context，第一个调用方断开会把
// 所有等待者一起带崩（表现为莫名其妙的 context canceled）。DoChan 配合 select 让每个
// 调用方只受自己的 ctx 约束，取消互不影响。
//
// 返回值：完整条目（内容未截断）与错误；调用方各自的 ctx 结束时返回其错误。
// 副作用：可能发起上游请求、读写缓存、消耗回退额度。
func (f *Fetcher) fetchShared(ctx context.Context, req Request) (CacheEntry, error) {
	key := cacheKey(req)
	result := f.group.DoChan(key, func() (interface{}, error) {
		// 上游抓取不跟随第一个调用方取消：已写出的请求不该被半途放弃，
		// 而且等待者仍需要这次结果。deadline 保留，总耗时仍有上界。
		flightCtx, cancel := detachContext(ctx)
		defer cancel()
		return f.fetchUpstream(flightCtx, req, key)
	})
	select {
	case <-ctx.Done():
		return CacheEntry{}, ctx.Err()
	case outcome := <-result:
		if outcome.Err != nil {
			return CacheEntry{}, outcome.Err
		}
		entry, ok := outcome.Val.(CacheEntry)
		if !ok {
			return CacheEntry{}, fmt.Errorf("Failed to fetch %s: 内部结果类型异常", req.URL)
		}
		return entry, nil
	}
}

// fetchUpstream 执行「取名额 → 内置抓取 → 按需回退 → 写缓存」的单飞主体。
//
// 并发名额只护住上游 IO（内置抓取与回退两步）：缓存命中与单飞等待者不占名额，
// 闸门要挡的是打向上游的并发，而不是进来的请求数。
//
// 已知取舍：若在名额上等待过久导致 ctx 超时，按传输层失败返回并因此进入回退判定，
// 不做特殊处理（与计划一致）。
//
// 返回值：最终条目与错误；错误仅来自内置抓取，回退失败不影响返回值。
// 副作用：读写的缓存文件、发出的上游请求与消耗的回退额度。
func (f *Fetcher) fetchUpstream(ctx context.Context, req Request, key string) (CacheEntry, error) {
	if err := f.gate.acquire(ctx); err != nil {
		return CacheEntry{}, err
	}
	defer f.gate.release()

	entry, fetchErr := f.fetchDirect(ctx, req)
	entry, fetchErr = f.applyFallback(ctx, req, entry, fetchErr)
	f.storeCache(key, entry, fetchErr)
	if fetchErr != nil {
		return CacheEntry{}, fetchErr
	}
	return entry, nil
}

// fetchDirect 执行内置抓取并归一化内容，返回条目与传输层错误。
//
// 状态码为 4xx/5xx 时不算错误：响应体本身是有效内容，调用方据此判断是否回退。
// 归一化（HTML 转 Markdown / JSON 压缩）在此完成，因此缓存里存的是归一化后的正文，
// 续读时无需重复转换。
func (f *Fetcher) fetchDirect(ctx context.Context, req Request) (CacheEntry, error) {
	resp, err := f.doRequest(ctx, req)
	if err != nil {
		return CacheEntry{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readBody(resp, f.maxResponseBytes)
	if err != nil {
		return CacheEntry{}, err
	}
	contentType := resp.Header.Get("Content-Type")
	text := decodeBody(body)
	if !req.Raw {
		text = contentForModel(text, strings.ToLower(contentType))
	}
	return CacheEntry{
		URL:         req.URL.String(),
		Method:      req.Method,
		StatusCode:  resp.StatusCode,
		ContentType: strings.TrimSpace(contentType),
		Channel:     ChannelDirect,
		Content:     text,
	}, nil
}

// storeCache 写入缓存，并在写完后把该键移出单飞组。
//
// 不缓存传输层失败（fetchErr 非 nil）：一次 DNS 抖动或超时若被缓存 120 秒，
// 会让一个本来正常的 URL 在窗口内持续失败。而 4xx/5xx 是内容型结果，必须缓存 ——
// 避免反复撞同一道门禁正是本次加缓存的初衷。
//
// 必须调用 group.Forget：单飞的键在 fn 返回后虽已从内部 map 删除，但这里显式调用
// 是为了在写缓存失败等分支上也保持「键不再被持有」的语义，避免后续请求命中旧条目。
//
// 副作用：写缓存文件；失败只记日志（缓存失效不该让抓取失败）。
func (f *Fetcher) storeCache(key string, entry CacheEntry, fetchErr error) {
	defer f.group.Forget(key)
	if !f.cacheEnabled() || fetchErr != nil {
		return
	}
	if err := writeCache(f.cacheDir, key, entry, f.cacheMaxBytes); err != nil {
		if errors.Is(err, ErrCacheTooLarge) {
			log.Printf("fetch: 内容超过单条缓存上限，未缓存 %s (%d 字节)", entry.URL, len(entry.Content))
			return
		}
		log.Printf("fetch: 写缓存失败 %s: %v", entry.URL, err)
	}
}

// resultFromEntry 把完整条目按本次请求的 max_length / start_index 切片为对外结果。
//
// 每次调用都重新切片，因此不同 max_length 的并发请求可以共享同一次上游抓取。
//
// 走回退通道时 StatusCode 一律报 200，而不是缓存条目里那个内置抓取的状态码：
// Result.Text() 对非 2xx 会在正文前补一行 `HTTP <状态码>`，回退成功却报 403 会让模型
// 在真正的正文最前面看到一行假的 403。原始状态码不丢 —— 它留在 CacheEntry 里决定
// 该条用哪种 TTL，而「这次走了回退」由 Channel 标明。
func (f *Fetcher) resultFromEntry(req Request, entry CacheEntry) Result {
	bounded := boundText(entry.Content, req)
	channel := entry.Channel
	if channel == "" {
		channel = ChannelDirect
	}
	statusCode := entry.StatusCode
	if channel == ChannelTavily {
		statusCode = http.StatusOK
	}
	return Result{
		URL:            req.URL.String(),
		Method:         req.Method,
		StatusCode:     statusCode,
		ContentType:    entry.ContentType,
		Channel:        channel,
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
//
// 包装用 %w 而非 %v：回退闸门需要靠 errors.Is(err, ErrPrivateTarget) 判断「这是被护栏
// 拦下的内网目标」，用 %v 会把哨兵错误拍平成字符串，判定随之静默失效。
func (f *Fetcher) doRequest(ctx context.Context, req Request) (*http.Response, error) {
	if f.proxy != nil {
		if err := checkTargetLiteral(req.URL, f.allowPrivate); err != nil {
			return nil, err
		}
	}
	httpReq, err := f.newHTTPRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch %s: %w", req.URL, err)
	}
	resp, err := f.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("Failed to fetch %s: %w", req.URL, err)
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
