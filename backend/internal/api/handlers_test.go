package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
	"github.com/one-search/one-search/backend/internal/search"
)

// TestFillAnonymousKeySupport 覆盖 listProviders 里「按 registry 补全 supports_anonymous_key」这一步：
// 该字段不是数据库列，只在 Handler 层由编排层现场填充，因此这里直接断言填充结果，
// 并覆盖两种边界 —— orchestrator 为 nil（零值 Handler，既有测试的构造方式）时不 panic 且保持 false，
// 以及渠道未注册时退化为 false（提示语义为「未声明支持匿名」，不影响放行）。
func TestFillAnonymousKeySupport(t *testing.T) {
	registry := provider.NewRegistry(
		provider.NewContext7Provider(provider.Config{}),
		provider.NewKeenableProvider(provider.Config{}),
		provider.NewExaProvider(provider.Config{}),
	)
	handler := &Handler{orchestrator: search.NewOrchestrator(registry, nil, nil)}
	providers := []model.ProviderConfig{
		{Name: model.ProviderContext7},
		{Name: model.ProviderKeenable},
		{Name: model.ProviderExa},
		{Name: "not-registered"},
	}
	handler.fillAnonymousKeySupport(providers)

	wants := map[string]bool{
		model.ProviderContext7: true,
		model.ProviderKeenable: true,
		model.ProviderExa:      false,
		"not-registered":       false,
	}
	for _, item := range providers {
		if got := item.SupportsAnonymousKey; got != wants[item.Name] {
			t.Fatalf("%s.SupportsAnonymousKey = %v, want %v", item.Name, got, wants[item.Name])
		}
	}

	// 零值 Handler：orchestrator 为 nil，填充必须安全跳过而不是 panic。
	empty := &Handler{}
	nilOrchestrator := []model.ProviderConfig{{Name: model.ProviderContext7}}
	empty.fillAnonymousKeySupport(nilOrchestrator)
	if nilOrchestrator[0].SupportsAnonymousKey {
		t.Fatalf("orchestrator 为 nil 时不应改写字段")
	}
}

// TestValidateProviderKeyBaseURL 覆盖 key 级 base_url 的保存期校验：
// 空值放行（回退渠道默认）、普通绝对地址放行、`#` 完整端点语法先剥前缀再校验、
// Jina 因查询词在 URL 路径中而不接受 `#`、以及缺 scheme / 缺主机一律拒绝。
func TestValidateProviderKeyBaseURL(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		providerName string
		wantErr      bool
	}{
		{name: "空串放行", raw: "", providerName: model.ProviderTavily},
		{name: "纯空白放行", raw: "   ", providerName: model.ProviderTavily},
		{name: "普通绝对地址放行", raw: "https://relay.example.com/tavily", providerName: model.ProviderTavily},
		{name: "完整端点语法放行", raw: "#https://relay.example.com/proxy/tavily/search", providerName: model.ProviderTavily},
		{name: "完整端点两侧空白被裁剪", raw: "  #  https://relay.example.com/search  ", providerName: model.ProviderTavily},
		{name: "完整端点自带查询串放行", raw: "#https://relay.example.com/search?token=abc", providerName: model.ProviderTavily},
		{name: "完整端点缺少 scheme 拒绝", raw: "#relay.example.com/search", providerName: model.ProviderTavily, wantErr: true},
		{name: "只有井号拒绝", raw: "#", providerName: model.ProviderTavily, wantErr: true},
		{name: "Jina 不接受完整端点", raw: "#https://relay.example.com/search", providerName: model.ProviderJina, wantErr: true},
		{name: "Jina 普通地址仍放行", raw: "https://relay.example.com", providerName: model.ProviderJina},
		{name: "缺 scheme 拒绝", raw: "relay.example.com", providerName: model.ProviderTavily, wantErr: true},
		{name: "非 http scheme 拒绝", raw: "ftp://relay.example.com", providerName: model.ProviderTavily, wantErr: true},
		{name: "缺主机拒绝", raw: "https://", providerName: model.ProviderTavily, wantErr: true},
	}
	for _, tc := range cases {
		err := validateProviderKeyBaseURL(tc.raw, tc.providerName)
		if tc.wantErr && err == nil {
			t.Fatalf("%s: validateProviderKeyBaseURL(%q, %q) = nil, want error", tc.name, tc.raw, tc.providerName)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: validateProviderKeyBaseURL(%q, %q) = %v, want nil", tc.name, tc.raw, tc.providerName, err)
		}
	}
}

// secretTestStore 是明文读取端点用例的 AppStore 替身：只实现被触达的三个方法。
//
// 与 fetchTestStore 同理内嵌 AppStore 接口，其余三十多个方法保持未实现（触达即 panic，
// 可当作「不该走这条路径」的断言）。audits 记录审计动作名，用于断言「先写审计再返回」。
type secretTestStore struct {
	AppStore
	token      model.APIToken
	adminKey   model.AdminAPIKey
	providerK  model.APIKey
	audits     []string
	revealCall int
}

// RevealAPIToken 返回用例设定的令牌明文。
func (s *secretTestStore) RevealAPIToken(context.Context, int64) (model.APIToken, error) {
	return s.token, nil
}

// RevealAdminAPIKey 返回用例设定的管理员密钥明文。
func (s *secretTestStore) RevealAdminAPIKey(context.Context) (model.AdminAPIKey, error) {
	return s.adminKey, nil
}

