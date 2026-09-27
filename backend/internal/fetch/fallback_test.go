package fetch

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeFallback 是回退通道的测试替身：记录调用参数并返回预设内容或错误。
//
// 全部用例都通过它验证回退行为，绝不打真实网络（真实 Tavily 调用会消耗额度，
// 也违反「测试必须离线」的项目约定）。
type fakeFallback struct {
	mu      sync.Mutex
	calls   []string
	content string
	credits float64
	err     error
	// block 非 nil 时回退会阻塞直到该 channel 被关闭，用于观察并发行为。
	block chan struct{}
}

// Fetch 记录调用地址并返回预设结果，实现 fetch.Fallback。
func (f *fakeFallback) Fetch(_ context.Context, targetURL string) (string, float64, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.calls = append(f.calls, targetURL)
	f.mu.Unlock()
	if f.err != nil {
		return "", 0, f.err
	}
	return f.content, f.credits, nil
}

// callCount 返回回退被调用的次数。
func (f *fakeFallback) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newFallbackFetcher 构造一个启用回退的 Fetcher，全部用例共用。
//
// AllowPrivate 必须打开：httptest 监听 127.0.0.1，属于环回地址。
// CacheDir 指向 t.TempDir()，保证用例之间不互相污染。
func newFallbackFetcher(t *testing.T, fallback Fallback) *Fetcher {
	t.Helper()
	return NewFetcher(Config{
		AllowPrivate:     true,
		Timeout:          5 * time.Second,
		Fallback:         fallback,
		FallbackMinChars: defaultFallbackMinChars,
		CacheDir:         t.TempDir(),
		CacheTTLSeconds:  120,
	})
}

// gateHTML 是一段可见文本充足的 HTML，用于「不该触发回退」的基线。
// 正文刻意写到远超默认阈值 80 字，避免因文案缩短而让基线用例变成「内容过少」。
const gateHTML = `<html><body><main><h1>足够长的标题</h1><p>` +
	`这是一段足够长的正文内容，用来确保可见文本长度明显超过默认阈值八十个字符。` +
	`这里继续补充一些没有实际含义的说明文字，目的只是把可见文本的字符数堆到阈值以上，` +
	`从而让回退判定走「不触发」这条分支，验证正常的短页面不会被误判为需要兜底。</p></main></body></html>`

// TestFallbackTriggerMatrix 以表驱动覆盖回退触发条件矩阵。
//
// 覆盖三类触发（传输失败、401/403/429、可见文本过少）与两类明确不触发（404、全部 5xx）。
// 这些边界是本次改动最容易被改坏的地方：把 5xx 也纳入回退会白白消耗第三方额度，
// 而漏掉 429 会让限流页面一直被返回给模型。
func TestFallbackTriggerMatrix(t *testing.T) {
	cases := []struct {
		name         string
		status       int
		body         string
		wantFallback bool
	}{
		{"403 触发", http.StatusForbidden, `<html><body>Just a moment...</body></html>`, true},
		{"401 触发", http.StatusUnauthorized, `<html><body>unauthorized</body></html>`, true},
		{"429 触发", http.StatusTooManyRequests, `<html><body>too many requests</body></html>`, true},
		{"404 不触发", http.StatusNotFound, `<html><body><main>` + strings.Repeat("内容", 100) + `</main></body></html>`, false},
		{"500 不触发", http.StatusInternalServerError, `<html><body><main>` + strings.Repeat("内容", 100) + `</main></body></html>`, false},
		{"502 不触发", http.StatusBadGateway, `<html><body><main>` + strings.Repeat("内容", 100) + `</main></body></html>`, false},
		{"503 不触发", http.StatusServiceUnavailable, `<html><body><main>` + strings.Repeat("内容", 100) + `</main></body></html>`, false},
		{"200 且内容充足不触发", http.StatusOK, gateHTML, false},
		{"200 但内容过少触发", http.StatusOK, `<html><body><main>短</main></body></html>`, true},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.WriteHeader(item.status)
				fmt.Fprint(w, item.body)
			}))
			defer server.Close()

			fallback := &fakeFallback{content: "# 回退正文\n\n来自 Tavily 的完整内容"}
			result, err := newFallbackFetcher(t, fallback).Fetch(context.Background(),
				mustParseRequest(t, map[string]any{"url": server.URL}))
			if err != nil {
				t.Fatalf("Fetch 报错: %v", err)
			}
			if got := fallback.callCount(); (got > 0) != item.wantFallback {
				t.Fatalf("回退调用次数 = %d, 期望触发 = %v", got, item.wantFallback)
			}
			assertTriggerOutcome(t, item.wantFallback, item.status, result)
		})
	}
}

