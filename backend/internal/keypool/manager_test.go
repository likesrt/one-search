package keypool

import (
	"context"
	"testing"

	"github.com/one-search/one-search/backend/internal/model"
	"github.com/one-search/one-search/backend/internal/provider"
)

type fakeStore struct {
	keys           []model.APIKey
	strategy       string
	maxConcurrency int
	// unavailable 记录 RecordKeyResult 报告过失败的 key，模拟真实 store 的状态流转。
	unavailable map[int64]bool
}

func (s *fakeStore) ListAvailableProviderKeys(ctx context.Context, providerName string) ([]model.APIKey, error) {
	keys := make([]model.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		if s.unavailable[key.ID] {
			continue
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (s *fakeStore) ProviderKeySettings(ctx context.Context, providerName string) (string, int, error) {
	return s.strategy, s.maxConcurrency, nil
}

func (s *fakeStore) RecordKeyResult(ctx context.Context, key model.APIKey, success bool, errorType string) error {
	if !success && s.unavailable != nil {
		s.unavailable[key.ID] = true
	}
	return nil
}

func TestAcquireAllowsConcurrentUseWhenProviderMaxConcurrencyZero(t *testing.T) {
	manager := NewManager(&fakeStore{keys: []model.APIKey{{ID: 1, ProviderName: model.ProviderYou, MaxConcurrency: 0}}})
	releases := make([]func(bool, error), 0, 5)

	for i := 0; i < 5; i++ {
		key, release, err := manager.Acquire(context.Background(), model.ProviderYou)
		if err != nil {
			t.Fatalf("Acquire #%d returned error: %v", i+1, err)
		}
		if key.ID != 1 {
			t.Fatalf("Acquire #%d key ID = %d, want 1", i+1, key.ID)
		}
		releases = append(releases, release)
	}

	for _, release := range releases {
		release(true, nil)
	}
}

func TestAcquireHonorsProviderMaxConcurrency(t *testing.T) {
	manager := NewManager(&fakeStore{
		keys:           []model.APIKey{{ID: 1, ProviderName: model.ProviderYou}, {ID: 2, ProviderName: model.ProviderYou}},
		maxConcurrency: 1,
	})

	_, release, err := manager.Acquire(context.Background(), model.ProviderYou)
	if err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}
	defer release(true, nil)

	_, _, err = manager.Acquire(context.Background(), model.ProviderYou)
	if provider.ErrorType(err) != provider.ErrorTypeRateLimited {
		t.Fatalf("second Acquire error type = %q, want %q (err=%v)", provider.ErrorType(err), provider.ErrorTypeRateLimited, err)
	}
}

func TestAcquireWeightPriorityPrefersHighestWeight(t *testing.T) {
	manager := NewManager(&fakeStore{
		keys:     []model.APIKey{{ID: 1, ProviderName: model.ProviderYou, Weight: 1}, {ID: 2, ProviderName: model.ProviderYou, Weight: 5}},
		strategy: "weight_priority",
	})

	for i := 0; i < 20; i++ {
		key, release, err := manager.Acquire(context.Background(), model.ProviderYou)
		if err != nil {
			t.Fatalf("Acquire #%d returned error: %v", i+1, err)
		}
		release(true, nil)
		if key.ID != 2 {
			t.Fatalf("Acquire #%d key ID = %d, want 2 (highest weight)", i+1, key.ID)
		}
	}
}

func TestAcquireWeightPriorityRandomWithinTier(t *testing.T) {
	manager := NewManager(&fakeStore{
		keys:     []model.APIKey{{ID: 1, ProviderName: model.ProviderYou, Weight: 3}, {ID: 2, ProviderName: model.ProviderYou, Weight: 3}},
		strategy: "weight_priority",
	})
	seen := map[int64]bool{}

	for i := 0; i < 100; i++ {
		key, release, err := manager.Acquire(context.Background(), model.ProviderYou)
		if err != nil {
			t.Fatalf("Acquire #%d returned error: %v", i+1, err)
		}
		release(true, nil)
		seen[key.ID] = true
	}
	if !seen[1] || !seen[2] {
		t.Fatalf("100 Acquire calls only saw keys %v, want both 1 and 2", seen)
	}
}

func TestAcquireWeightPriorityFallsToLowerTierOnFailure(t *testing.T) {
	store := &fakeStore{
		keys:        []model.APIKey{{ID: 1, ProviderName: model.ProviderYou, Weight: 1}, {ID: 2, ProviderName: model.ProviderYou, Weight: 9}},
		strategy:    "weight_priority",
		unavailable: map[int64]bool{},
	}
	manager := NewManager(store)

	first, release, err := manager.Acquire(context.Background(), model.ProviderYou)
	if err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}
	if first.ID != 2 {
		t.Fatalf("first Acquire key ID = %d, want 2", first.ID)
	}
	release(false, &provider.Error{Type: provider.ErrorTypeRateLimited, Message: "limited"})

	second, release, err := manager.Acquire(context.Background(), model.ProviderYou)
	if err != nil {
		t.Fatalf("second Acquire returned error: %v", err)
	}
	defer release(true, nil)
	if second.ID != 1 {
		t.Fatalf("second Acquire key ID = %d, want 1 (lower tier)", second.ID)
	}
}

func TestAcquireExcludesTriedKeys(t *testing.T) {
	manager := NewManager(&fakeStore{
		keys:     []model.APIKey{{ID: 1, ProviderName: model.ProviderYou, Weight: 2}, {ID: 2, ProviderName: model.ProviderYou, Weight: 2}},
		strategy: "weight_priority",
	})

	first, release, err := manager.Acquire(context.Background(), model.ProviderYou)
	if err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}
	release(true, nil)

	second, release, err := manager.Acquire(context.Background(), model.ProviderYou, first.ID)
	if err != nil {
		t.Fatalf("second Acquire returned error: %v", err)
	}
	defer release(true, nil)
	if second.ID == first.ID {
		t.Fatalf("second Acquire key ID = %d, want a different key than %d", second.ID, first.ID)
	}
}

func TestAcquireFallsBackToTriedKeyWhenAllExcluded(t *testing.T) {
	manager := NewManager(&fakeStore{
		keys:     []model.APIKey{{ID: 1, ProviderName: model.ProviderYou, Weight: 4}},
		strategy: "weight_priority",
	})

	key, release, err := manager.Acquire(context.Background(), model.ProviderYou, 1)
	if err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	defer release(true, nil)
	if key.ID != 1 {
		t.Fatalf("Acquire key ID = %d, want 1 (soft exclusion falls back to tried key)", key.ID)
	}
}

func TestAcquireWeightPriorityTreatsZeroWeightAsOne(t *testing.T) {
	manager := NewManager(&fakeStore{
		keys:     []model.APIKey{{ID: 1, ProviderName: model.ProviderYou, Weight: 0}, {ID: 2, ProviderName: model.ProviderYou, Weight: 2}},
		strategy: "weight_priority",
	})

	for i := 0; i < 20; i++ {
		key, release, err := manager.Acquire(context.Background(), model.ProviderYou)
		if err != nil {
			t.Fatalf("Acquire #%d returned error: %v", i+1, err)
		}
		release(true, nil)
		if key.ID != 2 {
			t.Fatalf("Acquire #%d key ID = %d, want 2 (weight 0 counts as 1)", i+1, key.ID)
		}
	}
}
