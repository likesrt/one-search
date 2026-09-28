package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/one-search/one-search/backend/internal/model"
)

func TestTavilyProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tavily-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["query"] != "golang" || body["max_results"] != float64(20) || body["search_depth"] != "advanced" || body["topic"] != "news" {
			t.Fatalf("unexpected request body: %#v", body)
		}
		writeJSON(t, w, map[string]interface{}{
			"usage": map[string]interface{}{"credits": 2},
			"results": []map[string]interface{}{
				{"title": "Tavily result", "url": "https://example.com/tavily", "content": "summary", "raw_content": "full content", "score": 0.7, "published_date": "2024-01-02"},
			},
		})
	}))
	defer server.Close()

	provider := NewTavilyProvider(Config{BaseURL: server.URL})
	response, err := provider.Search(context.Background(), model.SearchRequest{
		Query:      "golang",
		Limit:      50,
		IncludeRaw: true,
		Options: map[string]interface{}{
			"search_depth":    "advanced",
			"topic":           "news",
			"include_domains": []interface{}{"example.com"},
		},
	}, model.APIKey{Value: "tavily-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Provider != model.ProviderTavily || response.Results[0].Raw == nil {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
	if len(response.Usage) != 1 || response.Usage[0].Unit != "credits" || response.Usage[0].Quantity != 2 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

func TestFirecrawlProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v2/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer firecrawl-key" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["query"] != "scraping" || body["limit"] != float64(100) || body["tbs"] != "qdr:w" {
			t.Fatalf("unexpected request body: %#v", body)
		}
		scrapeOptions, _ := body["scrapeOptions"].(map[string]interface{})
		formats, _ := scrapeOptions["formats"].([]interface{})
		if len(formats) != 1 || formats[0] != "markdown" {
			t.Fatalf("unexpected scrapeOptions: %#v", scrapeOptions)
		}
		writeJSON(t, w, map[string]interface{}{
			"success":     true,
			"creditsUsed": 3,
			"data": []map[string]interface{}{
				{"title": "Firecrawl result", "url": "https://example.com/firecrawl", "description": "desc", "markdown": "full markdown", "metadata": map[string]interface{}{"statusCode": 200}},
				{"title": "Firecrawl news", "url": "https://example.com/firecrawl-news", "snippet": "news desc", "date": "2024-02-03", "position": 2},
			},
		})
	}))
	defer server.Close()

	provider := NewFirecrawlProvider(Config{BaseURL: server.URL})
	response, err := provider.Search(context.Background(), model.SearchRequest{Query: "scraping", Limit: 150, IncludeRaw: true, IncludeContent: true, Freshness: "week"}, model.APIKey{Value: "firecrawl-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 2 || response.Results[0].Provider != model.ProviderFirecrawl || response.Results[0].Content != "full markdown" {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
	if response.Results[1].PublishedAt == nil {
		t.Fatalf("expected published date: %#v", response.Results[1])
	}
	if len(response.Usage) != 1 || response.Usage[0].Quantity != 3 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

func TestSerperProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-KEY"); got != "serper-key" {
			t.Fatalf("X-API-KEY = %q", got)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if body["q"] != "serp" || body["num"] != float64(10) || body["gl"] != "us" || body["hl"] != "en" || body["page"] != float64(2) {
			t.Fatalf("unexpected request body: %#v", body)
		}
		writeJSON(t, w, map[string]interface{}{
			"credits": 1,
			"organic": []map[string]interface{}{
				{"title": "Serper result", "link": "https://example.com/serper", "snippet": "serper desc", "date": "2024-03-04", "position": 4},
			},
		})
	}))
	defer server.Close()

	provider := NewSerperProvider(Config{BaseURL: server.URL})
	response, err := provider.Search(context.Background(), model.SearchRequest{
		Query: "serp",
		Options: map[string]interface{}{
			"gl":   "us",
			"hl":   "en",
			"page": 2,
		},
	}, model.APIKey{Value: "serper-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Provider != model.ProviderSerper || response.Results[0].Score != 0.25 {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
	if len(response.Usage) != 1 || response.Usage[0].Unit != "credits" || response.Usage[0].Quantity != 1 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

func TestBraveProviderSearch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/web/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Subscription-Token"); got != "brave-key" {
			t.Fatalf("X-Subscription-Token = %q", got)
		}
		query := r.URL.Query()
		if query.Get("q") != "privacy" || query.Get("count") != "20" || query.Get("freshness") != "pw" || query.Get("extra_snippets") != "true" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		writeJSON(t, w, map[string]interface{}{
			"web": map[string]interface{}{
				"results": []map[string]interface{}{
					{"title": "Brave result", "url": "https://example.com/brave", "description": "brave desc", "extra_snippets": []string{"more context"}, "age": "2024-04-05"},
				},
			},
		})
	}))
	defer server.Close()

	provider := NewBraveProvider(Config{BaseURL: server.URL})
	response, err := provider.Search(context.Background(), model.SearchRequest{Query: "privacy", Limit: 50, IncludeRaw: true, IncludeContent: true, Freshness: "week"}, model.APIKey{Value: "brave-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Provider != model.ProviderBrave || !strings.Contains(response.Results[0].Content, "more context") || response.Results[0].Raw == nil {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
	if len(response.Usage) != 0 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

// TestBaseURLExactEndpointSyntax 覆盖 base_url 的 `#` 完整端点语法：
// `#` 开头表示该地址即完整端点，适配器不再拼接自己的路径（含自带 query 的 GET）。
// 这里不用 t.Fatalf 在 handler 里断言：handler 跑在服务端 goroutine，Fatal 只会中断那条 goroutine，
// 记录实际收到的地址再在测试里比对才能拿到确定的失败信息。
func TestBaseURLExactEndpointSyntax(t *testing.T) {
	t.Run("exact 端点不再拼接适配器路径", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			writeJSON(t, w, map[string]interface{}{
				"results": []map[string]interface{}{{"title": "exact", "url": "https://example.com/exact", "content": "c"}},
			})
		}))
		defer server.Close()

		provider := NewTavilyProvider(Config{BaseURL: "#" + server.URL + "/custom/tavily/search"})
		if _, err := provider.Search(context.Background(), model.SearchRequest{Query: "golang"}, model.APIKey{Value: "tavily-key"}); err != nil {
			t.Fatalf("Search returned error: %v", err)
		}
		if gotPath != "/custom/tavily/search" {
			t.Fatalf("request path = %q, want %q (exact 端点不应再拼接 /search)", gotPath, "/custom/tavily/search")
		}
	})

	t.Run("普通地址仍拼接适配器路径", func(t *testing.T) {
		var gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			writeJSON(t, w, map[string]interface{}{"results": []map[string]interface{}{}})
		}))
		defer server.Close()

		provider := NewTavilyProvider(Config{BaseURL: server.URL + "/tavily"})
		if _, err := provider.Search(context.Background(), model.SearchRequest{Query: "golang"}, model.APIKey{Value: "tavily-key"}); err != nil {
			t.Fatalf("Search returned error: %v", err)
		}
		if gotPath != "/tavily/search" {
			t.Fatalf("request path = %q, want %q", gotPath, "/tavily/search")
		}
	})

	t.Run("exact 端点自带 query 时用 & 连接", func(t *testing.T) {
		var gotQuery url.Values
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotQuery = r.URL.Query()
			writeJSON(t, w, map[string]interface{}{"web": map[string]interface{}{"results": []map[string]interface{}{}}})
		}))
		defer server.Close()

		// 自带 query 的完整端点若仍用 "?" 拼接参数，会生成两个 ? 的非法 URL。
		provider := NewBraveProvider(Config{BaseURL: "#" + server.URL + "/web/search?token=abc"})
		if _, err := provider.Search(context.Background(), model.SearchRequest{Query: "privacy", Limit: 5}, model.APIKey{Value: "brave-key"}); err != nil {
			t.Fatalf("Search returned error: %v", err)
		}
		if gotQuery.Get("token") != "abc" || gotQuery.Get("q") != "privacy" || gotQuery.Get("count") != "5" {
			t.Fatalf("unexpected query: %s", gotQuery.Encode())
		}
	})
}