// assertTriggerOutcome 断言一次触发/不触发场景的最终结果。
//
// 单独拆出是为了让触发矩阵的用例体保持在函数长度上限内。
// 参数 wantFallback 表示该场景是否应走回退通道；status 是上游状态码（仅不触发时有意义）；
// result 为 Fetch 的返回值。
func assertTriggerOutcome(t *testing.T, wantFallback bool, status int, result Result) {
	t.Helper()
	if !wantFallback {
		if result.Channel != ChannelDirect {
			t.Fatalf("不该回退时 channel = %q", result.Channel)
		}
		if result.StatusCode != status {
			t.Fatalf("不该回退时状态码 = %d, 期望 %d", result.StatusCode, status)
		}
		return
	}
	// 回退成功必须报 200：报原始 4xx 会让 Result.Text() 在真正文前补一行假的错误码
	if result.Channel != ChannelTavily || result.StatusCode != http.StatusOK {
		t.Fatalf("回退成功应报 200 + channel=tavily，实际 %+v", result)
	}
	if !strings.Contains(result.Content, "回退正文") {
		t.Fatalf("应返回回退正文: %q", result.Content)
	}
	if result.ContentType != "text/markdown" {
		t.Fatalf("回退通道的 Content-Type 应为 text/markdown，实际 %q", result.ContentType)
	}
}

// TestFallbackOnTransportFailure 验证传输层失败也会触发回退，且回退成功后不再报错。
func TestFallbackOnTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target := server.URL
	server.Close()

	fallback := &fakeFallback{content: "兜底正文"}
	result, err := newFallbackFetcher(t, fallback).Fetch(context.Background(),
		mustParseRequest(t, map[string]any{"url": target}))
	if err != nil {
		t.Fatalf("回退成功后不该报错: %v", err)
	}
	// 内置抓取连不上（无上游状态码），回退拿回内容后仍应报 200：
	// 报 0 或 502 会让调用方以为抓取失败，而正文其实是可用的
	if result.Channel != ChannelTavily || result.StatusCode != http.StatusOK {
		t.Fatalf("回退结果不符合预期: %+v", result)
	}
}

// TestFallbackFailureKeepsDirectResult 验证回退失败时用内置结果兜底。
//
// 这是回退功能最重要的一条契约：回退是加分项而非必要条件，
// 绝不能因为第三方通道故障把一次正常的抓取变成 502。
func TestFallbackFailureKeepsDirectResult(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		fallback error
	}{
		{"回退报错时保留 403 页面", http.StatusForbidden, errors.New("tavily fallback: no available key")},
		{"回退超时同样兜底", http.StatusForbidden, context.DeadlineExceeded},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(item.status)
				fmt.Fprint(w, `<html><body>Just a moment...</body></html>`)
			}))
			defer server.Close()

			fallback := &fakeFallback{err: item.fallback}
			result, err := newFallbackFetcher(t, fallback).Fetch(context.Background(),
				mustParseRequest(t, map[string]any{"url": server.URL}))
			if err != nil {
				t.Fatalf("回退失败不应把请求变成错误: %v", err)
			}
			if result.Channel != ChannelDirect || result.StatusCode != item.status {
				t.Fatalf("应返回内置结果: %+v", result)
			}
			if !strings.Contains(result.Content, "Just a moment") {
				t.Fatalf("内置正文应保留: %q", result.Content)
			}
		})
	}
}

