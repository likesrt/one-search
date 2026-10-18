package billing

import "testing"

func TestEstimateCostUSD(t *testing.T) {
	cost, ok := EstimateCostUSD("exa", "requests", 1000)
	if !ok || cost < 6.9 || cost > 7.1 {
		t.Fatalf("exa 1000 req => %v ok=%v", cost, ok)
	}
	cost, ok = EstimateCostUSD("tavily", "credits", 10)
	if !ok || cost < 0.07 || cost > 0.09 {
		t.Fatalf("tavily 10 credits => %v ok=%v", cost, ok)
	}
	if _, ok := EstimateCostUSD("unknown", "requests", 1); ok {
		t.Fatal("unknown provider should not estimate")
	}
}

func TestRateFromSettingsOverride(t *testing.T) {
	rate := RateFromSettings("exa", map[string]interface{}{"price_per_request": 0.01})
	cost, ok := EstimateCostUSDWithRate(rate, "requests", 100)
	if !ok || cost < 0.99 || cost > 1.01 {
		t.Fatalf("custom exa rate => %v ok=%v", cost, ok)
	}
	rate = RateFromSettings("tavily", map[string]interface{}{"default_billable_credits": 3})
	if rate.DefaultBillableCredit != 3 {
		t.Fatalf("default credits override => %v", rate.DefaultBillableCredit)
	}
}

// TestDeepSeekCostEstimate 锁定 DeepSeek 的 USD 估算单价：
// 官方 5 元/1000 次成功请求按近似汇率折算约 $0.0007/次，1000 次约 $0.7。
func TestDeepSeekCostEstimate(t *testing.T) {
	cost, ok := EstimateCostUSD("deepseek", "requests", 1000)
	if !ok || cost < 0.69 || cost > 0.71 {
		t.Fatalf("deepseek 1000 req => %v ok=%v", cost, ok)
	}
}

// TestEmptyResultsBillable 锁定「空结果是否计费」的渠道差异：
// DeepSeek 官方只对 results 非空的成功请求计费，其余渠道保持既有口径。
func TestEmptyResultsBillable(t *testing.T) {
	if EmptyResultsBillable("deepseek") {
		t.Fatal("deepseek 的空结果不应计费")
	}
	if !EmptyResultsBillable("exa") {
		t.Fatal("exa 应保持默认的 requests:1 口径")
	}
}