// TestKeenableProviderSearch 覆盖 Keenable 适配器的核心行为：
// 路径固定 /v1/search、鉴权头 X-API-Key、max_results 与 snippet_max_length 的上界裁剪、
// options 透传、snippet/description 回退，以及上游不返回 usage 时不报错（Usage 为空切片）。
func TestKeenableProviderSearch(t *testing.T) {
	var gotBody map[string]interface{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "keenable-key" {
			t.Errorf("X-API-Key = %q", got)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		writeJSON(t, w, map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": "Keenable result", "url": "https://example.com/keenable", "snippet": "snippet text", "description": "", "published_at": "2024-05-06"},
				{"title": "Keenable fallback", "url": "https://example.com/keenable-2", "snippet": "", "description": "description only"},
				{"title": "Keenable without url", "snippet": "dropped"},
			},
		})
	}))
	defer server.Close()

	provider := NewKeenableProvider(Config{BaseURL: server.URL})
	response, err := provider.Search(context.Background(), model.SearchRequest{
		Query:      "gateway",
		Limit:      200,
		IncludeRaw: true,
		// IncludeContent 必须显式打开：正文默认不返回（见 model.SearchRequest 的字段注释），
		// 而下面要断言 content 与 snippet 同源，不开就只能拿到空 Content。
		IncludeContent: true,
		Options: map[string]interface{}{
			"mode":               "realtime",
			"site":               "example.com",
			"snippet_max_length": 20000,
			"published_after":    "2024-01-01",
			"acquired_before":    "2024-12-31",
		},
	}, model.APIKey{Value: "keenable-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotBody["query"] != "gateway" || gotBody["max_results"] != float64(50) || gotBody["mode"] != "realtime" ||
		gotBody["site"] != "example.com" || gotBody["snippet_max_length"] != float64(10000) ||
		gotBody["published_after"] != "2024-01-01" || gotBody["acquired_before"] != "2024-12-31" {
		t.Fatalf("unexpected request body: %#v", gotBody)
	}
	// 缺 url 的条目被丢弃，因此只剩两条。
	if len(response.Results) != 2 {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
	first := response.Results[0]
	if first.Provider != model.ProviderKeenable || first.Snippet != "snippet text" || first.Content != "snippet text" || first.Score != 1 || first.Raw == nil {
		t.Fatalf("unexpected first result: %#v", first)
	}
	if first.PublishedAt == nil {
		t.Fatalf("expected published date: %#v", first)
	}
	// description 兜底：snippet 为空串时取 description。
	if second := response.Results[1]; second.Snippet != "description only" || second.Score != 0.5 {
		t.Fatalf("unexpected second result: %#v", second)
	}
	// 上游响应没有 usage 字段：应为空切片而不是报错。
	if len(response.Usage) != 0 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

// TestKeenableProviderRequestBodyDefaults 覆盖 Keenable 请求体的默认值与边界：
// max_results 夹紧到 1..50（<=0 时用 10）、mode 缺省 pro、snippet_max_length 夹紧到 180..10000
// （<=0 时不发送该键）。
func TestKeenableProviderRequestBodyDefaults(t *testing.T) {
	cases := []struct {
		name          string
		limit         int
		options       map[string]interface{}
		wantMaxResult float64
		wantMode      string
		wantSnippet   float64
	}{
		{name: "limit 为 0 时用默认 10", limit: 0, wantMaxResult: 10, wantMode: "pro"},
		{name: "limit 超界裁剪到 50", limit: 51, wantMaxResult: 50, wantMode: "pro"},
		{name: "limit 为 1 时保留", limit: 1, wantMaxResult: 1, wantMode: "pro"},
		{name: "非法 mode 回退 pro", limit: 5, options: map[string]interface{}{"mode": "fast"}, wantMaxResult: 5, wantMode: "pro"},
		{name: "snippet_max_length 下界抬到 180", limit: 5, options: map[string]interface{}{"snippet_max_length": 10}, wantMaxResult: 5, wantMode: "pro", wantSnippet: 180},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				writeJSON(t, w, map[string]interface{}{"results": []map[string]interface{}{}})
			}))
			defer server.Close()

			provider := NewKeenableProvider(Config{BaseURL: server.URL})
			if _, err := provider.Search(context.Background(), model.SearchRequest{Query: "q", Limit: tc.limit, Options: tc.options}, model.APIKey{Value: "keenable-key"}); err != nil {
				t.Fatalf("Search returned error: %v", err)
			}
			if gotBody["max_results"] != tc.wantMaxResult || gotBody["mode"] != tc.wantMode {
				t.Fatalf("unexpected request body: %#v", gotBody)
			}
			if tc.wantSnippet == 0 {
				if _, ok := gotBody["snippet_max_length"]; ok {
					t.Fatalf("snippet_max_length 不应出现: %#v", gotBody)
				}
				return
			}
			if gotBody["snippet_max_length"] != tc.wantSnippet {
				t.Fatalf("snippet_max_length = %#v, want %v", gotBody["snippet_max_length"], tc.wantSnippet)
			}
		})
	}
}