// TestFallbackFailureKeepsTransportError 验证内置抓取本身失败且回退也失败时，
// 仍返回内置的错误（调用方据此映射 502），而不是被回退掩盖成成功。
func TestFallbackFailureKeepsTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target := server.URL
	server.Close()

	fallback := &fakeFallback{err: errors.New("tavily fallback: upstream error")}
	_, err := newFallbackFetcher(t, fallback).Fetch(context.Background(),
		mustParseRequest(t, map[string]any{"url": target}))
	if err == nil {
		t.Fatal("回退失败时应保留内置抓取的传输层错误")
	}
	if !strings.Contains(err.Error(), "Failed to fetch") {
		t.Fatalf("错误文案应来自内置抓取: %v", err)
	}
}

// TestFallbackGateMatrix 以表驱动覆盖四道闸门。
//
// 闸门 1（内网目标）需要真实的护栏错误才能触发，无法用合法解析出的 URL 构造，
// 因此单列在 TestFallbackGatePrivateTargetSentinel 与下面的哨兵用例里。
func TestFallbackGateMatrix(t *testing.T) {
	// 内容过少的目标：若不考虑闸门则会触发回退，因此闸门是否生效可直接由回退调用数观察
	short := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><main>短</main></body></html>`)
	}))
	defer short.Close()

	cases := []struct {
		name       string
		args       map[string]any
		wantReason fallbackGateReason
	}{
		{"POST 被拦", map[string]any{"url": short.URL, "method": "POST", "body": "x"}, gateNotGet},
		{"带自定义请求头被拦", map[string]any{
			"url": short.URL, "headers": map[string]any{"Authorization": "Bearer secret"},
		}, gateCustomHeaders},
		{"raw=true 被拦", map[string]any{"url": short.URL, "raw": true}, gateRaw},
		{"普通 GET 放行", map[string]any{"url": short.URL}, gateNone},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			request := mustParseRequest(t, item.args)
			if reason := fallbackBlocked(request, nil); reason != item.wantReason {
				t.Fatalf("闸门判定 = %q, 期望 %q", reason, item.wantReason)
			}
			fallback := &fakeFallback{content: "回退正文"}
			result, err := newFallbackFetcher(t, fallback).Fetch(context.Background(), request)
			if err != nil {
				t.Fatalf("闸门被拦时应返回内置结果: %v", err)
			}
			if item.wantReason == gateNone {
				if fallback.callCount() != 1 || result.Channel != ChannelTavily {
					t.Fatalf("放行的请求应触发回退: calls=%d result=%+v", fallback.callCount(), result)
				}
				return
			}
			if fallback.callCount() != 0 {
				t.Fatalf("被闸门拦下时不应调用回退，实际 %v", fallback.calls)
			}
			if result.Channel != ChannelDirect {
				t.Fatalf("闸门被拦后 channel 应为 direct: %+v", result)
			}
			if item.wantReason == gateRaw && !strings.Contains(result.Content, "<main>") {
				// raw 的契约是原样返回源文本，被拦后应如实返回内置的原始内容
				t.Fatalf("raw 应保留原始 HTML: %q", result.Content)
			}
		})
	}

	// 闸门 1 由护栏错误触发：包装后的 ErrPrivateTarget 必须能被识别
	privateErr := fmt.Errorf("Failed to fetch %s: %w", "http://127.0.0.1/", ErrPrivateTarget)
	if reason := fallbackBlocked(mustParseRequest(t, map[string]any{"url": short.URL}), privateErr); reason != gatePrivateTarget {
		t.Fatalf("ErrPrivateTarget 应触发闸门 1，实际 %q", reason)
	}
}

// TestFallbackGatePrivateTargetSentinel 验证 ErrPrivateTarget 哨兵能穿透 url.Error 与
// net.OpError 的包装链被 errors.Is 识别 —— 这是回退闸门 1 生效的前提。
func TestFallbackGatePrivateTargetSentinel(t *testing.T) {
	// 关闭 AllowPrivate 让拨号层拦截环回目标（httptest 监听 127.0.0.1）
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	fetcher := NewFetcher(Config{Timeout: time.Second})
	fallback := &fakeFallback{content: "不该被调用"}
	fetcher.fallback = fallback
	fetcher.fallbackMinChars = defaultFallbackMinChars

	_, err := fetcher.Fetch(context.Background(), mustParseRequest(t, map[string]any{"url": server.URL}))
	if !errors.Is(err, ErrPrivateTarget) {
		t.Fatalf("拨号层拦截应可用 errors.Is 识别为 ErrPrivateTarget: %v", err)
	}
	if fallback.callCount() != 0 {
		t.Fatal("内网目标不得外发给回退通道")
	}
}

// TestVisibleTextLength 覆盖可见文本口径。
//
// 样本取自实测：36kr 质询页原始 1037 字符里 981 个是 base64 内联图片，
// 可见文本只有 56 字；example.com 是 131 字。若按原始长度判定，前者会被误判为内容丰富。
func TestVisibleTextLength(t *testing.T) {
	// 构造一份与 36kr 同构的内容：少量正文 + 一个巨大的 base64 内联图片
	inlineImage := "![chart](data:image/png;base64," + strings.Repeat("iVBORw0KGgo", 75) + ")"
	challenge := "正在进行安全检测，请稍候。" + inlineImage

	cases := []struct {
		name    string
		content string
		want    int
	}{
		{"内联图片不计入可见文本", challenge, len([]rune("正在进行安全检测，请稍候。"))},
		{"example.com 样本（标题计入可见文本）", "# Example Domain\n\nThis domain is for use in illustrative examples in documents.", 79},
		{"链接目标被剥离锚文本保留", "[点击这里](https://example.com/a/very/long/path) 结束", len([]rune("点击这里 结束"))},
		{"裸 URL 被剥离", "见 https://example.com/some/long/path 说明", len([]rune("见  说明"))},
		{"纯图片内容为 0", "![a](https://example.com/a.png)", 0},
		{"空串为 0", "", 0},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := visibleTextLength(item.content); got != item.want {
				t.Fatalf("visibleTextLength(%q) = %d, 期望 %d", item.content, got, item.want)
			}
		})
	}

	// 关键断言：按原始长度看很长的内容，可见文本依然很短
	if len([]rune(challenge)) < defaultFallbackMinChars {
		t.Fatal("样本构造有误：原始长度应远大于阈值")
	}
	if visibleTextLength(challenge) >= defaultFallbackMinChars {
		t.Fatal("36kr 同构样本的可见文本应低于阈值，否则会漏判")
	}
	// example.com 这类正常短页面不应被误触发
	normal := "This domain is for use in illustrative examples in documents. You may use this domain " +
		"in literature without prior coordination or asking for permission."
	if visibleTextLength(normal) < defaultFallbackMinChars {
		t.Fatalf("正常短页面的可见文本应高于阈值，实际 %d", visibleTextLength(normal))
	}
}

// TestFallbackResultNotTruncatedBeforeCaching 验证回退正文在写缓存前不被截断。
//
// 实测 Tavily 会返回 50 万字符量级的整页 Markdown，若在回退通道里先按 max_length 截断，
// 续读就永远拿不到后半段（缓存里只有第一段），因此截断必须只发生在返回给调用方这一步。
func TestFallbackResultNotTruncatedBeforeCaching(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<html><body>Just a moment...</body></html>`)
	}))
	defer server.Close()

	longContent := strings.Repeat("一二三四五", 4000)
	fallback := &fakeFallback{content: longContent}
	dir := t.TempDir()
	fetcher := NewFetcher(Config{
		AllowPrivate: true, Timeout: 5 * time.Second,
		Fallback: fallback, FallbackMinChars: defaultFallbackMinChars,
		CacheDir: dir, CacheTTLSeconds: 120,
	})
	request := mustParseRequest(t, map[string]any{"url": server.URL, "max_length": 100})

	first, err := fetcher.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch 报错: %v", err)
	}
	if !first.Truncated || first.NextStartIndex != 100 {
		t.Fatalf("回退结果应按 max_length 截断: %+v", first)
	}
	if first.TotalLength != len([]rune(longContent)) {
		t.Fatalf("total_length = %d, 期望 %d（缓存里应是未截断的完整正文）",
			first.TotalLength, len([]rune(longContent)))
	}

	// 续读必须命中缓存并从同一份完整正文里切片，回退不再被调用
	second, err := fetcher.Fetch(context.Background(), mustParseRequest(t, map[string]any{
		"url": server.URL, "max_length": 100, "start_index": 100,
	}))
	if err != nil {
		t.Fatalf("续读报错: %v", err)
	}
	if second.TotalLength != first.TotalLength {
		t.Fatalf("续读的 total_length 应与首次一致: %d vs %d", second.TotalLength, first.TotalLength)
	}
	if fallback.callCount() != 1 {
		t.Fatalf("续读应命中缓存，回退只应调用一次，实际 %d", fallback.callCount())
	}
	if !strings.HasPrefix(second.Content, "一二三四五") {
		t.Fatalf("续读内容不正确: %q", second.Content[:20])
	}
}

