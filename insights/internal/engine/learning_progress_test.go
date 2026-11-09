package engine

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestLearningOf_PromotedIsFrozen(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	bc := repo.BaselineClass{
		Promoted:                     true,
		ObservationCount:             100,
		ObservationCountAtLastChange: 50,
		LastChangedAt:                now.Add(-2 * time.Hour),
	}
	got := LearningOf(bc, repo.LearningPolicy{
		Mode: repo.LearningModeAny,
		Conditions: []repo.LearningCondition{
			{Type: repo.ConditionStableObservations, StableObservations: 20},
		},
	}, now)
	if got.Phase != "promoted" || got.ReadyToPromote {
		t.Fatalf("phase=%q ready=%v", got.Phase, got.ReadyToPromote)
	}
	if !got.Stability.Observations.Met || got.Stability.Observations.Current < got.Stability.Observations.Target {
		t.Fatalf("observations = %+v", got.Stability.Observations)
	}
	if got.Stability.Duration.Current != 120 {
		t.Fatalf("duration current = %d, want 120 quiet minutes", got.Stability.Duration.Current)
	}
}

func TestLearningOf_EmptyNeverGrew(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	bc := repo.BaselineClass{ObservationCount: 12}
	got := LearningOf(bc, repo.LearningPolicy{
		Mode: repo.LearningModeAny,
		Conditions: []repo.LearningCondition{
			{Type: repo.ConditionStableObservations, StableObservations: 20},
		},
	}, now)
	if got.Phase != "empty" {
		t.Fatalf("phase = %q", got.Phase)
	}
	if got.Stability.Observations.Current != 0 || got.Stability.Observations.Met || got.ReadyToPromote {
		t.Fatalf("empty class must not look stable: %+v", got)
	}
	if got.Stability.Duration.Current != 0 || got.Stability.Duration.Met {
		t.Fatalf("empty duration = %+v", got.Stability.Duration)
	}
}

func TestLearningOf_AllModeStableMetCountShort(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	bc := repo.BaselineClass{
		ObservationCount:             40,
		ObservationCountAtLastChange: 10,
		LastChangedAt:                now.Add(-time.Hour),
		LearningStartedAt:            now.Add(-2 * time.Hour),
	}
	policy := repo.LearningPolicy{
		Mode: repo.LearningModeAll,
		Conditions: []repo.LearningCondition{
			{Type: repo.ConditionMinObservations, MinObservations: 50},
			{Type: repo.ConditionStableObservations, StableObservations: 20},
		},
	}
	got := LearningOf(bc, policy, now)
	if got.Phase != "learning" || got.Mode != repo.LearningModeAll {
		t.Fatalf("phase=%q mode=%q", got.Phase, got.Mode)
	}
	obs := got.Stability.Observations
	if !obs.Met || !obs.DrivesPromotion || obs.Current != 30 {
		t.Fatalf("observations = %+v", obs)
	}
	if got.Stability.Duration.Current != 60 || got.Stability.Duration.DrivesPromotion {
		t.Fatalf("duration should be 'grew 60m ago' only: %+v", got.Stability.Duration)
	}
	if got.ReadyToPromote {
		t.Fatal("AND with count still short must not be ready_to_promote")
	}
}

func TestLearningOf_BothStabilityDimensions(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	bc := repo.BaselineClass{
		ObservationCount:             55,
		ObservationCountAtLastChange: 40,
		LastChangedAt:                now.Add(-30 * time.Minute),
		LearningStartedAt:            now.Add(-24 * time.Hour),
	}
	got := LearningOf(bc, repo.LearningPolicy{
		Mode: repo.LearningModeAny,
		Conditions: []repo.LearningCondition{
			{Type: repo.ConditionStableObservations, StableObservations: 20},
			{Type: repo.ConditionStableDuration, StableMinutes: 60},
		},
	}, now)
	obs, dur := got.Stability.Observations, got.Stability.Duration
	if !obs.DrivesPromotion || obs.Current != 15 || obs.Target != 20 || obs.Met {
		t.Fatalf("observations = %+v", obs)
	}
	if !dur.DrivesPromotion || dur.Current != 30 || dur.Target != 60 || dur.Met {
		t.Fatalf("duration = %+v", dur)
	}
	if got.ReadyToPromote {
		t.Fatal("neither stability dimension is met")
	}
}

func TestLearningOf_DisplayFallbackDoesNotDrivePromotion(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	bc := repo.BaselineClass{
		ObservationCount:             80,
		ObservationCountAtLastChange: 10,
		LastChangedAt:                now.Add(-time.Hour),
	}
	got := LearningOf(bc, repo.LearningPolicy{
		Mode: repo.LearningModeAny,
		Conditions: []repo.LearningCondition{
			{Type: repo.ConditionMinObservations, MinObservations: 200},
		},
	}, now)
	obs := got.Stability.Observations
	if obs.DrivesPromotion || obs.Target != defaultDisplayStableObservations || !obs.Met {
		t.Fatalf("display fallback = %+v", obs)
	}
	if got.Stability.Duration.DrivesPromotion || got.Stability.Duration.Target != 0 {
		t.Fatalf("duration should have no target: %+v", got.Stability.Duration)
	}
	if got.ReadyToPromote {
		t.Fatal("count floor 200 is not met")
	}
}
