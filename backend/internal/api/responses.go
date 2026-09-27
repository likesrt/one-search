package api

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
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
