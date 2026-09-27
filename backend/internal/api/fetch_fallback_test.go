package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/one-search/one-search/backend/internal/fetch"
	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
	"github.com/one-search/one-search/backend/internal/search"
)

// fallbackEndpointStore 是回退端点用例的单一替身，同时满足 api.AppStore、
// search.Store 与 search.KeyPool 三个接口。
//
// 用一个类型同时扮演三方，是为了让端点级用例覆盖真实的整条链路：
// 配置读取（api）→ 取 key（keypool 协议）→ 构建适配器（search）→ 真实 HTTP →
// 记账（api）。唯一被替换掉的只有最外层的上游 HTTP 服务。
//
// 内嵌 AppStore 而非逐条实现：接口有三十多个方法，只需覆盖被触达的几个。
type fallbackEndpointStore struct {
	AppStore
	settings model.FetchSettings
	// extractBaseURL 是假 Tavily 上游的地址，经 key.BaseURL 注入
	extractBaseURL string
	// usages 记录每次回退记账的 credits
	usages []float64
	mu     sync.Mutex
	// auditActions 记录审计动作名
	auditActions []string
}

// FetchSettings 返回用例设定的抓取配置。
func (s *fallbackEndpointStore) FetchSettings(context.Context) (model.FetchSettings, error) {
	return s.settings, nil
}

// UpdateFetchSettings 记录保存的配置并同步为后续读取的值。
func (s *fallbackEndpointStore) UpdateFetchSettings(_ context.Context, settings model.FetchSettings) error {
	s.settings = settings
	return nil
}

// RecordFetchFallbackUsage 记录回退消耗的 credits。
func (s *fallbackEndpointStore) RecordFetchFallbackUsage(_ context.Context, _ string, _ int64, credits float64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usages = append(s.usages, credits)
	return nil
}

// RecordAuditLog 记录审计动作名。
func (s *fallbackEndpointStore) RecordAuditLog(_ context.Context, input model.AuditLogInput) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditActions = append(s.auditActions, input.Action)
	return nil
}

// usageTotal 返回已记录的回退 credits 之和。
func (s *fallbackEndpointStore) usageTotal() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0.0
	for _, item := range s.usages {
		total += item
	}
	return total
}

// GetAPIKeyByID 返回零值密钥；回退链路不经过这个方法。
func (s *fallbackEndpointStore) GetAPIKeyByID(context.Context, int64) (model.APIKey, error) {
	return model.APIKey{}, nil
}

// RecordKeyResult 是 key 健康度回写，本案不关心。
func (s *fallbackEndpointStore) RecordKeyResult(context.Context, model.APIKey, bool, string) error {
	return nil
}

// UpdateProviderKeyOfficialQuota 是额度刷新回写，本案不关心。
func (s *fallbackEndpointStore) UpdateProviderKeyOfficialQuota(context.Context, int64, model.ProviderKeyQuotaResult) error {
	return nil
}

// RuntimeSettings 返回最小可用设置；回退链路只用到渠道配置。
func (s *fallbackEndpointStore) RuntimeSettings(context.Context) (model.RuntimeSettings, error) {
	return model.RuntimeSettings{RequestTimeoutMS: 2000}, nil
}

// ListProviders 返回一个启用的 tavily 渠道。
//
// 刻意不带 proxy_enabled：用例要断言「抓取功能的全局代理不会流到回退通道」，
// 渠道级与 key 级都无代理时，回退链路解析出的代理必然为空。
func (s *fallbackEndpointStore) ListProviders(context.Context) ([]model.ProviderConfig, error) {
	return []model.ProviderConfig{{
		Name: model.ProviderTavily, Enabled: true, Priority: 1, Weight: 1, TimeoutMS: 3000,
	}}, nil
}

// RecordSearchLog 是搜索日志写入；抓取链路不触达，恒成功。
func (s *fallbackEndpointStore) RecordSearchLog(context.Context, model.SearchLogInput) error {
	return nil
}

