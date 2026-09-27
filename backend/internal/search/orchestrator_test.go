package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
)

type orchestratorTestStore struct {
	mu        sync.Mutex
	settings  model.RuntimeSettings
	providers []model.ProviderConfig
	cache     map[string][]byte
	lastTTL   int
	setCount  int
}

func (s *orchestratorTestStore) GetAPIKeyByID(ctx context.Context, id int64) (model.APIKey, error) {
	return model.APIKey{}, nil
}

func (s *orchestratorTestStore) RecordKeyResult(ctx context.Context, key model.APIKey, success bool, errorType string) error {
	return nil
}

func (s *orchestratorTestStore) UpdateProviderKeyOfficialQuota(ctx context.Context, id int64, quota model.ProviderKeyQuotaResult) error {
	return nil
}

func (s *orchestratorTestStore) RuntimeSettings(ctx context.Context) (model.RuntimeSettings, error) {
	return s.settings, nil
}

func (s *orchestratorTestStore) ListProviders(ctx context.Context) ([]model.ProviderConfig, error) {
	return append([]model.ProviderConfig(nil), s.providers...), nil
}

func (s *orchestratorTestStore) RecordSearchLog(ctx context.Context, input model.SearchLogInput) error {
	return nil
}

func (s *orchestratorTestStore) GetCache(ctx context.Context, cacheKey string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		return nil, false, nil
	}
	payload, ok := s.cache[cacheKey]
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), payload...), true, nil
}

func (s *orchestratorTestStore) SetCache(ctx context.Context, cacheKey string, payload []byte, ttlSeconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string][]byte{}
	}
	s.cache[cacheKey] = append([]byte(nil), payload...)
	s.lastTTL = ttlSeconds
	s.setCount++
	return nil
}

type orchestratorTestKeyPool struct {
	mu       sync.Mutex
	acquired []string
	excluded [][]int64
	// ids 按 Acquire 顺序指定返回的 key ID，缺省为 0，语义是「未落库的临时 key」。
	// 默认取 0 是刻意的：callProvider 只对 ID>0 的 key 触发后台官方额度刷新，
	// 那个 goroutine 会真的发起 HTTP 请求，可能打到别的用例刚换过的全局额度端点地址，
	// 造成测试互相干扰（历史上表现为 TestQueryTavilyQuota 偶发 Authorization 不匹配）。
	// 需要断言 triedIDs 的用例请显式给出正数 ID。
	ids []int64
	// baseURLs 按 Acquire 顺序提供 key 级基础 URL 覆盖值，用于模拟不同 key 指向不同中转站；
	// 元素不足时 key.BaseURL 为空，即回退渠道默认地址。
	baseURLs []string
	// proxyModes / proxyURLs 同样按 Acquire 顺序提供 key 级代理三态配置，
	// 元素不足时为空：空模式按 inherit 处理、空地址表示 custom 无自有地址。
	proxyModes []string
	proxyURLs  []string
}

func (p *orchestratorTestKeyPool) Acquire(ctx context.Context, providerName string, excludeIDs ...int64) (model.APIKey, func(bool, error), error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acquired = append(p.acquired, providerName)
	p.excluded = append(p.excluded, append([]int64(nil), excludeIDs...))
	index := len(p.acquired) - 1
	id := int64(0)
	if index < len(p.ids) {
		id = p.ids[index]
	}
	baseURL := ""
	if index < len(p.baseURLs) {
		baseURL = p.baseURLs[index]
	}
	proxyMode := ""
	if index < len(p.proxyModes) {
		proxyMode = p.proxyModes[index]
	}
	proxyURL := ""
	if index < len(p.proxyURLs) {
		proxyURL = p.proxyURLs[index]
	}
	return model.APIKey{ID: id, ProviderName: providerName, Alias: providerName + "-key", Value: "test-key", BaseURL: baseURL, ProxyMode: proxyMode, ProxyURL: proxyURL}, func(bool, error) {}, nil
}

type orchestratorTestProvider struct {
	name       string
	delay      time.Duration
	err        error
	resultN    int
	empty      bool
	searchHook func()
}

func (p orchestratorTestProvider) Name() string {
	return p.name
}

func (p orchestratorTestProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	if p.searchHook != nil {
		p.searchHook()
	}
	if p.delay > 0 {
		select {
		case <-time.After(p.delay):
		case <-ctx.Done():
			return model.ProviderResponse{}, ctx.Err()
		}
	}
	if p.err != nil {
		return model.ProviderResponse{}, p.err
	}
	if p.empty {
		return model.ProviderResponse{}, nil
	}
	n := p.resultN
	if n <= 0 {
		n = 1
	}
	results := make([]model.SearchResult, 0, n)
	for i := 0; i < n; i++ {
		results = append(results, model.SearchResult{
			Title:    p.name,
			URL:      "https://example.com/" + p.name + "/" + string(rune('a'+i)),
			Provider: p.name,
			Score:    1,
		})
	}
	return model.ProviderResponse{Results: results}, nil
}

