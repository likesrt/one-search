package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/one-search/one-search/backend/internal/compat"
	"github.com/one-search/one-search/backend/internal/fetch"
	"github.com/one-search/one-search/backend/internal/model"
)

// DeepSeek 兼容接口的错误类型，取值沿用 OpenAI 的错误对象分类，
// 官方文档明确其错误响应为 OpenAI 兼容格式。
const (
	deepSeekErrorInvalidRequest = "invalid_request_error"
	deepSeekErrorAuthentication = "authentication_error"
	deepSeekErrorPermission     = "permission_error"
	deepSeekErrorNotFound       = "not_found_error"
	deepSeekErrorRateLimit      = "rate_limit_error"
	deepSeekErrorServer         = "server_error"
	deepSeekErrorTimeout        = "timeout_error"
)

// writeDeepSeekError 输出 DeepSeek/OpenAI 兼容的错误对象。
//
// 原生接口的 writeError 形状是 {message,status}，与官方契约不同；
// 兼容端点必须让调用方按同一套字段解析，因此单独实现而不改动全局错误格式。
// 官方约定 param 恒为 null，这里同样固定输出，code 未知也输出 null。
//
// 参数：status 为 HTTP 状态码，errorType 为错误分类，message 为可读原因。
// 副作用：写入响应头与响应体。
func writeDeepSeekError(w http.ResponseWriter, status int, errorType, message string) {
	writeJSON(w, status, map[string]interface{}{
		"error": map[string]interface{}{
			"message": message,
			"type":    errorType,
			"param":   nil,
			"code":    nil,
		},
	})
}

// writeDeepSeekErrorForStatus 是 writeDeepSeekError 的按状态码取类型的便捷入口，
// 供共用鉴权中间件使用（中间件只拿到状态码，不关心具体错误分类）。
func writeDeepSeekErrorForStatus(w http.ResponseWriter, status int, message string) {
	writeDeepSeekError(w, status, deepSeekErrorTypeForStatus(status), message)
}

// deepSeekErrorTypeForStatus 把 HTTP 状态码映射为 OpenAI 错误类型。
// 未覆盖的状态码一律归入 server_error，避免输出空 type 让客户端难以分支处理。
func deepSeekErrorTypeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return deepSeekErrorInvalidRequest
	case http.StatusUnauthorized:
		return deepSeekErrorAuthentication
	case http.StatusForbidden:
		return deepSeekErrorPermission
	case http.StatusNotFound:
		return deepSeekErrorNotFound
	case http.StatusTooManyRequests:
		return deepSeekErrorRateLimit
	case http.StatusGatewayTimeout:
		return deepSeekErrorTimeout
	default:
		return deepSeekErrorServer
	}
}

// deepSeekSearch 处理 DeepSeek 兼容搜索 POST /v1/tools/search。
//
// 请求与响应字段严格对齐官方 Search 接口：count 缺省按 10 条执行（不受渠道
// request_result_limit 改写），need_content 控制正文，响应只含 results 数组。
// 所有渠道都失败时返回 500，而不是原生接口那种 200 + 空结果，避免把上游故障
// 伪装成「没有搜索结果」。
//
// 副作用：调用搜索编排层（可能读写缓存、调用上游、写检索日志）。
func (h *Handler) deepSeekSearch(w http.ResponseWriter, r *http.Request) {
	settings, err := h.store.RuntimeSettings(r.Context())
	if err != nil {
		writeDeepSeekError(w, http.StatusInternalServerError, deepSeekErrorServer, err.Error())
		return
	}
	if !settings.CompatDeepSeekEnabled {
		writeDeepSeekError(w, http.StatusNotFound, deepSeekErrorNotFound, "deepseek compatibility endpoint is disabled")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeDeepSeekError(w, deepSeekBodyStatus(err), deepSeekErrorInvalidRequest, "invalid body")
		return
	}
	var req compat.DeepSeekSearchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeDeepSeekError(w, http.StatusBadRequest, deepSeekErrorInvalidRequest, "invalid json body")
		return
	}
	native, err := compat.DeepSeekToNative(req)
	if err != nil {
		writeDeepSeekError(w, http.StatusBadRequest, deepSeekErrorInvalidRequest, err.Error())
		return
	}
	// count 无论是否显式传入都固定为官方默认 10 或调用方值，不允许渠道配置覆盖。
	native.LimitExplicit = true
	native.ProvidersExplicit = hasJSONField(body, "providers")
	if err := h.filterTokenProviders(r, &native); err != nil {
		writeDeepSeekError(w, http.StatusForbidden, deepSeekErrorPermission, err.Error())
		return
	}
	response, err := h.orchestrator.Search(r.Context(), native, RequestID(r.Context()), APITokenID(r.Context()))
	if err != nil {
		writeDeepSeekError(w, http.StatusInternalServerError, deepSeekErrorServer, err.Error())
		return
	}
	if deepSeekSearchFailed(response) {
		writeDeepSeekError(w, http.StatusInternalServerError, deepSeekErrorServer, "search failed")
		return
	}
	writeJSON(w, http.StatusOK, compat.DeepSeekFromNative(response, req.NeedContent))
}

