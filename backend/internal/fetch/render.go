package fetch

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown"
	"golang.org/x/net/html"
)

const (
	// bodyPeekSize 是缺少 Content-Type 时用于判断 HTML 的探测长度
	bodyPeekSize = 256
)

var (
	// charsetRe 匹配 Content-Type 中的 charset 参数
	charsetRe = regexp.MustCompile(`(?i)charset\s*=\s*"?([\w-]+)`)
	// htmlPrefixRe 匹配正文开头的 HTML 特征，用于缺少 Content-Type 时探测
	htmlPrefixRe = regexp.MustCompile(`(?i)<\s*(?:!doctype\s+html|html)\b`)
	// blankLinesRe 把三个以上连续换行压成空行，避免 Markdown 出现大片空白
	blankLinesRe = regexp.MustCompile(`\n{3,}`)
	// spaceRunRe 把连续空白压成单个空格，用于纯文本兜底输出
	spaceRunRe = regexp.MustCompile(`\s+`)
)

// converter 是包级复用的 HTML→Markdown 转换器。
//
// 复用而非每次新建：转换器内部持规则表，构造有成本；v1 的 Converter 自带 RWMutex，
// 并发调用是安全的（见 html-to-markdown v1.6.0 from.go 的 Convert 方法）。
// 不额外调用 v1 的 Remove/Keep：噪声节点剔除由本包的 removeNoiseNodes 完成，
// 顺序是先删节点再转换，行为已在源工具中验证，再叠一层 Remove 会引入重复且难以解释的差异。
var converter = htmltomarkdown.NewConverter("", true, nil)

// noiseTags 是不参与正文转换的标签集合。
//
// 脚本、样式与导航类结构对模型没有价值，保留它们既浪费 token，
// 也会在 Markdown 中产生无意义的标题层级。
var noiseTags = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true,
	"svg": true, "iframe": true, "nav": true, "aside": true,
	"footer": true, "form": true,
}

// charsetOf 提取 Content-Type 中的 charset 参数并统一为小写；未声明时返回空串。
func charsetOf(contentType string) string {
	match := charsetRe.FindStringSubmatch(contentType)
	if len(match) < 2 {
		return ""
	}
	return strings.ToLower(match[1])
}

// decodeBody 把响应字节解码为文本。
//
// 优先按严格 UTF-8 解码，仅在字节序列确实不是合法 UTF-8 时才逐字节映射为码点。
// 这样处理的原因：绝大多数页面与 API 都是 UTF-8，但 HTTP 客户端在缺少 charset
// 声明时按 latin1 处理会把中文弄乱；而逐字节映射能让单字节编码的内容至少保持
// 可读形态，也避免后续按 rune 截断时切碎字节序列。
//
// 已知限制：Go 标准库不提供 GBK 等编码的解码器，这类内容会显示为乱码。
func decodeBody(body []byte) string {
	if utf8.Valid(body) {
		return string(body)
	}
	runes := make([]rune, len(body))
	for i, b := range body {
		runes[i] = rune(b)
	}
	return string(runes)
}

// contentForModel 把响应体归一化为适合模型阅读的形式。
//
// HTML 转紧凑 Markdown 以节省 token；JSON 压缩掉多余空白；
// 其余原样返回。归一化失败时保留原文，不让格式问题导致抓取整体失败。
//
// 参数 contentType 需为小写（调用方已统一处理）。
func contentForModel(body, contentType string) string {
	if isHTML(body, contentType) {
		return htmlToMarkdown(body)
	}
	if strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "+json") {
		var buf bytes.Buffer
		// 用 Compact 而非解码后重新编码：前者只去空白，不会改变数字的表示形式
		if err := json.Compact(&buf, []byte(body)); err == nil {
			return buf.String()
		}
	}
	return strings.TrimSpace(body)
}

// isHTML 判断响应内容是否为 HTML。
//
// Content-Type 明确时以其为准；缺失时探测正文开头是否出现 <!doctype html> 或
// <html> —— 不少站点不返回 Content-Type，只依据头部会漏判。
func isHTML(body, contentType string) bool {
	if strings.Contains(contentType, "text/html") ||
		strings.Contains(contentType, "application/xhtml+xml") {
		return true
	}
	if contentType != "" {
		return false
	}
	peek := body
	if len(peek) > bodyPeekSize {
		peek = peek[:bodyPeekSize]
	}
	return htmlPrefixRe.MatchString(peek)
}

