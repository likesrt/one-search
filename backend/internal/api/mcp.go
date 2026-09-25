package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/one-search/one-search/backend/internal/fetch"
	"github.com/one-search/one-search/backend/internal/model"
)

const (
	mcpLatestProtocolVersion  = "2025-06-18"
	mcpDefaultProtocolVersion = "2025-03-26"
)

var mcpSupportedProtocolVersions = []string{mcpLatestProtocolVersion, mcpDefaultProtocolVersion, "2024-11-05"}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *mcpError       `json:"error,omitempty"`
}

type mcpError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (h *Handler) mountMCP(r chi.Router, path string) {
	h.mountMCPPath(r, path)
	if strings.HasSuffix(path, "/") {
		trimmed := strings.TrimRight(path, "/")
		if trimmed != "" {
			h.mountMCPPath(r, trimmed)
		}
		return
	}
	h.mountMCPPath(r, path+"/")
}

func (h *Handler) mountMCPPath(r chi.Router, path string) {
	r.Get(path, h.mcpInfo)
	r.Post(path, h.mcp)
	r.Delete(path, h.mcpDelete)
}

func (h *Handler) mcpInfo(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":                     true,
		"transport":                   "streamable-http",
		"protocol_version":            mcpLatestProtocolVersion,
		"supported_protocol_versions": mcpSupportedProtocolVersions,
		"endpoint":                    r.URL.Path,
		"auth":                        "Authorization: Bearer <osr_...|oak_...> or X-API-Key",
		"tools":                       h.mcpToolNames(),
	})
}

// mcpToolNames 返回当前对外暴露的工具名清单。
//
// 与 tools/list 共用同一份判定，避免两处不一致：禁用抓取后元信息仍列出 fetch 会让
// 自检脚本误判服务状态（`curl /mcp` 是最常用的连通性检查）。
//
// 返回值：工具名切片，search 恒在，fetch 仅在功能启用时追加。
// 副作用：无（只读内存标志，不查库）。
func (h *Handler) mcpToolNames() []string {
	names := []string{"search"}
	if h.isFetchEnabled() {
		names = append(names, fetch.ToolName)
	}
	return names
}

// mcpToolSchemas 返回 tools/list 的完整工具定义清单。
//
// 顺序与 mcpToolNames 一致：search 在前、fetch 在后，元信息与清单可逐一对应。
// 边界条件：功能禁用时返回单元素切片（仅 search），而非 nil，
// 让 JSON 输出始终是数组，客户端无需处理 null。
func (h *Handler) mcpToolSchemas() []interface{} {
	schemas := []interface{}{mcpSearchToolSchema()}
	if h.isFetchEnabled() {
		schemas = append(schemas, fetch.ToolSchema())
	}
	return schemas
}