// GetCache 恒未命中；抓取缓存不走搜索缓存表。
func (s *fallbackEndpointStore) GetCache(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

// SetCache 恒成功；抓取不写搜索缓存。
func (s *fallbackEndpointStore) SetCache(context.Context, string, []byte, int) error {
	return nil
}

// Acquire 返回一条把 base_url 指向假 Tavily 上游的 key。
//
// key.ID 保持 0（未落库的临时 key）是刻意的：编排层只对 ID>0 的 key 触发官方额度
// 自动刷新，那个 goroutine 会发起真实网络请求，会让用例不再是离线的。
// ProxyMode 取 direct：这样回退链路的代理必然为空，代理隔离断言才有意义。
func (s *fallbackEndpointStore) Acquire(_ context.Context, providerName string, _ ...int64) (model.APIKey, func(bool, error), error) {
	return model.APIKey{
		ProviderName: providerName, Alias: "tavily-test", Value: "tvly-test",
		BaseURL: s.extractBaseURL, ProxyMode: model.ProxyModeDirect,
	}, func(bool, error) {}, nil
}

// newFallbackEndpointRouter 构造带回退能力的抓取路由，渠道用真实的 TavilyProvider 适配器。
func newFallbackEndpointRouter(store *fallbackEndpointStore) chi.Router {
	registry := provider.NewRegistry()
	registry.RegisterFactory(model.ProviderTavily, func(cfg provider.Config) provider.Provider {
		return provider.NewTavilyProvider(cfg)
	})
	h := &Handler{store: store, orchestrator: search.NewOrchestrator(registry, store, store)}
	r := chi.NewRouter()
	r.Get("/v1/fetch", h.fetch)
	r.Post("/v1/fetch", h.fetch)
	return r
}

// newExtractUpstream 起一个假的 Tavily /extract 上游，返回正文与 credits 由用例指定。
//
// 全部离线：用例绝不打真实 api.tavily.com（既消耗额度，也违反「测试不打真实网络」的约定）。
// calls 为 nil 时不做调用计数。
func newExtractUpstream(t *testing.T, content string, credits float64, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/extract" {
			t.Errorf("回退请求路径 = %q, 期望 /extract", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tvly-test" {
			t.Errorf("回退请求鉴权头 = %q", got)
		}
		if calls != nil {
			*calls++
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{{"url": "https://example.com", "raw_content": content}},
			"usage":   map[string]interface{}{"credits": credits},
		})
	}))
}

// newBlockedUpstream 起一个固定返回指定状态码与正文的内置抓取目标。
func newBlockedUpstream(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// fallbackEndpointSettings 返回启用回退的抓取配置，并把缓存目录指向临时目录。
//
// 缓存目录必须隔离：默认路径是容器内的 /app/data/fetch-cache，
// 用例之间共享它会让「是否命中缓存」的断言互相干扰。
func fallbackEndpointSettings(t *testing.T) model.FetchSettings {
	t.Helper()
	t.Setenv("FETCH_CACHE_DIR", t.TempDir())
	return model.FetchSettings{
		Enabled: true, AllowPrivate: true, TimeoutMS: 5000,
		FallbackEnabled: true, FallbackMinChars: 80,
		CacheTTLSeconds: 120, CacheErrorTTLSeconds: 120,
	}
}

// TestFetchEndpointFallbackEndToEnd 覆盖端点级的回退全链路：
// 内置抓取撞到 403 → 自动改用 Tavily extract → 返回 channel=tavily 的 Markdown。
//
// 回退成功必须报 200（而不是把 403 透出来）：Result.Text() 对非 2xx 会在正文前补一行
// HTTP <状态码>，报 403 会让模型在真正的正文前面看到一行假的 403。
func TestFetchEndpointFallbackEndToEnd(t *testing.T) {
	calls := 0
	extract := newExtractUpstream(t, "# 回退正文\n\n这是 Tavily 取回的整页 Markdown。", 1, &calls)
	defer extract.Close()
	upstream := newBlockedUpstream(t, http.StatusForbidden, `<html><body>Just a moment...</body></html>`)
	defer upstream.Close()

	store := &fallbackEndpointStore{settings: fallbackEndpointSettings(t), extractBaseURL: extract.URL}
	rec := httptest.NewRecorder()
	newFallbackEndpointRouter(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fetch?url="+upstream.URL, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200, body = %s", rec.Code, rec.Body.String())
	}
	var result fetch.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Channel != fetch.ChannelTavily || result.StatusCode != http.StatusOK {
		t.Fatalf("回退结果不符合预期: %+v", result)
	}
	if result.ContentType != "text/markdown" {
		t.Fatalf("回退通道的 content_type = %q", result.ContentType)
	}
	if !strings.Contains(result.Content, "回退正文") {
		t.Fatalf("应返回回退正文: %q", result.Content)
	}
	if calls != 1 {
		t.Fatalf("回退上游调用次数 = %d, 期望 1", calls)
	}
	if got := store.usageTotal(); got != 1 {
		t.Fatalf("回退应记 1 个 credit，实际 %v", got)
	}
}

