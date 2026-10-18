package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
)

func TestQueryTavilyQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/usage" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tavily-key" {
			t.Fatalf("Authorization = %q", got)
		}
		writeQuotaJSON(t, w, map[string]interface{}{
			"key": map[string]interface{}{
				"usage":        150,
				"limit":        1000,
				"search_usage": 100,
			},
			"account": map[string]interface{}{
				"current_plan": "Bootstrap",
				"plan_usage":   500,
				"plan_limit":   15000,
			},
		})
	}))
	defer server.Close()
	withQuotaEndpoint(t, &tavilyUsageURL, server.URL+"/usage")

	result, err := QueryOfficialQuota(context.Background(), model.APIKey{ProviderName: model.ProviderTavily, Alias: "tavily", Value: "tavily-key"}, model.ProviderKeyQuotaRequest{})
	if err != nil {
		t.Fatalf("QueryOfficialQuota returned error: %v", err)
	}
	if !result.Supported || result.Status != "success" || result.Unit != "credits" || result.Balance == nil || *result.Balance != 850 || result.TotalQuantity == nil || *result.TotalQuantity != 150 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestQueryFirecrawlQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/team/credit-usage" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer firecrawl-key" {
			t.Fatalf("Authorization = %q", got)
		}
		writeQuotaJSON(t, w, map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"remainingCredits":   1000,
				"planCredits":        500000,
				"billingPeriodStart": "2025-01-01T00:00:00Z",
				"billingPeriodEnd":   "2025-01-31T23:59:59Z",
			},
		})
	}))
	defer server.Close()
	withQuotaEndpoint(t, &firecrawlCreditUsageURL, server.URL+"/v2/team/credit-usage")

	result, err := QueryOfficialQuota(context.Background(), model.APIKey{ProviderName: model.ProviderFirecrawl, Alias: "firecrawl", Value: "firecrawl-key"}, model.ProviderKeyQuotaRequest{})
	if err != nil {
		t.Fatalf("QueryOfficialQuota returned error: %v", err)
	}
	if !result.Supported || result.Status != "success" || result.Unit != "credits" || result.Balance == nil || *result.Balance != 1000 || result.TotalQuantity == nil || *result.TotalQuantity != 499000 || result.Period["start"] == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestQuerySerperQuota(t *testing.T) {
	result, err := QueryOfficialQuota(context.Background(), model.APIKey{ProviderName: model.ProviderSerper, Alias: "serper", Value: "serper-key", MonthlyCredits: 100}, model.ProviderKeyQuotaRequest{})
	if err != nil {
		t.Fatalf("QueryOfficialQuota returned error: %v", err)
	}
	if !result.Supported || result.Status != "success" || result.Unit != "credits" || result.Balance == nil || *result.Balance != 2400 || result.TotalQuantity == nil || *result.TotalQuantity != 100 || !strings.Contains(result.Message, "默认总额度 2500") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestQueryOfficialQuotaUsesBoundedTimeout(t *testing.T) {
	originalTimeout := quotaRequestTimeout
	quotaRequestTimeout = 20 * time.Millisecond
	t.Cleanup(func() { quotaRequestTimeout = originalTimeout })

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writeQuotaJSON(t, w, map[string]interface{}{"data": map[string]interface{}{"attributes": map[string]interface{}{"balance": 100}}})
	}))
	defer server.Close()
	withQuotaEndpoint(t, &youQuotaURL, server.URL)

	_, err := QueryOfficialQuota(context.Background(), model.APIKey{ProviderName: model.ProviderYou, Alias: "you", Value: "you-key"}, model.ProviderKeyQuotaRequest{})
	if err == nil {
		t.Fatalf("QueryOfficialQuota returned nil error")
	}
}

func TestQueryBraveQuota(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/res/v1/web/search" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-Subscription-Token"); got != "brave-key" {
			t.Fatalf("X-Subscription-Token = %q", got)
		}
		if r.URL.Query().Get("count") != "1" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("X-RateLimit-Limit", "1, 15000")
		w.Header().Set("X-RateLimit-Policy", "1;w=1, 15000;w=2592000")
		w.Header().Set("X-RateLimit-Remaining", "0, 14523")
		w.Header().Set("X-RateLimit-Reset", "1, 1234567")
		writeQuotaJSON(t, w, map[string]interface{}{"type": "search", "web": map[string]interface{}{"results": []map[string]interface{}{}}})
	}))
	defer server.Close()
	withQuotaEndpoint(t, &braveWebSearchURL, server.URL+"/res/v1/web/search")

	result, err := QueryOfficialQuota(context.Background(), model.APIKey{ProviderName: model.ProviderBrave, Alias: "brave", Value: "brave-key"}, model.ProviderKeyQuotaRequest{})
	if err != nil {
		t.Fatalf("QueryOfficialQuota returned error: %v", err)
	}
	if !result.Supported || result.Status != "success" || result.Unit != "requests" || result.Balance == nil || *result.Balance != 14523 || result.TotalQuantity == nil || *result.TotalQuantity != 477 || !strings.Contains(result.RawText, "X-RateLimit-Remaining") {
		t.Fatalf("unexpected result: %#v", result)
	}
}

