package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/one-search/one-search/backend/internal/model"
)

const (
	// context7SnippetLimit 是 Context7 渠道自己的摘要封顶，与 jina 等适配器同档：
	// 上游的文档片段可能很长，整段塞进响应会迅速吃掉模型的上下文预算。
	//
	// 它作为渠道默认值传给 resultOptionsFrom，因此调用方可以用更小的 snippet_limit 收紧，
	// 但无法用它放大。正文侧没有对应的渠道常量：正文上限统一走请求的 max_content_length。
	context7SnippetLimit = 1000
	// context7NoDocumentationError 是上游「没有匹配到任何库文档」时响应体 error 字段的取值。
	context7NoDocumentationError = "no_documentation_found"
)

// Context7Provider 是 Context7 文档渠道适配器。
// 它检索的是 GitHub 仓库的结构化文档与代码片段，与其余渠道的「通用网页搜索」定位互补：
// 只实现 GET /v3/search，key 非空时带 Authorization: Bearer 鉴权，为空时匿名调用（实测同样返回 200）。
type Context7Provider struct {
	*HTTPProvider
}

// NewContext7Provider 构建 Context7 适配器。
// cfg.BaseURL 为空时回退官方地址 https://context7.com/api；端点固定拼接 /v3/search，
// 带 `#` 前缀的完整端点语法由 NewHTTPProvider 统一处理（无需本函数额外分支）。
// 返回值恒为非 nil 适配器；cfg 中的超时与代理沿用 HTTPProvider 的既有语义。
func NewContext7Provider(cfg Config) *Context7Provider {
	cfg.Name = model.ProviderContext7
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://context7.com/api"
	}
	return &Context7Provider{HTTPProvider: NewHTTPProvider(cfg)}
}

// SupportsAnonymousKey 报告 Context7 在无密钥时能否正常调用，恒为 true。
//
// 依据是实测：GET /v3/search 不带 Authorization 返回 200 与正常的 codeSnippets / infoSnippets，
// 而带无效 key 反而返回 401 invalid_api_key —— 说明该 key 是可选增强（更高配额）而非必需凭据。
// 返回 true 只影响管理台提示（创建空密钥时不弹确认框），不改变 Search 的放行逻辑。
func (p *Context7Provider) SupportsAnonymousKey() bool {
	return true
}