func (h *Handler) mcpDelete(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (h *Handler) mcp(w http.ResponseWriter, r *http.Request) {
	body, err := readBody(r)
	if err != nil {
		writeMCPError(w, http.StatusBadRequest, nil, -32700, "invalid body", nil)
		return
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		writeMCPError(w, http.StatusBadRequest, nil, -32700, "empty json-rpc body", nil)
		return
	}

	if trimmed[0] == '[' {
		var requests []mcpRequest
		if err := json.Unmarshal(trimmed, &requests); err != nil {
			writeMCPError(w, http.StatusBadRequest, nil, -32700, "parse error", err.Error())
			return
		}
		if h.mcpRequestsRequireAuth(requests) {
			ctx, authStatus, authMessage, err := h.mcpAuthContext(r)
			if err != nil {
				writeMCPError(w, authStatus, firstMCPRequestID(trimmed), -32001, authMessage, nil)
				return
			}
			r = r.WithContext(ctx)
		}
		h.handleMCPBatch(w, r, requests)
		return
	}

	var req mcpRequest
	if err := json.Unmarshal(trimmed, &req); err != nil {
		writeMCPError(w, http.StatusBadRequest, nil, -32700, "parse error", err.Error())
		return
	}
	if h.mcpRequestsRequireAuth([]mcpRequest{req}) {
		ctx, authStatus, authMessage, err := h.mcpAuthContext(r)
		if err != nil {
			writeMCPError(w, authStatus, req.ID, -32001, authMessage, nil)
			return
		}
		r = r.WithContext(ctx)
	}
	response, ok := h.handleMCPRequest(r, req)
	if !ok {
		writeMCPAccepted(w)
		return
	}
	writeMCPResponse(w, http.StatusOK, response)
}

func (h *Handler) handleMCPBatch(w http.ResponseWriter, r *http.Request, requests []mcpRequest) {
	if len(requests) == 0 {
		writeMCPError(w, http.StatusBadRequest, nil, -32600, "empty batch is not allowed", nil)
		return
	}
	responses := make([]mcpResponse, 0, len(requests))
	for _, req := range requests {
		response, ok := h.handleMCPRequest(r, req)
		if ok {
			responses = append(responses, response)
		}
	}
	if len(responses) == 0 {
		writeMCPAccepted(w)
		return
	}
	writeMCPBatchResponse(w, http.StatusOK, responses)
}

func (h *Handler) handleMCPRequest(r *http.Request, req mcpRequest) (mcpResponse, bool) {
	if req.ID == nil {
		h.handleMCPNotification(r, req)
		return mcpResponse{}, false
	}
	if req.JSONRPC != "2.0" {
		return newMCPError(req.ID, -32600, "jsonrpc must be 2.0", nil), true
	}
	if req.Method == "" {
		return newMCPError(req.ID, -32600, "method is required", nil), true
	}

	switch req.Method {
	case "initialize":
		return newMCPResult(req.ID, mcpInitializeResult(req.Params)), true
	case "ping":
		return newMCPResult(req.ID, map[string]interface{}{}), true
	case "tools/list":
		return newMCPResult(req.ID, map[string]interface{}{"tools": h.mcpToolSchemas()}), true
	case "tools/call":
		result, errResp := h.handleMCPToolCall(r, req)
		if errResp != nil {
			return *errResp, true
		}
		return newMCPResult(req.ID, result), true
	case "resources/list", "prompts/list":
		key := "resources"
		if req.Method == "prompts/list" {
			key = "prompts"
		}
		return newMCPResult(req.ID, map[string]interface{}{key: []interface{}{}}), true
	case "resources/templates/list":
		return newMCPResult(req.ID, map[string]interface{}{"resourceTemplates": []interface{}{}}), true
	default:
		return newMCPError(req.ID, -32601, "method not found", req.Method), true
	}
}

func (h *Handler) handleMCPNotification(r *http.Request, req mcpRequest) {
	_ = r
	_ = req
}

// handleMCPToolCall 解析 tools/call 的参数并按工具名分发。
//
// 参数解析失败与「工具名未知」都走 JSON-RPC 错误（-32602），因为二者都属于协议层问题；
// 工具自身的执行失败则以工具结果的 isError 表达（见 mcpCallSearch / mcpCallFetch），
// 便于模型读到具体原因并自行修正参数。
//
// 边界条件：params 缺失或非法返回 -32602。fetch 被禁用时**不**按「未知工具」处理，
// 而是走 mcpCallFetch 返回说明配置状态的工具结果 —— 让「隐藏」与「拒绝」共用同一判定。
func (h *Handler) handleMCPToolCall(r *http.Request, req mcpRequest) (interface{}, *mcpResponse) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(req.Params) == 0 {
		return nil, mcpInvalidParams(req.ID, "params are required")
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, mcpInvalidParams(req.ID, "invalid params")
	}
	switch params.Name {
	case "search":
		return h.mcpCallSearch(r, req, params.Arguments)
	case fetch.ToolName:
		return h.mcpCallFetch(r, params.Arguments)
	default:
		return nil, mcpInvalidParams(req.ID, "unknown tool: "+params.Name)
	}
}

