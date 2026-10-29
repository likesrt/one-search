package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/one-search/one-search/backend/internal/compat"
	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
	"github.com/one-search/one-search/backend/internal/search"
)

// deepSeekCompatStore 是兼容处理器的 AppStore 替身，只实现被触达的配置读取。
type deepSeekCompatStore struct {
	AppStore
	settings model.RuntimeSettings
	fetch    model.FetchSettings
}

// RuntimeSettings 返回用例设定的运行期配置。
func (s *deepSeekCompatStore) RuntimeSettings(context.Context) (model.RuntimeSettings, error) {
	return s.settings, nil
}

// FetchSettings 返回用例设定的抓取配置。
func (s *deepSeekCompatStore) FetchSettings(context.Context) (model.FetchSettings, error) {
	return s.fetch, nil
}

// deepSeekSearchStore 是编排层 Store 接口的替身：只覆盖搜索主流程会触达的方法。
type deepSeekSearchStore struct {
	search.Store
	settings  model.RuntimeSettings
	providers []model.ProviderConfig
}

// RuntimeSettings 返回编排层读取的运行期配置。
func (s *deepSeekSearchStore) RuntimeSettings(context.Context) (model.RuntimeSettings, error) {
	return s.settings, nil
}

// ListProviders 返回用例设定的渠道配置。
func (s *deepSeekSearchStore) ListProviders(context.Context) ([]model.ProviderConfig, error) {
	return s.providers, nil
}

// RecordSearchLog 吞掉检索日志，避免测试依赖数据库。
func (s *deepSeekSearchStore) RecordSearchLog(context.Context, model.SearchLogInput) error {
	return nil
}

// GetCache 固定返回未命中（用例未开启缓存）。
func (s *deepSeekSearchStore) GetCache(context.Context, string) ([]byte, bool, error) {
	return nil, false, nil
}

// SetCache 忽略缓存写入（用例未开启缓存）。
func (s *deepSeekSearchStore) SetCache(context.Context, string, []byte, int) error {
	return nil
}

// deepSeekKeyPool 是 KeyPool 替身，恒返回一把未落库的临时密钥。
type deepSeekKeyPool struct{}

// Acquire 返回临时密钥；ID 为 0 可避免触发官方额度刷新。
func (deepSeekKeyPool) Acquire(_ context.Context, providerName string, _ ...int64) (model.APIKey, func(bool, error), error) {
	return model.APIKey{ProviderName: providerName, Alias: "test", Value: "key"}, func(bool, error) {}, nil
}

// deepSeekTestProvider 是搜索渠道替身，按 err 返回成功结果或失败。
type deepSeekTestProvider struct{ err error }

// Name 返回编排层用于匹配的渠道名。
func (deepSeekTestProvider) Name() string { return "fake" }

// Search 返回一条固定结果，或按用例设定返回错误。
func (p deepSeekTestProvider) Search(context.Context, model.SearchRequest, model.APIKey) (model.ProviderResponse, error) {
	if p.err != nil {
		return model.ProviderResponse{}, p.err
	}
	return model.ProviderResponse{Results: []model.SearchResult{{
		Title: "结果标题", URL: "https://example.com/a", Snippet: "短摘要", Content: "正文内容", Score: 0.5,
	}}}, nil
}

// HealthCheck 恒成功，满足 Provider 接口。
func (deepSeekTestProvider) HealthCheck(context.Context, model.APIKey) error { return nil }

// SupportsAnonymousKey 声明不支持匿名，满足 Provider 接口。
func (deepSeekTestProvider) SupportsAnonymousKey() bool { return false }

// newDeepSeekSearchHandler 构造挂载了 DeepSeek 搜索端点的处理器。
//
// 编排层与处理器共用同一份运行期配置；渠道固定为单个 fake 渠道，
// 因此走 single 模式时不会受默认并行路由影响。
func newDeepSeekSearchHandler(t *testing.T, testProvider provider.Provider, enabled bool) chi.Router {
	t.Helper()
	settings := model.RuntimeSettings{
		DefaultMode:           model.SearchModeSingle,
		DefaultProviders:      []string{"fake"},
		DefaultLimit:          10,
		DefaultDedupe:         true,
		RequestTimeoutMS:      5000,
		CompatDeepSeekEnabled: enabled,
	}
	orch := search.NewOrchestrator(
		provider.NewRegistry(testProvider),
		deepSeekKeyPool{},
		&deepSeekSearchStore{settings: settings, providers: []model.ProviderConfig{{Name: "fake", Enabled: true}}},
	)
	h := &Handler{store: &deepSeekCompatStore{settings: settings}, orchestrator: orch}
	r := chi.NewRouter()
	r.Post("/v1/tools/search", h.deepSeekSearch)
	return r
}

