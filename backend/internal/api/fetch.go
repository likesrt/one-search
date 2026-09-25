package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/one-search/one-search/backend/internal/fetch"
	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
)

// requestBodyLimit 限制抓取端点的请求体大小，防止超大请求体耗尽内存。
const requestBodyLimit = 1 << 20 // 1 MiB

// 抓取端点的错误语义（REST 与管理台试抓一致）：
//   - 功能被禁用 → 404
//   - 参数非法 → 400（writeError 形状）
//   - 传输层失败（连不上 / 超时 / DNS 失败 / 被 SSRF 拦截）→ 502
//   - 成功抓到（含上游返回 4xx/5xx 页面）→ 200 + fetch.Result，其中 status_code 保留上游状态码
//
// 「抓到 404 页面」本身是成功：自定义 API 常把错误详情放在响应体里，只报状态码无法定位问题。

// fetchArgsFromRequest 按 HTTP 方法解析抓取参数。
//
// GET 形态参数取自查询串，且只允许出站 GET：GET 在 HTTP 语义中是安全方法，浏览器预取、
// 爬虫与缓存代理都可能在不经意间触发它，若允许 ?method=POST 就会让带副作用的请求
// 变成可被自动触发的操作。需要出站 POST 时改用 POST /v1/fetch。
//
// POST 形态的请求体为 JSON 对象，字段与 MCP 工具参数完全一致（url、method、headers、
// body、max_length、start_index、raw），便于两种形态共用同一套调用约定。
// 请求体超过 1MiB 时按截断后解析处理，非法 JSON 直接报错。
//
// 返回值：可直接交给 fetch.ParseRequest 的映射；解析失败时返回可读错误（调用方按 400 返回）。
func fetchArgsFromRequest(r *http.Request) (map[string]any, error) {
	if r.Method == http.MethodGet {
		return fetchArgsFromQuery(r)
	}
	return fetchArgsFromBody(r)
}

// fetchArgsFromQuery 解析 GET 形态的查询串参数，并拒绝非 GET 的出站方法。
func fetchArgsFromQuery(r *http.Request) (map[string]any, error) {
	args, err := fetch.ArgsFromQuery(r.URL.Query())
	if err != nil {
		return nil, err
	}
	if method, _ := args["method"].(string); method != "" &&
		!strings.EqualFold(strings.TrimSpace(method), http.MethodGet) {
		return nil, errors.New("Invalid method: GET /v1/fetch only allows an outbound GET; " +
			"use POST /v1/fetch to send a POST")
	}
	return args, nil
}

// fetchArgsFromBody 解析 POST 形态的 JSON 请求体。
//
// 空 body 视为空参数，交由后续校验给出可读错误而不是在这里报解析失败。
// 使用 UseNumber 保留数字原文，避免大整数经 float64 转换后丢精度。
func fetchArgsFromBody(r *http.Request) (map[string]any, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, requestBodyLimit))
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, errors.New("请求体不是合法 JSON 对象")
	}
	return args, nil
}

// fetchSettings 读取抓取配置，并把「是否启用」同步到内存标志。
//
// 同步的原因：MCP 的 tools/list 与 tools/call 只读内存标志、不查库（零值 Handler 因此
// 不会 panic），代价是需要有人把数据库里的最新状态推给它。每次 REST 调用与管理台读写
// 配置都会经过本函数，于是用配置页或任意一次抓取都能让 MCP 侧跟上最新状态。
//
// 返回值：配置与错误；查询失败时返回错误（调用方按 500 处理），此时不修改内存标志。
func (h *Handler) fetchSettings(ctx context.Context) (model.FetchSettings, error) {
	settings, err := h.store.FetchSettings(ctx)
	if err != nil {
		return model.FetchSettings{}, err
	}
	h.setFetchEnabled(settings.Enabled)
	return settings, nil
}

// fetcherFor 返回按当前配置构建的抓取执行体。
//
// 按配置指纹缓存：Fetcher 内部持有连接池（Transport），每次重建会丢弃连接复用，
// 批量抓取时退化为逐条新建连接，因此只在配置真的变化时才重建。
// 指纹基于规范化后的代理地址，避免同一代理的不同写法反复触发重建。
//
// 参数 settings 为本次请求读到的配置（调用方已读库，避免重复查询）。
// 返回值：可并发复用的 *fetch.Fetcher；构建过程不返回错误（代理不可用时内部降级为直连并记日志）。
//
// 副作用：首次使用或配置变化时构造 Fetcher（可能做一次代理域名解析）。
func (h *Handler) fetcherFor(settings model.FetchSettings) *fetch.Fetcher {
	proxyURL := provider.NormalizeProxyURL(settings.ProxyURL)
	key := fmt.Sprintf("%s|%t|%d", proxyURL, settings.AllowPrivate, settings.TimeoutMS)

	h.fetchMu.Lock()
	defer h.fetchMu.Unlock()
	if h.fetcher != nil && h.fetcherKey == key {
		return h.fetcher
	}
	h.fetcher = fetch.NewFetcher(fetch.Config{
		ProxyURL:     proxyURL,
		AllowPrivate: settings.AllowPrivate,
		Timeout:      time.Duration(settings.TimeoutMS) * time.Millisecond,
	})
	h.fetcherKey = key
	return h.fetcher
}

// fetch 处理 GET|POST /v1/fetch。
//
// 两种方法共用同一套参数校验与执行逻辑，差异只在参数来源（查询串 / JSON 请求体），
// 见 fetchArgsFromRequest。抓取结果不写入 search_requests / provider_logs：
// 那两张表是搜索形状（query/mode/渠道字段均为 NOT NULL），无法容纳抓取记录。
//
// 副作用：发起真实网络请求；通过 h.logInfo/logError 写访问日志；不写审计日志
// （与既有对外搜索一致，调用方身份由 loggingMiddleware 的 http_request 日志承载）。
func (h *Handler) fetch(w http.ResponseWriter, r *http.Request) {
	args, err := fetchArgsFromRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.runFetch(w, r, args, false)
}

