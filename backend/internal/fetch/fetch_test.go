package fetch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// newTestFetcher 构造一个允许访问内网的 Fetcher。
//
// 必须打开 AllowPrivate：httptest 服务器监听 127.0.0.1，属于环回地址，
// 会被默认的 SSRF 护栏拦截。护栏本身的断言另见 guard_test.go 的假地址用例。
func newTestFetcher(t *testing.T) *Fetcher {
	t.Helper()
	return NewFetcher(Config{AllowPrivate: true, Timeout: 5 * time.Second})
}

// mustParseRequest 解析参数并在失败时终止用例。
func mustParseRequest(t *testing.T, args map[string]any) Request {
	t.Helper()
	request, err := ParseRequest(args)
	if err != nil {
		t.Fatalf("ParseRequest(%v) 返回意外错误: %v", args, err)
	}
	return request
}

// TestParseRequestValidation 以表驱动覆盖参数校验矩阵：
// 每行只断言「是否报错」，错误文案的细节由其他用例与端点测试抽查。
// 覆盖缺 url、裸域名、非 http(s)、method 非 GET/POST、max_length 越界与小数、
// start_index 非负、body 与 method 的联动、以及 Content-Type 与 body 的联动。
func TestParseRequestValidation(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]any
		wantErr bool
	}{
		{"缺少 url", map[string]any{}, true},
		{"url 为空串", map[string]any{"url": "  "}, true},
		{"裸域名", map[string]any{"url": "example.com"}, true},
		{"非 http(s) 协议", map[string]any{"url": "ftp://example.com"}, true},
		{"合法 https", map[string]any{"url": "https://example.com"}, false},
		{"method 为 PUT", map[string]any{"url": "https://example.com", "method": "PUT"}, true},
		{"method 小写 post", map[string]any{"url": "https://example.com", "method": "post", "body": "{}"}, false},
		{"method 类型错误", map[string]any{"url": "https://example.com", "method": 1}, true},
		{"max_length 为 0", map[string]any{"url": "https://example.com", "max_length": 0}, true},
		{"max_length 为 50001", map[string]any{"url": "https://example.com", "max_length": 50001}, true},
		{"max_length 为 50000", map[string]any{"url": "https://example.com", "max_length": 50000}, false},
		{"max_length 为小数", map[string]any{"url": "https://example.com", "max_length": 1.5}, true},
		{"max_length 为字符串", map[string]any{"url": "https://example.com", "max_length": "10"}, true},
		{"start_index 为负", map[string]any{"url": "https://example.com", "start_index": -1}, true},
		{"GET 可续读", map[string]any{"url": "https://example.com", "start_index": 10}, false},
		{"POST 不可续读", map[string]any{"url": "https://example.com", "method": "POST", "body": "x", "start_index": 10}, true},
		{"GET 不可带 body", map[string]any{"url": "https://example.com", "body": "x"}, true},
		{"POST 可带 body", map[string]any{"url": "https://example.com", "method": "POST", "body": "x"}, false},
		{"POST 空字符串 body 合法", map[string]any{"url": "https://example.com", "method": "POST", "body": ""}, false},
		{"raw 类型错误", map[string]any{"url": "https://example.com", "raw": "yes"}, true},
		{"body 为对象自动序列化", map[string]any{"url": "https://example.com", "method": "POST", "body": map[string]any{"q": "x"}}, false},
		{"body 为数字非法", map[string]any{"url": "https://example.com", "method": "POST", "body": 42}, true},
		{"Content-Type 声明 gbk", map[string]any{"url": "https://example.com", "method": "POST", "body": "x", "headers": map[string]any{"Content-Type": "text/plain; charset=gbk"}}, true},
		{"Content-Type 声明 utf-8", map[string]any{"url": "https://example.com", "method": "POST", "body": "x", "headers": map[string]any{"Content-Type": "text/plain; charset=utf-8"}}, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			_, err := ParseRequest(item.args)
			if item.wantErr && err == nil {
				t.Fatalf("ParseRequest(%v) 期望报错，实际通过", item.args)
			}
			if !item.wantErr && err != nil {
				t.Fatalf("ParseRequest(%v) 期望通过，实际报错: %v", item.args, err)
			}
		})
	}
}