// TestCacheHitAvoidsUpstreamRequest 验证缓存命中不再打上游。
//
// 这正是用户提出缓存的初衷：同一 URL 短时间内重复抓取不应反复撞目标站点的门禁。
func TestCacheHitAvoidsUpstreamRequest(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, gateHTML)
	}))
	defer server.Close()

	fetcher := newFallbackFetcher(t, nil)
	request := mustParseRequest(t, map[string]any{"url": server.URL})
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("首次抓取报错: %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("第二次抓取报错: %v", err)
	}
	if hits != 1 {
		t.Fatalf("缓存命中后不应再打上游，实际上游请求 %d 次", hits)
	}
}

// TestCacheExpiresAndRefetches 验证条目过期后重新抓取。
func TestCacheExpiresAndRefetches(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, gateHTML)
	}))
	defer server.Close()

	dir := t.TempDir()
	fetcher := NewFetcher(Config{
		AllowPrivate: true, Timeout: 5 * time.Second,
		CacheDir: dir, CacheTTLSeconds: 120,
	})
	request := mustParseRequest(t, map[string]any{"url": server.URL})
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("首次抓取报错: %v", err)
	}
	// 把文件 mtime 改到 TTL 之外，模拟「条目已过期」
	path := filepath.Join(dir, cacheKey(request)+cacheFileSuffix)
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("修改 mtime 失败: %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("过期后重抓报错: %v", err)
	}
	if hits != 2 {
		t.Fatalf("过期后应重新抓取，实际上游请求 %d 次", hits)
	}
}

