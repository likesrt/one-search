package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
)

type contextKey string

const (
	requestIDKey  contextKey = "request_id"
	apiTokenIDKey contextKey = "api_token_id"
	apiTokenKey   contextKey = "api_token"
	adminActorKey contextKey = "admin_actor"
)

func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" {
			requestID = newRequestID()
		}
		ctx := context.WithValue(r.Context(), requestIDKey, requestID)
		w.Header().Set("X-Request-ID", requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func corsMiddleware(origins []string) func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	allowAll := false
	for _, origin := range origins {
		if origin == "*" {
			allowAll = true
		}
		allowed[origin] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if allowAll || allowed[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if allowAll && origin == "" {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				}
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, X-Request-ID, Mcp-Session-Id, Mcp-Protocol-Version, Last-Event-ID")
				w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Mcp-Session-Id")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeadersMiddleware 为所有响应加上安全相关的头。
//
// no-store 是这里唯一有实际后果的一项，其余是浏览器侧的加固。加它的原因是
// HTTP 的默认规则与直觉相反：GET 且状态码属于「启发式可缓存」时（200、203、204、
// 206、300、301、308、404、405、410、414、501，见 RFC 9110 §15.1），
// 即使响应里没有任何缓存头，缓存也被允许存储并直接复用 —— 「没写缓存头」不等于
// 「不缓存」。本服务的接口绝大多数是 GET 且返回 200，对外接口带用量与额度数据、
// 管理接口带渠道路由与凭据元信息，都不该被留在任何一层缓存里。
//
// 不用 no-cache 而用 no-store：前者只要求复用前回源校验，响应仍会落盘；
// 后者禁止存储，是这里真正需要的语义。
//
// 为什么不依赖 RFC 9111 §3.5「带 Authorization 头的请求，共享缓存不得复用其响应」：
// 那条只约束 shared cache，不约束浏览器自己的私有缓存；而且它要求请求确实带上该头，
// 一旦调用方把令牌放进查询串（或用 Cookie 鉴权）就不再成立。请求级规则挡不住的部分，
// 由响应级的 no-store 兜住。
//
// 注意本中间件不覆盖 nginx 直接返回的静态资源（前端 dist 里的 JS/CSS），
// 那些是公开内容，缓存反而有益；nginx 的 add_header 也未受影响。
//
// 副作用：写入响应头（在业务处理函数之前，因此处理函数显式设置的同名头会覆盖它）。
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'self'; object-src 'none'; frame-ancestors 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func bodyLimitMiddleware(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if limit > 0 && r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func loggingMiddleware(log requestLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			log.Info("http_request", map[string]interface{}{
				"method":     r.Method,
				"path":       r.URL.Path,
				"status":     recorder.status,
				"latency_ms": time.Since(start).Milliseconds(),
				"request_id": RequestID(r.Context()),
			})
		})
	}
}

type requestLogger interface {
	Info(message string, fields map[string]interface{})
	Error(message string, fields map[string]interface{})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

func RequestID(ctx context.Context) string {
	value, _ := ctx.Value(requestIDKey).(string)
	return value
}

func APITokenID(ctx context.Context) int64 {
	value, _ := ctx.Value(apiTokenIDKey).(int64)
	return value
}

func APIToken(ctx context.Context) (model.APIToken, bool) {
	value, ok := ctx.Value(apiTokenKey).(model.APIToken)
	return value, ok
}

func AdminActor(ctx context.Context) string {
	value, _ := ctx.Value(adminActorKey).(string)
	if value == "" {
		return "admin"
	}
	return value
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return strings.TrimSpace(r.RemoteAddr)
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	return ""
}

func newRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}
