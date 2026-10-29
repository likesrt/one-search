package compat

import (
	"strings"
	"testing"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
)

// TestDeepSeekToNative 覆盖官方 Search 请求的字段映射：
// count 缺省值、可选的 ori_question / localization、need_content 与扩展字段。
func TestDeepSeekToNative(t *testing.T) {
	count := 5
	question := "DeepSeek V4 是什么"

	native, err := DeepSeekToNative(DeepSeekSearchRequest{
		Query:        "DeepSeek V4",
		Count:        &count,
		OriQuestion:  &question,
		Localization: "203.0.113.9",
		NeedContent:  true,
		Providers:    []string{"deepseek"},
		Mode:         "single",
		Cache:        "bypass",
	})
	if err != nil {
		t.Fatalf("合法请求不应报错: %v", err)
	}
	if native.Query != "DeepSeek V4" || native.Limit != 5 {
		t.Fatalf("基础字段映射错误: %+v", native)
	}
	if !native.IncludeContent {
		t.Fatal("need_content 应映射为 IncludeContent")
	}
	if native.ContentLimit != provider.MaxContentLimit {
		t.Fatalf("need_content=true 时正文上限应为硬顶 %d，实际 %d", provider.MaxContentLimit, native.ContentLimit)
	}
	if native.CompatFormat != model.CompatFormatDeepSeek {
		t.Fatalf("CompatFormat = %q", native.CompatFormat)
	}
	if native.Mode != model.SearchModeSingle || native.Cache != model.CachePolicyBypass {
		t.Fatalf("扩展字段未透传: mode=%q cache=%q", native.Mode, native.Cache)
	}
	if native.Options["ori_question"] != question || native.Options["localization"] != "203.0.113.9" {
		t.Fatalf("options 映射错误: %+v", native.Options)
	}

	// 未传 count 时按官方默认 10；null 的 ori_question 视为未提供。
	native, err = DeepSeekToNative(DeepSeekSearchRequest{Query: "x", OriQuestion: nil})
	if err != nil {
		t.Fatalf("最小请求不应报错: %v", err)
	}
	if native.Limit != deepSeekDefaultCount {
		t.Fatalf("缺省 count 应映射为 %d，实际 %d", deepSeekDefaultCount, native.Limit)
	}
	if native.ContentLimit != 0 {
		t.Fatalf("need_content=false 时不应设置正文上限，实际 %d", native.ContentLimit)
	}
	if _, ok := native.Options["ori_question"]; ok {
		t.Fatalf("null ori_question 不应写入 options")
	}
}

// TestDeepSeekToNativeValidation 覆盖官方参数约束：空 query、超长 query、
// count 越界、ori_question 超长都必须返回错误（由调用方映射为 400）。
func TestDeepSeekToNativeValidation(t *testing.T) {
	zero, tooLarge := 0, 51
	longQuery := strings.Repeat("中", 129)
	longQuestion := strings.Repeat("问", 129)
	cases := []struct {
		name string
		req  DeepSeekSearchRequest
	}{
		{"空 query", DeepSeekSearchRequest{Query: ""}},
		{"全空白 query", DeepSeekSearchRequest{Query: "   "}},
		{"query 超长", DeepSeekSearchRequest{Query: longQuery}},
		{"count 为 0", DeepSeekSearchRequest{Query: "x", Count: &zero}},
		{"count 超上限", DeepSeekSearchRequest{Query: "x", Count: &tooLarge}},
		{"ori_question 超长", DeepSeekSearchRequest{Query: "x", OriQuestion: &longQuestion}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeepSeekToNative(tc.req); err == nil {
				t.Fatalf("期望报错，实际通过: %+v", tc.req)
			}
		})
	}

	// 128 个码点恰好合法（按 Unicode 字符而非字节计）。
	limit := strings.Repeat("中", 128)
	if _, err := DeepSeekToNative(DeepSeekSearchRequest{Query: limit}); err != nil {
		t.Fatalf("128 个字符应合法: %v", err)
	}
}