func (p orchestratorTestProvider) HealthCheck(ctx context.Context, key model.APIKey) error {
	return nil
}

// SupportsAnonymousKey 让测试替身满足 Provider 接口；测试渠道一律不具备匿名能力。
func (p orchestratorTestProvider) SupportsAnonymousKey() bool { return false }

func TestSearchSkipsDisabledDefaultProviders(t *testing.T) {
	keyPool := &orchestratorTestKeyPool{}
	orchestrator := newDisabledProviderTestOrchestrator(keyPool)

	response, err := orchestrator.Search(context.Background(), model.SearchRequest{Query: "golang"}, "request-id", 0)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	if got, want := keyPool.acquired, []string{model.ProviderSerper}; !stringSlicesEqual(got, want) {
		t.Fatalf("Acquire providers = %v, want %v", got, want)
	}
	if got, want := response.Meta.ProvidersQueried, []string{model.ProviderSerper}; !stringSlicesEqual(got, want) {
		t.Fatalf("ProvidersQueried = %v, want %v", got, want)
	}
	if len(response.Providers) != 1 || response.Providers[0].Provider != model.ProviderSerper {
		t.Fatalf("response providers = %+v, want only %s", response.Providers, model.ProviderSerper)
	}
}

func TestSearchSkipsExplicitDisabledProviders(t *testing.T) {
	keyPool := &orchestratorTestKeyPool{}
	orchestrator := newDisabledProviderTestOrchestrator(keyPool)

	response, err := orchestrator.Search(context.Background(), model.SearchRequest{Query: "golang", Providers: []string{model.ProviderExa}, ProvidersExplicit: true}, "request-id", 0)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}

	if len(keyPool.acquired) != 0 {
		t.Fatalf("Acquire providers = %v, want none", keyPool.acquired)
	}
	if len(response.Meta.ProvidersQueried) != 0 {
		t.Fatalf("ProvidersQueried = %v, want none", response.Meta.ProvidersQueried)
	}
	if len(response.Providers) != 0 {
		t.Fatalf("response providers = %+v, want none", response.Providers)
	}
}

func TestFilterEnabledProvidersKeepsUnknownProviders(t *testing.T) {
	providers := filterEnabledProviders([]string{model.ProviderExa, "custom"}, []model.ProviderConfig{{Name: model.ProviderExa, Enabled: false}})
	if got, want := providers, []string{"custom"}; !stringSlicesEqual(got, want) {
		t.Fatalf("filterEnabledProviders = %v, want %v", got, want)
	}
}

func TestApplyDefaultsDoesNotCapLimitAtFifty(t *testing.T) {
	request := applyDefaults(model.SearchRequest{Query: "golang", Limit: 120}, model.RuntimeSettings{DefaultLimit: 10, DefaultDedupe: true})
	if got, want := request.Limit, 120; got != want {
		t.Fatalf("Limit = %d, want %d", got, want)
	}
}

func TestSearchCacheHitAndRefresh(t *testing.T) {
	keyPool := &orchestratorTestKeyPool{}
	registry := provider.NewRegistry(orchestratorTestProvider{name: model.ProviderSerper})
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode:      model.SearchModeSingle,
			DefaultProviders: []string{model.ProviderSerper},
			DefaultLimit:     10,
			DefaultDedupe:    true,
			RequestTimeoutMS: 1000,
			CacheEnabled:     true,
			CacheTTLSeconds:  60,
		},
		providers: []model.ProviderConfig{
			{Name: model.ProviderSerper, Enabled: true, Priority: 1, Weight: 1},
		},
		cache: map[string][]byte{},
	}
	orchestrator := NewOrchestrator(registry, keyPool, store)
	req := model.SearchRequest{Query: "golang", Providers: []string{model.ProviderSerper}, ProvidersExplicit: true, Mode: model.SearchModeSingle}

	first, err := orchestrator.Search(context.Background(), req, "req-1", 0)
	if err != nil {
		t.Fatalf("first search: %v", err)
	}
	if first.Meta.CacheHit {
		t.Fatal("first search should miss cache")
	}
	if len(store.cache) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(store.cache))
	}
	acquiredAfterWrite := len(keyPool.acquired)

	second, err := orchestrator.Search(context.Background(), req, "req-2", 0)
	if err != nil {
		t.Fatalf("second search: %v", err)
	}
	if !second.Meta.CacheHit {
		t.Fatal("second search should hit cache")
	}
	if len(keyPool.acquired) != acquiredAfterWrite {
		t.Fatalf("cache hit should not acquire keys, acquired=%v", keyPool.acquired)
	}

	// provider order must not affect key
	reordered := req
	reordered.Providers = []string{model.ProviderSerper}
	third, err := orchestrator.Search(context.Background(), reordered, "req-3", 0)
	if err != nil {
		t.Fatalf("reordered search: %v", err)
	}
	if !third.Meta.CacheHit {
		t.Fatal("reordered providers should still hit cache")
	}

	refresh := req
	refresh.Cache = model.CachePolicyRefresh
	fourth, err := orchestrator.Search(context.Background(), refresh, "req-4", 0)
	if err != nil {
		t.Fatalf("refresh search: %v", err)
	}
	if fourth.Meta.CacheHit {
		t.Fatal("refresh should bypass read")
	}
	if len(keyPool.acquired) <= acquiredAfterWrite {
		t.Fatal("refresh should call providers")
	}
	if len(store.cache) != 1 {
		t.Fatalf("refresh should rewrite cache, entries=%d", len(store.cache))
	}
}