// mcpCallSearch 执行 MCP 的 search 工具调用。
//
// 参数：
//   - r：原始 HTTP 请求，用于取 context 中的令牌身份与 request id。
//   - req：JSON-RPC 请求，失败时用其 id 组装错误响应。
//   - arguments：工具参数的原始 JSON，可为空（此时按缺少 query 处理）。
//
// 返回值：成功时为工具结果（含文本与 structuredContent），失败时为 JSON-RPC 错误响应；
// 搜索链路自身的错误改为工具结果的 isError，让模型能看到原因。
//
// 边界条件：查询词为空返回 -32602；令牌限定了允许渠道但请求越权时返回 -32003。
// 副作用：发起真实搜索（含缓存读写与日志落库，见 Orchestrator）。
func (h *Handler) mcpCallSearch(r *http.Request, req mcpRequest, arguments json.RawMessage) (interface{}, *mcpResponse) {
	var searchReq model.SearchRequest
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &searchReq); err != nil {
			return nil, mcpInvalidParams(req.ID, "invalid search arguments")
		}
	}
	searchReq.Query = strings.TrimSpace(searchReq.Query)
	if searchReq.Query == "" {
		return nil, mcpInvalidParams(req.ID, "query is required")
	}
	searchReq.LimitExplicit = hasJSONField(arguments, "limit")
	searchReq.ProvidersExplicit = hasJSONField(arguments, "providers")
	searchReq.CompatFormat = model.CompatFormatNative
	if searchReq.Options == nil {
		searchReq.Options = map[string]interface{}{}
	}
	searchReq.Options["source"] = "mcp"

	if token, ok := APIToken(r.Context()); ok {
		filtered, err := applyTokenProviders(searchReq.Providers, token.AllowedProviders)
		if err != nil {
			return nil, &mcpResponse{JSONRPC: "2.0", ID: req.ID, Error: &mcpError{Code: -32003, Message: err.Error()}}
		}
		searchReq.Providers = filtered
	}

	response, err := h.orchestrator.Search(r.Context(), searchReq, RequestID(r.Context()), APITokenID(r.Context()))
	if err != nil {
		return mcpToolError(err.Error()), nil
	}
	payload, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return mcpToolError(err.Error()), nil
	}
	return map[string]interface{}{
		"content":           []mcpContent{{Type: "text", Text: string(payload)}},
		"structuredContent": response,
		"isError":           false,
	}, nil
}

// mcpCallFetch 执行 MCP 的 fetch 工具调用。
//
// 入口即强制校验功能开关：工具清单不是调用授权，即使某客户端缓存了旧的 tools/list、
// 或绕过清单直接发 tools/call{name:"fetch"}，禁用时也必须拒绝。拒绝用工具结果的
// isError 而不是 JSON-RPC 错误 —— 前者是「配置状态、可恢复」，后者是协议层错误，
// 两者对模型的含义不同。
//
// 参数与返回值语义同 mcpCallSearch，但参数非法也走工具结果的 isError（而非 -32602）：
// 抓取参数里的 url 常来自模型自行拼装，用 isError 把原因原样回传比协议错误更易自纠。
//
// 副作用：发起真实网络请求；读取一次抓取配置（用于构建/复用 Fetcher）。
func (h *Handler) mcpCallFetch(r *http.Request, arguments json.RawMessage) (interface{}, *mcpResponse) {
	if !h.isFetchEnabled() {
		return mcpToolError("抓取功能已禁用（可在管理台「网页抓取」页开启）"), nil
	}
	args := map[string]any{}
	if len(arguments) > 0 {
		if err := json.Unmarshal(arguments, &args); err != nil {
			return mcpToolError("invalid fetch arguments"), nil
		}
	}
	settings, err := h.fetchSettings(r.Context())
	if err != nil {
		return mcpToolError(err.Error()), nil
	}
	request, err := fetch.ParseRequest(args)
	if err != nil {
		return mcpToolError(err.Error()), nil
	}
	result, err := h.fetcherFor(settings).Fetch(r.Context(), request)
	if err != nil {
		return mcpToolError(err.Error()), nil
	}
	return mcpToolResult(result.Text()), nil
}

func (h *Handler) mcpRequestsRequireAuth(requests []mcpRequest) bool {
	for _, req := range requests {
		if mcpMethodRequiresAuth(req.Method) {
			return true
		}
	}
	return false
}

func mcpMethodRequiresAuth(method string) bool {
	switch method {
	case "", "initialize", "notifications/initialized", "ping", "tools/list", "resources/list", "resources/templates/list", "prompts/list":
		return false
	default:
		return true
	}
}

