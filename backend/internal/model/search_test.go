package model

import "testing"

// TestDefaultProvidersIncludesBuiltInProviders 锁定 DefaultProviders 的长度与逐位顺序：
// 该顺序对外可见（MCP 的 enum 与文档），新增渠道只能追加到末尾，改动必须同步更新本测试。
func TestDefaultProvidersIncludesBuiltInProviders(t *testing.T) {
	want := []string{ProviderExa, ProviderYou, ProviderJina, ProviderTavily, ProviderFirecrawl, ProviderSerper, ProviderBrave, ProviderKeenable, ProviderContext7}
	if len(DefaultProviders) != len(want) {
		t.Fatalf("DefaultProviders length = %d, want %d", len(DefaultProviders), len(want))
	}
	for index, provider := range want {
		if DefaultProviders[index] != provider {
			t.Fatalf("DefaultProviders[%d] = %q, want %q", index, DefaultProviders[index], provider)
		}
	}
}