// TestCacheKeySensitiveToRequestShape 验证缓存键对 method / body / headers / raw 敏感。
//
// 若键里漏掉任一项，两个不同的请求会互相污染缓存：带不同 body 的 POST 会拿到彼此的结果，
// 带认证头的请求会命中不带认证头时缓存的内容。
func TestCacheKeySensitiveToRequestShape(t *testing.T) {
	base := mustParseRequest(t, map[string]any{"url": "https://example.com"})
	variants := map[string]map[string]any{
		"不同 raw":     {"url": "https://example.com", "raw": true},
		"不同 method":  {"url": "https://example.com", "method": "POST", "body": ""},
		"不同 headers": {"url": "https://example.com", "method": "POST", "headers": map[string]any{"X-A": "1"}},
		"不同 body":    {"url": "https://example.com", "method": "POST", "body": "x"},
	}
	baseKey := cacheKey(base)
	for name, args := range variants {
		other := mustParseRequest(t, args)
		if cacheKey(other) == baseKey {
			t.Fatalf("%s 的缓存键与基准相同，会导致缓存互相污染", name)
		}
	}
	// 请求头顺序不同必须得到同一个键，否则缓存永远命不中
	left := mustParseRequest(t, map[string]any{"url": "https://example.com", "headers": map[string]any{"A": "1", "B": "2"}})
	right := mustParseRequest(t, map[string]any{"url": "https://example.com", "headers": map[string]any{"B": "2", "A": "1"}})
	if cacheKey(left) != cacheKey(right) {
		t.Fatal("请求头顺序不应影响缓存键")
	}
	// 带 body 的 POST 是唯一合法的续读外场景，其键必须与 GET 区分
	if cacheKey(base) == cacheKey(mustParseRequest(t, map[string]any{"url": "https://example.com", "method": "POST", "body": "x"})) {
		t.Fatal("GET 与带 body 的 POST 缓存键必须不同")
	}
}