func (h *Handler) mcpAuthContext(r *http.Request) (context.Context, int, string, error) {
	settings, err := h.store.RuntimeSettings(r.Context())
	if err != nil {
		return r.Context(), http.StatusInternalServerError, err.Error(), err
	}
	if !settings.APIAuthRequired {
		return r.Context(), http.StatusOK, "", nil
	}
	token := bearerToken(r)
	if token == "" {
		return r.Context(), http.StatusUnauthorized, "api token required", fmt.Errorf("api token required")
	}
	adminKey, ok, err := h.store.FindAdminAPIKey(r.Context(), token)
	if err != nil {
		return r.Context(), http.StatusInternalServerError, err.Error(), err
	}
	if ok {
		return context.WithValue(r.Context(), adminActorKey, adminAPIKeyActor(adminKey)), http.StatusOK, "", nil
	}
	apiToken, err := h.store.FindAPIToken(r.Context(), token)
	if err != nil {
		return r.Context(), http.StatusUnauthorized, "invalid api token", err
	}
	if !h.auth.allowToken(apiToken) {
		return r.Context(), http.StatusTooManyRequests, "api token rate limit exceeded", fmt.Errorf("api token rate limit exceeded")
	}
	ctx := context.WithValue(r.Context(), apiTokenIDKey, apiToken.ID)
	ctx = context.WithValue(ctx, apiTokenKey, apiToken)
	return ctx, http.StatusOK, "", nil
}

// mcpInitializeResult 构造 initialize 方法的返回结果，向客户端声明协议版本、能力集与服务端身份。
//
// 参数：
//   - params：客户端 initialize 请求的原始 params，其中 protocolVersion 用于版本协商。
//
// 返回值：符合 MCP initialize 结果结构的 map，含 protocolVersion、capabilities、
// serverInfo 与 instructions。
//
// 边界条件：params 为空、解析失败或 protocolVersion 不受支持时，版本协商回退为默认版本。
// instructions 是模型理解本服务用途的主要入口，措辞必须明确指向「实时联网」的两项能力：
// 搜索与网页抓取。只写搜索会让模型在需要读某个具体页面时不知道还有 fetch 可用，
// 从而退化为臆造内容。
//
// 副作用：无。
func mcpInitializeResult(params json.RawMessage) map[string]interface{} {
	return map[string]interface{}{
		"protocolVersion": negotiateMCPProtocolVersion(params),
		"capabilities": map[string]interface{}{
			"tools":     map[string]interface{}{"listChanged": false},
			"resources": map[string]interface{}{"listChanged": false},
			"prompts":   map[string]interface{}{"listChanged": false},
		},
		"serverInfo": map[string]interface{}{
			"name":    "one-search-relay",
			"title":   "One Search Relay",
			"version": "0.1.0",
		},
		"instructions": "Use the search tool to run live web searches on the public internet and get ranked results with titles, URLs and snippets. Call it whenever the user asks about recent events, current or up-to-date facts, or anything that requires information from the web. Use the fetch tool to read a specific URL — a page the user linked or a result returned by search — and get its content as compact Markdown; it fetches the page over plain HTTP and does not run JavaScript, so client-rendered pages come back empty.",
	}
}

func negotiateMCPProtocolVersion(params json.RawMessage) string {
	if len(params) == 0 {
		return mcpDefaultProtocolVersion
	}
	var payload struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return mcpDefaultProtocolVersion
	}
	for _, version := range mcpSupportedProtocolVersions {
		if payload.ProtocolVersion == version {
			return version
		}
	}
	return mcpDefaultProtocolVersion
}