// TestDeepSeekFromNative 覆盖响应映射：分数还原到 0..3、正文开关、
// summary 回退、date 的 UTC 格式、null 字段与空数组。
func TestDeepSeekFromNative(t *testing.T) {
	published := time.Date(2026, 6, 1, 8, 30, 0, 0, time.FixedZone("CST", 8*3600))
	response := model.SearchResponse{Results: []model.SearchResult{
		{
			Title:       "标题",
			URL:         "https://example.com/a",
			Snippet:     "短摘要",
			Content:     "正文内容",
			Score:       0.5,
			PublishedAt: &published,
		},
		{Title: "无日期", URL: "https://example.com/b", Snippet: "只有摘要", Score: 2},
	}}

	withContent := DeepSeekFromNative(response, true)
	if len(withContent.Results) != 2 {
		t.Fatalf("结果条数 = %d", len(withContent.Results))
	}
	first := withContent.Results[0]
	if first.RerankScore != 1.5 {
		t.Fatalf("0.5 应还原为 1.5，实际 %v", first.RerankScore)
	}
	if first.Content != "正文内容" || first.Summary != "正文内容" {
		t.Fatalf("need_content=true 时应返回正文并复用为 summary: %+v", first)
	}
	if first.Date == nil || *first.Date != "2026-06-01T00:30:00Z" {
		t.Fatalf("date 应格式化为 UTC RFC3339，实际 %v", first.Date)
	}
	if first.SiteName != nil || first.SiteIcon != nil {
		t.Fatal("site_name / site_icon 应输出 null")
	}
	if first.Authority != -1 {
		t.Fatalf("authority 未知应为 -1，实际 %d", first.Authority)
	}
	// 分数超过 3 时夹紧，避免破坏官方量纲。
	if withContent.Results[1].RerankScore != 3 {
		t.Fatalf("超上限分数应夹紧到 3，实际 %v", withContent.Results[1].RerankScore)
	}

	withoutContent := DeepSeekFromNative(response, false)
	if withoutContent.Results[0].Content != "" {
		t.Fatal("need_content=false 时 content 必须为空字符串")
	}
	if withoutContent.Results[0].Summary != "短摘要" {
		t.Fatalf("无正文时 summary 应回退 snippet，实际 %q", withoutContent.Results[0].Summary)
	}
	if withoutContent.Results[1].Date != nil {
		t.Fatal("未知日期应输出 null")
	}

	empty := DeepSeekFromNative(model.SearchResponse{}, false)
	if empty.Results == nil || len(empty.Results) != 0 {
		t.Fatalf("无结果时应返回非 nil 空数组，实际 %#v", empty.Results)
	}
}

// TestDeepSeekFetchToNative 覆盖官方 Fetch 请求校验：
// 合法 URL 透传并带上抓取硬上限，缺 url / 非 HTTP(S) / 带凭据 / 超长一律拒绝。
func TestDeepSeekFetchToNative(t *testing.T) {
	args, err := DeepSeekFetchToNative(DeepSeekFetchRequest{URL: "https://example.com/article"})
	if err != nil {
		t.Fatalf("合法 URL 不应报错: %v", err)
	}
	if args["url"] != "https://example.com/article" {
		t.Fatalf("url 未透传: %+v", args)
	}
	if args["max_length"] != 50000 {
		t.Fatalf("应使用抓取硬上限，实际 %v", args["max_length"])
	}

	cases := []struct {
		name string
		url  string
	}{
		{"缺 url", ""},
		{"裸域名", "example.com"},
		{"非 HTTP scheme", "ftp://example.com/x"},
		{"缺 host", "https://"},
		{"带用户名密码", "https://user:pass@example.com/x"},
		{"超长", "https://example.com/" + strings.Repeat("a", 8192)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeepSeekFetchToNative(DeepSeekFetchRequest{URL: tc.url}); err == nil {
				t.Fatalf("期望报错，实际通过: %q", tc.url)
			}
		})
	}
}