// TestCacheSkipsOversizedContent 验证超过 cache_max_bytes 的内容不写缓存。
func TestCacheSkipsOversizedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, gateHTML)
	}))
	defer server.Close()

	dir := t.TempDir()
	fetcher := NewFetcher(Config{
		AllowPrivate: true, Timeout: 5 * time.Second,
		CacheDir: dir, CacheTTLSeconds: 120, CacheMaxBytes: 16,
	})
	request := mustParseRequest(t, map[string]any{"url": server.URL})
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("抓取应成功: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取缓存目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("超限内容不应写缓存，实际有 %d 个文件", len(entries))
	}
	// 直接调用 writeCache 时应返回哨兵错误，供调用方区分「超限」与「IO 失败」
	err = writeCache(dir, "key", CacheEntry{Content: strings.Repeat("x", 100)}, 16)
	if !errors.Is(err, ErrCacheTooLarge) {
		t.Fatalf("超限应返回 ErrCacheTooLarge，实际 %v", err)
	}
}

// TestCacheErrorTTLApplies 验证错误态使用 cache_error_ttl_seconds：
// 门禁类结果必须缓存（避免反复撞门禁），但过期后要能重抓。
func TestCacheErrorTTLApplies(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<html><body>Just a moment...</body></html>`)
	}))
	defer server.Close()

	dir := t.TempDir()
	fetcher := NewFetcher(Config{
		AllowPrivate: true, Timeout: 5 * time.Second,
		CacheDir: dir, CacheTTLSeconds: 3600, CacheErrorTTLSeconds: 1,
	})
	request := mustParseRequest(t, map[string]any{"url": server.URL})
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("首次抓取报错: %v", err)
	}
	// 第二次应命中错误态缓存（错误 TTL 未到）
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("第二次抓取报错: %v", err)
	}
	if hits != 1 {
		t.Fatalf("错误态应被缓存，实际上游请求 %d 次", hits)
	}
	// 把 mtime 挪到错误 TTL 之外：应立即过期重抓，而不是等正常的 3600 秒
	path := filepath.Join(dir, cacheKey(request)+cacheFileSuffix)
	old := time.Now().Add(-5 * time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("修改 mtime 失败: %v", err)
	}
	if _, err := fetcher.Fetch(context.Background(), request); err != nil {
		t.Fatalf("第三次抓取报错: %v", err)
	}
	if hits != 2 {
		t.Fatalf("错误态 TTL 过期后应重抓，实际上游请求 %d 次", hits)
	}
}

// TestCacheServesContinuationWithoutUpstream 验证续读（start_index > 0）完整命中缓存：
// 同一 URL 只打一次上游，第二段内容由本地切片得出。
func TestCacheServesContinuationWithoutUpstream(t *testing.T) {
	var hits int
	body := `<html><body><main><p>` + strings.Repeat("这是一段很长的正文内容。", 60) + `</p></main></body></html>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	}))
	defer server.Close()

	fetcher := newFallbackFetcher(t, nil)
	first, err := fetcher.Fetch(context.Background(), mustParseRequest(t, map[string]any{
		"url": server.URL, "max_length": 50,
	}))
	if err != nil {
		t.Fatalf("首次抓取报错: %v", err)
	}
	second, err := fetcher.Fetch(context.Background(), mustParseRequest(t, map[string]any{
		"url": server.URL, "max_length": 50, "start_index": first.NextStartIndex,
	}))
	if err != nil {
		t.Fatalf("续读报错: %v", err)
	}
	if hits != 1 {
		t.Fatalf("续读应命中缓存，实际上游请求 %d 次", hits)
	}
	if second.TotalLength != first.TotalLength {
		t.Fatalf("续读的 total_length 应与首次一致: %d vs %d", second.TotalLength, first.TotalLength)
	}
	if second.Content == first.Content {
		t.Fatal("续读内容不应与首次相同")
	}
}

