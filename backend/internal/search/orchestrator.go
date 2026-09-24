package search

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
)

const emptyResultCacheTTLSeconds = 60

type KeyPool interface {
	Acquire(ctx context.Context, providerName string, excludeIDs ...int64) (model.APIKey, func(bool, error), error)
}

type Store interface {
	GetAPIKeyByID(ctx context.Context, id int64) (model.APIKey, error)
	RecordKeyResult(ctx context.Context, key model.APIKey, success bool, errorType string) error
	UpdateProviderKeyOfficialQuota(ctx context.Context, id int64, quota model.ProviderKeyQuotaResult) error
	RuntimeSettings(ctx context.Context) (model.RuntimeSettings, error)
	ListProviders(ctx context.Context) ([]model.ProviderConfig, error)
	RecordSearchLog(ctx context.Context, input model.SearchLogInput) error
	GetCache(ctx context.Context, cacheKey string) ([]byte, bool, error)
	SetCache(ctx context.Context, cacheKey string, payload []byte, ttlSeconds int) error
}

type Orchestrator struct {
	registry       *provider.Registry
	keyPool        KeyPool
	store          Store
	quotaMu        sync.Mutex
	quotaRefreshes map[int64]quotaRefreshState
	searchGroup    singleflight.Group
}

type quotaRefreshState struct {
	inFlight     bool
	lastStarted  time.Time
	lastFinished time.Time
}

func NewOrchestrator(registry *provider.Registry, keyPool KeyPool, store Store) *Orchestrator {
	return &Orchestrator{registry: registry, keyPool: keyPool, store: store, quotaRefreshes: map[int64]quotaRefreshState{}}
}

func (o *Orchestrator) Search(ctx context.Context, req model.SearchRequest, requestID string, apiTokenID int64) (model.SearchResponse, error) {
	started := time.Now()
	settings, err := o.store.RuntimeSettings(ctx)
	if err != nil {
		return model.SearchResponse{}, err
	}
	providerConfigs, err := o.store.ListProviders(ctx)
	if err != nil {
		return model.SearchResponse{}, err
	}
	req = applyDefaults(req, settings)
	if !req.ProvidersExplicit {
		req.Providers = routeProviders(req.Providers, providerConfigs, settings.ProviderRoutingStrategy)
	}
	req.Providers = filterEnabledProviders(req.Providers, providerConfigs)
	providerConfigByName := providerConfigMap(providerConfigs)
	providerSettings := providerSettingsFromProviders(providerConfigs)
	providerLimits := providerResultLimits(providerSettings)
	keyRetryCounts := providerKeyRetryCounts(providerSettings)
	providerTimeouts := providerTimeouts(providerSettings)
	providerProxies := providerProxies(providerSettings)
	providerRetryableErrors := providerRetryableErrors(providerSettings)
	if !req.LimitExplicit && len(req.Providers) == 1 {
		if limit := providerLimits[req.Providers[0]]; limit > 0 {
			req.Limit = limit
		}
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(effectiveRequestTimeoutMS(settings.RequestTimeoutMS, providerTimeouts, keyRetryCounts, req.Providers))*time.Millisecond)
	defer cancel()

	cacheKey := o.cacheKey(req, providerLimits)
	cacheRead := settings.CacheEnabled && req.Cache != model.CachePolicyBypass && req.Cache != model.CachePolicyRefresh
	cacheWrite := settings.CacheEnabled && req.Cache != model.CachePolicyBypass
	if cacheRead {
		if payload, hit, err := o.store.GetCache(ctx, cacheKey); err != nil {
			return model.SearchResponse{}, err
		} else if hit {
			var cached model.SearchResponse
			if err := json.Unmarshal(payload, &cached); err == nil {
				cached.Meta.CacheHit = true
				cached.Meta.RequestID = requestID
				cached.Meta.LatencyMS = time.Since(started).Milliseconds()
				cached.Meta.CacheKey = cacheKey
				requestJSON, _ := json.Marshal(req)
				responseJSON, _ := json.Marshal(cached)
				_ = o.store.RecordSearchLog(context.Background(), model.SearchLogInput{
					RequestID:    requestID,
					APITokenID:   apiTokenID,
					Query:        req.Query,
					Mode:         string(req.Mode),
					CompatFormat: string(req.CompatFormat),
					Providers:    req.Providers,
					CachePolicy:  string(req.Cache),
					CacheHit:     true,
					ResultCount:  len(cached.Results),
					Status:       "success",
					LatencyMS:    cached.Meta.LatencyMS,
					RequestJSON:  requestJSON,
					ResponseJSON: responseJSON,
				})
				return cached, nil
			}
		}
	}

	run := func(runCtx context.Context) (searchOutcome, error) {
		return o.executeSearch(runCtx, req, cacheKey, cacheWrite, settings, providerConfigByName, providerLimits, keyRetryCounts, providerTimeouts, providerProxies, providerRetryableErrors)
	}

	var outcome searchOutcome
	var errRun error
	if cacheWrite {
		v, err, _ := o.searchGroup.Do(cacheKey, func() (interface{}, error) {
			flightCtx, cancel := detachContext(ctx)
			defer cancel()
			out, err := run(flightCtx)
			if err != nil {
				return nil, err
			}
			return out, nil
		})
		errRun = err
		if errRun == nil {
			outcome = v.(searchOutcome)
		}
	} else {
		outcome, errRun = run(ctx)
	}
	if errRun != nil {
		return model.SearchResponse{}, errRun
	}

	response := outcome.response
	response.Meta.RequestID = requestID
	response.Meta.LatencyMS = time.Since(started).Milliseconds()
	response.Meta.CacheHit = false
	response.Meta.CacheKey = cacheKey

	requestJSON, _ := json.Marshal(req)
	responseJSON, _ := json.Marshal(responseLogPayload(response, outcome.providerResults))
	_ = o.store.RecordSearchLog(context.Background(), model.SearchLogInput{
		RequestID:    requestID,
		APITokenID:   apiTokenID,
		Query:        req.Query,
		Mode:         string(req.Mode),
		CompatFormat: string(req.CompatFormat),
		Providers:    req.Providers,
		CachePolicy:  string(req.Cache),
		CacheHit:     false,
		ResultCount:  len(response.Results),
		Status:       outcome.status,
		ErrorMessage: outcome.errorMessage,
		LatencyMS:    response.Meta.LatencyMS,
		RequestJSON:  requestJSON,
		ResponseJSON: responseJSON,
		Calls:        callLogs(outcome.providerResults),
	})
	return response, nil
}

