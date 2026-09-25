package fetch

import (
	"strings"
	"testing"
)

// TestHtmlToMarkdownRemovesNoise 验证噪声节点被剔除：脚本、导航、页脚内容不应出现在结果里。
func TestHtmlToMarkdownRemovesNoise(t *testing.T) {
	source := `<html><head><style>body{color:red}</style><script>var x=1</script></head>
<body><nav><a href="/">首页</a></nav><main><h1>正文标题</h1><p>正文内容</p></main>
<aside>侧栏</aside><footer>版权所有</footer><form><input></form></body></html>`
	markdown := htmlToMarkdown(source)
	for _, noise := range []string{"var x=1", "color:red", "首页", "侧栏", "版权所有"} {
		if strings.Contains(markdown, noise) {
			t.Fatalf("噪声内容 %q 未被剔除: %q", noise, markdown)
		}
	}
	if !strings.Contains(markdown, "正文标题") || !strings.Contains(markdown, "正文内容") {
		t.Fatalf("正文内容丢失: %q", markdown)
	}
	// main 被选为正文容器，标题应渲染为 Markdown 标题
	if !strings.Contains(markdown, "#") {
		t.Fatalf("标题未转换为 Markdown 标题: %q", markdown)
	}
}

// TestHtmlToMarkdownPrefersMainContent 验证 main/article/[role=main] 优先于 body 全文。
func TestHtmlToMarkdownPrefersMainContent(t *testing.T) {
	cases := []struct {
		name   string
		source string
		noise  string
	}{
		{"main 优先", `<body><div>页面头部广告</div><main>目标正文</main></body>`, "页面头部广告"},
		{"article 优先", `<body><div>页面头部广告</div><article>目标正文</article></body>`, "页面头部广告"},
		{"role=main 优先", `<body><div>页面头部广告</div><div role="main">目标正文</div></body>`, "页面头部广告"},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			markdown := htmlToMarkdown(item.source)
			if !strings.Contains(markdown, "目标正文") {
				t.Fatalf("未提取到正文: %q", markdown)
			}
			if strings.Contains(markdown, item.noise) {
				t.Fatalf("正文容器外的内容应被排除: %q", markdown)
			}
		})
	}
}

// TestHtmlToMarkdownFallsBackToText 验证转换结果为空时退化为纯文本，而不是返回空内容。
func TestHtmlToMarkdownFallsBackToText(t *testing.T) {
	// 空 body 且无任何可转换节点时，回退路径返回压平后的纯文本
	markdown := htmlToMarkdown(`<html><body><span>  只有   文本  </span></body></html>`)
	if !strings.Contains(markdown, "只有") {
		t.Fatalf("兜底文本丢失: %q", markdown)
	}
	// 整页只有 script 时噪声被剔除，结果为空（上层据此返回 noMoreContent）
	if got := htmlToMarkdown(`<html><body><script>var x=1</script></body></html>`); strings.TrimSpace(got) != "" {
		t.Fatalf("纯脚本页面应为空，实际 %q", got)
	}
}

// TestIsHTML 覆盖 HTML 判定：Content-Type 优先，缺失时探测正文前缀。
func TestIsHTML(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		contentType string
		want        bool
	}{
		{"Content-Type 声明 html", "", "text/html; charset=utf-8", true},
		{"Content-Type 声明 xhtml", "", "application/xhtml+xml", true},
		{"Content-Type 为 json", "<!doctype html>", "application/json", false},
		{"无 Content-Type 且有 doctype", "<!DOCTYPE html><html>", "", true},
		{"无 Content-Type 且有 html 标签", "<html><body>x</body></html>", "", true},
		{"无 Content-Type 且是纯文本", "hello world", "", false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := isHTML(item.body, item.contentType); got != item.want {
				t.Fatalf("isHTML(%q, %q) = %v, 期望 %v", item.body, item.contentType, got, item.want)
			}
		})
	}
}