// runFetch 执行一次抓取并把结果写到响应。
//
// 参数 args 为原始参数映射（来自查询串或 JSON 请求体）；isAdmin 为 true 表示来自
// 管理台试抓，此时额外写一条 fetch.test 审计。
//
// 状态码语义见文件头注释。传输层失败与参数错误分别映射为 502 与 400，
// 且都带可读原因：抓取失败的原因（DNS、TLS、超时、SSRF 拦截）对排错是必要信息。
func (h *Handler) runFetch(w http.ResponseWriter, r *http.Request, args map[string]any, isAdmin bool) {
	settings, err := h.fetchSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !settings.Enabled {
		writeError(w, http.StatusNotFound, "fetch endpoint is disabled")
		return
	}
	request, err := fetch.ParseRequest(args)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.executeFetch(w, r, h.fetcherFor(settings), request, isAdmin)
}

// executeFetch 调用执行体并把结果映射为 HTTP 响应，同时写访问日志与（管理台场景的）审计。
//
// 单独拆出的原因：runFetch 已经承担配置读取、开关判定与参数校验三件事，
// 再把结果映射与日志塞进去会超出函数长度上限。
func (h *Handler) executeFetch(w http.ResponseWriter, r *http.Request, fetcher *fetch.Fetcher, request fetch.Request, isAdmin bool) {
	start := time.Now()
	result, err := fetcher.Fetch(r.Context(), request)
	latency := time.Since(start)
	if err != nil {
		h.logError("fetch_failed", map[string]interface{}{
			"url": request.URL.String(), "error": err.Error(),
			"latency_ms": latency.Milliseconds(), "request_id": RequestID(r.Context()),
		})
		if isAdmin {
			h.audit(r, "admin", "fetch.test", "fetch", request.URL.String(),
				map[string]interface{}{"status": "error", "error": err.Error()})
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.logInfo("fetch_done", map[string]interface{}{
		"url": result.URL, "status_code": result.StatusCode, "truncated": result.Truncated,
		"total_length": result.TotalLength, "latency_ms": latency.Milliseconds(),
		"request_id": RequestID(r.Context()),
	})
	if isAdmin {
		h.audit(r, "admin", "fetch.test", "fetch", request.URL.String(),
			map[string]interface{}{"status": "ok", "status_code": result.StatusCode, "truncated": result.Truncated})
	}
	writeJSON(w, http.StatusOK, result)
}

// getFetchSettings 返回抓取功能配置（GET /api/admin/fetch/settings）。
//
// 返回值始终带默认值（读不到配置行时为启用、直连、30s），因此前端无需处理缺省分支。
// 副作用：把配置中的 enabled 同步到 MCP 侧使用的内存标志。
func (h *Handler) getFetchSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.fetchSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// updateFetchSettings 保存抓取功能配置（PUT /api/admin/fetch/settings）。
//
// 参数校验走 requests 体解码 + 显式范围检查：timeout_ms 必须在 1..60000，
// proxy_url 必须是可解析的代理地址（空串表示直连）。校验失败返回 400，
// 写入失败返回 500，成功回包为入库后的配置（前端以它重置脏检查基线）。
//
// 副作用：写 settings 表；写 fetch.settings.update 审计；同步内存中的 enabled 标志。
func (h *Handler) updateFetchSettings(w http.ResponseWriter, r *http.Request) {
	var payload model.FetchSettings
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if err := validateFetchSettings(payload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.store.UpdateFetchSettings(r.Context(), payload); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	settings, err := h.fetchSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "admin", "fetch.settings.update", "settings", "fetch", map[string]interface{}{
		"enabled": settings.Enabled, "proxy_enabled": settings.ProxyURL != "",
		"allow_private": settings.AllowPrivate, "timeout_ms": settings.TimeoutMS,
	})
	writeJSON(w, http.StatusOK, settings)
}

// validateFetchSettings 校验抓取配置的取值范围。
//
// timeout_ms 上界 60000 与 deploy/nginx.conf 的 proxy_read_timeout(65s) 对齐：
// 配得更长只会先被反代断开，对外表现为 504 而非真实的抓取错误。
// 放入库前统一格式；代理地址格式非法时直接拒绝，避免存入一个永远连不上的值。
//
// 返回值：可直接作为 400 响应体的错误；全部合法时返回 nil。
func validateFetchSettings(settings model.FetchSettings) error {
	if settings.TimeoutMS <= 0 || settings.TimeoutMS > 60000 {
		return errors.New("timeout_ms 必须在 1 到 60000 之间（上限受反向代理 65s 超时约束）")
	}
	raw := strings.TrimSpace(settings.ProxyURL)
	if raw == "" {
		return nil
	}
	if provider.NormalizeProxyURL(raw) == "" {
		return errors.New("proxy_url 为空或无法解析")
	}
	return nil
}

// testFetch 管理台试抓（POST /api/admin/fetch/test）。
//
// 请求体与 /v1/fetch 的 POST 形态完全一致，因此管理台可以用同一套参数构造请求；
// 响应格式与状态码语义也与对外端点一致（禁用 404、参数非法 400、传输失败 502、成功 200）。
//
// 副作用：发起真实网络请求；成功与失败都会写一条 fetch.test 审计。
func (h *Handler) testFetch(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	h.runFetch(w, r, payload, true)
}
