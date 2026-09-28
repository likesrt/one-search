package api

import (
	"encoding/json"
	"io"
	"net/http"
)

// writeJSON 输出 JSON 响应，非 ASCII 字符与 < > & 均按 UTF-8 原样写出。
//
// 与 Go 默认行为的关键差异是关掉了 HTML 转义（见 writeJSONBody）：默认编码会把
// 中文写成 \uXXXX、把 < > & 写成 < 一类序列，而搜索结果里这些字符极其常见
// （中文标题与摘要、URL 查询串、代码片段、Markdown 围栏）。转义后每个中文字符
// 从 3 字节膨胀到 6 字节、`<` 从 1 字节膨胀到 6 字节，纯属浪费。
//
// 本服务只产出 JSON，不把响应拼进 HTML 文档，因此这里没有 XSS 面要防；
// JSON 语义上两种写法完全等价，任何合规解析器读到的字符串都一样。
//
// 参数 status 为 HTTP 状态码，payload 为待序列化的响应体。
// 副作用：写入响应头与响应体；编码失败时错误只能被丢弃 —— 状态码在编码前已发出，无法回退。
func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeJSONBody(w, payload)
}

// writeJSONBody 用关闭 HTML 转义的编码器把 payload 写入 w。
//
// 调用方必须先设好响应头并调用 WriteHeader：本函数只管序列化与写入。
// 抽成独立函数而不是在每处写三行，是为了让原生接口与 MCP 接口共用同一口径 ——
// 否则很容易出现「原生接口不转义、MCP 还在转义」这种不一致。
//
// 参数 w 为写入目标（HTTP 响应或任意 io.Writer），payload 为待序列化对象。
// 边界条件：payload 不可序列化时（如含 channel）错误被丢弃，响应体将不完整；
// 这是既有行为，本函数不改变错误处理策略。
// 副作用：向 w 写入一行 JSON 并附带换行符（encoding/json 的 Encode 行为）。
func writeJSONBody(w io.Writer, payload interface{}) {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"status":  status,
		},
	})
}

// writeSecretJSON 输出含凭据明文的响应，并显式禁止任何一层缓存留存。
//
// 与 writeJSON 的唯一差别是多两个响应头，但这一差别是必需的：明文一旦被中间代理
// 或浏览器磁盘缓存留存，就脱离了服务端的审计与轮换控制（轮换后旧明文仍可被读回）。
// 项目部署形态是 nginx 反代 + 浏览器管理台，两端都可能缓存，因此用 no-store
// 而非 no-cache —— 后者只是要求回源校验，仍允许落盘。
//
// Cache-Control 之外同时设 Pragma：后者是 HTTP/1.0 时代的等价头，部分老旧代理只认它。
// 注意本函数只负责响应头，读取动作的审计由调用方在调用前写入。
func writeSecretJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, payload)
}
