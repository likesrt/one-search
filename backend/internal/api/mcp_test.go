package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/one-search/one-search/backend/internal/model"
)

// mcpToolSchema 是单个 MCP 工具的响应结构，便于按名查找与断言字段。
type mcpToolSchema struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

// mcpToolsResponse 是 tools/list 的响应结构，独立成类型避免每个用例重复声明匿名结构。
type mcpToolsResponse struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Result  struct {
		Tools []mcpToolSchema `json:"tools"`
	} `json:"result"`
}

// mcpToolByName 按名查找工具。
//
// 现有测试曾按索引 0 取工具，加入第二个工具后该写法必然错位；
// 工具顺序不是协议保证的一部分，断言必须按名字而不是位置。
// 未命中返回 (零值, false)，由调用方给出具体失败信息。
func mcpToolByName(tools []mcpToolSchema, name string) (mcpToolSchema, bool) {
	for _, item := range tools {
		if item.Name == name {
			return item, true
		}
	}
	return mcpToolSchema{}, false
}

// mcpTestStore 是只实现 MCP 工具调用路径所需两个方法的 AppStore 替身。
//
// 用「内嵌接口」而非逐条实现：AppStore 有三十多个方法，全部实现既冗长又与本次改动无关；
// 内嵌未实现的 AppStore 接口即可满足类型要求，只要测试不触及其余方法就不会 panic。
// 之所以需要替身：tools/call 属于需鉴权的 MCP 方法，鉴权会读 RuntimeSettings，
// 零值 Handler 的 store 为 nil 会在那里 panic（这是既有行为，不是本次改动引入的）。
type mcpTestStore struct {
	AppStore
	fetchSettings model.FetchSettings
}

// RuntimeSettings 返回关闭鉴权的配置，让 tools/call 免令牌即可进入分发逻辑。
func (s *mcpTestStore) RuntimeSettings(context.Context) (model.RuntimeSettings, error) {
	return model.RuntimeSettings{APIAuthRequired: false}, nil
}

// FetchSettings 返回用例设定的抓取配置，用于验证启用/禁用两条路径。
func (s *mcpTestStore) FetchSettings(context.Context) (model.FetchSettings, error) {
	return s.fetchSettings, nil
}

// mcpPost 向 MCP 路由发送一个 JSON-RPC 请求并返回响应记录器。
func mcpPost(t *testing.T, r chi.Router, payload string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// mcpListTools 请求 tools/list 并解析结果，失败即终止用例。
func mcpListTools(t *testing.T, r chi.Router) mcpToolsResponse {
	t.Helper()
	rec := mcpPost(t, r, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp mcpToolsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	return resp
}

func TestMCPStreamableHTTPHandshakeAndListTools(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	post := func(payload string) *httptest.ResponseRecorder { return mcpPost(t, r, payload) }

	initRec := post(`{
		"jsonrpc":"2.0",
		"id":1,
		"method":"initialize",
		"params":{
			"protocolVersion":"2025-06-18",
			"capabilities":{},
			"clientInfo":{"name":"test-client","version":"1.0.0"}
		}
	}`)
	if initRec.Code != http.StatusOK {
		t.Fatalf("initialize status = %d, body = %s", initRec.Code, initRec.Body.String())
	}
	var initResp struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools map[string]interface{} `json:"tools"`
			} `json:"capabilities"`
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initRec.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if initResp.JSONRPC != "2.0" || initResp.ID != 1 || initResp.Result.ProtocolVersion != "2025-06-18" {
		t.Fatalf("unexpected initialize response: %+v", initResp)
	}
	if initResp.Result.ServerInfo.Name != "one-search-relay" || initResp.Result.ServerInfo.Version == "" {
		t.Fatalf("unexpected serverInfo: %+v", initResp.Result.ServerInfo)
	}
	if initResp.Result.Capabilities.Tools == nil {
		t.Fatalf("tools capability missing: %+v", initResp.Result.Capabilities)
	}

	initializedRec := post(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if initializedRec.Code != http.StatusAccepted {
		t.Fatalf("initialized status = %d, body = %s", initializedRec.Code, initializedRec.Body.String())
	}

	toolsResp := mcpListTools(t, r)
	// 零值 Handler 下 fetch 未启用，因此只列出 search；
	// 这同时验证了「tools/list 不查库」——store 为 nil 也不会 panic。
	if toolsResp.JSONRPC != "2.0" || toolsResp.ID != 2 || len(toolsResp.Result.Tools) != 1 {
		t.Fatalf("unexpected tools/list response: %+v", toolsResp)
	}
	tool, ok := mcpToolByName(toolsResp.Result.Tools, "search")
	if !ok {
		t.Fatalf("search tool missing: %+v", toolsResp.Result.Tools)
	}
	if tool.Name != "search" || tool.Description == "" || tool.InputSchema["type"] != "object" {
		t.Fatalf("unexpected tool schema: %+v", tool)
	}
	properties, ok := tool.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool properties missing: %+v", tool.InputSchema)
	}
	providers, ok := properties["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers schema missing: %+v", properties)
	}
	items, ok := providers["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers items schema missing: %+v", providers)
	}
	enumValues, ok := items["enum"].([]interface{})
	if !ok || len(enumValues) != len(model.DefaultProviders) {
		t.Fatalf("unexpected providers enum: %+v", items["enum"])
	}
	for index, provider := range model.DefaultProviders {
		if enumValues[index] != provider {
			t.Fatalf("providers enum[%d] = %v, want %s", index, enumValues[index], provider)
		}
	}
}