func TestCacheKeyStableAcrossProviderOrder(t *testing.T) {
	o := &Orchestrator{}
	left := o.cacheKey(model.SearchRequest{Query: "q", Providers: []string{"b", "a"}, Mode: model.SearchModeParallel, Limit: 10}, map[string]int{"a": 5, "b": 8, "c": 9})
	right := o.cacheKey(model.SearchRequest{Query: "q", Providers: []string{"a", "b"}, Mode: model.SearchModeParallel, Limit: 10}, map[string]int{"a": 5, "b": 8, "c": 9})
	if left != right {
		t.Fatalf("cache keys differ: %s vs %s", left, right)
	}
}

func TestCacheTruncatesResultsOnWrite(t *testing.T) {
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeSingle, DefaultProviders: []string{model.ProviderSerper}, DefaultLimit: 10,
			DefaultDedupe: true, RequestTimeoutMS: 1000, CacheEnabled: true, CacheTTLSeconds: 60, CacheMaxResults: 2,
		},
		providers: []model.ProviderConfig{{Name: model.ProviderSerper, Enabled: true, Priority: 1, Weight: 1}},
		cache:     map[string][]byte{},
	}
	orchestrator := NewOrchestrator(provider.NewRegistry(orchestratorTestProvider{name: model.ProviderSerper, resultN: 5}), &orchestratorTestKeyPool{}, store)
	resp, err := orchestrator.Search(context.Background(), model.SearchRequest{Query: "q", Providers: []string{model.ProviderSerper}, ProvidersExplicit: true, Mode: model.SearchModeSingle}, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 5 {
		t.Fatalf("live response results = %d, want 5", len(resp.Results))
	}
	if len(store.cache) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(store.cache))
	}
	var cached model.SearchResponse
	for _, payload := range store.cache {
		if err := json.Unmarshal(payload, &cached); err != nil {
			t.Fatal(err)
		}
	}
	if len(cached.Results) != 2 {
		t.Fatalf("cached results = %d, want 2", len(cached.Results))
	}
}

func TestCacheSkipsPartialParallelErrors(t *testing.T) {
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeParallel, DefaultProviders: []string{model.ProviderSerper, model.ProviderBrave},
			DefaultLimit: 10, DefaultDedupe: true, RequestTimeoutMS: 1000, CacheEnabled: true, CacheTTLSeconds: 60,
		},
		providers: []model.ProviderConfig{
			{Name: model.ProviderSerper, Enabled: true, Priority: 1, Weight: 1},
			{Name: model.ProviderBrave, Enabled: true, Priority: 2, Weight: 1},
		},
		cache: map[string][]byte{},
	}
	orchestrator := NewOrchestrator(provider.NewRegistry(
		orchestratorTestProvider{name: model.ProviderSerper},
		orchestratorTestProvider{name: model.ProviderBrave, err: context.DeadlineExceeded},
	), &orchestratorTestKeyPool{}, store)
	resp, err := orchestrator.Search(context.Background(), model.SearchRequest{
		Query: "q", Providers: []string{model.ProviderSerper, model.ProviderBrave}, ProvidersExplicit: true, Mode: model.SearchModeParallel,
	}, "r1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("expected partial results")
	}
	if len(store.cache) != 0 {
		t.Fatalf("partial parallel errors should not cache, entries=%d", len(store.cache))
	}
}

func TestCacheEmptyResultsUseShortTTL(t *testing.T) {
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeSingle, DefaultProviders: []string{model.ProviderSerper}, DefaultLimit: 10,
			DefaultDedupe: true, RequestTimeoutMS: 1000, CacheEnabled: true, CacheTTLSeconds: 3600,
		},
		providers: []model.ProviderConfig{{Name: model.ProviderSerper, Enabled: true, Priority: 1, Weight: 1}},
		cache:     map[string][]byte{},
	}
	orchestrator := NewOrchestrator(provider.NewRegistry(orchestratorTestProvider{name: model.ProviderSerper, empty: true}), &orchestratorTestKeyPool{}, store)
	if _, err := orchestrator.Search(context.Background(), model.SearchRequest{Query: "q", Providers: []string{model.ProviderSerper}, ProvidersExplicit: true, Mode: model.SearchModeSingle}, "r1", 0); err != nil {
		t.Fatal(err)
	}
	if store.setCount != 1 {
		t.Fatalf("setCount=%d, want 1", store.setCount)
	}
	if store.lastTTL != emptyResultCacheTTLSeconds {
		t.Fatalf("empty ttl=%d, want %d", store.lastTTL, emptyResultCacheTTLSeconds)
	}
}