// TestFetchEndpointFallbackDisabled 验证关闭回退时完全不碰 Tavily：
// 返回内置的 403 页面，且回退上游零调用（零开销是默认关闭的主要收益）。
func TestFetchEndpointFallbackDisabled(t *testing.T) {
	calls := 0
	extract := newExtractUpstream(t, "不该被调用", 1, &calls)
	defer extract.Close()
	upstream := newBlockedUpstream(t, http.StatusForbidden, `<html><body>Just a moment...</body></html>`)
	defer upstream.Close()

	settings := fallbackEndpointSettings(t)
	settings.FallbackEnabled = false
	store := &fallbackEndpointStore{settings: settings, extractBaseURL: extract.URL}
	rec := httptest.NewRecorder()
	newFallbackEndpointRouter(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fetch?url="+upstream.URL, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	var result fetch.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Channel != fetch.ChannelDirect || result.StatusCode != http.StatusForbidden {
		t.Fatalf("回退关闭时应原样返回内置结果: %+v", result)
	}
	if calls != 0 {
		t.Fatalf("回退关闭时不应调用 Tavily，实际 %d 次", calls)
	}
	if got := store.usageTotal(); got != 0 {
		t.Fatalf("回退关闭时不应记账，实际 %v", got)
	}
}

// TestFetchEndpointFallbackSkips404 验证 404 不触发回退：
// 对端明确说「资源不存在」，重试无意义，也不该消耗第三方额度。
func TestFetchEndpointFallbackSkips404(t *testing.T) {
	calls := 0
	extract := newExtractUpstream(t, "不该被调用", 1, &calls)
	defer extract.Close()
	upstream := newBlockedUpstream(t, http.StatusNotFound, `<html><body><main>`+
		strings.Repeat("这是一段足够长的正文内容。", 20)+`</main></body></html>`)
	defer upstream.Close()

	store := &fallbackEndpointStore{settings: fallbackEndpointSettings(t), extractBaseURL: extract.URL}
	rec := httptest.NewRecorder()
	newFallbackEndpointRouter(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fetch?url="+upstream.URL, nil))

	var result fetch.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.StatusCode != http.StatusNotFound {
		t.Fatalf("404 应原样返回，实际 %+v", result)
	}
	if calls != 0 {
		t.Fatalf("404 不应触发回退，实际 %d 次", calls)
	}
}

// TestFetchEndpointFallbackSkipsPrivateTarget 验证内网目标不触发回退。
//
// 这是 SSRF 护栏能否成立的关键：内网地址一旦被外发给第三方，
// 「拨号层拦截内网」这层防护就形同虚设。
func TestFetchEndpointFallbackSkipsPrivateTarget(t *testing.T) {
	calls := 0
	extract := newExtractUpstream(t, "不该被调用", 1, &calls)
	defer extract.Close()

	settings := fallbackEndpointSettings(t)
	// 关闭 allow_private，让拨号层真的拦下内网目标并返回 ErrPrivateTarget
	settings.AllowPrivate = false
	store := &fallbackEndpointStore{settings: settings, extractBaseURL: extract.URL}
	rec := httptest.NewRecorder()
	newFallbackEndpointRouter(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/fetch?url=http://169.254.169.254/latest/meta-data/", nil))

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("被 SSRF 拦截应返回 502，实际 %d, body = %s", rec.Code, rec.Body.String())
	}
	if calls != 0 {
		t.Fatal("内网目标绝不能被外发给回退通道")
	}
}

// TestFetchEndpointFallbackSkipsCustomHeaders 验证带自定义请求头的请求不回退。
//
// headers 里可能是 Authorization 或 Cookie，把它们转发给第三方等于泄露调用方凭据。
func TestFetchEndpointFallbackSkipsCustomHeaders(t *testing.T) {
	calls := 0
	extract := newExtractUpstream(t, "不该被调用", 1, &calls)
	defer extract.Close()
	upstream := newBlockedUpstream(t, http.StatusForbidden, `<html><body>Just a moment...</body></html>`)
	defer upstream.Close()

	store := &fallbackEndpointStore{settings: fallbackEndpointSettings(t), extractBaseURL: extract.URL}
	payload := `{"url":"` + upstream.URL + `","headers":{"Authorization":"Bearer secret"}}`
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/fetch", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	newFallbackEndpointRouter(store).ServeHTTP(rec, request)

	var result fetch.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.StatusCode != http.StatusForbidden || result.Channel != fetch.ChannelDirect {
		t.Fatalf("带自定义请求头时应返回内置结果: %+v", result)
	}
	if calls != 0 {
		t.Fatal("带自定义请求头时不得把请求外发给第三方")
	}
}

// TestFetchEndpointFallbackKeepsRawSemantics 验证 raw=true 不回退：
// raw 的契约是「原样返回源文本」，用 Markdown 替代会静默改变语义。
func TestFetchEndpointFallbackKeepsRawSemantics(t *testing.T) {
	calls := 0
	extract := newExtractUpstream(t, "不该被调用", 1, &calls)
	defer extract.Close()
	upstream := newBlockedUpstream(t, http.StatusForbidden, `<html><body>Just a moment...</body></html>`)
	defer upstream.Close()

	store := &fallbackEndpointStore{settings: fallbackEndpointSettings(t), extractBaseURL: extract.URL}
	rec := httptest.NewRecorder()
	newFallbackEndpointRouter(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/fetch?url="+upstream.URL+"&raw=true", nil))

	var result fetch.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if !strings.Contains(result.Content, "<html>") {
		t.Fatalf("raw 应返回原始 HTML: %q", result.Content)
	}
	if calls != 0 {
		t.Fatal("raw 请求不得回退")
	}
}

// TestFetchEndpointFallbackFailureKeepsDirectResult 验证回退失败不把请求变成 502：
// 回退是加分项而非必要条件，第三方故障不该影响原本可用的内置抓取。
func TestFetchEndpointFallbackFailureKeepsDirectResult(t *testing.T) {
	// 回退上游固定返回 401，模拟「没有可用 key / 配额问题」
	extract := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid api key"}`))
	}))
	defer extract.Close()
	upstream := newBlockedUpstream(t, http.StatusForbidden, `<html><body>Just a moment...</body></html>`)
	defer upstream.Close()

	store := &fallbackEndpointStore{settings: fallbackEndpointSettings(t), extractBaseURL: extract.URL}
	rec := httptest.NewRecorder()
	newFallbackEndpointRouter(store).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fetch?url="+upstream.URL, nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("回退失败不应把请求变成 502，实际 %d, body = %s", rec.Code, rec.Body.String())
	}
	var result fetch.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	if result.Channel != fetch.ChannelDirect || result.StatusCode != http.StatusForbidden {
		t.Fatalf("回退失败时应返回内置结果: %+v", result)
	}
}