// Search 调用 Context7 的 GET /v3/search。
//
// 参数：req.Query 为必填的检索原文；req.Options 里的 library（可多值）/version/language 原样透传给上游；
// req.Limit 只在本适配器内生效（上游不接受 limit 参数）；req.Freshness 不读取（上游没有时效参数）。
// key.Value 去空白后为空时不发送 Authorization 头（匿名调用），非空时发送 Bearer。
// 判据刻意与 security.MaskSecret、keenable 的匿名分支同源（TrimSpace 后为空）：
// 管理台正是按 MaskSecret 的结果把空 key_hint 显示成「匿名」，若这里只认严格空串，
// 一条纯空白的密钥就会「显示为匿名、实际发出 Authorization: Bearer （空值）」并被上游判 401。
//
// 返回值：命中时为归一化后的文档页结果（同一文档文件的多段片段已聚合为一条）；
// 上游 404 且 error 为 no_documentation_found 时返回空结果且 err == nil —— 这是「该问题没有库文档」
// 的正常反馈，若当故障处理，fallback 模式会白记一次失败、parallel 模式会把渠道状态染红。
//
// 边界条件：其它任何错误状态码、网络错误与响应体解析失败都照常返回错误。
// 副作用：发起一次真实 HTTP 请求；Response.Body 由 decodeResponseWithStatus 关闭。
func (p *Context7Provider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	request, err := p.newGETRequest(ctx, "/v3/search", context7Params(req))
	if err != nil {
		return model.ProviderResponse{}, err
	}
	if strings.TrimSpace(key.Value) != "" {
		request.Header.Set("Authorization", "Bearer "+key.Value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return model.ProviderResponse{}, err
	}
	payload, status, err := p.decodeResponseWithStatus(response)
	if err != nil {
		if context7NoDocumentation(status, err) {
			return model.ProviderResponse{Results: []model.SearchResult{}}, nil
		}
		return model.ProviderResponse{}, err
	}
	results := normalizeContext7Results(payload, resultOptionsFrom(req, context7SnippetLimit), req.Limit)
	return model.ProviderResponse{Results: results, Usage: usageMeasurements(model.ProviderContext7, payload), Raw: payload}, nil
}

// context7Params 组装 GET /v3/search 的查询参数。
// query 与 type=json 恒发送（type 缺失时上游默认返回纯文本，无法归一化）；
// library 取 options.library，可重复出现（上游最多接受 4 个，这里不替上游裁剪，超限由上游报参数错误）；
// version / language 为空串时不发送，避免用空值覆盖上游默认行为。
// 返回值恒为非 nil 的 url.Values（调用方无需判空）。
func context7Params(req model.SearchRequest) url.Values {
	params := url.Values{}
	params.Set("query", req.Query)
	params.Set("type", "json")
	// Add 而非 Set：同名键重复出现才会被编码成 library=a&library=b。
	for _, library := range context7Libraries(req.Options) {
		params.Add("library", library)
	}
	if version := optionString(req.Options, "version"); version != "" {
		params.Set("version", version)
	}
	if language := optionString(req.Options, "language"); language != "" {
		params.Set("language", language)
	}
	return params
}

// context7Libraries 解析 options.library。
// 上游接受模糊名（next.js）与精确 ID（/vercel/next.js），两种写法都原样透传，不做规范化。
// 标量写法（"library": "next.js"）也兼容：模型常常只传一个字符串，静默忽略会让用户误以为库名没生效。
// 返回值：去空后的库名切片，未配置时返回 nil。
func context7Libraries(options map[string]interface{}) []string {
	if libraries := optionStringSlice(options, "library"); len(libraries) > 0 {
		return libraries
	}
	if library := optionString(options, "library"); library != "" {
		return []string{library}
	}
	return nil
}

// context7NoDocumentation 判断某次失败是否为「上游没有收录该问题的文档」。
// 只有 404 且响应体 error 字段恰为 no_documentation_found 才返回 true；
// 其它 404（例如 base_url 配错）以及非 404 状态一律返回 false，照常按渠道故障上报。
// 边界条件：decodeResponseWithStatus 在非 2xx 时不解析 payload，只把响应体（截断到 300 字符）
// 放进 *Error.Message，因此这里自行解析该字段；解析失败（响应体被截断或上游改回纯文本）
// 时退化为子串匹配 —— 该取值出现在响应体开头，实测不会被截断。
func context7NoDocumentation(status int, err error) bool {
	if status != http.StatusNotFound {
		return false
	}
	var providerErr *Error
	if !errors.As(err, &providerErr) {
		return false
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(providerErr.Message), &payload) == nil && payload.Error != "" {
		return payload.Error == context7NoDocumentationError
	}
	return strings.Contains(providerErr.Message, context7NoDocumentationError)
}

// context7Collector 按「文档文件」聚合 Context7 返回的片段，并保持首次出现顺序。
//
// 之所以必须聚合：编排层 mergeResults 的去重键 canonicalURL 会剥掉 URL 的 fragment，
// 而 codeId 形如 .../quickstart.md#_snippet_0、#_snippet_1，一个文件就会有多条片段。
// 不聚合的话同一文件的多段代码会在编排层被误去重，Content 只保留第一段，后续片段静默丢失。
// 聚合后一条结果 = 一个文档页，Content 承载该页的全部片段，limit 的语义也变成「文档页数」。
type context7Collector struct {
	order []string
	byURL map[string]*model.SearchResult
}

// newContext7Collector 构造一个空的聚合器（order 与 byURL 均已初始化，可直接使用）。
func newContext7Collector() *context7Collector {
	return &context7Collector{order: []string{}, byURL: map[string]*model.SearchResult{}}
}

// add 把一条已归一化的片段并入其所属文档文件。
// 参数：url 为已剥 fragment 的文件地址，为空时直接丢弃该条目（与其余适配器一致）；
// title/snippet/content 为归一化后的字段（content 为空串表示本次不输出正文）；raw 为原始条目。
// opts 只用于决定是否写入 Raw —— 正文是否填充由调用方在传参前决定（不填充时 content 传空串）。
// 合并规则：正文按出现顺序拼接，标题与摘要只在原值为空时补齐（保留首个非空值）。
// 副作用：修改 c 内部的累积状态。
func (c *context7Collector) add(url, title, snippet, content string, raw map[string]interface{}, opts resultOptions) {
	if url == "" {
		return
	}
	existing, ok := c.byURL[url]
	if !ok {
		result := model.SearchResult{
			Title:     title,
			URL:       url,
			Snippet:   snippet,
			Content:   content,
			Provider:  model.ProviderContext7,
			Providers: []string{model.ProviderContext7},
		}
		if opts.IncludeRaw {
			result.Raw = raw
		}
		c.byURL[url] = &result
		c.order = append(c.order, url)
		return
	}
	if content != "" {
		existing.Content = existing.Content + context7ContentSeparator + content
	}
	if existing.Title == "" {
		existing.Title = title
	}
	if existing.Snippet == "" {
		existing.Snippet = snippet
	}
}

// results 按首次出现顺序输出聚合结果，并把正文统一收口到 contentCap
// （单条片段在聚合时可超过上限，同一文件的多个片段拼接后更会超限，因此在这里统一裁）。
//
// 参数 contentCap 由调用方按本次请求口径传入（见 resultOptionsFrom）；未输出正文时为 0，
// 此时不写 Content 字段 —— 这与「正文为空串」不同：后者会让 content 以空值出现在 JSON 里。
//
// 返回值为新切片，修改它不会影响聚合器内部状态。
func (c *context7Collector) results(contentCap int) []model.SearchResult {
	results := make([]model.SearchResult, 0, len(c.order))
	for _, url := range c.order {
		result := *c.byURL[url]
		if contentCap > 0 {
			result.Content = truncate(result.Content, contentCap)
		}
		results = append(results, result)
	}
	return results
}

// context7ContentSeparator 是同一文档文件内多段片段之间的分隔符（空行，保证 Markdown 代码块不被粘连）。
const context7ContentSeparator = "\n\n"

// normalizeContext7Results 把 Context7 的 type=json 响应归一化为统一的搜索结果。
// 参数：payload 为响应体；opts 决定截断口径、是否输出正文与是否保留原始条目；
// limit 为请求条数（<=0 表示不截断）。
// 处理顺序为 codeSnippets 在前、infoSnippets 在后，与上游响应字段顺序一致。
// Score 用 1/(序号+1) 按名次折算（上游不返回 score，与 jina / serper / keenable 同一套兜底口径）。
// 上游不接受 limit 参数，因此截断只发生在本函数内，语义是「文档页数」。
// 边界条件：IncludeContent 为 false 时不产出 Content —— 该渠道的正文就是代码示例本身，
// 关掉后只剩 codeDescription 摘要，需要代码时应显式打开 include_content。
func normalizeContext7Results(payload map[string]interface{}, opts resultOptions, limit int) []model.SearchResult {
	collector := newContext7Collector()
	context7AddCodeSnippets(collector, payload, opts)
	context7AddInfoSnippets(collector, payload, opts)
	// 未输出正文时传 0：聚合器的 results 据此跳过 Content 赋值（而不是写入空串）。
	contentCap := 0
	if opts.IncludeContent {
		contentCap = opts.ContentCap
	}
	aggregated := collector.results(contentCap)
	for index := range aggregated {
		aggregated[index].Score = 1 / float64(index+1)
	}
	if limit > 0 && len(aggregated) > limit {
		return aggregated[:limit]
	}
	return aggregated
}

// context7AddCodeSnippets 把 codeSnippets 数组并入聚合器。
// 字段映射：URL = 剥掉 fragment 的 codeId（缺 URL 丢弃）、Title = codeTitle（空则回退 pageTitle）、
// Snippet = codeDescription（按 opts.SnippetCap 截断）、
// Content = codeList 各段代码按围栏拼接（仅 opts.IncludeContent 时产出，按 opts.ContentCap 收口）。
func context7AddCodeSnippets(collector *context7Collector, payload map[string]interface{}, opts resultOptions) {
	for _, rawItem := range resultArray(payload, "codeSnippets") {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		title := stringValue(item, "codeTitle")
		if title == "" {
			title = stringValue(item, "pageTitle")
		}
		snippet := truncate(stringValue(item, "codeDescription"), opts.SnippetCap)
		content := ""
		if opts.IncludeContent {
			content = truncate(context7CodeBlocks(item), opts.ContentCap)
		}
		collector.add(
			stripURLFragment(stringValue(item, "codeId")),
			title,
			snippet,
			content,
			item,
			opts,
		)
	}
}

// context7AddInfoSnippets 把 infoSnippets 数组并入聚合器。
// 字段映射：URL = 剥掉 fragment 的 pageId（缺 URL 丢弃）、Title = breadcrumb（空则回退 pageTitle）、
// Snippet 取 content 按 opts.SnippetCap 截断；Content 同源但按 opts.ContentCap 截断，
// 仅在 opts.IncludeContent 时产出。之所以两处都用同一份 content 变量取值，
// 是因为该渠道的上游只给一个正文字段，摘要与正文的差异仅在于截断上限。
func context7AddInfoSnippets(collector *context7Collector, payload map[string]interface{}, opts resultOptions) {
	for _, rawItem := range resultArray(payload, "infoSnippets") {
		item := mapFromInterface(rawItem)
		if item == nil {
			continue
		}
		title := stringValue(item, "breadcrumb")
		if title == "" {
			title = stringValue(item, "pageTitle")
		}
		content := stringValue(item, "content")
		snippet := truncate(content, opts.SnippetCap)
		outContent := ""
		if opts.IncludeContent {
			outContent = truncate(content, opts.ContentCap)
		}
		collector.add(
			stripURLFragment(stringValue(item, "pageId")),
			title,
			snippet,
			outContent,
			item,
			opts,
		)
	}
}

// context7CodeBlocks 把 codeList 里的每段代码包进 ```<language> 围栏后拼接。
// 围栏语言优先取该段自身的 language，缺失时回退片段级 codeLanguage；两者都空则用无语言围栏。
// 用围栏而不是裸拼接，是为了让 Content 直接成为模型可复用的代码块（与 Markdown 文档同构）。
// 返回值：以空行分隔的围栏代码块；codeList 为空或无有效代码时返回空串。
func context7CodeBlocks(item map[string]interface{}) string {
	blocks := make([]string, 0, 4)
	for _, rawEntry := range resultArray(item, "codeList") {
		entry := mapFromInterface(rawEntry)
		if entry == nil {
			continue
		}
		code := stringValue(entry, "code")
		if strings.TrimSpace(code) == "" {
			continue
		}
		language := stringValue(entry, "language")
		if language == "" {
			language = stringValue(item, "codeLanguage")
		}
		blocks = append(blocks, "```"+language+"\n"+code+"\n```")
	}
	return strings.Join(blocks, context7ContentSeparator)
}

// stripURLFragment 剥掉 URL 的 fragment（`#` 及其后内容）。
// 该口径必须与编排层 canonicalURL 对 fragment 的处理一致：本函数的返回值既是聚合键也是最终 URL，
// 两边不一致就会重新出现「同一文件的不同片段被误去重、后续片段静默丢失」的问题。
// 参数 raw 允许为空或非 URL 文本（上游出错时可能给出提示串），此时原样（去空白后）返回。
func stripURLFragment(raw string) string {
	value := strings.TrimSpace(raw)
	if index := strings.Index(value, "#"); index >= 0 {
		return value[:index]
	}
	return value
}