// GetAPIKeyByID 返回用例设定的渠道密钥（含明文）。
func (s *secretTestStore) GetAPIKeyByID(context.Context, int64) (model.APIKey, error) {
	return s.providerK, nil
}

// RecordAuditLog 记录审计动作名；解密成功即应有一条，失败路径不应有。
func (s *secretTestStore) RecordAuditLog(_ context.Context, input model.AuditLogInput) error {
	s.audits = append(s.audits, input.Action)
	return nil
}

// TestRevealEndpointsDisableCaching 验证三个明文读取接口都禁止缓存留存。
//
// 明文响应一旦被中间代理或浏览器磁盘缓存留下，就脱离了服务端的审计与轮换控制
// （轮换后旧明文仍可被读回），因此 no-store 是安全属性而非可选优化。
// 三个接口一起断言：只给新增的两个加头会留下 provider_key.reveal 这个既有出口。
func TestRevealEndpointsDisableCaching(t *testing.T) {
	store := &secretTestStore{
		token:     model.APIToken{ID: 1, Name: "客户端", TokenPrefix: "osr_abcd", Token: "osr_secret_value"},
		adminKey:  model.AdminAPIKey{KeyPrefix: "oak_abcd", Key: "oak_secret_value"},
		providerK: model.APIKey{ID: 2, ProviderName: model.ProviderTavily, Alias: "主力", Value: "tvly-secret"},
	}
	h := &Handler{store: store}

	cases := []struct {
		name   string
		path   string
		handle http.HandlerFunc
	}{
		{"渠道密钥明文", "/api/admin/keys/2/secret", h.revealKey},
		{"接口令牌明文", "/api/admin/tokens/1/secret", h.revealToken},
		{"管理员密钥明文", "/api/admin/settings/admin-api-key/secret", h.revealAdminAPIKey},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store.audits = nil
			rec := httptest.NewRecorder()
			serveSecretRoute(t, tc.path, tc.handle, rec)

			if rec.Code != http.StatusOK {
				t.Fatalf("状态码 = %d, 期望 200, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, 期望 no-store", got)
			}
			if got := rec.Header().Get("Pragma"); got != "no-cache" {
				t.Fatalf("Pragma = %q, 期望 no-cache", got)
			}
			// 明文先写审计再返回：读取动作必须留痕，否则轮换与追责都无从谈起
			if len(store.audits) != 1 {
				t.Fatalf("应写一条审计，实际 %v", store.audits)
			}
		})
	}
}

// serveSecretRoute 把明文读取端点的路由与请求都带上 `{id}` 占位符。
//
// 单独拆出是因为两个测试都要它：路径里的 id 必须注册成 chi 的参数
// （如 /api/admin/tokens/{id}/secret），否则处理函数里的 chi.URLParam 取不到值，
// 会先被判成 400 —— 那样断言状态码的用例会失败，且失败原因与本次要验的行为无关。
// 请求仍打到具体 id 的路径上，返回值写进调用方传入的 recorder。
func serveSecretRoute(t *testing.T, concretePath string, handle http.HandlerFunc, rec *httptest.ResponseRecorder) {
	t.Helper()
	pattern := concretePath
	for _, prefix := range []string{"/api/admin/keys/", "/api/admin/tokens/"} {
		if strings.HasPrefix(concretePath, prefix) {
			pattern = prefix + "{id}/secret"
			break
		}
	}
	router := chi.NewRouter()
	router.Method(http.MethodGet, pattern, handle)
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, concretePath, nil))
}

// TestRevealEndpointsDoNotAuditFailures 验证失败路径不写审计。
//
// 审计记录的是「有人读到了明文」：目标不存在或明文未留存时并没有明文外泄，
// 写进去只会让审计页出现误导性的读取记录。三条失败语义分别是 404（不存在）
// 与 409（迁移 0002 之前的老令牌没有密文列）。
func TestRevealEndpointsDoNotAuditFailures(t *testing.T) {
	store := &secretTestStore{}
	h := &Handler{store: store}

	cases := []struct {
		name       string
		path       string
		handle     http.HandlerFunc
		wantStatus int
	}{
		{"令牌不存在", "/api/admin/tokens/9/secret", h.revealToken, http.StatusNotFound},
		{"管理员密钥未生成", "/api/admin/settings/admin-api-key/secret", h.revealAdminAPIKey, http.StatusNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store.audits = nil
			rec := httptest.NewRecorder()
			serveSecretRoute(t, tc.path, tc.handle, rec)

			if rec.Code != tc.wantStatus {
				t.Fatalf("状态码 = %d, 期望 %d, body = %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if len(store.audits) != 0 {
				t.Fatalf("失败路径不应写审计，实际 %v", store.audits)
			}
		})
	}

	// 老令牌：密文列缺失（迁移 0002 之前创建），明文读不出来，用 409 区别于「不存在」
	store.token = model.APIToken{ID: 3, Name: "老令牌", TokenPrefix: "osr_old"}
	rec := httptest.NewRecorder()
	serveSecretRoute(t, "/api/admin/tokens/3/secret", h.revealToken, rec)
	if rec.Code != http.StatusConflict {
		t.Fatalf("明文未留存应回 409，实际 %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(store.audits) != 0 {
		t.Fatalf("明文未留存时不应写审计，实际 %v", store.audits)
	}
}