type searchOutcome struct {
	response        model.SearchResponse
	providerResults []providerExecution
	status          string
	errorMessage    string
}

func (o *Orchestrator) executeSearch(
	ctx context.Context,
	req model.SearchRequest,
	cacheKey string,
	cacheWrite bool,
	settings model.RuntimeSettings,
	providerConfigByName map[string]model.ProviderConfig,
	providerLimits map[string]int,
	keyRetryCounts map[string]int,
	providerTimeouts map[string]int,
	providerProxies map[string]string,
	providerRetryableErrors map[string]map[string]bool,
) (searchOutcome, error) {
	var providerResults []providerExecution
	switch req.Mode {
	case model.SearchModeFallback:
		providerResults = o.searchFallback(ctx, req, providerConfigByName, providerLimits, keyRetryCounts, providerTimeouts, providerProxies, providerRetryableErrors)
	case model.SearchModeSingle:
		providerResults = o.searchSingle(ctx, req, providerConfigByName, providerLimits, keyRetryCounts, providerTimeouts, providerProxies, providerRetryableErrors)
	default:
		providerResults = o.searchParallel(ctx, req, providerConfigByName, providerLimits, keyRetryCounts, providerTimeouts, providerProxies, providerRetryableErrors)
	}

	results, deduped := mergeResults(providerResults, req)
	status := "success"
	errorMessage := ""
	if len(results) == 0 && hasOnlyErrors(providerResults) {
		status = "error"
		errorMessage = firstError(providerResults)
	}
	response := model.SearchResponse{
		Results:   results,
		Providers: summaries(providerResults),
		Meta: model.SearchMeta{
			Mode:             req.Mode,
			CompatFormat:     req.CompatFormat,
			TotalResults:     len(results),
			DedupedResults:   deduped,
			CacheHit:         false,
			CacheKey:         cacheKey,
			ProvidersQueried: providersQueried(providerResults),
		},
	}
	if cacheWrite && shouldWriteSearchCache(req.Mode, status, providerResults) {
		toStore := truncateResultsForCache(response, settings.CacheMaxResults)
		if payload, err := json.Marshal(toStore); err == nil {
			_ = o.store.SetCache(context.Background(), cacheKey, payload, cacheTTLSeconds(settings.CacheTTLSeconds, len(toStore.Results)))
		}
	}
	return searchOutcome{response: response, providerResults: providerResults, status: status, errorMessage: errorMessage}, nil
}

func detachContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline)
	}
	return context.WithCancel(context.Background())
}

func shouldWriteSearchCache(mode model.SearchMode, status string, executions []providerExecution) bool {
	if status != "success" {
		return false
	}
	switch mode {
	case model.SearchModeFallback, model.SearchModeSingle:
		for _, execution := range executions {
			if execution.err == nil {
				return true
			}
		}
		return false
	default: // parallel
		return !hasAnyProviderError(executions)
	}
}

func hasAnyProviderError(executions []providerExecution) bool {
	for _, execution := range executions {
		if execution.err != nil {
			return true
		}
	}
	return false
}

func truncateResultsForCache(response model.SearchResponse, maxResults int) model.SearchResponse {
	if maxResults <= 0 || len(response.Results) <= maxResults {
		return response
	}
	cloned := response
	cloned.Results = append([]model.SearchResult(nil), response.Results[:maxResults]...)
	cloned.Meta.TotalResults = len(cloned.Results)
	return cloned
}

func cacheTTLSeconds(configuredTTL, resultCount int) int {
	if resultCount == 0 {
		if configuredTTL > 0 && configuredTTL < emptyResultCacheTTLSeconds {
			return configuredTTL
		}
		return emptyResultCacheTTLSeconds
	}
	return configuredTTL
}