// TestQueryDeepSeekQuota 覆盖 DeepSeek 官方 /user/balance：
// CNY 余额、USD 余额（同时换算 balance_usd）、多币种取有余额的一项、
// is_available=false 归零、以及没有 balance_infos 时标记为不支持。
func TestQueryDeepSeekQuota(t *testing.T) {
	cases := []struct {
		name        string
		payload     map[string]interface{}
		wantSupport bool
		wantStatus  string
		wantUnit    string
		wantBalance float64
		wantUSD     *float64
	}{
		{
			name: "CNY 余额",
			payload: map[string]interface{}{
				"is_available": true,
				"balance_infos": []map[string]interface{}{
					{"currency": "CNY", "total_balance": "110.00", "granted_balance": "10.00", "topped_up_balance": "100.00"},
				},
			},
			wantSupport: true, wantStatus: "success", wantUnit: "cny", wantBalance: 110,
		},
		{
			name: "USD 余额",
			payload: map[string]interface{}{
				"is_available": true,
				"balance_infos": []map[string]interface{}{
					{"currency": "USD", "total_balance": "50.00", "granted_balance": "0", "topped_up_balance": "50.00"},
				},
			},
			wantSupport: true, wantStatus: "success", wantUnit: "usd", wantBalance: 50, wantUSD: floatPtrForTest(50),
		},
		{
			name: "多币种优先有余额的一项",
			payload: map[string]interface{}{
				"is_available": true,
				"balance_infos": []map[string]interface{}{
					{"currency": "CNY", "total_balance": "0", "granted_balance": "0", "topped_up_balance": "0"},
					{"currency": "USD", "total_balance": "10.00", "granted_balance": "0", "topped_up_balance": "10.00"},
				},
			},
			wantSupport: true, wantStatus: "success", wantUnit: "usd", wantBalance: 10, wantUSD: floatPtrForTest(10),
		},
		{
			name: "账户不可用归零",
			payload: map[string]interface{}{
				"is_available": false,
				"balance_infos": []map[string]interface{}{
					{"currency": "CNY", "total_balance": "100.00", "granted_balance": "0", "topped_up_balance": "100.00"},
				},
			},
			wantSupport: true, wantStatus: "success", wantUnit: "cny", wantBalance: 0,
		},
		{
			name:        "没有 balance_infos",
			payload:     map[string]interface{}{"is_available": true, "balance_infos": []map[string]interface{}{}},
			wantSupport: false, wantStatus: "unsupported",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/user/balance" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer deepseek-key" {
					t.Fatalf("Authorization = %q", got)
				}
				if got := r.Header.Get("Accept"); got != "application/json" {
					t.Fatalf("Accept = %q", got)
				}
				writeQuotaJSON(t, w, tc.payload)
			}))
			defer server.Close()
			withQuotaEndpoint(t, &deepseekBalanceURL, server.URL+"/user/balance")

			result, err := QueryOfficialQuota(context.Background(), model.APIKey{ProviderName: model.ProviderDeepSeek, Alias: "deepseek", Value: "deepseek-key"}, model.ProviderKeyQuotaRequest{})
			if err != nil {
				t.Fatalf("QueryOfficialQuota returned error: %v", err)
			}
			if result.Supported != tc.wantSupport || result.Status != tc.wantStatus {
				t.Fatalf("unexpected supported/status: %#v", result)
			}
			if !tc.wantSupport {
				return
			}
			if result.Unit != tc.wantUnit || result.Balance == nil || *result.Balance != tc.wantBalance {
				t.Fatalf("unexpected balance: %#v", result)
			}
			if tc.wantUSD == nil {
				if result.BalanceUSD != nil {
					t.Fatalf("非 USD 余额不应填 balance_usd: %#v", result)
				}
			} else if result.BalanceUSD == nil || *result.BalanceUSD != *tc.wantUSD {
				t.Fatalf("unexpected balance_usd: %#v", result)
			}
			if len(result.Breakdown) == 0 {
				t.Fatalf("balance_infos 应进入 breakdown: %#v", result)
			}
		})
	}
}

// floatPtrForTest 是测试侧构造期望指针的小工具，避免为每个用例写一遍取地址。
func floatPtrForTest(value float64) *float64 {
	return &value
}

func withQuotaEndpoint(t *testing.T, target *string, value string) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

func writeQuotaJSON(t *testing.T, w http.ResponseWriter, payload interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