// filterTokenProviders 把请求渠道收敛到 API Token 的白名单。
//
// 与原生 /v1/search 的鉴权口径一致：令牌未配置白名单时原样放行；
// 配置了白名单而调用方显式请求了越权渠道时返回错误（调用方按 403 返回）。
// 没有令牌上下文（鉴权关闭）时不做任何改动。
func (h *Handler) filterTokenProviders(r *http.Request, request *model.SearchRequest) error {
	token, ok := APIToken(r.Context())
	if !ok {
		return nil
	}
	filtered, err := applyTokenProviders(request.Providers, token.AllowedProviders)
	if err != nil {
		return err
	}
	request.Providers = filtered
	return nil
}

// deepSeekSearchFailed 判断本次搜索是否所有渠道都失败。
//
// 编排层对「全部渠道出错」只记日志、仍返回空结果，原生接口因此表现为 200；
// DeepSeek 契约要求服务端故障返回 5xx，这里据此上抛。没有执行过任何渠道时
// （例如未配置渠道）不算失败，仍返回空 results。
func deepSeekSearchFailed(response model.SearchResponse) bool {
	if len(response.Results) > 0 || len(response.Providers) == 0 {
		return false
	}
	for _, item := range response.Providers {
		if item.Status != "error" {
			return false
		}
	}
	return true
}

// deepSeekFetch 处理 DeepSeek 兼容抓取 POST /v1/tools/fetch。
//
// 请求只接受 url；响应只含 title 与 content，content 为 Markdown 正文。
// 本服务不采集页面标题，title 按官方约定返回空字符串。正文为空时按官方语义
// 返回 404（未获取到可用内容），SSRF 拦截返回 403，超时返回 504，其余失败返回 500。
//
// 副作用：读取抓取配置、发起真实网络请求、写访问日志。
func (h *Handler) deepSeekFetch(w http.ResponseWriter, r *http.Request) {
	settings, err := h.fetchSettings(r.Context())
	if err != nil {
		writeDeepSeekError(w, http.StatusInternalServerError, deepSeekErrorServer, err.Error())
		return
	}
	if !settings.Enabled {
		writeDeepSeekError(w, http.StatusNotFound, deepSeekErrorNotFound, "fetch endpoint is disabled")
		return
	}
	body, err := readBody(r)
	if err != nil {
		writeDeepSeekError(w, deepSeekBodyStatus(err), deepSeekErrorInvalidRequest, "invalid body")
		return
	}
	var req compat.DeepSeekFetchRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeDeepSeekError(w, http.StatusBadRequest, deepSeekErrorInvalidRequest, "invalid json body")
		return
	}
	args, err := compat.DeepSeekFetchToNative(req)
	if err != nil {
		writeDeepSeekError(w, http.StatusBadRequest, deepSeekErrorInvalidRequest, err.Error())
		return
	}
	request, err := fetch.ParseRequest(args)
	if err != nil {
		writeDeepSeekError(w, http.StatusBadRequest, deepSeekErrorInvalidRequest, err.Error())
		return
	}
	result, err := h.fetcherFor(settings).Fetch(r.Context(), request)
	if err != nil {
		h.writeDeepSeekFetchError(w, r, request.URL.String(), err)
		return
	}
	// TotalLength 为 0 表示归一化后没有任何正文；此时 Result.Content 会是
	// 抓取层的「No more content available.」占位提示，不能当成有效内容返回。
	if result.TotalLength == 0 || strings.TrimSpace(result.Content) == "" {
		writeDeepSeekError(w, http.StatusNotFound, deepSeekErrorNotFound, "no usable content could be extracted from the URL")
		return
	}
	h.logInfo("fetch_done", map[string]interface{}{
		"url": result.URL, "status_code": result.StatusCode, "channel": result.Channel,
		"truncated": result.Truncated, "total_length": result.TotalLength,
		"request_id": RequestID(r.Context()), "compat_format": string(model.CompatFormatDeepSeek),
	})
	writeJSON(w, http.StatusOK, compat.DeepSeekFetchResponse{Content: result.Content})
}

// writeDeepSeekFetchError 把抓取执行体的传输层错误映射为 DeepSeek 契约的状态码。
//
// 官方没有 502：安全策略拒绝是 403，下游超时是 504，其余归入 500。
// 与原生 /v1/fetch 的 502 口径不同是有意的 —— 兼容端点优先对齐官方状态码。
func (h *Handler) writeDeepSeekFetchError(w http.ResponseWriter, r *http.Request, targetURL string, err error) {
	h.logError("fetch_failed", map[string]interface{}{
		"url": targetURL, "error": err.Error(), "request_id": RequestID(r.Context()),
		"compat_format": string(model.CompatFormatDeepSeek),
	})
	switch {
	case errors.Is(err, fetch.ErrPrivateTarget):
		writeDeepSeekError(w, http.StatusForbidden, deepSeekErrorPermission, "fetch was rejected by the security policy")
	case isTimeoutError(err):
		writeDeepSeekError(w, http.StatusGatewayTimeout, deepSeekErrorTimeout, "downstream request timed out")
	default:
		writeDeepSeekError(w, http.StatusInternalServerError, deepSeekErrorServer, err.Error())
	}
}

// isTimeoutError 判断错误是否由超时引起。
// 抓取执行体可能直接返回 context 超时，也可能由 net/http 包装成 net.Error，两种都要识别。
func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// deepSeekBodyStatus 把读取请求体的错误映射为状态码。
// 请求体超过全局上限时 MaxBytesReader 返回 *http.MaxBytesError，官方契约对应 413；
// 其余读取失败按 400 处理。抽成函数是为了让两个兼容处理器共用同一判定。
func deepSeekBodyStatus(err error) int {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