func (o *Orchestrator) searchParallel(ctx context.Context, req model.SearchRequest, providerConfigs map[string]model.ProviderConfig, providerLimits map[string]int, keyRetryCounts map[string]int, providerTimeouts map[string]int, providerProxies map[string]string, retryableErrors map[string]map[string]bool) []providerExecution {
	var wg sync.WaitGroup
	results := make([]providerExecution, len(req.Providers))
	for index, name := range req.Providers {
		wg.Add(1)
		go func(i int, providerName string) {
			defer wg.Done()
			results[i] = o.callProvider(ctx, req, providerName, providerConfigs, providerLimits, keyRetryCounts, providerTimeouts, providerProxies, retryableErrors)
		}(index, name)
	}
	wg.Wait()
	return results
}

func (o *Orchestrator) searchFallback(ctx context.Context, req model.SearchRequest, providerConfigs map[string]model.ProviderConfig, providerLimits map[string]int, keyRetryCounts map[string]int, providerTimeouts map[string]int, providerProxies map[string]string, retryableErrors map[string]map[string]bool) []providerExecution {
	results := []providerExecution{}
	for _, name := range req.Providers {
		if ctx.Err() != nil {
			break
		}
		execution := o.callProvider(ctx, req, name, providerConfigs, providerLimits, keyRetryCounts, providerTimeouts, providerProxies, retryableErrors)
		results = append(results, execution)
		if !shouldContinueFallback(execution) {
			break
		}
	}
	return results
}

// shouldContinueFallback decides whether fallback mode tries the next provider.
// Continues on operational failures (rate limit / quota / timeout / upstream / no_key / auth / invalid_response)
// and empty success. Stops only when a provider returns non-empty results.
func shouldContinueFallback(execution providerExecution) bool {
	if execution.err == nil {
		return len(execution.results) == 0
	}
	switch execution.errorType {
	case provider.ErrorTypeRateLimited,
		provider.ErrorTypeQuotaExhausted,
		provider.ErrorTypeTimeout,
		provider.ErrorTypeUpstream,
		provider.ErrorTypeNoKey,
		provider.ErrorTypeAuth,
		provider.ErrorTypeInvalidResponse:
		return true
	default:
		// Unknown error types still skip this provider and try the next one.
		return true
	}
}

func (o *Orchestrator) searchSingle(ctx context.Context, req model.SearchRequest, providerConfigs map[string]model.ProviderConfig, providerLimits map[string]int, keyRetryCounts map[string]int, providerTimeouts map[string]int, providerProxies map[string]string, retryableErrors map[string]map[string]bool) []providerExecution {
	if len(req.Providers) == 0 {
		return nil
	}
	return []providerExecution{o.callProvider(ctx, req, req.Providers[0], providerConfigs, providerLimits, keyRetryCounts, providerTimeouts, providerProxies, retryableErrors)}
}

