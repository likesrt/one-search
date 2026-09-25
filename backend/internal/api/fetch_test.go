package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/one-search/one-search/backend/internal/model"
)

// fetchTestStore 是抓取端点测试用的 AppStore 替身。
//
// 与 mcpTestStore 同理内嵌 AppStore 接口：只实现被触达的方法，其余保持未实现
// （触达即 panic，可当作「不该走这条路径」的断言）。记录 UpdateFetchSettings 的入参，
// 用于断言保存链路；RecordAuditLog 只记录动作名，用于断言审计是否写入。
type fetchTestStore struct {
	AppStore
	settings model.FetchSettings
	saved    *model.FetchSettings
	audits   []string
}

// FetchSettings 返回用例设定的抓取配置。
func (s *fetchTestStore) FetchSettings(context.Context) (model.FetchSettings, error) {
	return s.settings, nil
}

// UpdateFetchSettings 记录保存的配置，供用例断言。
func (s *fetchTestStore) UpdateFetchSettings(_ context.Context, settings model.FetchSettings) error {
	saved := settings
	s.saved = &saved
	s.settings = settings
	return nil
}

// RecordAuditLog 记录审计动作名；抓取端点的审计不应影响主流程，故不返回错误。
func (s *fetchTestStore) RecordAuditLog(_ context.Context, input model.AuditLogInput) error {
	s.audits = append(s.audits, input.Action)
	return nil
}

// newFetchTestRouter 构造只挂载 /v1/fetch 的路由。
//
// 不走 h.Mount 是因为 Mount 会连带注册 MCP 与整套管理台路由，测试只需要这一条路径；
// auth 传 nil 会让 requireAPIToken 被跳过（直接调 h.fetch）。
func newFetchTestRouter(store AppStore) chi.Router {
	h := &Handler{store: store}
	r := chi.NewRouter()
	r.Get("/v1/fetch", h.fetch)
	r.Post("/v1/fetch", h.fetch)
	return r
}