// TestDeepSeekSearchEndpoint 覆盖兼容搜索的核心契约：
// 字段映射、默认 count、响应字段形状，以及参数错误返回 OpenAI 兼容错误对象。
func TestDeepSeekSearchEndpoint(t *testing.T) {
	router := newDeepSeekSearchHandler(t, deepSeekTestProvider{}, true)

	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/tools/search",
		strings.NewReader(`{"query":"测试","count":5,"need_content":true}`))
	router.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload compat.DeepSeekSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if len(payload.Results) != 1 {
		t.Fatalf("结果条数 = %d", len(payload.Results))
	}
	result := payload.Results[0]
	if result.RerankScore != 1.5 || result.Title != "结果标题" || result.URL != "https://example.com/a" {
		t.Fatalf("结果映射错误: %+v", result)
	}
	if result.Content != "正文内容" || result.Summary != "正文内容" {
		t.Fatalf("need_content=true 应返回正文: %+v", result)
	}
	if result.Authority != -1 || result.Date != nil {
		t.Fatalf("未知元数据应为 -1 / null: %+v", result)
	}
	// 字段必须始终出现，缺失值为 JSON null，而不是被省略。
	body := rec.Body.String()
	for _, fragment := range []string{`"site_name":null`, `"site_icon":null`, `"date":null`, `"authority":-1`} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("响应缺少 %s: %s", fragment, body)
		}
	}
}

// TestDeepSeekSearchValidation 覆盖官方参数约束与兼容错误对象形状。
func TestDeepSeekSearchValidation(t *testing.T) {
	router := newDeepSeekSearchHandler(t, deepSeekTestProvider{}, true)
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"空 query", `{"query":"   "}`, "query"},
		{"count 越界", `{"query":"x","count":51}`, "count"},
		{"非法 JSON", `{`, "invalid json body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tools/search", strings.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
			}
			var payload struct {
				Error struct {
					Message string      `json:"message"`
					Type    string      `json:"type"`
					Param   interface{} `json:"param"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("错误响应不是合法 JSON: %v", err)
			}
			if payload.Error.Type != deepSeekErrorInvalidRequest || payload.Error.Param != nil {
				t.Fatalf("错误对象应为 invalid_request_error 且 param=null: %+v", payload.Error)
			}
			if !strings.Contains(payload.Error.Message, tc.wantMsg) {
				t.Fatalf("错误信息应包含 %q，实际 %q", tc.wantMsg, payload.Error.Message)
			}
		})
	}
}

// TestDeepSeekSearchDisabled 验证开关关闭时返回 404（兼容错误对象）。
func TestDeepSeekSearchDisabled(t *testing.T) {
	router := newDeepSeekSearchHandler(t, deepSeekTestProvider{}, false)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tools/search", strings.NewReader(`{"query":"x"}`)))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"not_found_error"`) {
		t.Fatalf("错误类型应为 not_found_error: %s", rec.Body.String())
	}
}

// TestDeepSeekSearchAllProvidersFailed 验证所有渠道失败时返回 500，
// 而不是 200 + 空 results —— 后者会把上游故障伪装成「无结果」。
func TestDeepSeekSearchAllProvidersFailed(t *testing.T) {
	router := newDeepSeekSearchHandler(t, deepSeekTestProvider{err: errors.New("upstream down")}, true)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tools/search", strings.NewReader(`{"query":"x"}`)))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"server_error"`) {
		t.Fatalf("错误类型应为 server_error: %s", rec.Body.String())
	}
}

// TestDeepSeekSearchEnforcesTokenProviders 验证兼容端点同样执行令牌渠道白名单。
func TestDeepSeekSearchEnforcesTokenProviders(t *testing.T) {
	router := newDeepSeekSearchHandler(t, deepSeekTestProvider{}, true)
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/tools/search",
		strings.NewReader(`{"query":"x","providers":["fake"]}`))
	ctx := context.WithValue(request.Context(), apiTokenKey, model.APIToken{AllowedProviders: []string{"exa"}})
	router.ServeHTTP(rec, request.WithContext(ctx))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "not allowed to request provider") {
		t.Fatalf("应返回白名单拒绝原因: %s", rec.Body.String())
	}
}

// deepSeekAuthStore 是鉴权中间件测试用的 AuthStore 替身。
type deepSeekAuthStore struct {
	AuthStore
	settings model.RuntimeSettings
}

// RuntimeSettings 返回开启令牌鉴权的配置。
func (s *deepSeekAuthStore) RuntimeSettings(context.Context) (model.RuntimeSettings, error) {
	return s.settings, nil
}