// TestKeenableProviderAnonymousEndpoint 覆盖「空 key 即匿名」的两条路径差异：
// 空 key 必须改打 /v1/search/public 并带 X-Keenable-Title 应用标识，且**不发 X-API-Key**
// （实测空 X-API-Key 头与不带鉴权同样被上游判 401，发空头等于自废匿名）；
// 非空 key 走 /v1/search + X-API-Key，且不带 X-Keenable-Title。
// 两条路径的请求体必须逐字段一致：上游 public 端点接受同一份 body，差异只在端点与鉴权头。
// 纯空白 key 与空串同组断言，用于确认判据与 security.MaskSecret 的空值口径同源（否则会被显示成匿名却走 keyed 路径）。
func TestKeenableProviderAnonymousEndpoint(t *testing.T) {
	cases := []struct {
		name            string
		key             model.APIKey
		wantPath        string
		wantAPIKey      string
		wantTitleHeader string
	}{
		{name: "空 key 走 public 端点并带应用标识", key: model.APIKey{}, wantPath: "/v1/search/public", wantTitleHeader: "OneSearchRelay"},
		{name: "纯空白 key 视为匿名", key: model.APIKey{Value: "   "}, wantPath: "/v1/search/public", wantTitleHeader: "OneSearchRelay"},
		{name: "非空 key 走 keyed 端点并带 X-API-Key", key: model.APIKey{Value: "keenable-key"}, wantPath: "/v1/search", wantAPIKey: "keenable-key"},
	}
	// options 与 limit 刻意取得复杂一些，用来确认两条路径的 body 组装走的是同一段代码。
	request := model.SearchRequest{
		Query: "gateway",
		Limit: 20,
		Options: map[string]interface{}{
			"mode":               "realtime",
			"site":               "example.com",
			"snippet_max_length": 500,
			"published_after":    "2024-01-01",
		},
	}
	bodies := map[string]map[string]interface{}{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			var gotAPIKey, gotTitle string
			var gotBody map[string]interface{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAPIKey = r.Header.Get("X-API-Key")
				gotTitle = r.Header.Get("X-Keenable-Title")
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				writeJSON(t, w, map[string]interface{}{"results": []map[string]interface{}{}})
			}))
			defer server.Close()

			provider := NewKeenableProvider(Config{BaseURL: server.URL})
			if _, err := provider.Search(context.Background(), request, tc.key); err != nil {
				t.Fatalf("Search returned error: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotAPIKey != tc.wantAPIKey {
				t.Fatalf("X-API-Key = %q, want %q", gotAPIKey, tc.wantAPIKey)
			}
			if gotTitle != tc.wantTitleHeader {
				t.Fatalf("X-Keenable-Title = %q, want %q", gotTitle, tc.wantTitleHeader)
			}
			// 同一路径可能被多个用例覆盖（空串与纯空白），后写的值相同，这里保留最后一次即可。
			bodies[tc.wantPath] = gotBody
		})
	}
	// 两条路径的请求体必须一致：上游对 public 端点接受同一份 body，任何分叉都会让匿名与带 key
	// 的调用结果口径不同，也会让「切到匿名只需留空 key」的承诺不成立。
	keyed, anonymous := bodies["/v1/search"], bodies["/v1/search/public"]
	if len(keyed) == 0 || len(anonymous) == 0 {
		t.Fatalf("请求体未采集完整: keyed=%#v anonymous=%#v", keyed, anonymous)
	}
	if !reflect.DeepEqual(keyed, anonymous) {
		t.Fatalf("两条路径请求体不一致: keyed=%#v anonymous=%#v", keyed, anonymous)
	}
}