func TestSearchSingleflightCoalesces(t *testing.T) {
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeSingle, DefaultProviders: []string{model.ProviderSerper}, DefaultLimit: 10,
			DefaultDedupe: true, RequestTimeoutMS: 2000, CacheEnabled: true, CacheTTLSeconds: 60,
		},
		providers: []model.ProviderConfig{{Name: model.ProviderSerper, Enabled: true, Priority: 1, Weight: 1}},
		cache:     map[string][]byte{},
	}
	keyPool := &orchestratorTestKeyPool{}
	orchestrator := NewOrchestrator(provider.NewRegistry(orchestratorTestProvider{name: model.ProviderSerper, delay: 80 * time.Millisecond}), keyPool, store)
	req := model.SearchRequest{Query: "coalesce", Providers: []string{model.ProviderSerper}, ProvidersExplicit: true, Mode: model.SearchModeSingle}

	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("req-%d", i)
		go func(requestID string) {
			_, err := orchestrator.Search(context.Background(), req, requestID, 0)
			done <- err
		}(id)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(keyPool.acquired) != 1 {
		t.Fatalf("singleflight should acquire once, acquired=%v", keyPool.acquired)
	}
}

func TestProviderResultLimitsDoNotCapAtFifty(t *testing.T) {
	limits := providerResultLimits(map[string]map[string]interface{}{
		model.ProviderExa: {"request_result_limit": 120},
	})
	if got, want := limits[model.ProviderExa], 120; got != want {
		t.Fatalf("provider limit = %d, want %d", got, want)
	}
}

func TestEffectiveRequestTimeoutUsesProviderTimeoutWhenLarger(t *testing.T) {
	got := effectiveRequestTimeoutMS(20000, map[string]int{model.ProviderFirecrawl: 60000}, nil, []string{model.ProviderFirecrawl})
	if want := 61000; got != want {
		t.Fatalf("effective timeout = %d, want %d", got, want)
	}
	got = effectiveRequestTimeoutMS(20000, map[string]int{model.ProviderJina: 15000}, map[string]int{model.ProviderJina: 3}, []string{model.ProviderJina})
	if want := 31000; got != want {
		t.Fatalf("retry timeout = %d, want %d", got, want)
	}
}

func TestSearchProviderTimeoutCanExceedRuntimeTimeout(t *testing.T) {
	keyPool := &orchestratorTestKeyPool{}
	registry := provider.NewRegistry(orchestratorTestProvider{name: model.ProviderFirecrawl, delay: 60 * time.Millisecond})
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode:      model.SearchModeSingle,
			DefaultProviders: []string{model.ProviderFirecrawl},
			DefaultLimit:     10,
			DefaultDedupe:    true,
			RequestTimeoutMS: 20,
		},
		providers: []model.ProviderConfig{
			{Name: model.ProviderFirecrawl, Enabled: true, Priority: 1, Weight: 1, TimeoutMS: 100},
		},
	}
	orchestrator := NewOrchestrator(registry, keyPool, store)

	response, err := orchestrator.Search(context.Background(), model.SearchRequest{Query: "golang"}, "request-id", 0)
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if len(response.Results) != 1 || response.Providers[0].Status != "success" {
		t.Fatalf("response = %+v, want successful provider result", response)
	}
}

func newDisabledProviderTestOrchestrator(keyPool *orchestratorTestKeyPool) *Orchestrator {
	registry := provider.NewRegistry(orchestratorTestProvider{name: model.ProviderExa}, orchestratorTestProvider{name: model.ProviderSerper})
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode:      model.SearchModeParallel,
			DefaultProviders: []string{model.ProviderExa, model.ProviderSerper},
			DefaultLimit:     10,
			DefaultDedupe:    true,
			RequestTimeoutMS: 1000,
		},
		providers: []model.ProviderConfig{
			{Name: model.ProviderExa, Enabled: false, Priority: 1, Weight: 1},
			{Name: model.ProviderSerper, Enabled: true, Priority: 2, Weight: 1},
		},
	}
	return NewOrchestrator(registry, keyPool, store)
}

func stringSlicesEqual(left, right []string) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}

func TestShouldRetryWithNextKeyIncludesTimeout(t *testing.T) {
	if !shouldRetryWithNextKey(context.DeadlineExceeded, nil) {
		t.Fatal("timeout should retry with next key")
	}
	if !shouldRetryWithNextKey(fmt.Errorf("dial tcp: i/o timeout"), nil) {
		t.Fatal("network/upstream error should retry with next key")
	}
	if shouldRetryWithNextKey(&provider.Error{Type: provider.ErrorTypeInvalidResponse, Message: "bad json"}, nil) {
		t.Fatal("invalid_response should not retry by default")
	}
}