// callProvider 调用单个渠道，失败时按配置的重试次数换 key 重试。
// 每次尝试都基于当前 key 重新构建 adapter：key 级 base_url 覆盖下不同 key 可能指向不同中转站，
// adapter 因此不能跨 key 复用。返回的 providerExecution 记录实际使用的 key、各次尝试的错误类型与耗时。
func (o *Orchestrator) callProvider(ctx context.Context, req model.SearchRequest, providerName string, providerConfigs map[string]model.ProviderConfig, providerLimits map[string]int, keyRetryCounts map[string]int, providerTimeouts map[string]int, providerProxies map[string]string, retryableErrors map[string]map[string]bool) providerExecution {
	started := time.Now()
	execution := providerExecution{provider: providerName, status: "error"}
	// 先校验渠道是否注册，再进入取 key 的循环：未注册渠道没有可用 adapter，
	// 提前返回可避免消耗 key 池，也避免把「渠道不存在」的失败记到 key 上。
	if !o.providerRegistered(providerName) {
		err := &provider.Error{Type: provider.ErrorTypeUpstream, Message: "provider is not registered"}
		execution.err = err
		execution.errorType = provider.ErrorType(err)
		execution.latencyMS = time.Since(started).Milliseconds()
		execution.attempts = append(execution.attempts, providerAttempt{
			AttemptIndex: 1,
			Status:       "error",
			ErrorType:    execution.errorType,
			Err:          err,
			LatencyMS:    execution.latencyMS,
		})
		return execution
	}
	providerReq := req
	if limit := providerLimits[providerName]; limit > 0 {
		providerReq.Limit = limit
	}
	retryCount, ok := keyRetryCounts[providerName]
	if !ok {
		retryCount = 3
	}
	if retryCount < 0 {
		retryCount = 0
	}
	if retryCount > 20 {
		retryCount = 20
	}
	attempts := retryCount + 1
	var triedIDs []int64
	for attempt := 0; attempt < attempts; attempt++ {
		attemptStarted := time.Now()
		attemptIndex := attempt + 1
		key, release, err := o.keyPool.Acquire(ctx, providerName, triedIDs...)
		if err != nil {
			execution.err = err
			execution.errorType = provider.ErrorType(err)
			execution.latencyMS = time.Since(started).Milliseconds()
			execution.attempts = append(execution.attempts, providerAttempt{
				AttemptIndex: attemptIndex,
				Status:       "error",
				ErrorType:    execution.errorType,
				Err:          err,
				LatencyMS:    time.Since(attemptStarted).Milliseconds(),
			})
			return execution
		}
		// adapter 必须按当前 key 构建：key 可覆盖 base_url，同一渠道的不同 key 可能指向不同中转站。
		adapter, ok := o.adapterForProvider(providerName, providerConfigs[providerName], key.BaseURL, providerTimeouts[providerName], providerProxies[providerName])
		if !ok {
			// 注册性已在上方校验过，这里是防御性兜底；key 已经取出，必须释放以免并发计数泄漏。
			registrationErr := &provider.Error{Type: provider.ErrorTypeUpstream, Message: "provider is not registered"}
			release(false, registrationErr)
			execution.err = registrationErr
			execution.errorType = provider.ErrorType(registrationErr)
			execution.latencyMS = time.Since(started).Milliseconds()
			execution.attempts = append(execution.attempts, providerAttempt{
				Key:          key,
				KeyAlias:     key.Alias,
				AttemptIndex: attemptIndex,
				Status:       "error",
				ErrorType:    execution.errorType,
				Err:          registrationErr,
				LatencyMS:    time.Since(attemptStarted).Milliseconds(),
			})
			return execution
		}
		callCtx := ctx
		cancel := func() {}
		if timeout := providerTimeouts[providerName]; timeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, time.Duration(timeout)*time.Millisecond)
		}
		providerResponse, err := adapter.Search(callCtx, providerReq, key)
		cancel()
		if key.ID > 0 {
			// Remember the attempted key so the next retry prefers a different one.
			triedIDs = append(triedIDs, key.ID)
		}
		success := err == nil
		release(success, err)
		o.refreshOfficialQuota(key)
		attemptLatency := time.Since(attemptStarted).Milliseconds()
		execution.latencyMS = time.Since(started).Milliseconds()
		execution.key = key
		execution.keyAlias = key.Alias
		if err == nil {
			execution.status = "success"
			execution.err = nil
			execution.errorType = ""
			execution.results = providerResponse.Results
			execution.attempts = append(execution.attempts, providerAttempt{
				Key:          key,
				KeyAlias:     key.Alias,
				AttemptIndex: attemptIndex,
				Status:       "success",
				LatencyMS:    attemptLatency,
				ResultCount:  len(providerResponse.Results),
				Usage:        providerResponse.Usage,
			})
			return execution
		}
		willRetry := attempt < attempts-1 && shouldRetryWithNextKey(err, retryableErrors[providerName])
		execution.err = err
		execution.errorType = provider.ErrorType(err)
		execution.attempts = append(execution.attempts, providerAttempt{
			Key:          key,
			KeyAlias:     key.Alias,
			AttemptIndex: attemptIndex,
			WillRetry:    willRetry,
			Status:       "error",
			ErrorType:    execution.errorType,
			Err:          err,
			LatencyMS:    attemptLatency,
		})
		if !willRetry {
			return execution
		}
	}
	return execution
}

func shouldRetryWithNextKey(err error, allowed map[string]bool) bool {
	errorType := provider.ErrorType(err)
	if len(allowed) == 0 {
		switch errorType {
		case provider.ErrorTypeAuth, provider.ErrorTypeQuotaExhausted, provider.ErrorTypeRateLimited, provider.ErrorTypeTimeout, provider.ErrorTypeUpstream:
			return true
		default:
			return false
		}
	}
	return allowed[errorType]
}

func (o *Orchestrator) refreshOfficialQuota(key model.APIKey) {
	if key.ID == 0 || !autoRefreshOfficialQuota(key.ProviderName) {
		return
	}
	if key.ProviderName == model.ProviderExa && strings.TrimSpace(key.ExaServiceKey) == "" {
		return
	}
	now := time.Now()
	interval := quotaRefreshInterval(key.ProviderName)
	o.quotaMu.Lock()
	state := o.quotaRefreshes[key.ID]
	if state.inFlight || (!state.lastStarted.IsZero() && now.Sub(state.lastStarted) < interval) || (!state.lastFinished.IsZero() && now.Sub(state.lastFinished) < interval) {
		o.quotaMu.Unlock()
		return
	}
	state.inFlight = true
	state.lastStarted = now
	o.quotaRefreshes[key.ID] = state
	o.quotaMu.Unlock()

	go func() {
		defer func() {
			o.quotaMu.Lock()
			state := o.quotaRefreshes[key.ID]
			state.inFlight = false
			state.lastFinished = time.Now()
			o.quotaRefreshes[key.ID] = state
			o.quotaMu.Unlock()
		}()
		quota, err := QueryOfficialQuota(context.Background(), key, model.ProviderKeyQuotaRequest{})
		if err != nil {
			quota = model.ProviderKeyQuotaResult{Provider: key.ProviderName, Alias: key.Alias, Supported: true, Status: "error", Message: err.Error(), FetchedAt: time.Now()}
		}
		_ = o.store.UpdateProviderKeyOfficialQuota(context.Background(), key.ID, quota)
	}()
}

