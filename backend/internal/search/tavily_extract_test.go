package search

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
)

// extractTestProvider 是同时实现 Search 与 Extract 的假适配器。
//
// 它满足 urlExtractor，因此能被 TavilyExtract 通过类型断言取用；
// 记录的调用参数用于断言 key 级配置是否被正确透传。
type extractTestProvider struct {
	mu sync.Mutex
	// content 是 Extract 返回的正文
	content string
	// credits 是 Extract 返回的额度
	credits float64
	// err 非 nil 时 Extract 返回该错误
	err error
	// gotURLs 记录每次 Extract 收到的目标地址
	gotURLs []string
	// gotProxy 记录构建适配器时使用的代理地址（由 Config 注入）
	gotProxy string
}

// Name 返回渠道名，固定为 tavily。
func (p *extractTestProvider) Name() string { return model.ProviderTavily }

// Search 是搜索接口的空实现；本用例只关心 Extract。
func (p *extractTestProvider) Search(context.Context, model.SearchRequest, model.APIKey) (model.ProviderResponse, error) {
	return model.ProviderResponse{}, nil
}

// HealthCheck 恒返回 nil。
func (p *extractTestProvider) HealthCheck(context.Context, model.APIKey) error { return nil }

// SupportsAnonymousKey 报告该测试渠道不支持匿名调用。
func (p *extractTestProvider) SupportsAnonymousKey() bool { return false }

// Extract 记录调用参数并返回预设结果。
func (p *extractTestProvider) Extract(_ context.Context, targetURL string, _ model.APIKey) (string, float64, error) {
	p.mu.Lock()
	p.gotURLs = append(p.gotURLs, targetURL)
	p.mu.Unlock()
	if p.err != nil {
		return "", 0, p.err
	}
	return p.content, p.credits, nil
}

// searchOnlyTestProvider 只实现 provider.Provider 而不实现 urlExtractor，
// 用于验证「渠道不支持 extract」这条分支不会被误当成故障。
type searchOnlyTestProvider struct{}

// Name 返回一个与 tavily 同名的渠道名，使注册校验通过。
func (searchOnlyTestProvider) Name() string { return model.ProviderTavily }

// Search 是搜索接口的空实现。
func (searchOnlyTestProvider) Search(context.Context, model.SearchRequest, model.APIKey) (model.ProviderResponse, error) {
	return model.ProviderResponse{}, nil
}

// HealthCheck 恒返回 nil。
func (searchOnlyTestProvider) HealthCheck(context.Context, model.APIKey) error { return nil }

// SupportsAnonymousKey 报告该测试渠道不支持匿名调用。
func (searchOnlyTestProvider) SupportsAnonymousKey() bool { return false }

// newExtractTestOrchestrator 构造一个只注册 tavily 的编排层，渠道配置由入参决定。
//
// providers 用于注入渠道级设置（超时、代理开关），keyPool 用于观察取 key 行为。
func newExtractTestOrchestrator(adapter provider.Provider, keyPool *orchestratorTestKeyPool, providers []model.ProviderConfig) *Orchestrator {
	registry := provider.NewRegistry(adapter)
	store := &orchestratorTestStore{
		settings:  model.RuntimeSettings{RequestTimeoutMS: 2000},
		providers: providers,
	}
	return NewOrchestrator(registry, keyPool, store)
}

// TestTavilyExtractUsesKeyPoolAndAdapter 验证正常路径：
// 从 key 池取 tavily key、把目标地址交给适配器、把 credits 原样返回。
func TestTavilyExtractUsesKeyPoolAndAdapter(t *testing.T) {
	adapter := &extractTestProvider{content: "# 整页正文", credits: 1}
	keyPool := &orchestratorTestKeyPool{}
	orchestrator := newExtractTestOrchestrator(adapter, keyPool, []model.ProviderConfig{
		{Name: model.ProviderTavily, Enabled: true, Priority: 1, Weight: 1, TimeoutMS: 5000},
	})

	content, credits, err := orchestrator.TavilyExtract(context.Background(), "https://example.com/page")
	if err != nil {
		t.Fatalf("TavilyExtract 报错: %v", err)
	}
	if content != "# 整页正文" || credits != 1 {
		t.Fatalf("返回值不符合预期: content=%q credits=%v", content, credits)
	}
	if got, want := keyPool.acquired, []string{model.ProviderTavily}; !stringSlicesEqual(got, want) {
		t.Fatalf("取 key 的渠道 = %v, 期望 %v", got, want)
	}
	if len(adapter.gotURLs) != 1 || adapter.gotURLs[0] != "https://example.com/page" {
		t.Fatalf("目标地址未透传: %v", adapter.gotURLs)
	}
}

// TestTavilyExtractPropagatesError 验证适配器错误被原样返回（不吞成空正文）。
func TestTavilyExtractPropagatesError(t *testing.T) {
	adapter := &extractTestProvider{err: &provider.Error{Type: provider.ErrorTypeAuth, Message: "invalid api key"}}
	orchestrator := newExtractTestOrchestrator(adapter, &orchestratorTestKeyPool{}, []model.ProviderConfig{
		{Name: model.ProviderTavily, Enabled: true, Priority: 1, Weight: 1},
	})

	if _, _, err := orchestrator.TavilyExtract(context.Background(), "https://example.com"); err == nil {
		t.Fatal("适配器报错时必须透传")
	} else if provider.ErrorType(err) != provider.ErrorTypeAuth {
		t.Fatalf("错误类型 = %q", provider.ErrorType(err))
	}
}