func TestSearchRetriesTimeoutWithNextKey(t *testing.T) {
	keyPool := &orchestratorTestKeyPool{}
	registry := provider.NewRegistry(orchestratorTestProvider{name: model.ProviderJina, err: context.DeadlineExceeded})
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeSingle, DefaultProviders: []string{model.ProviderJina}, DefaultLimit: 5,
			DefaultDedupe: true, RequestTimeoutMS: 2000, CacheEnabled: false,
		},
		providers: []model.ProviderConfig{
			{Name: model.ProviderJina, Enabled: true, Priority: 1, Weight: 1, TimeoutMS: 50, Settings: map[string]interface{}{"key_retry_count": 1}},
		},
	}
	orch := NewOrchestrator(registry, keyPool, store)
	_, err := orch.Search(context.Background(), model.SearchRequest{
		Query: "q", Providers: []string{model.ProviderJina}, ProvidersExplicit: true, Mode: model.SearchModeSingle,
	}, "req-jina-retry", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(keyPool.acquired) != 2 {
		t.Fatalf("expected 2 key attempts, got %v", keyPool.acquired)
	}
}

type flakyTestProvider struct {
	name  string
	calls int
}

func (p *flakyTestProvider) Name() string { return p.name }

func (p *flakyTestProvider) Search(ctx context.Context, req model.SearchRequest, key model.APIKey) (model.ProviderResponse, error) {
	p.calls++
	if p.calls == 1 {
		return model.ProviderResponse{}, context.DeadlineExceeded
	}
	return model.ProviderResponse{Results: []model.SearchResult{{Title: p.name, URL: "https://example.com/" + p.name, Provider: p.name, Score: 1}}}, nil
}

func (p *flakyTestProvider) HealthCheck(ctx context.Context, key model.APIKey) error { return nil }

// SupportsAnonymousKey 让测试替身满足 Provider 接口；该替身用于换 key 重试场景，与匿名能力无关。
func (p *flakyTestProvider) SupportsAnonymousKey() bool { return false }

func TestSearchRetryPassesTriedKeyIDsToKeyPool(t *testing.T) {
	// 这里需要正数 key ID：triedIDs 只在 key.ID>0 时记录，才能断言第二次取 key 时被排除。
	keyPool := &orchestratorTestKeyPool{ids: []int64{1, 2}}
	registry := provider.NewRegistry(&flakyTestProvider{name: model.ProviderJina})
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeSingle, DefaultProviders: []string{model.ProviderJina}, DefaultLimit: 5,
			DefaultDedupe: true, RequestTimeoutMS: 2000, CacheEnabled: false,
		},
		providers: []model.ProviderConfig{
			{Name: model.ProviderJina, Enabled: true, Priority: 1, Weight: 1, TimeoutMS: 50, Settings: map[string]interface{}{"key_retry_count": 1}},
		},
	}
	orch := NewOrchestrator(registry, keyPool, store)
	if _, err := orch.Search(context.Background(), model.SearchRequest{
		Query: "q", Providers: []string{model.ProviderJina}, ProvidersExplicit: true, Mode: model.SearchModeSingle,
	}, "req-jina-exclude", 0); err != nil {
		t.Fatal(err)
	}
	if len(keyPool.excluded) != 2 {
		t.Fatalf("Acquire calls = %d, want 2", len(keyPool.excluded))
	}
	if got := keyPool.excluded[0]; len(got) != 0 {
		t.Fatalf("first Acquire excludeIDs = %v, want none", got)
	}
	if got := keyPool.excluded[1]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("second Acquire excludeIDs = %v, want [1]", got)
	}
}

func TestShouldContinueFallback(t *testing.T) {
	if !shouldContinueFallback(providerExecution{err: nil, results: nil}) {
		t.Fatal("empty success should continue")
	}
	if shouldContinueFallback(providerExecution{err: nil, results: []model.SearchResult{{URL: "https://x"}}}) {
		t.Fatal("non-empty success should stop")
	}
	for _, errorType := range []string{
		provider.ErrorTypeRateLimited,
		provider.ErrorTypeQuotaExhausted,
		provider.ErrorTypeTimeout,
		provider.ErrorTypeUpstream,
		provider.ErrorTypeNoKey,
		provider.ErrorTypeAuth,
		provider.ErrorTypeInvalidResponse,
	} {
		if !shouldContinueFallback(providerExecution{err: &provider.Error{Type: errorType, Message: "x"}, errorType: errorType}) {
			t.Fatalf("%s should continue", errorType)
		}
	}
}

func TestSearchFallbackSkipsFailedProvider(t *testing.T) {
	registry := provider.NewRegistry(
		orchestratorTestProvider{name: "a", err: &provider.Error{Type: provider.ErrorTypeRateLimited, Message: "limited"}},
		orchestratorTestProvider{name: "b", resultN: 1},
	)
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeFallback, DefaultProviders: []string{"a", "b"}, DefaultLimit: 5,
			DefaultDedupe: true, RequestTimeoutMS: 2000, CacheEnabled: false,
		},
		providers: []model.ProviderConfig{
			{Name: "a", Enabled: true, Priority: 1, Weight: 1},
			{Name: "b", Enabled: true, Priority: 2, Weight: 1},
		},
	}
	orch := NewOrchestrator(registry, &orchestratorTestKeyPool{}, store)
	resp, err := orch.Search(context.Background(), model.SearchRequest{
		Query: "q", Providers: []string{"a", "b"}, ProvidersExplicit: true, Mode: model.SearchModeFallback,
	}, "req-fallback", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Provider != "b" {
		t.Fatalf("expected result from b, got %+v", resp.Results)
	}
	if len(resp.Providers) != 2 {
		t.Fatalf("expected both provider summaries, got %+v", resp.Providers)
	}
}