// htmlToMarkdown 把 HTML 转为紧凑 Markdown。
//
// 流程：先剔除噪声节点，再优先提取 main/article/[role=main] 作为正文，最后转换。
// 提取正文容器能显著减少导航、侧栏等重复内容，它们在同一站点的每个页面上出现，
// 对模型判断内容价值毫无帮助却占用大量 token。
// 转换结果为空时退化为纯文本，避免返回空内容。
func htmlToMarkdown(source string) string {
	doc, err := html.Parse(strings.NewReader(source))
	if err != nil {
		return strings.TrimSpace(source)
	}
	removeNoiseNodes(doc)
	root, outer := findMainContent(doc)
	if markdown, ok := convertNode(root, outer); ok {
		return markdown
	}
	return collapseSpaces(nodeText(root))
}

// convertNode 渲染指定节点并转换为 Markdown。
//
// 第二个返回值表示转换是否产出了有效内容；为 false 时调用方应退化为纯文本。
func convertNode(node *html.Node, outer bool) (string, bool) {
	markdown, err := converter.ConvertString(renderNode(node, outer))
	if err != nil {
		return "", false
	}
	trimmed := strings.TrimSpace(markdown)
	if trimmed == "" {
		return "", false
	}
	return blankLinesRe.ReplaceAllString(trimmed, "\n\n"), true
}

// findMainContent 定位正文容器，按 main/article → body → 整个文档依次退化。
//
// 返回的 outer 表示应按元素自身（含标签）序列化还是仅序列化其子节点：
// main/article 用 outer 保留语义标签，body 用 inner 避免多包一层 <body>。
func findMainContent(doc *html.Node) (node *html.Node, outer bool) {
	if element := findFirst(doc, isMainContentNode); element != nil {
		return element, true
	}
	if body := findFirst(doc, isBodyNode); body != nil {
		return body, false
	}
	return doc, true
}

// isMainContentNode 判断节点是否为正文容器：main、article 或 [role=main]。
func isMainContentNode(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if n.Data == "main" || n.Data == "article" {
		return true
	}
	for _, attr := range n.Attr {
		if attr.Key == "role" && strings.EqualFold(attr.Val, "main") {
			return true
		}
	}
	return false
}

// isBodyNode 判断节点是否为 body 元素。
func isBodyNode(n *html.Node) bool {
	return n.Type == html.ElementNode && n.Data == "body"
}

// findFirst 深度优先查找第一个满足 match 的元素节点；未找到返回 nil。
func findFirst(n *html.Node, match func(*html.Node) bool) *html.Node {
	if match(n) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findFirst(child, match); found != nil {
			return found
		}
	}
	return nil
}

// removeNoiseNodes 原地剔除噪声节点。
//
// 先取 next 再删除当前节点，否则 RemoveChild 会使 NextSibling 失效。
func removeNoiseNodes(n *html.Node) {
	var next *html.Node
	for child := n.FirstChild; child != nil; child = next {
		next = child.NextSibling
		if child.Type == html.ElementNode && noiseTags[child.Data] {
			n.RemoveChild(child)
			continue
		}
		removeNoiseNodes(child)
	}
}

// renderNode 序列化节点：outer 为 true 时输出元素自身，x/net/html 没有等价的
// innerHTML，因此需要逐个渲染子节点再拼装。
func renderNode(n *html.Node, outer bool) string {
	var buf strings.Builder
	if outer {
		_ = html.Render(&buf, n)
		return buf.String()
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		_ = html.Render(&buf, child)
	}
	return buf.String()
}

// nodeText 递归收集节点下的全部纯文本，用于 Markdown 转换失败时兜底。
func nodeText(n *html.Node) string {
	var buf strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			buf.WriteString(node.Data)
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return buf.String()
}

// collapseSpaces 把连续空白压成单个空格并去除首尾空白，用于纯文本兜底输出。
func collapseSpaces(s string) string {
	return strings.TrimSpace(spaceRunRe.ReplaceAllString(s, " "))
}
