package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	response, err := provider.Search(context.Background(), model.SearchRequest{Query: "scraping", Limit: 150, IncludeRaw: true, Freshness: "week"}, model.APIKey{Value: "firecrawl-key"})
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
	response, err := provider.Search(context.Background(), model.SearchRequest{Query: "privacy", Limit: 50, IncludeRaw: true, Freshness: "week"}, model.APIKey{Value: "brave-key"})
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

func writeJSON(t *testing.T, w http.ResponseWriter, payload interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