// mcpSearchToolSchema 构造 tools/list 返回的 search 工具定义。
//
// 参数：无。
//
// 返回值：符合 MCP 工具描述结构的 map，含 name、title、description、
// inputSchema、annotations。
//
// 边界条件：inputSchema 仅声明 query、providers、mode、limit、freshness、
// dedupe、cache、include_raw 八个字段，不含原生接口的 options 与 rerank；
// 其中只有 query 为必填。description 直接决定模型是否选中本工具，必须显式点明
// 「实时联网搜索」并给出适用场景，避免被误判为本地知识库检索。
//
// 副作用：无。
func mcpSearchToolSchema() map[string]interface{} {
	return map[string]interface{}{
		"name":        "search",
		"title":       "One Search",
		"description": "Search the live public web and return ranked results with titles, URLs and snippets. Use this tool for recent events, up-to-date facts, or any question that needs information from the open internet.",
		"inputSchema": map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{
					"type":        "string",
					"description": "Search query.",
				},
				"providers": map[string]interface{}{
					"type":        "array",
					"description": "Optional providers to use. Defaults to runtime settings.",
					"items":       map[string]interface{}{"type": "string", "enum": model.DefaultProviders},
				},
				"mode": map[string]interface{}{
					"type":        "string",
					"description": "Search mode.",
					"enum":        []string{string(model.SearchModeParallel), string(model.SearchModeFallback), string(model.SearchModeSingle)},
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum results, capped at 50.",
					"minimum":     1,
					"maximum":     50,
				},
				"freshness": map[string]interface{}{
					"type":        "string",
					"description": "Optional freshness hint.",
				},
				"dedupe": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to deduplicate results by URL.",
				},
				"cache": map[string]interface{}{
					"type":        "string",
					"description": "Cache policy.",
					"enum":        []string{string(model.CachePolicyDefault), string(model.CachePolicyBypass), string(model.CachePolicyRefresh)},
				},
				"include_raw": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to include raw upstream result items.",
				},
			},
			"required": []string{"query"},
		},
		"annotations": map[string]interface{}{
			"title":         "One Search",
			"readOnlyHint":  true,
			"openWorldHint": true,
		},
	}
}

// mcpToolError 构造失败的工具结果。
//
// 用 isError 而非 JSON-RPC 错误表达执行失败：模型能读到具体原因并自行修正参数重试，
// 而协议层错误通常会被客户端直接抛给用户。
func mcpToolError(message string) map[string]interface{} {
	return map[string]interface{}{
		"content": []mcpContent{{Type: "text", Text: message}},
		"isError": true,
	}
}

// mcpToolResult 构造成功的工具结果（单条 text 内容）。
//
// 与 mcpToolError 成对使用，保证成功与失败两条路径的结果结构一致
// （都只含 content 与 isError），客户端无需按错误与否分别解析。
func mcpToolResult(text string) map[string]interface{} {
	return map[string]interface{}{
		"content": []mcpContent{{Type: "text", Text: text}},
		"isError": false,
	}
}

func mcpInvalidParams(id json.RawMessage, message string) *mcpResponse {
	response := newMCPError(id, -32602, message, nil)
	return &response
}

func newMCPResult(id json.RawMessage, result interface{}) mcpResponse {
	return mcpResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func newMCPError(id json.RawMessage, code int, message string, data interface{}) mcpResponse {
	if id == nil {
		id = json.RawMessage("null")
	}
	return mcpResponse{JSONRPC: "2.0", ID: id, Error: &mcpError{Code: code, Message: message, Data: data}}
}

func writeMCPError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string, data interface{}) {
	writeMCPResponse(w, status, newMCPError(id, code, message, data))
}

func writeMCPAccepted(w http.ResponseWriter) {
	w.Header().Set("Mcp-Protocol-Version", mcpLatestProtocolVersion)
	w.WriteHeader(http.StatusAccepted)
}

func writeMCPResponse(w http.ResponseWriter, status int, response mcpResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Mcp-Protocol-Version", mcpLatestProtocolVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(response)
}

func writeMCPBatchResponse(w http.ResponseWriter, status int, responses []mcpResponse) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Mcp-Protocol-Version", mcpLatestProtocolVersion)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(responses)
}

func firstMCPRequestID(body []byte) json.RawMessage {
	if len(body) == 0 {
		return nil
	}
	if body[0] == '[' {
		var requests []mcpRequest
		if err := json.Unmarshal(body, &requests); err == nil && len(requests) > 0 {
			return requests[0].ID
		}
		return nil
	}
	var req mcpRequest
	if err := json.Unmarshal(body, &req); err == nil {
		return req.ID
	}
	return nil
}