// TestTavilyExtractRejectsUnsupportedAdapter 验证只实现 Search 的适配器会被明确拒绝，
// 而不是返回空正文让上层以为回退成功。
func TestTavilyExtractRejectsUnsupportedAdapter(t *testing.T) {
	orchestrator := newExtractTestOrchestrator(searchOnlyTestProvider{}, &orchestratorTestKeyPool{}, []model.ProviderConfig{
		{Name: model.ProviderTavily, Enabled: true, Priority: 1, Weight: 1},
	})

	_, _, err := orchestrator.TavilyExtract(context.Background(), "https://example.com")
	if err == nil {
		t.Fatal("不支持的适配器必须报错")
	}
	if !strings.Contains(err.Error(), "does not support extract") {
		t.Fatalf("错误文案应说明是能力缺失: %v", err)
	}
}

// TestTavilyExtractWithoutRegisteredProvider 验证渠道未注册时不消耗 key。
//
// 未注册的渠道没有可用适配器，提前返回可避免白白占用 key 的并发名额。
func TestTavilyExtractWithoutRegisteredProvider(t *testing.T) {
	registry := provider.NewRegistry()
	keyPool := &orchestratorTestKeyPool{}
	orchestrator := NewOrchestrator(registry, keyPool, &orchestratorTestStore{})

	if _, _, err := orchestrator.TavilyExtract(context.Background(), "https://example.com"); err == nil {
		t.Fatal("渠道未注册时必须报错")
	}
	if len(keyPool.acquired) != 0 {
		t.Fatalf("渠道未注册时不应取 key，实际 %v", keyPool.acquired)
	}
}

// TestTavilyExtractProxyIsolation 验证代理隔离：回退通道的代理来自 tavily 的 key 级配置
// 与渠道级设置，与「抓取功能的全局代理」完全是两条路径。
//
// 这是硬性要求：抓取代理是给「抓取任意 URL」这个危险动作准备的出口，
// 绝不能顺带改变调用 Tavily API 的出站路径。
func TestTavilyExtractProxyIsolation(t *testing.T) {
	cases := []struct {
		name        string
		keyProxy    string
		keyMode     string
		providerSet map[string]interface{}
		wantProxy   string
	}{
		{
			name:      "key 级 custom 代理生效",
			keyProxy:  "http://key-proxy:8080",
			keyMode:   model.ProxyModeCustom,
			wantProxy: "http://key-proxy:8080",
		},
		{
			name:        "key 级 direct 覆盖渠道级代理",
			keyMode:     model.ProxyModeDirect,
			providerSet: map[string]interface{}{"proxy_enabled": true, "proxy_url": "http://channel-proxy:8080"},
			wantProxy:   "",
		},
		{
			name:        "inherit 继承渠道级代理",
			keyMode:     model.ProxyModeInherit,
			providerSet: map[string]interface{}{"proxy_enabled": true, "proxy_url": "http://channel-proxy:8080"},
			wantProxy:   "http://channel-proxy:8080",
		},
		{
			name:      "渠道级未开启时直连",
			keyMode:   model.ProxyModeInherit,
			wantProxy: "",
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			var gotProxy string
			// 用工厂捕获构建适配器时的代理配置，这是代理真正生效的唯一入口
			registry := provider.NewRegistry()
			registry.RegisterFactory(model.ProviderTavily, func(cfg provider.Config) provider.Provider {
				gotProxy = cfg.ProxyURL
				return &extractTestProvider{content: "正文"}
			})
			keyPool := &orchestratorTestKeyPool{proxyModes: []string{item.keyMode}, proxyURLs: []string{item.keyProxy}}
			store := &orchestratorTestStore{
				settings: model.RuntimeSettings{RequestTimeoutMS: 2000},
				providers: []model.ProviderConfig{{
					Name: model.ProviderTavily, Enabled: true, Priority: 1, Weight: 1,
					Settings: item.providerSet,
				}},
			}
			orchestrator := NewOrchestrator(registry, keyPool, store)

			if _, _, err := orchestrator.TavilyExtract(context.Background(), "https://example.com"); err != nil {
				t.Fatalf("TavilyExtract 报错: %v", err)
			}
			if gotProxy != item.wantProxy {
				t.Fatalf("回退通道的代理 = %q, 期望 %q", gotProxy, item.wantProxy)
			}
		})
	}
}

// TestTavilyExtractUsesProviderTimeout 验证 extract 复用 Tavily 渠道的 timeout_ms：
// 渠道超时很短时，慢上游必须被超时中断，而不是一直挂着。
func TestTavilyExtractUsesProviderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(400 * time.Millisecond)
		_, _ = w.Write([]byte(`{"results":[{"raw_content":"late"}]}`))
	}))
	defer server.Close()

	registry := provider.NewRegistry()
	registry.RegisterFactory(model.ProviderTavily, func(cfg provider.Config) provider.Provider {
		return provider.NewTavilyProvider(cfg)
	})
	keyPool := &orchestratorTestKeyPool{baseURLs: []string{server.URL}}
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{RequestTimeoutMS: 5000},
		providers: []model.ProviderConfig{{
			Name: model.ProviderTavily, Enabled: true, Priority: 1, Weight: 1, TimeoutMS: 50,
		}},
	}
	orchestrator := NewOrchestrator(registry, keyPool, store)

	started := time.Now()
	if _, _, err := orchestrator.TavilyExtract(context.Background(), "https://example.com"); err == nil {
		t.Fatal("渠道超时生效时应收敛为错误")
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("渠道超时未生效，耗时 %v", elapsed)
	}
}
