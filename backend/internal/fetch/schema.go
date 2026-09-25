package fetch

// 本文件集中定义 fetch 工具的 MCP schema 与面向模型的描述常量。
//
// 措辞直接面向模型，因此必须与实际行为严格一致：描述里承诺而实现不支持的，
// 会让模型反复尝试并浪费轮次；实现支持而描述未提的，模型则不会使用。
// 每条描述都对应实现中的一处具体校验或行为（见括号内文件），修改描述前应先确认该行为。

const (
	// descURL 描述 url 参数（校验见 request.go 的 parseRequestURL）
	descURL = "The full URL to fetch, including the scheme. http:// or https:// is " +
		"required: https://example.com is valid, while example.com is invalid. " +
		"Pass it as given; do not rewrite the host."
	// descMethod 描述 method 参数（校验见 request.go 的 parseRequestMethod）
	descMethod = "HTTP method. GET (the default) reads a web page; use POST only " +
		"to call an API endpoint the user asked you to call."
	// descHeaders 描述 headers 参数（合并逻辑见 fetch.go 的 newHTTPRequest）
	descHeaders = "Optional request headers, such as Authorization or Accept. Values " +
		"given here override the defaults, including the User-Agent."
	// descBody 描述 body 参数（自动序列化见 request.go 的 parseRequestBody）
	descBody = "Request body, allowed only with method=POST. A string is sent as-is; " +
		"an object or array is serialized to JSON for you. Sent as UTF-8, and " +
		"Content-Type defaults to application/json when omitted."
	// descMaxLength 描述 max_length 参数（范围校验见 request.go 的 fillNumericArgs）
	descMaxLength = "Maximum number of characters to return. Counted in Unicode code " +
		"points, so a multi-byte character is never split in half."
	// descStartIdx 描述 start_index 参数（仅 GET 可用，见 fillNumericArgs）
	descStartIdx = "Character index to resume from when continuing truncated content. " +
		"Take the value from the truncation notice. GET only."
	// descRaw 描述 raw 参数（跳过归一化见 fetch.go 的 Fetch）
	descRaw = "Return the source as received, skipping HTML-to-Markdown conversion and " +
		"JSON compaction. Use it when the exact source matters, or to read pages " +
		"in non-UTF-8 encodings."
)

// ToolDescription 是 MCP 工具描述，供 /mcp 的 tools/list 使用。
//
// 关于「只抓取对话中已出现的 URL」：这属于提示词层面的约束，依赖模型自觉遵守，
// 代码不做强制 —— 服务端无法判断某个 URL 是否真的在对话里出现过。
// 该约束的价值是降低模型臆造地址去抓取的概率，真正的安全边界由 SSRF 防护承担。
//
// 关于会话保持：客户端未配置 CookieJar，因此 Cookie 不跨请求保留，
// 需要完成登录流程的页面无法访问。这是实现事实而非策略限制，
// 描述中如实说明可避免模型反复尝试用 Cookie 维持会话。
//
// 不包含 proxy 参数：代理是本功能的管理员级全局配置，不接受请求级传参
// （客户端指定任意代理等于把服务变成内网跳板），因此 schema 里也不暴露该字段。
const ToolDescription = "Fetch a web page, or call an HTTP API. Only fetch a URL that " +
	"already appears in the conversation: one provided by the user or returned by " +
	"a previous tool call. Sessions are not kept: no cookies carry over between " +
	"requests, so pages that require completing a login flow cannot be reached. " +
	"Credentials supplied explicitly through headers do work. HTML is converted to " +
	"compact Markdown and JSON responses are compacted. Output is bounded by default: " +
	"continue truncated content with start_index, or use raw=true for the source as " +
	"received. method=POST with a body calls an API endpoint the user has asked for; " +
	"a POST response cannot be continued with start_index, so raise max_length when " +
	"it is truncated. JavaScript is not executed: pages rendered entirely on the " +
	"client, and sites that gate content behind a browser challenge, come back " +
	"empty or as the challenge page itself."

// ToolSchema 返回 fetch 工具的 MCP 定义。
//
// 返回值：符合 MCP 工具描述结构的映射，含 name、title、description、inputSchema
// 与 annotations。schema 中只有 url 为必填，其余参数均有默认值，模型可以省略。
//
// 边界条件：本函数无状态、不读配置；是否对外暴露 fetch 由调用方按 FetchSettings.enabled 决定。
// 副作用：无。
func ToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"name":        ToolName,
		"title":       "Fetch Web Page",
		"description": ToolDescription,
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"url":         map[string]interface{}{"type": "string", "description": descURL},
				"method":      map[string]interface{}{"type": "string", "description": descMethod, "enum": []string{"GET", "POST"}, "default": "GET"},
				"headers":     map[string]interface{}{"type": "object", "description": descHeaders},
				"body":        map[string]interface{}{"type": "string", "description": descBody},
				"max_length":  map[string]interface{}{"type": "integer", "description": descMaxLength, "default": defaultMaxLength, "minimum": 1, "maximum": maximumMaxLength},
				"start_index": map[string]interface{}{"type": "integer", "description": descStartIdx, "default": 0, "minimum": 0},
				"raw":         map[string]interface{}{"type": "boolean", "description": descRaw, "default": false},
			},
			"required": []string{"url"},
		},
		"annotations": map[string]interface{}{
			"title":         "Fetch Web Page",
			"readOnlyHint":  false,
			"openWorldHint": true,
		},
	}
}