// FindAdminAPIKey 固定返回未命中，模拟普通令牌路径。
func (s *deepSeekAuthStore) FindAdminAPIKey(context.Context, string) (model.AdminAPIKey, bool, error) {
	return model.AdminAPIKey{}, false, nil
}

// FindAPIToken 固定返回错误，模拟无效令牌。
func (s *deepSeekAuthStore) FindAPIToken(context.Context, string) (model.APIToken, error) {
	return model.APIToken{}, errors.New("not found")
}

// TestDeepSeekAuthErrorShape 验证鉴权失败也输出 OpenAI 兼容错误对象，
// 而不是原生接口的 {message,status}。
func TestDeepSeekAuthErrorShape(t *testing.T) {
	auth := NewAuthService(
		&deepSeekAuthStore{settings: model.RuntimeSettings{APIAuthRequired: true}},
		time.Hour, 5, time.Minute, time.Minute,
	)
	r := chi.NewRouter()
	r.With(auth.requireAPITokenDeepSeek).Post("/v1/tools/search", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tools/search", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"authentication_error"`) ||
		!strings.Contains(rec.Body.String(), `"param":null`) {
		t.Fatalf("鉴权错误应为 OpenAI 兼容对象: %s", rec.Body.String())
	}
}

// newDeepSeekFetchRouter 构造只挂载 /v1/tools/fetch 的路由。
func newDeepSeekFetchRouter(store AppStore) chi.Router {
	h := &Handler{store: store}
	r := chi.NewRouter()
	r.Post("/v1/tools/fetch", h.deepSeekFetch)
	return r
}

// TestDeepSeekFetchEndpoint 覆盖兼容抓取的正常返回与主要错误映射。
func TestDeepSeekFetchEndpoint(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>标题</h1><p>正文</p></main></body></html>`))
	}))
	defer upstream.Close()

	store := &fetchTestStore{settings: model.FetchSettings{Enabled: true, AllowPrivate: true, TimeoutMS: 5000}}
	router := newDeepSeekFetchRouter(store)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tools/fetch",
		strings.NewReader(`{"url":"`+upstream.URL+`/page"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	var payload compat.DeepSeekFetchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	if payload.Title != "" {
		t.Fatalf("title 应为空字符串，实际 %q", payload.Title)
	}
	if !strings.Contains(payload.Content, "标题") || !strings.Contains(payload.Content, "正文") {
		t.Fatalf("content 应包含 Markdown 正文: %q", payload.Content)
	}
}

// TestDeepSeekFetchErrorMapping 覆盖官方状态码映射：
// 参数错误 400、SSRF 拦截 403、正文为空 404、传输失败 500、请求体过大 413。
func TestDeepSeekFetchErrorMapping(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html></html>`))
	}))
	defer empty.Close()

	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedURL := closed.URL
	closed.Close()

	cases := []struct {
		name     string
		settings model.FetchSettings
		body     string
		want     int
	}{
		{"缺 url", model.FetchSettings{Enabled: true, TimeoutMS: 2000}, `{}`, http.StatusBadRequest},
		{"非法 url", model.FetchSettings{Enabled: true, TimeoutMS: 2000}, `{"url":"example.com"}`, http.StatusBadRequest},
		{"SSRF 拦截", model.FetchSettings{Enabled: true, TimeoutMS: 2000}, `{"url":"http://169.254.169.254/latest/meta-data/"}`, http.StatusForbidden},
		{"正文为空", model.FetchSettings{Enabled: true, AllowPrivate: true, TimeoutMS: 5000}, `{"url":"` + empty.URL + `"}`, http.StatusNotFound},
		{"传输失败", model.FetchSettings{Enabled: true, AllowPrivate: true, TimeoutMS: 2000}, `{"url":"` + closedURL + `"}`, http.StatusInternalServerError},
		{"功能关闭", model.FetchSettings{Enabled: false}, `{"url":"https://example.com"}`, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			router := newDeepSeekFetchRouter(&fetchTestStore{settings: tc.settings})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tools/fetch", strings.NewReader(tc.body)))
			if rec.Code != tc.want {
				t.Fatalf("状态码 = %d, 期望 %d, body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestDeepSeekFetchBodyTooLarge 验证请求体超过全局上限时返回 413。
func TestDeepSeekFetchBodyTooLarge(t *testing.T) {
	h := &Handler{store: &fetchTestStore{settings: model.FetchSettings{Enabled: true, TimeoutMS: 2000}}}
	r := chi.NewRouter()
	r.Use(bodyLimitMiddleware(4))
	r.Post("/v1/tools/fetch", h.deepSeekFetch)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/tools/fetch", strings.NewReader(`{"url":"https://example.com"}`)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
}
