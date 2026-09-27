package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/one-search/one-search/backend/internal/config"
)

// discardLogger 是 requestLogger 的空实现，供这里构造 Server 时占位。
//
// NewServer 的日志中间件会直接调用 logger，传 nil 会 panic；而 logging.New()
// 会把访问日志打到 stdout，让测试输出充满噪声。因此本文件自带一个丢弃实现。
type discardLogger struct{}

// Info 丢弃一条信息级日志。
func (discardLogger) Info(string, map[string]interface{}) {}

// Error 丢弃一条错误级日志。
func (discardLogger) Error(string, map[string]interface{}) {}

// TestServerAppliesNoStoreToEveryResponse 验证服务端所有响应都带 Cache-Control: no-store。
//
// 这条断言的价值在于 HTTP 的默认规则与直觉相反：GET 且状态码属于「启发式可缓存」时
// （200、203、204、206、300、301、308、404、405、410、414、501，见 RFC 9110 §15.1），
// 即使响应里没有任何缓存头，缓存也被允许存储并直接复用。本服务的接口绝大多数是
// GET 且返回 200，因此「没写缓存头」会被当成「可以缓存」——这正是必须显式 no-store 的原因。
//
// 覆盖 200 与 404 两种状态码、以及带查询串的路径：no-store 是中间件在业务处理前统一写入的，
// 与状态码和路径无关，这两组取值用于确认它没有被某条分支绕开。
func TestServerAppliesNoStoreToEveryResponse(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"健康检查 200", "/healthz"},
		{"带查询串的路径", "/healthz?probe=1"},
		{"未命中的 404", "/no-such-route"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := NewServer(config.Config{}, discardLogger{})
			rec := httptest.NewRecorder()
			server.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))

			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, 期望 no-store", got)
			}
		})
	}
}

// TestSecurityHeadersDoNotOverrideExplicitCacheControl 验证业务处理函数可以覆盖中间件的 no-store。
//
// 中间件在业务处理之前写头，因此处理函数显式设置的同名头必须胜出。
// 这条不是为了「允许缓存某些接口」，而是确认覆盖机制成立：
// 将来若某个端点确有正当理由要换成 no-cache（例如需要浏览器参与校验），
// 可以在处理函数里改，而不必拆掉全局的默认值。
func TestSecurityHeadersDoNotOverrideExplicitCacheControl(t *testing.T) {
	handler := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/anything", nil))

	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Fatalf("处理函数显式设置的头应生效，实际 %q", got)
	}
}