// TestProviderSupportsAnonymousKey 覆盖全部九家渠道的 SupportsAnonymousKey 取值：
// 只有实测确认无密钥可用的 context7 与 keenable 返回 true，其余七家走 HTTPProvider 默认实现返回 false。
// 该标志仅用于管理台提示，不参与放行判断，因此这里只断言「渠道级常量」的取值，
// 并顺带确认未在 Registry 中注册的渠道名不会 panic。
func TestProviderSupportsAnonymousKey(t *testing.T) {
	cases := []struct {
		name     string
		provider Provider
		want     bool
	}{
		{name: model.ProviderContext7, provider: NewContext7Provider(Config{}), want: true},
		{name: model.ProviderKeenable, provider: NewKeenableProvider(Config{}), want: true},
		{name: model.ProviderExa, provider: NewExaProvider(Config{})},
		{name: model.ProviderYou, provider: NewYouProvider(Config{})},
		{name: model.ProviderJina, provider: NewJinaProvider(Config{})},
		{name: model.ProviderTavily, provider: NewTavilyProvider(Config{})},
		{name: model.ProviderFirecrawl, provider: NewFirecrawlProvider(Config{})},
		{name: model.ProviderSerper, provider: NewSerperProvider(Config{})},
		{name: model.ProviderBrave, provider: NewBraveProvider(Config{})},
	}
	for _, tc := range cases {
		if got := tc.provider.SupportsAnonymousKey(); got != tc.want {
			t.Fatalf("%s.SupportsAnonymousKey() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// writeJSON 把 payload 以 JSON 写入响应（状态码 200），供假上游使用。
// 参数 t 仅用于编码失败时终止测试；副作用：设置 Content-Type 并写入响应体。
func writeJSON(t *testing.T, w http.ResponseWriter, payload interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// writeJSONStatus 与 writeJSON 相同，但显式指定 HTTP 状态码（假上游模拟 404/500 用）。
// 注意必须先 WriteHeader 再写 body：反过来的话状态码会被隐式固定成 200。
func writeJSONStatus(t *testing.T, w http.ResponseWriter, status int, payload interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// context7CodeSnippet 是假上游 codeSnippets 数组里的一条片段的构造辅助。
// 参数 codeID 为片段地址（含 #fragment，用于验证聚合），language 为片段级语言，
// codeList 为该片段携带的代码段；返回值可直接放进响应 JSON。
func context7CodeSnippet(codeID, language string, codeList []map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"codeTitle":       "Add middleware to chi router",
		"codeDescription": "Shows how to add common middleware.",
		"codeLanguage":    language,
		"codeId":          codeID,
		"pageTitle":       "Unknown",
		"codeList":        codeList,
	}
}

// TestContext7ProviderSearch 覆盖 Context7 正常命中路径：端点 /v3/search、type=json、
// 同一文件的多段片段聚合为一条（去重键会剥 fragment，不聚合会静默丢片段）、
// codeTitle/pageTitle 回退、codeLanguage 回退、缺 URL 条目丢弃、Score 按序号递减。
// 断言拆到 context7AssertAggregated / context7AssertFallbacks 两个辅助函数里，避免单个函数过长。
func TestContext7ProviderSearch(t *testing.T) {
	var gotPath string
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.Query()
		writeJSON(t, w, context7Payload())
	}))
	defer server.Close()

	provider := NewContext7Provider(Config{BaseURL: server.URL})
	response, err := provider.Search(context.Background(), model.SearchRequest{Query: "chi middleware", IncludeRaw: true, IncludeContent: true}, model.APIKey{Value: "ctx7-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if gotPath != "/v3/search" || gotQuery.Get("query") != "chi middleware" || gotQuery.Get("type") != "json" {
		t.Fatalf("unexpected request: path=%q query=%s", gotPath, gotQuery.Encode())
	}
	// codeSnippets 里 4 条片段按文件聚合成 2 条（其中 1 条缺 URL 被丢弃），再加 1 条 infoSnippet。
	if len(response.Results) != 3 {
		t.Fatalf("unexpected results: %#v", response.Results)
	}
	context7AssertAggregated(t, response.Results[0])
	context7AssertFallbacks(t, response.Results[1], response.Results[2])
	// Score 按序号递减：上游不给 score，用 1/(序号+1) 兜底。
	if response.Results[0].Score != 1 || response.Results[1].Score != 0.5 || response.Results[2].Score != 1.0/3.0 {
		t.Fatalf("unexpected scores: %v %v %v", response.Results[0].Score, response.Results[1].Score, response.Results[2].Score)
	}
}

// context7Payload 构造假上游的 type=json 响应，含 4 个片段与 1 条 infoSnippet。
// 覆盖场景：同文件多片段（#_snippet_0/1）、codeTitle 为空回退 pageTitle、
// 代码段未写 language 回退 codeLanguage、以及缺 codeId 的条目。
func context7Payload() map[string]interface{} {
	return map[string]interface{}{
		"codeSnippets": []map[string]interface{}{
			context7CodeSnippet("https://github.com/go-chi/docs/blob/master/quickstart.md#_snippet_0", "go",
				[]map[string]interface{}{{"language": "go", "code": "router.Use(middleware.Logger)"}}),
			// 与上一条剥掉 fragment 后同址：必须并入同一条结果，而不能另起一条。
			context7CodeSnippet("https://github.com/go-chi/docs/blob/master/quickstart.md#_snippet_1", "go",
				[]map[string]interface{}{{"language": "go", "code": "router.Use(middleware.Recoverer)"}}),
			{
				"codeTitle":    "",
				"pageTitle":    "Routing guide",
				"codeLanguage": "go",
				"codeId":       "https://github.com/go-chi/docs/blob/master/routing.md#_snippet_0",
				"codeList":     []map[string]interface{}{{"code": "r.Get(\"/\", handler)"}},
			},
			// 缺 URL 的条目直接丢弃（与其余适配器一致）。
			{"codeTitle": "missing url", "codeList": []map[string]interface{}{{"language": "go", "code": "drop"}}},
		},
		"infoSnippets": []map[string]interface{}{
			{"pageId": "https://github.com/go-chi/docs/blob/master/guide.md#intro", "breadcrumb": "go-chi > docs > Guide", "content": "Guide content"},
		},
	}
}

// context7AssertAggregated 断言聚合结果：URL 已剥 fragment、多段代码都留在 Content 里、
// 围栏语言正确、Provider/Providers/Raw 字段齐备。
func context7AssertAggregated(t *testing.T, first model.SearchResult) {
	t.Helper()
	if first.URL != "https://github.com/go-chi/docs/blob/master/quickstart.md" {
		t.Fatalf("URL 未剥掉 fragment: %q", first.URL)
	}
	if first.Title != "Add middleware to chi router" || first.Snippet != "Shows how to add common middleware." {
		t.Fatalf("unexpected first result: %#v", first)
	}
	// 两段代码都必须留在 Content 里，且带 ```go 围栏（这是聚合生效的核心证据）。
	if !strings.Contains(first.Content, "router.Use(middleware.Logger)") || !strings.Contains(first.Content, "router.Use(middleware.Recoverer)") {
		t.Fatalf("同一文件的多段代码未聚合: %q", first.Content)
	}
	if !strings.Contains(first.Content, "```go") {
		t.Fatalf("Content 缺少围栏代码块: %q", first.Content)
	}
	if first.Provider != model.ProviderContext7 || len(first.Providers) != 1 || first.Providers[0] != model.ProviderContext7 || first.Raw == nil {
		t.Fatalf("unexpected first result fields: %#v", first)
	}
}

// context7AssertFallbacks 断言回退路径：codeTitle 为空取 pageTitle、
// 代码段无 language 时取片段级 codeLanguage、infoSnippet 的 breadcrumb/content 映射。
// 参数 second 为 codeSnippet 聚合结果，third 为 infoSnippet 聚合结果。
func context7AssertFallbacks(t *testing.T, second, third model.SearchResult) {
	t.Helper()
	if second.URL != "https://github.com/go-chi/docs/blob/master/routing.md" || second.Title != "Routing guide" {
		t.Fatalf("pageTitle 回退失效: %#v", second)
	}
	if !strings.Contains(second.Content, "```go\nr.Get") {
		t.Fatalf("codeLanguage 回退失效: %q", second.Content)
	}
	if third.URL != "https://github.com/go-chi/docs/blob/master/guide.md" || third.Title != "go-chi > docs > Guide" || third.Snippet != "Guide content" {
		t.Fatalf("infoSnippet 映射失效: %#v", third)
	}
}

// TestContext7ProviderSearchNoDocumentation 覆盖 404 + error=no_documentation_found：
// 这是「该问题没有对应库文档」的正常反馈，必须返回空结果且 err == nil，
// 否则 fallback 模式会白记一次失败、parallel 模式会把渠道状态染红。
func TestContext7ProviderSearchNoDocumentation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSONStatus(t, w, http.StatusNotFound, map[string]interface{}{
			"error":   "no_documentation_found",
			"message": "No documentation library matched the request, try rephrasing the query.",
		})
	}))
	defer server.Close()

	provider := NewContext7Provider(Config{BaseURL: server.URL})
	response, err := provider.Search(context.Background(), model.SearchRequest{Query: "unknown lib"}, model.APIKey{Value: "ctx7-key"})
	if err != nil {
		t.Fatalf("库未命中不应报错: %v", err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("库未命中应返回空结果: %#v", response.Results)
	}
	if len(response.Usage) != 0 {
		t.Fatalf("unexpected usage: %#v", response.Usage)
	}
}