// TestFetchEndpointCacheHitsSecondTime 验证端点级缓存：同一 URL 连抓两次，
// 第二次不再打任何上游（内置与回退都不打）。
func TestFetchEndpointCacheHitsSecondTime(t *testing.T) {
	extractCalls := 0
	extract := newExtractUpstream(t, "# 回退正文", 1, &extractCalls)
	defer extract.Close()
	directCalls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directCalls++
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<html><body>Just a moment...</body></html>`))
	}))
	defer upstream.Close()

	store := &fallbackEndpointStore{settings: fallbackEndpointSettings(t), extractBaseURL: extract.URL}
	router := newFallbackEndpointRouter(store)
	target := "/v1/fetch?url=" + upstream.URL
	for index := 0; index < 2; index++ {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 次抓取状态码 = %d, body = %s", index+1, rec.Code, rec.Body.String())
		}
	}
	if directCalls != 1 {
		t.Fatalf("第二次应命中缓存，内置抓取上游调用 = %d, 期望 1", directCalls)
	}
	if extractCalls != 1 {
		t.Fatalf("第二次应命中缓存，回退调用 = %d, 期望 1", extractCalls)
	}
}

// TestFetchSettingsFingerprintIncludesFallbackAndCache 验证配置指纹的变化会导致重建：
// 管理台改任一新增字段后，行为必须立即改变，而不是沿用旧的 Fetcher。
func TestFetchSettingsFingerprintIncludesFallbackAndCache(t *testing.T) {
	t.Setenv("FETCH_CACHE_DIR", t.TempDir())
	store := &fallbackEndpointStore{settings: fallbackEndpointSettings(t)}
	h := &Handler{store: store}

	base := h.fetcherFor(store.settings)
	mutations := map[string]func(s *model.FetchSettings){
		"回退开关":    func(s *model.FetchSettings) { s.FallbackEnabled = !s.FallbackEnabled },
		"回退阈值":    func(s *model.FetchSettings) { s.FallbackMinChars += 1 },
		"缓存 TTL":  func(s *model.FetchSettings) { s.CacheTTLSeconds += 1 },
		"错误态 TTL": func(s *model.FetchSettings) { s.CacheErrorTTLSeconds += 1 },
		"单条缓存上限":  func(s *model.FetchSettings) { s.CacheMaxBytes += 1 },
		"缓存总量上限":  func(s *model.FetchSettings) { s.CacheMaxTotalBytes += 1 },
		"并发上限":    func(s *model.FetchSettings) { s.MaxConcurrency += 1 },
	}
	for name, mutate := range mutations {
		changed := store.settings
		mutate(&changed)
		if h.fetcherFor(changed) == base {
			t.Fatalf("改动 %s 后应重建 Fetcher，实际复用了旧对象", name)
		}
		// 还原，避免后续子用例互相影响
		h.fetcher = base
		h.fetcherKey = ""
		h.fetcherFor(store.settings)
		base = h.fetcher
	}
}