// TestFetchTransportErrorNotCached 验证传输层失败不写缓存。
//
// 一次 DNS 抖动或超时若被缓存 120 秒，会让一个本来正常的 URL 在窗口内持续失败，
// 因此「没拿到上游响应」的结果绝不落盘。
func TestFetchTransportErrorNotCached(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target := server.URL
	server.Close()

	dir := t.TempDir()
	fetcher := NewFetcher(Config{
		AllowPrivate: true, Timeout: time.Second,
		CacheDir: dir, CacheTTLSeconds: 120,
	})
	if _, err := fetcher.Fetch(context.Background(), mustParseRequest(t, map[string]any{"url": target})); err == nil {
		t.Fatal("连不上的目标应报错")
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("读取缓存目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("传输层失败不应写缓存，实际有 %d 个文件", len(entries))
	}
}

// TestCleanCacheRemovesExpiredAndOversized 覆盖清扫任务的两条规则：
// 过期条目被删、未过期条目保留；总量超阈值时按 mtime 从旧到新删到阈值以下。
func TestCleanCacheRemovesExpiredAndOversized(t *testing.T) {
	dir := t.TempDir()
	write := func(key string, age time.Duration) string {
		t.Helper()
		if err := writeCache(dir, key, CacheEntry{Content: strings.Repeat("x", 200)}, 0); err != nil {
			t.Fatalf("写缓存失败: %v", err)
		}
		path := filepath.Join(dir, key+cacheFileSuffix)
		stamp := time.Now().Add(-age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatalf("修改 mtime 失败: %v", err)
		}
		return path
	}

	fresh := write("fresh", time.Minute)
	old := write("old", 10*time.Minute)
	if err := CleanCache(dir, 120, 120, 0); err != nil {
		t.Fatalf("CleanCache 报错: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("过期条目应被删除")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("未过期条目应保留: %v", err)
	}

	// 总量淘汰：再加两个未过期但更旧的文件，阈值校准为「刚好只容得下两个文件」，
	// 这样只需淘汰最旧的一个即低于阈值，可精确断言淘汰顺序是 mtime 从旧到新。
	// 年龄必须都在 TTL（120s）以内，否则会被上一段过期规则先删掉，就测不到总量淘汰了
	older := write("older", 110*time.Second)
	middle := write("middle", 100*time.Second)
	info, err := os.Stat(fresh)
	if err != nil {
		t.Fatalf("读取缓存文件信息失败: %v", err)
	}
	// 阈值取「两个文件大小 + 1 字节」：三个文件必然超限，删掉最旧的一个即降到阈值以下
	if err := CleanCache(dir, 120, 120, info.Size()*2+1); err != nil {
		t.Fatalf("CleanCache 报错: %v", err)
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Fatal("总量超限时最旧的文件应先被删除")
	}
	if _, err := os.Stat(middle); err != nil {
		t.Fatalf("降到阈值以下后应停止淘汰: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("最新文件应保留: %v", err)
	}
	// 目录不存在时不应报错，也不应创建目录
	missing := filepath.Join(t.TempDir(), "nope")
	if err := CleanCache(missing, 120, 120, 0); err != nil {
		t.Fatalf("目录不存在时应返回 nil: %v", err)
	}
}

// TestConcurrencyGateLimitsInFlight 验证 max_concurrency 是真实的上游并发上限。
//
// 断言口径是「同时在上游处理中的请求数」，而不是「被接受的请求数」：
// 闸门的目的正是让超出的请求排队，因此后者恒等于发起数，没有诊断价值。
func TestConcurrencyGateLimitsInFlight(t *testing.T) {
	var mu sync.Mutex
	inFlight, peak := 0, 0
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		mu.Unlock()
		<-release
		mu.Lock()
		inFlight--
		mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, gateHTML)
	}))
	defer server.Close()

	fetcher := NewFetcher(Config{
		AllowPrivate: true, Timeout: 10 * time.Second, MaxConcurrency: 2,
	})
	var wg sync.WaitGroup
	for index := 0; index < 8; index++ {
		wg.Add(1)
		// Go 1.22 之前循环变量按引用共享，必须显式复制，否则所有 goroutine 用同一个地址
		target := fmt.Sprintf("%s/?i=%d", server.URL, index)
		go func() {
			defer wg.Done()
			_, _ = fetcher.Fetch(context.Background(), mustParseRequest(t, map[string]any{"url": target}))
		}()
	}
	// 等到确实有 2 个请求进入上游处理，再放开它们
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		current := inFlight
		mu.Unlock()
		if current >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > 2 {
		t.Fatalf("同时在上游的请求数峰值 = %d, 超过 max_concurrency=2", peak)
	}
	if peak < 2 {
		t.Fatalf("并发未被真正发起（峰值 %d），用例失去意义", peak)
	}
}