// TestContext7ProviderSearchErrors 覆盖真故障路径：500 与「错误码不同」的 404
// 都必须照常报错，不能被 no_documentation_found 的特判吞掉。
func TestContext7ProviderSearchErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		payload map[string]interface{}
	}{
		{name: "500 上游故障", status: http.StatusInternalServerError, payload: map[string]interface{}{"error": "internal_error"}},
		{name: "非法 key 的 401", status: http.StatusUnauthorized, payload: map[string]interface{}{"error": "invalid_api_key"}},
		{name: "不认识的 404 错误码", status: http.StatusNotFound, payload: map[string]interface{}{"error": "route_not_found"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writeJSONStatus(t, w, tc.status, tc.payload)
			}))
			defer server.Close()

			provider := NewContext7Provider(Config{BaseURL: server.URL})
			response, err := provider.Search(context.Background(), model.SearchRequest{Query: "q"}, model.APIKey{Value: "ctx7-key"})
			if err == nil {
				t.Fatalf("状态码 %d 应返回错误，实际 results=%#v", tc.status, response.Results)
			}
			if len(response.Results) != 0 {
				t.Fatalf("失败时不应返回结果: %#v", response.Results)
			}
		})
	}
}

// TestContext7ProviderAuthorization 覆盖鉴权头：key.Value 为空时不发送 Authorization
// （Context7 的 key 是可选的，发空 Bearer 会被上游判成无效 key），非空时发送 Bearer。
func TestContext7ProviderAuthorization(t *testing.T) {
	cases := []struct {
		name     string
		keyValue string
		want     []string
	}{
		{name: "无 key 时不发头", keyValue: "", want: nil},
		{name: "有 key 时发 Bearer", keyValue: "ctx7sk-abc", want: []string{"Bearer ctx7sk-abc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotHeaders []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotHeaders = r.Header.Values("Authorization")
				writeJSON(t, w, map[string]interface{}{"codeSnippets": []map[string]interface{}{}})
			}))
			defer server.Close()

			provider := NewContext7Provider(Config{BaseURL: server.URL})
			if _, err := provider.Search(context.Background(), model.SearchRequest{Query: "q"}, model.APIKey{Value: tc.keyValue}); err != nil {
				t.Fatalf("Search returned error: %v", err)
			}
			if len(gotHeaders) != len(tc.want) {
				t.Fatalf("Authorization = %#v, want %#v", gotHeaders, tc.want)
			}
			for index, want := range tc.want {
				if gotHeaders[index] != want {
					t.Fatalf("Authorization[%d] = %q, want %q", index, gotHeaders[index], want)
				}
			}
		})
	}
}