// resolveBaseURL 的解析优先级：key 级覆盖 -> 渠道默认，且两侧都做 TrimSpace。
func TestResolveBaseURL(t *testing.T) {
	cases := []struct {
		name             string
		keyBaseURL       string
		providerBaseURL  string
		want             string
	}{
		{name: "key 覆盖优先并去除空白", keyBaseURL: "  https://key.example.com/res/v1  ", providerBaseURL: "https://provider.example.com", want: "https://key.example.com/res/v1"},
		{name: "key 为空串回退渠道", keyBaseURL: "", providerBaseURL: "https://provider.example.com", want: "https://provider.example.com"},
		{name: "key 为纯空白回退渠道", keyBaseURL: "   ", providerBaseURL: "  https://provider.example.com  ", want: "https://provider.example.com"},
		{name: "两者都为空", keyBaseURL: "", providerBaseURL: "", want: ""},
	}
	for _, tc := range cases {
		if got := resolveBaseURL(tc.keyBaseURL, tc.providerBaseURL); got != tc.want {
			t.Fatalf("%s: resolveBaseURL(%q, %q) = %q, want %q", tc.name, tc.keyBaseURL, tc.providerBaseURL, got, tc.want)
		}
	}
}

// ResolveProxyURL 的三态语义：inherit（含空 / 非法值）回退渠道级、direct 强制直连、
// custom 用 key 自己的地址且地址为空时回退渠道级（而不是直连），所有输入都做 TrimSpace。
func TestResolveProxyURL(t *testing.T) {
	cases := []struct {
		name             string
		keyProxyMode     string
		keyProxyURL      string
		providerProxyURL string
		want             string
	}{
		{name: "inherit 回退渠道代理", keyProxyMode: "inherit", providerProxyURL: "http://channel:8080", want: "http://channel:8080"},
		{name: "inherit 且渠道未配置即直连", keyProxyMode: "inherit", providerProxyURL: "", want: ""},
		{name: "模式为空串按 inherit 处理", keyProxyMode: "", providerProxyURL: "http://channel:8080", want: "http://channel:8080"},
		{name: "非法模式按 inherit 处理", keyProxyMode: "bogus", providerProxyURL: "http://channel:8080", want: "http://channel:8080"},
		{name: "direct 忽略渠道代理与 key 地址", keyProxyMode: "direct", keyProxyURL: "http://key:9090", providerProxyURL: "http://channel:8080", want: ""},
		{name: "direct 且渠道未配置仍为直连", keyProxyMode: "direct", providerProxyURL: "", want: ""},
		{name: "custom 用 key 自己的地址", keyProxyMode: "custom", keyProxyURL: "http://key:9090", providerProxyURL: "http://channel:8080", want: "http://key:9090"},
		{name: "custom 地址为空回退渠道代理而非直连", keyProxyMode: "custom", keyProxyURL: "", providerProxyURL: "http://channel:8080", want: "http://channel:8080"},
		{name: "custom 地址为纯空白回退渠道代理", keyProxyMode: "custom", keyProxyURL: "   ", providerProxyURL: "http://channel:8080", want: "http://channel:8080"},
		{name: "custom 且两侧都没有地址时为空", keyProxyMode: "custom", keyProxyURL: "", providerProxyURL: "", want: ""},
		{name: "两侧带空白时裁剪", keyProxyMode: " custom ", keyProxyURL: "  http://key:9090  ", providerProxyURL: "  http://channel:8080  ", want: "http://key:9090"},
		{name: "direct 带空白也能识别", keyProxyMode: " direct ", providerProxyURL: "http://channel:8080", want: ""},
	}
	for _, tc := range cases {
		if got := ResolveProxyURL(tc.keyProxyMode, tc.keyProxyURL, tc.providerProxyURL); got != tc.want {
			t.Fatalf("%s: ResolveProxyURL(%q, %q, %q) = %q, want %q", tc.name, tc.keyProxyMode, tc.keyProxyURL, tc.providerProxyURL, got, tc.want)
		}
	}
}

