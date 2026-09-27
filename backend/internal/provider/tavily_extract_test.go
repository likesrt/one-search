package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
)

// TestTavilyProviderExtract 覆盖 /extract 的成功路径：
// 请求体格式、Bearer 鉴权、正文取自 results[].raw_content、credits 取自 usage.credits。
func TestTavilyProviderExtract(t *testing.T) {
	longContent := strings.Repeat("整页 Markdown 正文。", 500)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/extract" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tvly-test" {
			t.Fatalf("Authorization = %q", got)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		urls, ok := body["urls"].([]interface{})
		if !ok || len(urls) != 1 || urls[0] != "https://example.com/page" {
			t.Fatalf("unexpected urls: %#v", body["urls"])
		}
		// 这三个字段是接口契约的一部分：basic 控制成本、markdown 决定返回形态、
		// include_usage 决定 credits 能否记账
		if body["extract_depth"] != "basic" || body["format"] != "markdown" || body["include_usage"] != true {
			t.Fatalf("unexpected request body: %#v", body)
		}
		writeJSON(t, w, map[string]interface{}{
			"results": []map[string]interface{}{
				{"url": "https://example.com/page", "raw_content": longContent},
			},
			"failed_results": []map[string]interface{}{},
			"usage":          map[string]interface{}{"credits": 1},
		})
	}))
	defer server.Close()

	provider := NewTavilyProvider(Config{BaseURL: server.URL})
	content, credits, err := provider.Extract(context.Background(), "https://example.com/page", model.APIKey{Value: "tvly-test"})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if content != longContent {
		t.Fatalf("正文不一致：长度 %d，期望 %d", len([]rune(content)), len([]rune(longContent)))
	}
	if credits != 1 {
		t.Fatalf("credits = %v, 期望 1", credits)
	}
}

// TestTavilyProviderExtractDoesNotTruncate 验证超长正文不被适配器截断。
//
// 实测 Tavily 会返回 Wikipedia 577743 字符、Gutenberg 全书 1221259 字符的整页内容，
// 若在这里截断，抓取侧的续读就永远拿不到后半段（截断点必须留给 max_length）。
func TestTavilyProviderExtractDoesNotTruncate(t *testing.T) {
	huge := strings.Repeat("x", 200000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"results": []map[string]interface{}{{"url": "https://example.com", "raw_content": huge}},
		})
	}))
	defer server.Close()

	content, _, err := NewTavilyProvider(Config{BaseURL: server.URL}).Extract(
		context.Background(), "https://example.com", model.APIKey{Value: "k"})
	if err != nil {
		t.Fatalf("Extract returned error: %v", err)
	}
	if len(content) != len(huge) {
		t.Fatalf("正文被截断：长度 %d，期望 %d", len(content), len(huge))
	}
}

// TestTavilyProviderExtractHTTP200WithFailedResults 覆盖「HTTP 200 但只带 failed_results」。
//
// 这是本接口最容易踩的坑：只看状态码会把「提取失败」当成成功并返回空正文，
// 上层随后把空内容当作抓取结果返回给模型。
func TestTavilyProviderExtractHTTP200WithFailedResults(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"results":        []map[string]interface{}{},
			"failed_results": []map[string]interface{}{{"url": "https://example.com", "error": "Access denied by the target site"}},
			"usage":          map[string]interface{}{"credits": 0},
		})
	}))
	defer server.Close()

	_, _, err := NewTavilyProvider(Config{BaseURL: server.URL}).Extract(
		context.Background(), "https://example.com", model.APIKey{Value: "k"})
	if err == nil {
		t.Fatal("只有 failed_results 时必须报错")
	}
	if !strings.Contains(err.Error(), "Access denied by the target site") {
		t.Fatalf("错误应带上 failed_results[].error: %v", err)
	}
}

// TestTavilyProviderExtractEmptyContent 覆盖 results 存在但正文为空的情况。
//
// 实测知乎与 reddit 会返回空 raw_content（登录墙），此时返回空串会让上层误以为回退成功。
func TestTavilyProviderExtractEmptyContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]interface{}{
			"results": []map[string]interface{}{{"url": "https://example.com", "raw_content": ""}},
		})
	}))
	defer server.Close()

	_, _, err := NewTavilyProvider(Config{BaseURL: server.URL}).Extract(
		context.Background(), "https://example.com", model.APIKey{Value: "k"})
	if err == nil {
		t.Fatal("正文为空时必须报错")
	}
}

// TestTavilyProviderExtractAuthError 覆盖 401：应被归类为 auth 错误。
func TestTavilyProviderExtractAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSONStatus(t, w, http.StatusUnauthorized, map[string]interface{}{"detail": "invalid api key"})
	}))
	defer server.Close()

	_, _, err := NewTavilyProvider(Config{BaseURL: server.URL}).Extract(
		context.Background(), "https://example.com", model.APIKey{Value: "bad"})
	if err == nil {
		t.Fatal("401 必须报错")
	}
	if got := ErrorType(err); got != ErrorTypeAuth {
		t.Fatalf("错误类型 = %q, 期望 %q", got, ErrorTypeAuth)
	}
}

// TestTavilyProviderExtractTimeout 覆盖超时：必须返回错误而不是空正文，
// 否则抓取侧会把一次超时当成「回退成功但内容为空」。
func TestTavilyProviderExtractTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer server.Close()

	provider := NewTavilyProvider(Config{BaseURL: server.URL, Timeout: 50 * time.Millisecond})
	_, _, err := provider.Extract(context.Background(), "https://example.com", model.APIKey{Value: "k"})
	if err == nil {
		t.Fatal("超时必须报错")
	}
}

// TestTavilyExtractContentPrefersRawContent 验证正文取值优先级与回退分支。
func TestTavilyExtractContentPrefersRawContent(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]interface{}
		want    string
		wantErr bool
	}{
		{
			name: "优先 raw_content",
			payload: map[string]interface{}{
				"results": []interface{}{map[string]interface{}{"raw_content": "完整正文", "content": "摘要"}},
			},
			want: "完整正文",
		},
		{
			name: "缺少 raw_content 时回落到 content",
			payload: map[string]interface{}{
				"results": []interface{}{map[string]interface{}{"content": "备用正文"}},
			},
			want: "备用正文",
		},
		{
			name:    "两个数组都为空时报错",
			payload: map[string]interface{}{"results": []interface{}{}, "failed_results": []interface{}{}},
			wantErr: true,
		},
		{
			name:    "仅 failed_results 时报错",
			payload: map[string]interface{}{"failed_results": []interface{}{map[string]interface{}{"error": "boom"}}},
			wantErr: true,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			got, err := tavilyExtractContent(item.payload)
			if item.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际得到 %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外报错: %v", err)
			}
			if got != item.want {
				t.Fatalf("正文 = %q, 期望 %q", got, item.want)
			}
		})
	}
}
