package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestLoadLearningConfig_ReadsDBRows(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	d1 := repo.LearningPolicy{
		Class: repo.D1CallEdges, Mode: repo.LearningModeAny,
		Conditions: []repo.LearningCondition{
			{Type: repo.ConditionMinObservations, MinObservations: 50},
			{Type: repo.ConditionMinDuration, MinDurationMinutes: 1440},
		},
	}
	d3 := repo.LearningPolicy{
		Class: repo.D3Latency, Mode: repo.LearningModeAll,
		Conditions: []repo.LearningCondition{
			{Type: repo.ConditionMinObservations, MinObservations: 200},
			{Type: repo.ConditionMinDuration, MinDurationMinutes: 2880},
		},
	}
	for _, p := range []repo.LearningPolicy{d1, d3} {
		if err := r.UpsertLearningPolicy(ctx, p); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	cfg, err := LoadLearningConfig(ctx, r)
	if err != nil {
		t.Fatalf("LoadLearningConfig: %v", err)
	}

	if got := cfg.For(repo.D1CallEdges); !reflect.DeepEqual(got, d1) {
		t.Errorf("D1: %+v", got)
	}
	if got := cfg.For(repo.D3Latency); !reflect.DeepEqual(got, d3) {
		t.Errorf("D3: %+v", got)
	}
	// A class with no row returns the zero policy.
	if got := cfg.For(repo.D2Egress); !reflect.DeepEqual(got, repo.LearningPolicy{}) {
		t.Errorf("D2 should be the zero policy, got %+v", got)
	}
}

func TestReload_PicksUpEdits(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	cfg, err := LoadLearningConfig(ctx, r)
	if err != nil {
		t.Fatalf("LoadLearningConfig: %v", err)
	}
	if got := cfg.For(repo.D1CallEdges); len(got.Conditions) != 0 {
		t.Fatalf("expected no policy before seeding, got %+v", got)
	}

	want := repo.LearningPolicy{
		Class: repo.D1CallEdges, Mode: repo.LearningModeAny,
		Conditions: []repo.LearningCondition{{Type: repo.ConditionMinObservations, MinObservations: 10}},
	}
	if err := r.UpsertLearningPolicy(ctx, want); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := cfg.Reload(ctx, r); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := cfg.For(repo.D1CallEdges); !reflect.DeepEqual(got, want) {
		t.Errorf("after reload: %+v", got)
	}
}

func TestReload_ErrorKeepsPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	r := newFakeRepo()
	seeded := repo.LearningPolicy{
		Class: repo.D1CallEdges, Mode: repo.LearningModeAny,
		Conditions: []repo.LearningCondition{{Type: repo.ConditionMinObservations, MinObservations: 10}},
	}
	if err := r.UpsertLearningPolicy(ctx, seeded); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	cfg, err := LoadLearningConfig(ctx, r)
	if err != nil {
		t.Fatalf("LoadLearningConfig: %v", err)
	}

	if err := cfg.Reload(ctx, listErrRepo{r}); err == nil {
		t.Fatalf("expected an error from the failing repo")
	}
	if got := cfg.For(repo.D1CallEdges); !reflect.DeepEqual(got, seeded) {
		t.Errorf("a failed reload must keep the previous snapshot, got %+v", got)
	}
}

func TestLoadLearningConfig_ErrorReturnsEmptySnapshot(t *testing.T) {
	cfg, err := LoadLearningConfig(context.Background(), listErrRepo{newFakeRepo()})
	if err == nil {
		t.Fatalf("expected an error from the failing repo")
	}
	if cfg == nil {
		t.Fatalf("expected a non-nil (empty) snapshot, got nil")
	}
	if got := cfg.For(repo.D1CallEdges); !reflect.DeepEqual(got, repo.LearningPolicy{}) {
		t.Errorf("on load error every class should fall back to the zero policy")
	}
}

func TestLearningConfig_For_ZeroPolicyFallbacks(t *testing.T) {
	var nilCfg *LearningConfig
	if got := nilCfg.For(repo.D1CallEdges); !reflect.DeepEqual(got, repo.LearningPolicy{}) {
		t.Errorf("nil config should return the zero policy")
	}
	cfg := newLearningConfig(map[repo.DeviationClass]repo.LearningPolicy{})
	if got := cfg.For("D99_unknown"); !reflect.DeepEqual(got, repo.LearningPolicy{}) {
		t.Errorf("unknown class should return the zero policy")
	}
}

func TestTimeSweepCutoffs(t *testing.T) {
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)

	t.Run("nil and empty skip the sweep", func(t *testing.T) {
		var nilCfg *LearningConfig
		if _, ok := nilCfg.timeSweepCutoffs(now); ok {
			t.Fatal("nil config must skip the DB sweep")
		}
		empty := newLearningConfig(map[repo.DeviationClass]repo.LearningPolicy{})
		if _, ok := empty.timeSweepCutoffs(now); ok {
			t.Fatal("no time conditions must skip the DB sweep")
		}
		countOnly := newLearningConfig(map[repo.DeviationClass]repo.LearningPolicy{
			repo.D1CallEdges: {Conditions: []repo.LearningCondition{
				{Type: repo.ConditionMinObservations, MinObservations: 50},
			}},
		})
		if _, ok := countOnly.timeSweepCutoffs(now); ok {
			t.Fatal("observation-only policies must not scan unpromoted baselines")
		}
	})

	t.Run("takes the most permissive minutes across policies", func(t *testing.T) {
		cfg := newLearningConfig(map[repo.DeviationClass]repo.LearningPolicy{
			repo.D1CallEdges: {Conditions: []repo.LearningCondition{
				{Type: repo.ConditionMinDuration, MinDurationMinutes: 60},
				{Type: repo.ConditionStableDuration, StableMinutes: 30},
			}},
			repo.D3Latency: {Conditions: []repo.LearningCondition{
				{Type: repo.ConditionMinDuration, MinDurationMinutes: 10},
				{Type: repo.ConditionStableDuration, StableMinutes: 90},
			}},
		})
		got, ok := cfg.timeSweepCutoffs(now)
		if !ok {
			t.Fatal("expected cutoffs when any policy uses a time condition")
		}
		if !got.MinDurationEnabled || !got.StableDurationEnabled {
			t.Fatalf("both time conditions should be enabled, got %+v", got)
		}
		if !got.MinDurationCutoff.Equal(now.Add(-10 * time.Minute)) {
			t.Fatalf("min-duration cutoff = %s, want now-10m (the smaller window)", got.MinDurationCutoff)
		}
		if !got.StableDurationCutoff.Equal(now.Add(-30 * time.Minute)) {
			t.Fatalf("stable cutoff = %s, want now-30m (the smaller window)", got.StableDurationCutoff)
		}
	})
}

// listErrRepo wraps the in-memory fake but fails ListLearningPolicies, to
// exercise the empty-snapshot fallback paths.
type listErrRepo struct{ *fakeRepo }

func (listErrRepo) ListLearningPolicies(context.Context) ([]repo.LearningPolicy, error) {
	return nil, errors.New("boom")
}