// TestParseRequestDefaultsAndHeaderMerge 验证缺省值、对象 body 的自动序列化、
// Content-Type 默认补齐，以及空值请求头被忽略。
func TestParseRequestDefaultsAndHeaderMerge(t *testing.T) {
	request := mustParseRequest(t, map[string]any{
		"url":     "https://example.com",
		"method":  "POST",
		"body":    map[string]any{"q": "关键词"},
		"headers": map[string]any{"Accept": "application/json", "X-Empty": nil},
	})
	if request.MaxLength != defaultMaxLength || request.StartIndex != 0 || request.Raw {
		t.Fatalf("默认值不符合预期: %+v", request)
	}
	if !request.HasBody || request.Body != `{"q":"关键词"}` {
		t.Fatalf("对象 body 应被序列化为 JSON: hasBody=%v body=%q", request.HasBody, request.Body)
	}
	// 未声明 Content-Type 时补默认值，且空值请求头被忽略
	if got := request.Headers["Content-Type"]; got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type 默认值 = %q", got)
	}
	if _, ok := request.Headers["X-Empty"]; ok {
		t.Fatalf("空值请求头应被忽略: %v", request.Headers)
	}
	if request.Headers["Accept"] != "application/json" {
		t.Fatalf("Accept 请求头未保留: %v", request.Headers)
	}
}

// TestArgsFromQuery 验证查询串形态的类型转换与白名单：
// 只接受 GET 可用字段，headers/body/proxy 不在其中；非法写法必须报错而非静默取默认值。
func TestArgsFromQuery(t *testing.T) {
	// 查询串形态只接受 GET 可用字段，且类型转换失败必须报错而非静默取默认值。
	args, err := ArgsFromQuery(httptest.NewRequest(http.MethodGet,
		"/v1/fetch?url=https%3A%2F%2Fexample.com&max_length=200&start_index=10&raw=true", nil).URL.Query())
	if err != nil {
		t.Fatalf("ArgsFromQuery 报错: %v", err)
	}
	if args["url"] != "https://example.com" || args["max_length"] != 200 ||
		args["start_index"] != 10 || args["raw"] != true {
		t.Fatalf("查询串转换结果不符合预期: %v", args)
	}
	// headers 与 body 不在查询串形态的支持范围内，不应被带出
	if _, ok := args["headers"]; ok {
		t.Fatalf("查询串形态不应支持 headers: %v", args)
	}
	// proxy 已刻意不暴露给调用方
	if _, ok := args["proxy"]; ok {
		t.Fatalf("查询串形态不应支持 proxy: %v", args)
	}

	if _, err := ArgsFromQuery(httptest.NewRequest(http.MethodGet,
		"/v1/fetch?url=x&max_length=abc", nil).URL.Query()); err == nil {
		t.Fatal("非整数 max_length 应报错")
	}
	if _, err := ArgsFromQuery(httptest.NewRequest(http.MethodGet,
		"/v1/fetch?url=x&raw=yes", nil).URL.Query()); err == nil {
		t.Fatal("非布尔 raw 应报错")
	}
}