// TestFetchEndpointStatusSemantics 覆盖抓取端点对外端口的状态码语义矩阵。
//
// 这是本功能最容易误解的一点：「抓到上游 404 页面」是成功（200 + status_code=404），
// 只有传输层失败才是 502。httptest 服务器监听 127.0.0.1，因此用例配置里必须
// 打开 AllowPrivate，否则会被 SSRF 护栏拦成 502。
func TestFetchEndpointStatusSemantics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><body><main><h1>标题</h1></main></body></html>`))
	}))
	defer upstream.Close()

	enabled := fetchTestStore{settings: model.FetchSettings{Enabled: true, AllowPrivate: true, TimeoutMS: 5000}}
	router := newFetchTestRouter(&enabled)

	cases := []struct {
		name       string
		query      string
		wantStatus int
		wantBody   string
	}{
		{"成功抓取", "url=" + upstream.URL + "/page", http.StatusOK, `"status_code":200`},
		{"上游 404 仍是 200", "url=" + upstream.URL + "/missing", http.StatusOK, `"status_code":404`},
		{"参数非法：裸域名", "url=example.com", http.StatusBadRequest, "Invalid url"},
		{"参数非法：缺 url", "", http.StatusBadRequest, "expected an object containing url"},
		{"参数非法：max_length 越界", "url=" + upstream.URL + "/page&max_length=99999", http.StatusBadRequest, "Invalid max_length"},
		{"参数非法：max_length 非整数", "url=" + upstream.URL + "/page&max_length=abc", http.StatusBadRequest, "Invalid max_length"},
		{"参数非法：raw 非布尔", "url=" + upstream.URL + "/page&raw=yes", http.StatusBadRequest, "Invalid raw"},
		{
			// GET 形态只从查询串读 url/method/max_length/start_index/raw：
			// body 与 headers 不在白名单里，写了也被静默忽略（不是错误），
			// 因为认证头放进 URL 会留在访问日志与浏览器历史里，刻意只在 POST 形态提供。
			"GET 形态忽略 body 参数",
			"url=" + upstream.URL + "/page&body=x", http.StatusOK, `"method":"GET"`,
		},
		{"截断并给出续读起点", "url=" + upstream.URL + "/page&max_length=2", http.StatusOK, `"truncated":true`},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fetch?"+item.query, nil))
			if rec.Code != item.wantStatus {
				t.Fatalf("状态码 = %d, 期望 %d, body = %s", rec.Code, item.wantStatus, rec.Body.String())
			}
			if item.wantBody != "" && !strings.Contains(rec.Body.String(), item.wantBody) {
				t.Fatalf("响应体应包含 %q，实际 %s", item.wantBody, rec.Body.String())
			}
		})
	}
}

// TestFetchEndpointDisabledReturns404 验证功能关闭时对外端点返回 404（而非 400 或 502）。
func TestFetchEndpointDisabledReturns404(t *testing.T) {
	disabled := fetchTestStore{settings: model.FetchSettings{Enabled: false}}
	router := newFetchTestRouter(&disabled)

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fetch?url=https://example.com", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, 期望 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestFetchEndpointTransportFailureReturns502 验证传输层失败映射为 502。
//
// 目标用真实存在但无人监听的私网地址，且用例配置打开 AllowPrivate 让请求真的走到拨号，
// 从而失败原因来自连接而非护栏 —— 与「被 SSRF 拦截」区分开（后者同样 502，见下个用例）。
func TestFetchEndpointTransportFailureReturns502(t *testing.T) {
	// 先起一个服务器拿到端口再关掉，确保该端口无人监听
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := upstream.URL
	upstream.Close()

	store := fetchTestStore{settings: model.FetchSettings{Enabled: true, AllowPrivate: true, TimeoutMS: 2000}}
	router := newFetchTestRouter(&store)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/fetch?url="+url, nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 502, body = %s", rec.Code, rec.Body.String())
	}
}

// TestFetchEndpointBlocksPrivateTarget 验证 SSRF 护栏在端点链路上生效（同样映射为 502）。
func TestFetchEndpointBlocksPrivateTarget(t *testing.T) {
	store := fetchTestStore{settings: model.FetchSettings{Enabled: true, TimeoutMS: 2000}}
	router := newFetchTestRouter(&store)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/fetch?url=http://169.254.169.254/latest/meta-data/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("状态码 = %d, 期望 502, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "已拦截") {
		t.Fatalf("响应体应说明被护栏拦截: %s", rec.Body.String())
	}
}

// TestFetchEndpointPostBody 验证 POST 形态的 JSON 请求体解析与出站 POST。
func TestFetchEndpointPostBody(t *testing.T) {
	var gotMethod, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		buffer := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buffer)
		gotBody = string(buffer)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	store := fetchTestStore{settings: model.FetchSettings{Enabled: true, AllowPrivate: true, TimeoutMS: 5000}}
	router := newFetchTestRouter(&store)
	payload := `{"url":"` + upstream.URL + `","method":"POST","body":{"q":"关键词"}}`
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/fetch", strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, request)

	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("出站方法 = %q, 期望 POST", gotMethod)
	}
	// 对象形式的 body 由 fetch 包自动序列化
	if gotBody != `{"q":"关键词"}` {
		t.Fatalf("出站请求体 = %q", gotBody)
	}
	if !strings.Contains(rec.Body.String(), `"method":"POST"`) {
		t.Fatalf("结果应回显方法: %s", rec.Body.String())
	}
}

// TestFetchEndpointRejectsOutboundPostViaGet 验证 GET 形态不允许出站 POST。
//
// GET 在 HTTP 语义中是安全方法，可能被浏览器预取或爬虫自动触发；
// 若允许 ?method=POST 就会让带副作用的请求变成可被自动触发的操作。
func TestFetchEndpointRejectsOutboundPostViaGet(t *testing.T) {
	store := fetchTestStore{settings: model.FetchSettings{Enabled: true, AllowPrivate: true, TimeoutMS: 2000}}
	router := newFetchTestRouter(&store)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/fetch?url=https://example.com&method=POST", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "only allows an outbound GET") {
		t.Fatalf("响应体应说明 GET 形态的限制: %s", rec.Body.String())
	}
}

// TestValidateFetchSettings 覆盖抓取配置的保存期校验。
func TestValidateFetchSettings(t *testing.T) {
	cases := []struct {
		name     string
		settings model.FetchSettings
		wantErr  bool
	}{
		{name: "默认值合法", settings: model.FetchSettings{Enabled: true, TimeoutMS: 30000}},
		{name: "上界合法", settings: model.FetchSettings{TimeoutMS: 60000}},
		{name: "超上界拒绝", settings: model.FetchSettings{TimeoutMS: 60001}, wantErr: true},
		{name: "零值拒绝", settings: model.FetchSettings{TimeoutMS: 0}, wantErr: true},
		{name: "负值拒绝", settings: model.FetchSettings{TimeoutMS: -1}, wantErr: true},
		{name: "空代理合法", settings: model.FetchSettings{TimeoutMS: 1000, ProxyURL: "  "}},
		{name: "合法代理", settings: model.FetchSettings{TimeoutMS: 1000, ProxyURL: "http://127.0.0.1:7890"}},
		{name: "无 scheme 代理自动补 http", settings: model.FetchSettings{TimeoutMS: 1000, ProxyURL: "127.0.0.1:7890"}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			err := validateFetchSettings(item.settings)
			if item.wantErr && err == nil {
				t.Fatalf("期望报错，实际通过: %+v", item.settings)
			}
			if !item.wantErr && err != nil {
				t.Fatalf("期望通过，实际报错: %v", err)
			}
		})
	}
}

// TestUpdateFetchSettingsNormalizesBounds 验证管理台保存链路：越界值被拒（400），
// 合法值被落库并回包，且回包里的 enabled 会同步到 MCP 侧的内存标志。
func TestUpdateFetchSettingsNormalizesBounds(t *testing.T) {
	store := fetchTestStore{settings: model.FetchSettings{Enabled: true, TimeoutMS: 30000}}
	h := &Handler{store: &store}
	r := chi.NewRouter()
	r.Put("/api/admin/fetch/settings", h.updateFetchSettings)

	// 越界：必须 400 且不写入
	rec := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/api/admin/fetch/settings",
		strings.NewReader(`{"enabled":true,"timeout_ms":120000}`))
	r.ServeHTTP(rec, request)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("越界配置状态码 = %d, 期望 400, body = %s", rec.Code, rec.Body.String())
	}
	if store.saved != nil {
		t.Fatalf("越界配置不应被写入: %+v", store.saved)
	}

	// 合法：200，落库，且内存标志同步
	rec = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPut, "/api/admin/fetch/settings",
		strings.NewReader(`{"enabled":false,"proxy_url":" http://127.0.0.1:7890 ","allow_private":false,"timeout_ms":15000}`))
	r.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("合法配置状态码 = %d, body = %s", rec.Code, rec.Body.String())
	}
	if store.saved == nil || store.saved.ProxyURL != " http://127.0.0.1:7890 " {
		t.Fatalf("配置未按原值落库（裁剪由 store 负责）: %+v", store.saved)
	}
	if h.isFetchEnabled() {
		t.Fatal("保存 enabled=false 后内存标志应同步为关闭")
	}
	// 保存必须写审计：抓取配置含代理与内网放行开关，属于需要留痕的管理操作
	if len(store.audits) != 1 || store.audits[0] != "fetch.settings.update" {
		t.Fatalf("保存配置应写一条 fetch.settings.update 审计，实际: %v", store.audits)
	}
}