// TestContentForModel 覆盖归一化分支：HTML→Markdown、JSON 压缩、其余去首尾空白。
func TestContentForModel(t *testing.T) {
	htmlOut := contentForModel(`<html><body><h1>标题</h1></body></html>`, "text/html")
	if !strings.Contains(htmlOut, "# 标题") {
		t.Fatalf("HTML 未转为 Markdown: %q", htmlOut)
	}

	// JSON 用 Compact 只去空白，不改变数字表示形式
	jsonOut := contentForModel(`{ "a" : 1.50, "b" : [ 1 , 2 ] }`, "application/json")
	if strings.Contains(jsonOut, "\n") || strings.Contains(jsonOut, " ") {
		t.Fatalf("JSON 未被压缩: %q", jsonOut)
	}
	if !strings.Contains(jsonOut, "1.50") {
		t.Fatalf("数字表示形式被改变: %q", jsonOut)
	}

	if got := contentForModel("  plain text  ", "text/plain"); got != "plain text" {
		t.Fatalf("纯文本应去首尾空白，实际 %q", got)
	}
	// 声明是 JSON 但内容非法时保留原文，不让格式问题导致整体失败
	if got := contentForModel("{not json", "application/json"); got != "{not json" {
		t.Fatalf("非法 JSON 应保留原文，实际 %q", got)
	}
}

// TestDecodeBody 覆盖编码解码：合法 UTF-8 原样、非法 UTF-8 逐字节映射。
func TestDecodeBody(t *testing.T) {
	if got := decodeBody([]byte("中文 😀")); got != "中文 😀" {
		t.Fatalf("合法 UTF-8 应原样返回: %q", got)
	}
	// GBK 的「中文」字节序列不是合法 UTF-8，逐字节映射后可读但不等价
	gbk := []byte{0xD6, 0xD0, 0xCE, 0xC4}
	decoded := decodeBody(gbk)
	if len([]rune(decoded)) != len(gbk) {
		t.Fatalf("逐字节映射后应保持字节数不变，实际 %q", decoded)
	}
	if strings.ContainsRune(decoded, '\uFFFD') {
		t.Fatalf("不应产生替换字符: %q", decoded)
	}
}

// TestCharsetOf 覆盖 Content-Type 的 charset 解析。
func TestCharsetOf(t *testing.T) {
	cases := map[string]string{
		"text/html; charset=utf-8":   "utf-8",
		"text/html; charset=UTF-8":   "utf-8",
		`text/html; charset="gbk"`:   "gbk",
		"application/json":           "",
		"text/plain; charset=gb2312": "gb2312",
	}
	for raw, want := range cases {
		if got := charsetOf(raw); got != want {
			t.Fatalf("charsetOf(%q) = %q, 期望 %q", raw, got, want)
		}
	}
}

// TestToolSchema 验证 MCP schema：url 必填、参数齐全，且不暴露请求级 proxy。
func TestToolSchema(t *testing.T) {
	schema := ToolSchema()
	if schema["name"] != ToolName || schema["description"] == "" {
		t.Fatalf("工具定义缺少名称或描述: %v", schema)
	}
	input, ok := schema["inputSchema"].(map[string]interface{})
	if !ok || input["type"] != "object" {
		t.Fatalf("inputSchema 结构不正确: %v", schema["inputSchema"])
	}
	properties, ok := input["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties 缺失: %v", input)
	}
	for _, name := range []string{"url", "method", "headers", "body", "max_length", "start_index", "raw"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("schema 缺少参数 %s: %v", name, properties)
		}
	}
	// 代理是管理员级全局配置，不接受请求级传参，因此 schema 里也不应有该字段
	if _, ok := properties["proxy"]; ok {
		t.Fatal("schema 不应暴露 proxy 参数")
	}
	required, ok := input["required"].([]string)
	if !ok || len(required) != 1 || required[0] != "url" {
		t.Fatalf("required 应仅有 url: %v", input["required"])
	}
}