// TestContext7ProviderLibraryOptions 覆盖 options 透传：library 支持 []interface{} 多值
// 并编码成重复的查询参数（上游最多接受 4 个），version / language 单值透传，
// 单个字符串形式的 library 也要兼容（模型常只传一个库名）。
func TestContext7ProviderLibraryOptions(t *testing.T) {
	cases := []struct {
		name        string
		options     map[string]interface{}
		wantLibrary []string
		wantVersion string
		wantLang    string
	}{
		{
			name:        "数组多值透传",
			options:     map[string]interface{}{"library": []interface{}{"next.js", "/vercel/next.js"}, "version": "15.0.0", "language": "typescript"},
			wantLibrary: []string{"next.js", "/vercel/next.js"},
			wantVersion: "15.0.0",
			wantLang:    "typescript",
		},
		{
			name:        "单个字符串也支持",
			options:     map[string]interface{}{"library": "next.js"},
			wantLibrary: []string{"next.js"},
		},
		{
			name:    "未配置时不发送这些键",
			options: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotQuery := context7QueryForOptions(t, tc.options)
			context7AssertStringList(t, "library", gotQuery["library"], tc.wantLibrary)
			if gotQuery.Get("version") != tc.wantVersion || gotQuery.Get("language") != tc.wantLang {
				t.Fatalf("unexpected version/language: %s", gotQuery.Encode())
			}
		})
	}
}

