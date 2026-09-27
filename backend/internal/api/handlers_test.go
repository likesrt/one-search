package api

import (
	"testing"

	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
	"github.com/one-search/one-search/backend/internal/search"
)

// TestFillAnonymousKeySupport 覆盖 listProviders 里「按 registry 补全 supports_anonymous_key」这一步：
// 该字段不是数据库列，只在 Handler 层由编排层现场填充，因此这里直接断言填充结果，
// 并覆盖两种边界 —— orchestrator 为 nil（零值 Handler，既有测试的构造方式）时不 panic 且保持 false，
// 以及渠道未注册时退化为 false（提示语义为「未声明支持匿名」，不影响放行）。
func TestFillAnonymousKeySupport(t *testing.T) {
	registry := provider.NewRegistry(
		provider.NewContext7Provider(provider.Config{}),
		provider.NewKeenableProvider(provider.Config{}),
		provider.NewExaProvider(provider.Config{}),
	)
	handler := &Handler{orchestrator: search.NewOrchestrator(registry, nil, nil)}
	providers := []model.ProviderConfig{
		{Name: model.ProviderContext7},
		{Name: model.ProviderKeenable},
		{Name: model.ProviderExa},
		{Name: "not-registered"},
	}
	handler.fillAnonymousKeySupport(providers)

	wants := map[string]bool{
		model.ProviderContext7: true,
		model.ProviderKeenable: true,
		model.ProviderExa:      false,
		"not-registered":       false,
	}
	for _, item := range providers {
		if got := item.SupportsAnonymousKey; got != wants[item.Name] {
			t.Fatalf("%s.SupportsAnonymousKey = %v, want %v", item.Name, got, wants[item.Name])
		}
	}

	// 零值 Handler：orchestrator 为 nil，填充必须安全跳过而不是 panic。
	empty := &Handler{}
	nilOrchestrator := []model.ProviderConfig{{Name: model.ProviderContext7}}
	empty.fillAnonymousKeySupport(nilOrchestrator)
	if nilOrchestrator[0].SupportsAnonymousKey {
		t.Fatalf("orchestrator 为 nil 时不应改写字段")
	}
}

// TestValidateProviderKeyBaseURL 覆盖 key 级 base_url 的保存期校验：
// 空值放行（回退渠道默认）、普通绝对地址放行、`#` 完整端点语法先剥前缀再校验、
// Jina 因查询词在 URL 路径中而不接受 `#`、以及缺 scheme / 缺主机一律拒绝。
func TestValidateProviderKeyBaseURL(t *testing.T) {
	cases := []struct {
		name         string
		raw          string
		providerName string
		wantErr      bool
	}{
		{name: "空串放行", raw: "", providerName: model.ProviderTavily},
		{name: "纯空白放行", raw: "   ", providerName: model.ProviderTavily},
		{name: "普通绝对地址放行", raw: "https://relay.example.com/tavily", providerName: model.ProviderTavily},
		{name: "完整端点语法放行", raw: "#https://relay.example.com/proxy/tavily/search", providerName: model.ProviderTavily},
		{name: "完整端点两侧空白被裁剪", raw: "  #  https://relay.example.com/search  ", providerName: model.ProviderTavily},
		{name: "完整端点自带查询串放行", raw: "#https://relay.example.com/search?token=abc", providerName: model.ProviderTavily},
		{name: "完整端点缺少 scheme 拒绝", raw: "#relay.example.com/search", providerName: model.ProviderTavily, wantErr: true},
		{name: "只有井号拒绝", raw: "#", providerName: model.ProviderTavily, wantErr: true},
		{name: "Jina 不接受完整端点", raw: "#https://relay.example.com/search", providerName: model.ProviderJina, wantErr: true},
		{name: "Jina 普通地址仍放行", raw: "https://relay.example.com", providerName: model.ProviderJina},
		{name: "缺 scheme 拒绝", raw: "relay.example.com", providerName: model.ProviderTavily, wantErr: true},
		{name: "非 http scheme 拒绝", raw: "ftp://relay.example.com", providerName: model.ProviderTavily, wantErr: true},
		{name: "缺主机拒绝", raw: "https://", providerName: model.ProviderTavily, wantErr: true},
	}
	for _, tc := range cases {
		err := validateProviderKeyBaseURL(tc.raw, tc.providerName)
		if tc.wantErr && err == nil {
			t.Fatalf("%s: validateProviderKeyBaseURL(%q, %q) = nil, want error", tc.name, tc.raw, tc.providerName)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%s: validateProviderKeyBaseURL(%q, %q) = %v, want nil", tc.name, tc.raw, tc.providerName, err)
		}
	}
}