// TestSingleflightCoalescesSameURL 验证同一 URL 的并发首次抓取只产生一次上游请求。
//
// 缓存挡「之后」的重复请求，单飞挡「同时」的重复请求；缺了单飞，
// 同一秒内 10 个相同 URL 会打 10 次上游，缓存形同虚设。
func TestSingleflightCoalescesSameURL(t *testing.T) {
	var hits int32
	var mu sync.Mutex
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, gateHTML)
	}))
	defer server.Close()

	fetcher := NewFetcher(Config{AllowPrivate: true, Timeout: 10 * time.Second})
	request := mustParseRequest(t, map[string]any{"url": server.URL})

	var wg sync.WaitGroup
	for index := 0; index < 5; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := fetcher.Fetch(context.Background(), request); err != nil {
				t.Errorf("Fetch 报错: %v", err)
			}
		}()
	}
	// 等到第一个请求确实进了上游，再放开
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("同一 URL 的并发抓取应只打一次上游，实际 %d 次", hits)
	}
}

// TestSingleflightWaiterDoesNotFollowFirstCallerCancel 验证第一个调用方断开不会带崩等待者。
//
// 这正是必须用 DoChan 而不是 Do 的原因：Do 让等待者复用第一个调用方的 context，
// 第一个调用方取消时等待者会一起拿到 context canceled，表现为莫名其妙的批量失败。
func TestSingleflightWaiterDoesNotFollowFirstCallerCancel(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, gateHTML)
	}))
	defer server.Close()

	fetcher := NewFetcher(Config{AllowPrivate: true, Timeout: 10 * time.Second})
	request := mustParseRequest(t, map[string]any{"url": server.URL})

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		_, err := fetcher.Fetch(ctx, request)
		first <- err
	}()
	// 等第一个请求进入上游后取消它，再去发起第二个（此时仍处于同一次单飞）
	time.Sleep(100 * time.Millisecond)
	cancel()

	waiter := make(chan error, 1)
	go func() {
		_, err := fetcher.Fetch(context.Background(), request)
		waiter <- err
	}()
	time.Sleep(100 * time.Millisecond)
	close(release)

	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("第一个调用方应因自身取消而报错，实际 %v", err)
	}
	select {
	case err := <-waiter:
		if err != nil {
			t.Fatalf("等待者不应跟随第一个调用方被取消: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("等待者未在预期时间内返回")
	}
}

// TestFetchChannelAlwaysSet 验证未启用回退时 Channel 也恒为 direct，
// 客户端因此不需要处理空值分支。
func TestFetchChannelAlwaysSet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, gateHTML)
	}))
	defer server.Close()

	result, err := newTestFetcher(t).Fetch(context.Background(),
		mustParseRequest(t, map[string]any{"url": server.URL}))
	if err != nil {
		t.Fatalf("Fetch 报错: %v", err)
	}
	if result.Channel != ChannelDirect {
		t.Fatalf("channel = %q, 期望 %q", result.Channel, ChannelDirect)
	}
}

// TestDefaultCacheDirEnvOverride 验证 FETCH_CACHE_DIR 能覆盖缓存目录，
// 为空时回落到容器内的绝对路径。
func TestDefaultCacheDirEnvOverride(t *testing.T) {
	t.Setenv(cacheDirEnv, "/tmp/one-search-test-cache")
	if got := DefaultCacheDir(); got != "/tmp/one-search-test-cache" {
		t.Fatalf("DefaultCacheDir() = %q", got)
	}
	t.Setenv(cacheDirEnv, "  ")
	if got := DefaultCacheDir(); got != defaultCacheDir {
		t.Fatalf("空白环境变量应回落到默认目录，实际 %q", got)
	}
}