// context7QueryForOptions 起一个假上游，用给定 options 发一次请求并返回上游实际收到的查询参数。
// 参数 options 为待透传的请求选项；返回值是服务端侧的 url.Values（含重复键的原始顺序）。
func context7QueryForOptions(t *testing.T, options map[string]interface{}) url.Values {
	t.Helper()
	var gotQuery url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		writeJSON(t, w, map[string]interface{}{"codeSnippets": []map[string]interface{}{}})
	}))
	defer server.Close()

	provider := NewContext7Provider(Config{BaseURL: server.URL})
	if _, err := provider.Search(context.Background(), model.SearchRequest{Query: "q", Options: options}, model.APIKey{Value: "ctx7-key"}); err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	return gotQuery
}

// context7AssertStringList 断言同名查询参数的数量与顺序（用于验证 library 重复键编码）。
// 参数 key 为参数名，got / want 为实际与期望的值序列；两者长度或逐位取值不一致即失败。
func context7AssertStringList(t *testing.T, key string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %#v, want %#v", key, got, want)
	}
	for index, value := range want {
		if got[index] != value {
			t.Fatalf("%s[%d] = %q, want %q", key, index, got[index], value)
		}
	}
}

// TestContext7ProviderLimitTruncation 覆盖本地 limit 截断：上游不接受 limit 参数，
// 因此截断只在适配器内发生，且 limit <= 0 时不截断（截断单位是聚合后的「文档页」）。
func TestContext7ProviderLimitTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["limit"]; ok {
			t.Errorf("不应向上游发送 limit: %s", r.URL.RawQuery)
		}
		writeJSON(t, w, map[string]interface{}{
			"codeSnippets": []map[string]interface{}{
				context7CodeSnippet("https://example.com/a.md#_snippet_0", "go", []map[string]interface{}{{"language": "go", "code": "a"}}),
				context7CodeSnippet("https://example.com/b.md#_snippet_0", "go", []map[string]interface{}{{"language": "go", "code": "b"}}),
				context7CodeSnippet("https://example.com/c.md#_snippet_0", "go", []map[string]interface{}{{"language": "go", "code": "c"}}),
			},
		})
	}))
	defer server.Close()

	provider := NewContext7Provider(Config{BaseURL: server.URL})
	trimmed, err := provider.Search(context.Background(), model.SearchRequest{Query: "q", Limit: 2}, model.APIKey{Value: "ctx7-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(trimmed.Results) != 2 {
		t.Fatalf("limit=2 应截断到 2 条: %#v", trimmed.Results)
	}
	full, err := provider.Search(context.Background(), model.SearchRequest{Query: "q"}, model.APIKey{Value: "ctx7-key"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(full.Results) != 3 {
		t.Fatalf("limit<=0 不应截断: %#v", full.Results)
	}
}