// TestBoundTextRunes 验证按 rune 截断：中文与 emoji 不被切碎，
// next_index 与 total_length 自洽，超界时返回空提示且不再标记截断。
func TestBoundTextRunes(t *testing.T) {
	// 截断按 rune 计：中文与 emoji 都不能被切碎，next_index 与 total_length 必须自洽。
	text := strings.Repeat("中", 10) + "😀abc"
	request := Request{Method: http.MethodGet, MaxLength: 5}

	// 避免未使用变量告警的同时验证未截断分支
	full := boundText(text, Request{Method: http.MethodGet, MaxLength: maximumMaxLength})
	if full.Truncated || full.Content != text || full.NextIndex != len([]rune(text)) {
		t.Fatalf("未截断场景不符合预期: %+v", full)
	}

	first := boundText(text, request)
	if !first.Truncated {
		t.Fatalf("期望被截断: %+v", first)
	}
	if first.NextIndex != 5 || first.Total != len([]rune(text)) {
		t.Fatalf("截断元信息不符: %+v", first)
	}
	if !strings.HasPrefix(first.Content, "中中中中中") {
		t.Fatalf("截断内容未按 rune 对齐: %q", first.Content)
	}
	if !strings.Contains(first.Content, "start_index=5") {
		t.Fatalf("GET 续读提示缺少 start_index: %q", first.Content)
	}

	// 续读第二段：起点落在 emoji 之后，内容应包含完整 emoji
	second := boundText(text, Request{Method: http.MethodGet, MaxLength: 10, StartIndex: 10})
	if !strings.Contains(second.Content, "😀") {
		t.Fatalf("续读段应包含完整 emoji: %q", second.Content)
	}

	// 超界返回空提示，且不再标记截断
	over := boundText(text, Request{Method: http.MethodGet, MaxLength: 5, StartIndex: 999})
	if over.Content != noMoreContent || over.Truncated {
		t.Fatalf("超界续读不符合预期: %+v", over)
	}
}

// TestBoundTextPostNotice 验证 POST 被截断时的提示指向 max_length 而非 start_index
// （续读需重放请求，POST 可能造成副作用，因此不提供续读）。
func TestBoundTextPostNotice(t *testing.T) {
	// POST 不能续读，提示必须指向 max_length 而不是 start_index。
	out := boundText(strings.Repeat("a", 100), Request{Method: http.MethodPost, MaxLength: 10})
	if !out.Truncated {
		t.Fatalf("期望被截断: %+v", out)
	}
	if !strings.Contains(out.Content, "Raise max_length") ||
		strings.Contains(out.Content, "start_index=") {
		t.Fatalf("POST 截断提示不正确: %q", out.Content)
	}
}

// TestFetchSuccessAndTruncation 验证抓取主流程：默认 UA 与自定义请求头透传、
// HTML 转 Markdown、Content-Type 透出，以及截断元信息。
func TestFetchSuccessAndTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("默认 User-Agent 未设置")
		}
		if r.Header.Get("X-Custom") != "1" {
			t.Errorf("自定义请求头未透传: %v", r.Header)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><main><h1>标题</h1><p>正文内容</p></main></body></html>`)
	}))
	defer server.Close()

	fetcher := newTestFetcher(t)
	request := mustParseRequest(t, map[string]any{
		"url": server.URL, "max_length": 6, "headers": map[string]any{"X-Custom": "1"},
	})
	result, err := fetcher.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch 报错: %v", err)
	}
	if result.StatusCode != http.StatusOK || result.Method != http.MethodGet {
		t.Fatalf("结果元信息不符: %+v", result)
	}
	if !strings.Contains(result.ContentType, "text/html") {
		t.Fatalf("Content-Type 未透出: %q", result.ContentType)
	}
	// HTML 已转 Markdown：标题应成为 # 前缀
	if !strings.Contains(result.Content, "#") {
		t.Fatalf("HTML 未转换为 Markdown: %q", result.Content)
	}
	if !result.Truncated || result.NextStartIndex >= result.TotalLength {
		t.Fatalf("截断元信息不符: %+v", result)
	}
}

// TestFetchKeepsUpstreamErrorStatus 验证上游 4xx/5xx 仍算抓取成功：
// 状态码透出、响应体保留，且 Text() 会补上状态码行供 MCP 文本通道使用。
func TestFetchKeepsUpstreamErrorStatus(t *testing.T) {
	// 上游 4xx/5xx 仍算抓取成功：状态码透出，响应体保留。
	for _, status := range []int{http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"Not Found","documentation_url":"https://example.com/docs"}`)
			}))
			defer server.Close()

			result, err := newTestFetcher(t).Fetch(context.Background(),
				mustParseRequest(t, map[string]any{"url": server.URL}))
			if err != nil {
				t.Fatalf("上游 %d 不应被视为传输层失败: %v", status, err)
			}
			if result.StatusCode != status {
				t.Fatalf("StatusCode = %d, 期望 %d", result.StatusCode, status)
			}
			if !strings.Contains(result.Content, "Not Found") {
				t.Fatalf("错误响应体应被保留: %q", result.Content)
			}
			// Text 会为错误状态补状态码行，供 MCP 文本通道使用
			if !strings.HasPrefix(result.Text(), fmt.Sprintf("HTTP %d", status)) {
				t.Fatalf("Text() 未补状态码: %q", result.Text())
			}
		})
	}
}