func autoRefreshOfficialQuota(providerName string) bool {
	switch providerName {
	case model.ProviderSerper, model.ProviderBrave:
		return false
	default:
		return true
	}
}

func quotaRefreshInterval(providerName string) time.Duration {
	switch providerName {
	case model.ProviderExa:
		return 5 * time.Minute
	case model.ProviderYou, model.ProviderJina, model.ProviderTavily, model.ProviderFirecrawl, model.ProviderSerper, model.ProviderBrave:
		return time.Minute
	default:
		return 5 * time.Minute
	}
}

func applyDefaults(req model.SearchRequest, settings model.RuntimeSettings) model.SearchRequest {
	req.Query = strings.TrimSpace(req.Query)
	if req.Mode == "" {
		req.Mode = settings.DefaultMode
	}
	if req.Mode == "" {
		req.Mode = model.SearchModeParallel
	}
	if len(req.Providers) == 0 {
		req.Providers = settings.DefaultProviders
	}
	if len(req.Providers) == 0 {
		req.Providers = append([]string(nil), model.DefaultProviders...)
	}
	if req.Limit <= 0 {
		req.Limit = settings.DefaultLimit
	}
	if req.Limit <= 0 {
		req.Limit = 10
	}
	if req.Dedupe == nil {
		req.Dedupe = &settings.DefaultDedupe
	}
	if req.Cache == "" {
		req.Cache = model.CachePolicyDefault
	}
	if req.CompatFormat == "" {
		req.CompatFormat = model.CompatFormatNative
	}
	return req
}

func filterEnabledProviders(providers []string, configs []model.ProviderConfig) []string {
	if len(providers) == 0 || len(configs) == 0 {
		return providers
	}
	configByName := map[string]model.ProviderConfig{}
	for _, item := range configs {
		configByName[item.Name] = item
	}
	filtered := make([]string, 0, len(providers))
	for _, name := range providers {
		if config, ok := configByName[name]; ok && !config.Enabled {
			continue
		}
		filtered = append(filtered, name)
	}
	return filtered
}

func routeProviders(providers []string, configs []model.ProviderConfig, strategy string) []string {
	if len(providers) <= 1 {
		return providers
	}
	configByName := map[string]model.ProviderConfig{}
	for _, item := range configs {
		configByName[item.Name] = item
	}
	ordered := append([]string(nil), providers...)
	switch strategy {
	case "priority":
		sort.SliceStable(ordered, func(i, j int) bool {
			left := configByName[ordered[i]]
			right := configByName[ordered[j]]
			if left.Priority == right.Priority {
				return ordered[i] < ordered[j]
			}
			return left.Priority < right.Priority
		})
	case "weighted":
		sort.SliceStable(ordered, func(i, j int) bool {
			left := configByName[ordered[i]]
			right := configByName[ordered[j]]
			if left.Weight == right.Weight {
				return left.Priority < right.Priority
			}
			return left.Weight > right.Weight
		})
	case "random":
		rand.Shuffle(len(ordered), func(i, j int) { ordered[i], ordered[j] = ordered[j], ordered[i] })
	case "weighted_random":
		return weightedProviderOrder(ordered, configByName)
	case "available_keys":
		sort.SliceStable(ordered, func(i, j int) bool {
			left := configByName[ordered[i]]
			right := configByName[ordered[j]]
			if left.AvailableKeys == right.AvailableKeys {
				return left.Priority < right.Priority
			}
			return left.AvailableKeys > right.AvailableKeys
		})
	}
	return ordered
}

func weightedProviderOrder(providers []string, configByName map[string]model.ProviderConfig) []string {
	remaining := append([]string(nil), providers...)
	ordered := make([]string, 0, len(providers))
	for len(remaining) > 0 {
		totalWeight := 0
		for _, name := range remaining {
			weight := configByName[name].Weight
			if weight <= 0 {
				weight = 1
			}
			totalWeight += weight
		}
		pick := rand.Intn(totalWeight)
		selected := 0
		for index, name := range remaining {
			weight := configByName[name].Weight
			if weight <= 0 {
				weight = 1
			}
			if pick < weight {
				selected = index
				break
			}
			pick -= weight
		}
		ordered = append(ordered, remaining[selected])
		remaining = append(remaining[:selected], remaining[selected+1:]...)
	}
	return ordered
}