// newTavilyStubServer 起一个只接受 POST /search 的 stub 服务，返回给定标题的 Tavily 格式结果。
// status >= 400 时返回 {"error":"boom"}（不含 quota/credit 字样，保证被归类为可重试的 upstream）。
// hits 用于断言请求实际打到了哪个地址。
func newTavilyStubServer(t *testing.T, title string, status int, hits *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		if r.Method != http.MethodPost || r.URL.Path != "/search" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if status >= http.StatusBadRequest {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": title, "url": "https://example.com/" + title, "content": "stub content"},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// newKeyBaseURLTestOrchestrator 构造一个注册了真实 Tavily 工厂的 orchestrator，
// 使 base_url 解析结果真正作用到 HTTP 请求地址上（而非被假 provider 吞掉）。
// providerBaseURL 是渠道默认地址，retryCount 是渠道的 key_retry_count。
func newKeyBaseURLTestOrchestrator(keyPool KeyPool, providerBaseURL string, retryCount int) *Orchestrator {
	return newTavilyTestOrchestrator(keyPool, providerBaseURL, "", retryCount)
}

// newTavilyTestOrchestrator 同 newKeyBaseURLTestOrchestrator，但可额外开启渠道级代理
// （channelProxyURL 非空时写入 proxy_enabled/proxy_url），用于验证 key 级代理三态是否真的改变了出口。
func newTavilyTestOrchestrator(keyPool KeyPool, providerBaseURL, channelProxyURL string, retryCount int) *Orchestrator {
	registry := provider.NewRegistry()
	registry.RegisterFactory(model.ProviderTavily, func(cfg provider.Config) provider.Provider {
		return provider.NewTavilyProvider(cfg)
	})
	settings := map[string]interface{}{"key_retry_count": retryCount}
	if channelProxyURL != "" {
		settings["proxy_enabled"] = true
		settings["proxy_url"] = channelProxyURL
	}
	store := &orchestratorTestStore{
		settings: model.RuntimeSettings{
			DefaultMode: model.SearchModeSingle, DefaultProviders: []string{model.ProviderTavily},
			DefaultLimit: 5, DefaultDedupe: false, RequestTimeoutMS: 2000, CacheEnabled: false,
		},
		providers: []model.ProviderConfig{
			{
				Name: model.ProviderTavily, Enabled: true, Priority: 1, Weight: 1, TimeoutMS: 1000,
				BaseURL:  providerBaseURL,
				Settings: settings,
			},
		},
	}
	return NewOrchestrator(registry, keyPool, store)
}

// keyBaseURLTestRequest 返回一个只查 Tavily 的显式单渠道请求。
func keyBaseURLTestRequest() model.SearchRequest {
	return model.SearchRequest{
		Query: "q", Providers: []string{model.ProviderTavily}, ProvidersExplicit: true,
		Mode: model.SearchModeSingle,
	}
}

// key 配置了 base_url 时必须打到该地址，而不是渠道默认地址。
func TestSearchUsesKeyBaseURLOverride(t *testing.T) {
	var providerHits, keyHits int32
	providerServer := newTavilyStubServer(t, "provider-default", http.StatusOK, &providerHits)
	keyServer := newTavilyStubServer(t, "key-override", http.StatusOK, &keyHits)

	keyPool := &orchestratorTestKeyPool{baseURLs: []string{keyServer.URL}}
	orch := newKeyBaseURLTestOrchestrator(keyPool, providerServer.URL, 0)
	resp, err := orch.Search(context.Background(), keyBaseURLTestRequest(), "req-key-base-url", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "key-override" {
		t.Fatalf("results = %+v, want result from key base_url stub", resp.Results)
	}
	if got := atomic.LoadInt32(&keyHits); got != 1 {
		t.Fatalf("key base_url stub hits = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&providerHits); got != 0 {
		t.Fatalf("provider default base_url hits = %d, want 0", got)
	}
}

// key 未配置 base_url 时必须回退到渠道默认地址。
func TestSearchFallsBackToProviderBaseURL(t *testing.T) {
	var providerHits int32
	providerServer := newTavilyStubServer(t, "provider-default", http.StatusOK, &providerHits)

	keyPool := &orchestratorTestKeyPool{}
	orch := newKeyBaseURLTestOrchestrator(keyPool, providerServer.URL, 0)
	resp, err := orch.Search(context.Background(), keyBaseURLTestRequest(), "req-provider-base-url", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "provider-default" {
		t.Fatalf("results = %+v, want result from provider default base_url", resp.Results)
	}
	if got := atomic.LoadInt32(&providerHits); got != 1 {
		t.Fatalf("provider default base_url hits = %d, want 1", got)
	}
}

// newProxyStubServer 起一个记录命中次数的 HTTP 代理 stub。
// 走代理的请求以绝对形式（`POST http://host/path`）到达，这里不转发，直接按 Tavily 格式返回结果，
// 因此「命中代理 stub」本身就证明请求确实经过了代理。
func newProxyStubServer(t *testing.T, title string, hits *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"results": []map[string]interface{}{
				{"title": title, "url": "https://example.com/" + title, "content": "proxied"},
			},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// requireHostLoopbackProxy 在容器内跳过依赖 loopback 代理地址的用例：
// provider.NormalizeProxyURL 检测到 /.dockerenv 时会把 `//127.0.0.1:` 与 `//localhost:` 改写成
// `host.docker.internal`，本测试用的 httptest 代理地址会因此不可达 —— 这是环境差异而非被测逻辑的问题。
func requireHostLoopbackProxy(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/.dockerenv"); err == nil {
		t.Skip("容器内 loopback 代理地址会被改写为 host.docker.internal，跳过出口验证")
	}
}

// key 级代理三态必须真正作用到出口上（而不是只体现在配置里）。
// 渠道默认地址设为不可达的 127.0.0.1:1：只有真的经过代理 stub 才能拿到结果，
// 反过来「不该走代理」的用例则要求代理 stub 命中数为 0。
func TestSearchResolvesKeyProxyMode(t *testing.T) {
	cases := []struct {
		name         string
		proxyModes   []string
		proxyURLs    []string
		baseURLs     []string
		wantTitle    string
		wantChannel  int32
		wantKeyProxy int32
	}{
		{
			name: "inherit 走渠道代理", proxyModes: []string{model.ProxyModeInherit},
			wantTitle: "via-channel-proxy", wantChannel: 1,
		},
		{
			name: "模式为空按 inherit 走渠道代理", proxyModes: []string{""},
			wantTitle: "via-channel-proxy", wantChannel: 1,
		},
		{
			name: "direct 强制直连不走渠道代理", proxyModes: []string{model.ProxyModeDirect},
			// 直连时用 key 级 base_url 指向可达的真实上游 stub，证明请求绕过了渠道代理。
			baseURLs: []string{"PROVIDER_STUB"}, wantTitle: "direct-connection", wantChannel: 0,
		},
		{
			name: "custom 用 key 自己的代理", proxyModes: []string{model.ProxyModeCustom}, proxyURLs: []string{"KEY_PROXY_STUB"},
			wantTitle: "via-key-proxy", wantKeyProxy: 1,
		},
		{
			name: "custom 地址为空回退渠道代理", proxyModes: []string{model.ProxyModeCustom}, proxyURLs: []string{"   "},
			wantTitle: "via-channel-proxy", wantChannel: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requireHostLoopbackProxy(t)

			var channelHits, keyProxyHits, providerHits int32
			channelProxy := newProxyStubServer(t, "via-channel-proxy", &channelHits)
			keyProxy := newProxyStubServer(t, "via-key-proxy", &keyProxyHits)
			providerStub := newTavilyStubServer(t, "direct-connection", http.StatusOK, &providerHits)

			providerBaseURL := "http://127.0.0.1:1"
			keyPool := &orchestratorTestKeyPool{proxyModes: tc.proxyModes}
			keyPool.proxyURLs = replacePlaceholder(tc.proxyURLs, "KEY_PROXY_STUB", keyProxy.URL)
			keyPool.baseURLs = replacePlaceholder(tc.baseURLs, "PROVIDER_STUB", providerStub.URL)

			orch := newTavilyTestOrchestrator(keyPool, providerBaseURL, channelProxy.URL, 0)
			resp, err := orch.Search(context.Background(), keyBaseURLTestRequest(), "req-key-proxy-mode", 0)
			if err != nil {
				t.Fatalf("Search returned error: %v", err)
			}
			if len(resp.Results) != 1 || resp.Results[0].Title != tc.wantTitle {
				t.Fatalf("results = %+v, want title %q", resp.Results, tc.wantTitle)
			}
			if got := atomic.LoadInt32(&channelHits); got != tc.wantChannel {
				t.Fatalf("渠道代理命中 = %d, want %d", got, tc.wantChannel)
			}
			if got := atomic.LoadInt32(&keyProxyHits); got != tc.wantKeyProxy {
				t.Fatalf("key 代理命中 = %d, want %d", got, tc.wantKeyProxy)
			}
		})
	}
}

// replacePlaceholder 把表驱动用例里的占位串换成运行时才知道的 stub 地址（stub 起在用例内部）。
func replacePlaceholder(values []string, placeholder, replacement string) []string {
	if len(values) == 0 {
		return nil
	}
	replaced := make([]string, 0, len(values))
	for _, value := range values {
		if value == placeholder {
			value = replacement
		}
		replaced = append(replaced, value)
	}
	return replaced
}

// 重试换 key 时必须按新 key 重建 adapter：两个 key 指向不同中转站，第一次失败后第二次要打到新地址。
func TestSearchRebuildsAdapterPerKeyBaseURL(t *testing.T) {
	var firstHits, secondHits int32
	firstServer := newTavilyStubServer(t, "first-key", http.StatusInternalServerError, &firstHits)
	secondServer := newTavilyStubServer(t, "second-key", http.StatusOK, &secondHits)

	keyPool := &orchestratorTestKeyPool{baseURLs: []string{firstServer.URL, secondServer.URL}}
	// 渠道默认地址不可达：若 adapter 未按 key 重建，请求不会命中第二个 stub。
	orch := newKeyBaseURLTestOrchestrator(keyPool, "http://127.0.0.1:1", 1)
	resp, err := orch.Search(context.Background(), keyBaseURLTestRequest(), "req-key-base-url-retry", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Title != "second-key" {
		t.Fatalf("results = %+v, want result from second key base_url", resp.Results)
	}
	if got := atomic.LoadInt32(&firstHits); got != 1 {
		t.Fatalf("first key base_url hits = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&secondHits); got != 1 {
		t.Fatalf("second key base_url hits = %d, want 1", got)
	}
}