// TestMCPLimitAdviceIsConsistentAcrossInstructionsAndSchema 验证给模型的 limit 建议值
// 在 instructions 与 search 工具 schema 里保持一致。
//
// 两处文案都由常量 mcpRecommendedLimit 生成，本用例是防回归的哨兵：若有人只把其中一处
// 写回硬编码，建议值就会分叉，模型收到的两条信息自相矛盾。
//
// 边界条件：只断言「包含建议值」，不做逐字比对，避免文案微调即失败。
func TestMCPLimitAdviceIsConsistentAcrossInstructionsAndSchema(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	initRec := mcpPost(t, r, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	var initResp struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(initRec.Body.Bytes(), &initResp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	advice := strconv.Itoa(mcpRecommendedLimit)
	if !strings.Contains(initResp.Result.Instructions, advice) {
		t.Fatalf("instructions 未包含建议值 %s: %q", advice, initResp.Result.Instructions)
	}

	toolsResp := mcpListTools(t, r)
	tool, ok := mcpToolByName(toolsResp.Result.Tools, "search")
	if !ok {
		t.Fatalf("search tool missing: %+v", toolsResp.Result.Tools)
	}
	properties, ok := tool.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool properties missing: %+v", tool.InputSchema)
	}
	limitSchema, ok := properties["limit"].(map[string]interface{})
	if !ok {
		t.Fatalf("limit schema missing: %+v", properties)
	}
	description, _ := limitSchema["description"].(string)
	if !strings.Contains(description, advice) {
		t.Fatalf("limit 描述未包含建议值 %s: %q", advice, description)
	}
}

// TestMCPGuidesModelToContext7First 验证「文档渠道优先」的引导同时落在两处：
// initialize 的 instructions 与 search 工具 schema 里 providers 的 description。
// 只写其中一处不够：enum 里多一个陌生的 context7 而没有说明时，模型不会主动选它。
//
// 边界条件：只断言「提到 context7」这一定性事实，不做逐字比对，避免文案微调即失败。
// TestMCPGuidesModelToContext7First 验证三段引导文案里都提到了 context7。
//
// 覆盖 instructions、search 工具的 description 与 providers 字段的 description 三处：
// 三处的失效方式互不相同 —— instructions 客户端可以整段丢弃，工具 description 可能被
// 客户端截断，providers 字段的 description 只在 schema 全量下发时才可见，
// 因此任一处的引导都不足以单独兜底，必须分别断言。
func TestMCPGuidesModelToContext7First(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	initRec := mcpPost(t, r, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
	if !strings.Contains(mcpInstructions(t, initRec), model.ProviderContext7) {
		t.Fatalf("instructions 未提及 context7")
	}

	toolsResp := mcpListTools(t, r)
	tool, ok := mcpToolByName(toolsResp.Result.Tools, "search")
	if !ok {
		t.Fatalf("search tool missing: %+v", toolsResp.Result.Tools)
	}
	if !strings.Contains(tool.Description, model.ProviderContext7) {
		t.Fatalf("search 工具 description 未提及 context7: %q", tool.Description)
	}
	if !strings.Contains(mcpProvidersDescription(t, tool), model.ProviderContext7) {
		t.Fatalf("providers 描述未提及 context7")
	}
}

// TestMCPProvidersDescriptionWarnsEnumIsNotAvailability 锁定 providers 描述里的
// 「合法值是全集，不等于可用集」这句免责声明。
//
// 为什么值得单独立一条用例：enum 只是 schema 层的可取值清单，服务端并不据此校验，
// 被停用或缺 key 的渠道会静默跳过或报错。一旦这句被删（例如为了缩短文案），
// 模型会把「这个渠道没结果」误判成「这个渠道查不到」，反复重试同一个不可用渠道，
// 而这种回归在功能测试里完全看不出来 —— 只有文案层面能挡住。
func TestMCPProvidersDescriptionWarnsEnumIsNotAvailability(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	toolsResp := mcpListTools(t, r)
	tool, ok := mcpToolByName(toolsResp.Result.Tools, "search")
	if !ok {
		t.Fatalf("search tool missing: %+v", toolsResp.Result.Tools)
	}
	description := mcpProvidersDescription(t, tool)
	// 只断言「不保证可用」这层语义的关键措辞，不做逐字比对，避免文案微调即失败。
	if !strings.Contains(description, "not a guarantee") {
		t.Fatalf("providers 描述缺少「枚举不等于可用」的说明: %q", description)
	}
	if !strings.Contains(description, "API key") {
		t.Fatalf("providers 描述未说明缺 key 的渠道不可用: %q", description)
	}
}

// mcpInstructions 从 initialize 响应体里取出 instructions 文本。
// 参数 rec 为 initialize 的响应记录；解析失败或字段缺失时终止测试。
func mcpInstructions(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Result struct {
			Instructions string `json:"instructions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if resp.Result.Instructions == "" {
		t.Fatalf("instructions 为空: %s", rec.Body.String())
	}
	return resp.Result.Instructions
}

// mcpProvidersDescription 从 search 工具的 inputSchema 里取出 providers 字段的 description。
// 参数 tool 为 search 工具定义；schema 结构不符或 description 不是字符串时终止测试并打印实际结构。
func mcpProvidersDescription(t *testing.T, tool mcpToolSchema) string {
	t.Helper()
	properties, ok := tool.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("tool properties missing: %+v", tool.InputSchema)
	}
	providers, ok := properties["providers"].(map[string]interface{})
	if !ok {
		t.Fatalf("providers schema missing: %+v", properties)
	}
	description, ok := providers["description"].(string)
	if !ok || description == "" {
		t.Fatalf("providers description missing: %+v", providers)
	}
	return description
}

// TestMCPToolsListIncludesFetchWhenEnabled 验证启用后清单同时含 search 与 fetch。
func TestMCPToolsListIncludesFetchWhenEnabled(t *testing.T) {
	h := &Handler{}
	h.EnableFetch(true)
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	resp := mcpListTools(t, r)
	if len(resp.Result.Tools) != 2 {
		t.Fatalf("启用时应列出 2 个工具，实际 %d: %+v", len(resp.Result.Tools), resp.Result.Tools)
	}
	for _, name := range []string{"search", "fetch"} {
		if _, ok := mcpToolByName(resp.Result.Tools, name); !ok {
			t.Fatalf("工具 %s 未出现在清单中: %+v", name, resp.Result.Tools)
		}
	}
	fetchTool, _ := mcpToolByName(resp.Result.Tools, "fetch")
	input, ok := fetchTool.InputSchema["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("fetch schema 缺少 properties: %+v", fetchTool.InputSchema)
	}
	if _, ok := input["url"]; !ok {
		t.Fatalf("fetch schema 缺少 url 参数: %+v", input)
	}
	// 代理是管理员级全局配置，schema 不应暴露请求级 proxy
	if _, ok := input["proxy"]; ok {
		t.Fatal("fetch schema 不应暴露 proxy 参数")
	}
}

// TestMCPFetchDisabledIsRejectedAndHidden 验证禁用时「隐藏」与「拒绝」两条路径一致：
// 清单里没有 fetch，且直接发 tools/call 也会被拒（工具清单不是调用授权）。
func TestMCPFetchDisabledIsRejectedAndHidden(t *testing.T) {
	h := &Handler{store: &mcpTestStore{}}
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	resp := mcpListTools(t, r)
	if _, ok := mcpToolByName(resp.Result.Tools, "fetch"); ok {
		t.Fatalf("禁用时不应列出 fetch: %+v", resp.Result.Tools)
	}

	callRec := mcpPost(t, r, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"fetch","arguments":{"url":"https://example.com"}}}`)
	if callRec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, body = %s", callRec.Code, callRec.Body.String())
	}
	var callResp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(callRec.Body.Bytes(), &callResp); err != nil {
		t.Fatalf("decode tools/call response: %v", err)
	}
	// 用工具结果的 isError 而非 JSON-RPC 错误：前者是「配置状态、可恢复」，与
	// 「未知工具名」这类协议层错误区分开
	if !callResp.Result.IsError || len(callResp.Result.Content) == 0 {
		t.Fatalf("禁用时 tools/call 应返回 isError 工具结果: %s", callRec.Body.String())
	}
	if !strings.Contains(callResp.Result.Content[0].Text, "抓取功能已禁用") {
		t.Fatalf("拒绝原因应说明功能已禁用，实际: %q", callResp.Result.Content[0].Text)
	}
}

// TestMCPInfoToolsMatchToolsList 验证 /mcp 元信息与 tools/list 同源。
//
// 不同源时禁用抓取后元信息仍会列出 fetch，而 `curl /mcp` 是最常用的自检手段，
// 会让排错方向跑偏。
func TestMCPInfoToolsMatchToolsList(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "禁用", true: "启用"}[enabled], func(t *testing.T) {
			h := &Handler{}
			h.EnableFetch(enabled)
			r := chi.NewRouter()
			h.mountMCP(r, "/mcp")

			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /mcp status = %d", rec.Code)
			}
			var info struct {
				Tools []string `json:"tools"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
				t.Fatalf("decode info response: %v", err)
			}

			list := mcpListTools(t, r)
			if len(info.Tools) != len(list.Result.Tools) {
				t.Fatalf("元信息 tools=%v 与清单数量不一致: %+v", info.Tools, list.Result.Tools)
			}
			for _, name := range info.Tools {
				if _, ok := mcpToolByName(list.Result.Tools, name); !ok {
					t.Fatalf("元信息列出的 %s 不在 tools/list 中: %+v", name, list.Result.Tools)
				}
			}
		})
	}
}

// TestMCPUnknownToolStillReturnsProtocolError 验证未知工具名仍是 JSON-RPC 错误，
// 与「已知但被禁用」的工具结果 isError 区分开。
func TestMCPUnknownToolStillReturnsProtocolError(t *testing.T) {
	h := &Handler{store: &mcpTestStore{}}
	h.EnableFetch(true)
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	rec := mcpPost(t, r, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"nope","arguments":{}}}`)
	var resp struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != -32602 {
		t.Fatalf("未知工具名应返回 -32602，实际: %s", rec.Body.String())
	}
}

func TestMCPStreamableHTTPGetSSEIsNotOffered(t *testing.T) {
	h := &Handler{}
	r := chi.NewRouter()
	h.mountMCP(r, "/mcp")

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET SSE status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