// TestFetchRawSkipsNormalization 验证 raw=true 时原样返回 HTML，不做 Markdown 转换。
func TestFetchRawSkipsNormalization(t *testing.T) {
	// raw=true 应原样返回 HTML，不做 Markdown 转换。
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><h1>标题</h1></body></html>`)
	}))
	defer server.Close()

	result, err := newTestFetcher(t).Fetch(context.Background(),
		mustParseRequest(t, map[string]any{"url": server.URL, "raw": true}))
	if err != nil {
		t.Fatalf("Fetch 报错: %v", err)
	}
	if !strings.Contains(result.Content, "<h1>") {
		t.Fatalf("raw=true 应保留原始 HTML: %q", result.Content)
	}
}

// TestFetchTransportFailure 验证拨号失败返回 error（REST 层据此映射 502），而不是空结果。
func TestFetchTransportFailure(t *testing.T) {
	// 拨号失败必须返回 error（REST 层据此映射 502），而不是空结果。
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	_, err := newTestFetcher(t).Fetch(context.Background(),
		mustParseRequest(t, map[string]any{"url": url}))
	if err == nil {
		t.Fatal("连接已关闭的服务器应返回错误")
	}
	if !strings.Contains(err.Error(), "Failed to fetch") {
		t.Fatalf("错误文案应包含目标地址: %v", err)
	}
}

// TestFetchRespectsContextCancellation 验证 context 取消能中断抓取，
// 否则客户端断开后连接会一直挂到超时。
func TestFetchRespectsContextCancellation(t *testing.T) {
	// context 取消必须能中断抓取，否则客户端断开后连接会一直挂到超时。
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-blocked
	}))
	defer server.Close()
	defer close(blocked)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	if _, err := newTestFetcher(t).Fetch(ctx,
		mustParseRequest(t, map[string]any{"url": server.URL})); err == nil {
		t.Fatal("context 取消后应返回错误")
	}
}

// TestNewFetcherNormalizesConfig 验证 Config 的零值与越界值都被收敛为安全默认值：
// 超时上界对齐反代 65s、非法代理按直连处理、合法代理保留。
func TestNewFetcherNormalizesConfig(t *testing.T) {
	// 零值与越界值都必须被收敛为安全默认值，否则会出现「无超时」这类危险配置。
	fetcher := NewFetcher(Config{})
	if fetcher.timeout != defaultTimeout {
		t.Fatalf("零值 Timeout 应收敛为 %v，实际 %v", defaultTimeout, fetcher.timeout)
	}
	if fetcher.maxResponseBytes != defaultMaxResponseBytes {
		t.Fatalf("零值 MaxResponseBytes 应收敛为 %d", defaultMaxResponseBytes)
	}
	if fetcher.userAgent == "" || fetcher.proxy != nil {
		t.Fatalf("默认配置不符合预期: ua=%q proxy=%v", fetcher.userAgent, fetcher.proxy)
	}
	// 超过 nginx 65s 上限的配置应被收敛，避免对外表现为 504
	if over := NewFetcher(Config{Timeout: 5 * time.Minute}); over.timeout != maxTimeout {
		t.Fatalf("超长 Timeout 应收敛为 %v，实际 %v", maxTimeout, over.timeout)
	}
	// 非法代理按直连处理，不留存可疑代理
	if bad := NewFetcher(Config{ProxyURL: "ftp://example.com:21"}); bad.proxy != nil {
		t.Fatalf("不支持的代理协议应按直连处理，实际 %v", bad.proxy)
	}
	// 合法代理（指向本机，配置层不拦截）应被保留
	if good := NewFetcher(Config{ProxyURL: "http://127.0.0.1:7890"}); good.proxy == nil {
		t.Fatal("合法代理地址应被保留")
	}
}
