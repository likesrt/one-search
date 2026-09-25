package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/search"
)

// keyQuota 手动查询单个密钥的官方额度（POST /api/admin/keys/{id}/quota）。
// 代理按 key 级三态解析（custom 用 key 自己的地址、direct 强制直连、inherit 回退渠道级），
// 与搜索路径口径一致；请求体里的 proxy_url 恒被忽略（ProviderKeyQuotaRequest 该字段 json:"-"），
// 防止客户端注入任意代理绕过管理台配置。
// 查询失败不返回 4xx/5xx：HTTP 200，错误放在响应体的 status/message 里并写回库。
func (h *Handler) keyQuota(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req model.ProviderKeyQuotaRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	key, err := h.store.GetAPIKeyByID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.ProxyURL = search.ResolveProxyURL(key.ProxyMode, key.ProxyURL, h.providerProxyURL(r.Context(), key.ProviderName))
	start := time.Now()
	h.logInfo("provider_key_quota_start", map[string]interface{}{"provider": key.ProviderName, "key_id": id, "alias": key.Alias, "proxy_enabled": req.ProxyURL != "", "request_id": RequestID(r.Context())})
	response, err := search.QueryOfficialQuota(r.Context(), key, req)
	if err != nil {
		response = model.ProviderKeyQuotaResult{Provider: key.ProviderName, Alias: key.Alias, Supported: true, Status: "error", Message: err.Error(), FetchedAt: time.Now()}
		h.logError("provider_key_quota_failed", map[string]interface{}{"provider": key.ProviderName, "key_id": id, "alias": key.Alias, "error": err.Error(), "latency_ms": time.Since(start).Milliseconds(), "request_id": RequestID(r.Context())})
	} else {
		h.logInfo("provider_key_quota_done", map[string]interface{}{"provider": key.ProviderName, "key_id": id, "alias": key.Alias, "status": response.Status, "latency_ms": time.Since(start).Milliseconds(), "request_id": RequestID(r.Context())})
	}
	if err := h.store.UpdateProviderKeyOfficialQuota(r.Context(), id, response); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "admin", "provider_key.quota", "provider_key", strconv.FormatInt(id, 10), map[string]interface{}{"provider": response.Provider, "status": response.Status, "unit": response.Unit})
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) providerProxyURL(ctx context.Context, providerName string) string {
	providers, err := h.store.ListProviders(ctx)
	if err != nil {
		return ""
	}
	for _, item := range providers {
		if item.Name != providerName || !providerBoolSetting(item.Settings, "proxy_enabled") {
			continue
		}
		return strings.TrimSpace(providerStringSetting(item.Settings, "proxy_url"))
	}
	return ""
}

func providerBoolSetting(settings map[string]interface{}, key string) bool {
	value, ok := settings[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") || strings.TrimSpace(typed) == "1"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return false
	}
}

func providerStringSetting(settings map[string]interface{}, key string) string {
	value, ok := settings[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}