func mergeResults(executions []providerExecution, req model.SearchRequest) ([]model.SearchResult, int) {
	merged := []model.SearchResult{}
	seen := map[string]int{}
	deduped := 0
	dedupe := req.Dedupe == nil || *req.Dedupe
	for _, execution := range executions {
		for _, result := range execution.results {
			canonical := canonicalURL(result.URL)
			if canonical == "" {
				continue
			}
			if dedupe {
				if existingIndex, ok := seen[canonical]; ok {
					existing := &merged[existingIndex]
					existing.Providers = appendUnique(existing.Providers, result.Provider)
					if result.Score > existing.Score {
						existing.Score = result.Score
					}
					deduped++
					continue
				}
				seen[canonical] = len(merged)
			}
			if len(result.Providers) == 0 && result.Provider != "" {
				result.Providers = []string{result.Provider}
			}
			merged = append(merged, result)
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score == merged[j].Score {
			return merged[i].Title < merged[j].Title
		}
		return merged[i].Score > merged[j].Score
	})
	if req.Limit > 0 && len(merged) > req.Limit {
		merged = merged[:req.Limit]
	}
	return merged, deduped
}

func canonicalURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return strings.TrimSpace(raw)
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return strings.TrimRight(parsed.String(), "/")
}

func appendUnique(values []string, value string) []string {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func providerConfigMap(providers []model.ProviderConfig) map[string]model.ProviderConfig {
	items := map[string]model.ProviderConfig{}
	for _, item := range providers {
		items[item.Name] = item
	}
	return items
}

// resolveBaseURL 解析生效的基础 URL：key 级覆盖优先，为空（含纯空白）时回退到渠道默认值。
// 两侧都会 TrimSpace，避免把只有空白的配置当成有效覆盖。
func resolveBaseURL(keyBaseURL string, providerBaseURL string) string {
	if trimmed := strings.TrimSpace(keyBaseURL); trimmed != "" {
		return trimmed
	}
	return strings.TrimSpace(providerBaseURL)
}

// adapterForProvider 按渠道名构建适配器。
// keyBaseURL 是当前 key 的基础 URL 覆盖值（可为空，此时使用 cfg.BaseURL）；
// timeoutMS 为渠道级超时覆盖，<=0 时回退 cfg.TimeoutMS；proxyURL 为渠道代理地址。
// 渠道未注册工厂时回退到已注册的实例；两者都不存在时返回 false。
func (o *Orchestrator) adapterForProvider(name string, cfg model.ProviderConfig, keyBaseURL string, timeoutMS int, proxyURL string) (provider.Provider, bool) {
	providerCfg := provider.Config{BaseURL: resolveBaseURL(keyBaseURL, cfg.BaseURL), ProxyURL: proxyURL}
	if timeoutMS <= 0 {
		timeoutMS = cfg.TimeoutMS
	}
	if timeoutMS > 0 {
		providerCfg.Timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	if adapter, ok := o.registry.Build(name, providerCfg); ok {
		return adapter, true
	}
	return o.registry.Get(name)
}

// providerRegistered 报告渠道是否已注册（工厂或实例任一存在）。
// 复用 Registry 既有的 Names() 只读接口；渠道数量固定且很少，这里线性判断的开销可忽略。
func (o *Orchestrator) providerRegistered(name string) bool {
	for _, item := range o.registry.Names() {
		if item == name {
			return true
		}
	}
	return false
}

func providerSettingsFromProviders(providers []model.ProviderConfig) map[string]map[string]interface{} {
	settings := map[string]map[string]interface{}{}
	for _, item := range providers {
		providerSettings := map[string]interface{}{}
		for key, value := range item.Settings {
			providerSettings[key] = value
		}
		providerSettings["_timeout_ms"] = item.TimeoutMS
		settings[item.Name] = providerSettings
	}
	return settings
}

func providerResultLimits(settings map[string]map[string]interface{}) map[string]int {
	limits := map[string]int{}
	for name, item := range settings {
		if limit := intSetting(item, "request_result_limit"); limit > 0 {
			limits[name] = limit
		}
	}
	return limits
}

func providerKeyRetryCounts(settings map[string]map[string]interface{}) map[string]int {
	counts := map[string]int{}
	for name, item := range settings {
		count := 3
		if _, ok := item["key_retry_count"]; ok {
			count = intSetting(item, "key_retry_count")
		}
		if count < 0 {
			count = 0
		}
		if count > 20 {
			count = 20
		}
		counts[name] = count
	}
	return counts
}

func providerTimeouts(settings map[string]map[string]interface{}) map[string]int {
	timeouts := map[string]int{}
	for name, item := range settings {
		if timeout := intSetting(item, "_timeout_ms"); timeout > 0 {
			timeouts[name] = timeout
		}
	}
	return timeouts
}

func effectiveRequestTimeoutMS(runtimeTimeoutMS int, providerTimeouts map[string]int, keyRetryCounts map[string]int, providerNames []string) int {
	timeout := runtimeTimeoutMS
	for _, name := range providerNames {
		providerTimeout := providerTimeouts[name]
		if providerTimeout <= 0 {
			continue
		}
		needed := providerTimeout + 1000
		if keyRetryCounts[name] > 0 {
			needed += providerTimeout
		}
		if needed > timeout {
			timeout = needed
		}
	}
	if timeout <= 0 {
		return 20000
	}
	return timeout
}

func providerProxies(settings map[string]map[string]interface{}) map[string]string {
	proxies := map[string]string{}
	for name, item := range settings {
		if boolSetting(item, "proxy_enabled") {
			if proxyURL := strings.TrimSpace(stringSetting(item, "proxy_url")); proxyURL != "" {
				proxies[name] = proxyURL
			}
		}
	}
	return proxies
}

func providerRetryableErrors(settings map[string]map[string]interface{}) map[string]map[string]bool {
	result := map[string]map[string]bool{}
	for name, item := range settings {
		values := stringListSetting(item, "retry_error_types")
		if len(values) == 0 {
			continue
		}
		allowed := map[string]bool{}
		for _, value := range values {
			if value != "" {
				allowed[value] = true
			}
		}
		result[name] = allowed
	}
	return result
}

func boolSetting(settings map[string]interface{}, key string) bool {
	if settings == nil {
		return false
	}
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

func stringSetting(settings map[string]interface{}, key string) string {
	if settings == nil {
		return ""
	}
	value, ok := settings[key]
	if !ok {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func intSetting(settings map[string]interface{}, key string) int {
	if settings == nil {
		return 0
	}
	value, ok := settings[key]
	if !ok {
		return 0
	}
	result := 0
	switch typed := value.(type) {
	case int:
		result = typed
	case int64:
		result = int(typed)
	case float64:
		result = int(typed)
	case string:
		_, _ = fmt.Sscanf(typed, "%d", &result)
	}
	return result
}

func stringListSetting(settings map[string]interface{}, key string) []string {
	if settings == nil {
		return nil
	}
	value, ok := settings[key]
	if !ok {
		return nil
	}
	switch typed := value.(type) {
	case []string:
		return typed
	case []interface{}:
		items := []string{}
		for _, item := range typed {
			if text, ok := item.(string); ok {
				items = append(items, strings.TrimSpace(text))
			}
		}
		return items
	case string:
		parts := strings.Split(typed, ",")
		items := []string{}
		for _, part := range parts {
			items = append(items, strings.TrimSpace(part))
		}
		return items
	default:
		return nil
	}
}

func (o *Orchestrator) cacheKey(req model.SearchRequest, providerLimits map[string]int) string {
	providers := append([]string(nil), req.Providers...)
	sort.Strings(providers)
	limits := map[string]int{}
	for _, name := range providers {
		if limit, ok := providerLimits[name]; ok {
			limits[name] = limit
		}
	}
	var dedupe interface{}
	if req.Dedupe != nil {
		dedupe = *req.Dedupe
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"query":           req.Query,
		"providers":       providers,
		"provider_limits": limits,
		"mode":            req.Mode,
		"limit":           req.Limit,
		"freshness":       req.Freshness,
		"dedupe":          dedupe,
		"rerank":          req.Rerank,
		"compat":          req.CompatFormat,
		"options":         req.Options,
	})
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

type providerExecution struct {
	provider  string
	key       model.APIKey
	keyAlias  string
	status    string
	errorType string
	err       error
	latencyMS int64
	results   []model.SearchResult
	attempts  []providerAttempt
}

type providerAttempt struct {
	Key          model.APIKey
	KeyAlias     string
	AttemptIndex int
	WillRetry    bool
	Status       string
	ErrorType    string
	Err          error
	LatencyMS    int64
	ResultCount  int
	Usage        []model.UsageMeasurement
}

type searchResponseLogPayload struct {
	Results         []model.SearchResult        `json:"results"`
	Providers       []model.ProviderCallSummary `json:"providers"`
	Meta            model.SearchMeta            `json:"meta"`
	ProviderResults []providerResultLog         `json:"provider_results,omitempty"`
	ProviderCalls   []model.ProviderCallLog     `json:"provider_calls,omitempty"`
}

type providerResultLog struct {
	Provider    string               `json:"provider"`
	KeyAlias    string               `json:"key_alias,omitempty"`
	Status      string               `json:"status"`
	ErrorType   string               `json:"error_type,omitempty"`
	Error       string               `json:"error,omitempty"`
	LatencyMS   int64                `json:"latency_ms"`
	ResultCount int                  `json:"result_count"`
	Cached      bool                 `json:"cached"`
	Results     []model.SearchResult `json:"results"`
}

func responseLogPayload(response model.SearchResponse, executions []providerExecution) searchResponseLogPayload {
	return searchResponseLogPayload{
		Results:         response.Results,
		Providers:       response.Providers,
		Meta:            response.Meta,
		ProviderResults: providerResultLogs(executions),
		ProviderCalls:   callLogs(executions),
	}
}

func providerResultLogs(executions []providerExecution) []providerResultLog {
	items := make([]providerResultLog, 0, len(executions))
	for _, execution := range executions {
		message := ""
		if execution.err != nil {
			message = execution.err.Error()
		}
		items = append(items, providerResultLog{
			Provider:    execution.provider,
			KeyAlias:    execution.keyAlias,
			Status:      execution.status,
			ErrorType:   execution.errorType,
			Error:       message,
			LatencyMS:   execution.latencyMS,
			ResultCount: len(execution.results),
			Cached:      false,
			Results:     execution.results,
		})
	}
	return items
}

func summaries(executions []providerExecution) []model.ProviderCallSummary {
	items := make([]model.ProviderCallSummary, 0, len(executions))
	for _, execution := range executions {
		message := ""
		if execution.err != nil {
			message = execution.err.Error()
		}
		items = append(items, model.ProviderCallSummary{
			Provider:    execution.provider,
			KeyAlias:    execution.keyAlias,
			Status:      execution.status,
			ErrorType:   execution.errorType,
			Error:       message,
			LatencyMS:   execution.latencyMS,
			ResultCount: len(execution.results),
			Cached:      false,
		})
	}
	return items
}

func callLogs(executions []providerExecution) []model.ProviderCallLog {
	items := []model.ProviderCallLog{}
	for _, execution := range executions {
		if len(execution.attempts) == 0 {
			message := ""
			if execution.err != nil {
				message = execution.err.Error()
			}
			items = append(items, model.ProviderCallLog{
				ProviderKeyID: execution.key.ID,
				ProviderName:  execution.provider,
				KeyAlias:      execution.keyAlias,
				AttemptIndex:  1,
				Status:        execution.status,
				ErrorType:     execution.errorType,
				ErrorMessage:  message,
				LatencyMS:     execution.latencyMS,
				ResultCount:   len(execution.results),
				Cached:        false,
			})
			continue
		}
		for _, attempt := range execution.attempts {
			message := ""
			if attempt.Err != nil {
				message = attempt.Err.Error()
			}
			attemptIndex := attempt.AttemptIndex
			if attemptIndex <= 0 {
				attemptIndex = 1
			}
			items = append(items, model.ProviderCallLog{
				ProviderKeyID: attempt.Key.ID,
				ProviderName:  execution.provider,
				KeyAlias:      attempt.KeyAlias,
				AttemptIndex:  attemptIndex,
				WillRetry:     attempt.WillRetry,
				Status:        attempt.Status,
				ErrorType:     attempt.ErrorType,
				ErrorMessage:  message,
				LatencyMS:     attempt.LatencyMS,
				ResultCount:   attempt.ResultCount,
				Cached:        false,
				Usage:         attempt.Usage,
			})
		}
	}
	return items
}

func providersQueried(executions []providerExecution) []string {
	items := make([]string, 0, len(executions))
	for _, execution := range executions {
		items = append(items, execution.provider)
	}
	return items
}

func hasOnlyErrors(executions []providerExecution) bool {
	if len(executions) == 0 {
		return true
	}
	for _, execution := range executions {
		if execution.err == nil {
			return false
		}
	}
	return true
}

func firstError(executions []providerExecution) string {
	for _, execution := range executions {
		if execution.err != nil {
			return execution.err.Error()
		}
	}
	return ""
}

// TestProviderKey 用指定密钥发起一次真实搜索，供管理台「测试密钥」使用。
// 它绕过 keyPool 直接持有完整 APIKey，因此必须同样传入 key.BaseURL，
// 否则测试打的是渠道默认地址，与生产行为不一致；调用结果会写回 key 的成功/失败统计。
// query 为空时使用内置示例词，limit<=0 时取 3；失败时同时返回 summary 与 error。
func (o *Orchestrator) TestProviderKey(ctx context.Context, keyID int64, query string, limit int) (model.ProviderCallSummary, []model.SearchResult, error) {
	if query == "" {
		query = "latest AI search API news"
	}
	if limit <= 0 {
		limit = 3
	}
	key, err := o.store.GetAPIKeyByID(ctx, keyID)
	if err != nil {
		return model.ProviderCallSummary{}, nil, err
	}
	providerConfigs, err := o.store.ListProviders(ctx)
	if err != nil {
		return model.ProviderCallSummary{}, nil, err
	}
	providerConfigByName := providerConfigMap(providerConfigs)
	adapter, ok := o.adapterForProvider(key.ProviderName, providerConfigByName[key.ProviderName], key.BaseURL, 0, "")
	if !ok {
		err := &provider.Error{Type: provider.ErrorTypeUpstream, Message: "provider is not registered"}
		return model.ProviderCallSummary{Provider: key.ProviderName, KeyAlias: key.Alias, Status: "error", ErrorType: provider.ErrorType(err), Error: err.Error()}, nil, err
	}
	started := time.Now()
	req := model.SearchRequest{Query: query, Providers: []string{key.ProviderName}, Mode: model.SearchModeSingle, Limit: limit, Cache: model.CachePolicyBypass}
	providerResponse, err := adapter.Search(ctx, req, key)
	latency := time.Since(started).Milliseconds()
	summary := model.ProviderCallSummary{Provider: key.ProviderName, KeyAlias: key.Alias, LatencyMS: latency, ResultCount: len(providerResponse.Results), Cached: false}
	if err != nil {
		summary.Status = "error"
		summary.ErrorType = provider.ErrorType(err)
		summary.Error = err.Error()
		_ = o.store.RecordKeyResult(context.Background(), key, false, summary.ErrorType)
		return summary, nil, err
	}
	summary.Status = "success"
	_ = o.store.RecordKeyResult(context.Background(), key, true, "")
	return summary, providerResponse.Results, nil
}
