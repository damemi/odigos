package engine

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestShouldPromote(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	// bc builds a learning-phase baseline row with the given observation
	// count and a learning start `age` before `now`.
	bc := func(count int64, age time.Duration) repo.BaselineClass {
		return repo.BaselineClass{ObservationCount: count, LearningStartedAt: t0.Add(-age)}
	}

	count := func(n int64) repo.LearningCondition {
		return repo.LearningCondition{Type: repo.ConditionMinObservations, MinObservations: n}
	}
	afterMinutes := func(m int64) repo.LearningCondition {
		return repo.LearningCondition{Type: repo.ConditionMinDuration, MinDurationMinutes: m}
	}
	policy := func(mode repo.LearningMode, conds ...repo.LearningCondition) repo.LearningPolicy {
		return repo.LearningPolicy{Mode: mode, Conditions: conds}
	}

	cases := []struct {
		name   string
		policy repo.LearningPolicy
		bc     repo.BaselineClass
		want   bool
	}{
		// count-only.
		{"count only: below", policy(repo.LearningModeAny, count(50)), bc(49, 0), false},
		{"count only: met", policy(repo.LearningModeAny, count(50)), bc(50, 0), true},
		{"count only under all: below", policy(repo.LearningModeAll, count(50)), bc(49, 0), false},
		{"count only under all: met", policy(repo.LearningModeAll, count(50)), bc(50, 0), true},

		// duration-only (60 minutes).
		{"duration only: too soon", policy(repo.LearningModeAny, afterMinutes(60)), bc(0, 30*time.Minute), false},
		{"duration only: elapsed", policy(repo.LearningModeAny, afterMinutes(60)), bc(0, 2*time.Hour), true},

		// both conditions, all: need count AND duration.
		{"all: count met, time too soon", policy(repo.LearningModeAll, count(50), afterMinutes(60)), bc(50, 30*time.Minute), false},
		{"all: time met, count short", policy(repo.LearningModeAll, count(50), afterMinutes(60)), bc(10, 2*time.Hour), false},
		{"all: both met", policy(repo.LearningModeAll, count(50), afterMinutes(60)), bc(50, 2*time.Hour), true},

		// both conditions, any: either is enough.
		{"any: count met, time too soon", policy(repo.LearningModeAny, count(50), afterMinutes(60)), bc(50, 30*time.Minute), true},
		{"any: time met, count short", policy(repo.LearningModeAny, count(50), afterMinutes(60)), bc(10, 2*time.Hour), true},
		{"any: neither met", policy(repo.LearningModeAny, count(50), afterMinutes(60)), bc(10, 30*time.Minute), false},

		// k_of: at least 2 of 3 conditions.
		{"k_of 2/3: one met", repo.LearningPolicy{Mode: repo.LearningModeKOf, MinMatches: 2, Conditions: []repo.LearningCondition{count(50), afterMinutes(60), count(1_000_000)}}, bc(50, 30*time.Minute), false},
		{"k_of 2/3: two met", repo.LearningPolicy{Mode: repo.LearningModeKOf, MinMatches: 2, Conditions: []repo.LearningCondition{count(50), afterMinutes(60), count(1_000_000)}}, bc(50, 2*time.Hour), true},

		// empty mode defaults to any.
		{"empty mode defaults to any: time met", policy("", count(50), afterMinutes(60)), bc(10, 2*time.Hour), true},

		// no conditions -> count-only DefaultMinObservations guard.
		{"no conditions: under default", repo.LearningPolicy{}, bc(DefaultMinObservations-1, 0), false},
		{"no conditions: at default", repo.LearningPolicy{}, bc(DefaultMinObservations, 0), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPromote(c.policy, c.bc, t0); got != c.want {
				t.Errorf("shouldPromote = %v, want %v", got, c.want)
			}
		})
	}
}

func TestShouldPromote_Stability(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	stableObs := func(n int64) repo.LearningCondition {
		return repo.LearningCondition{Type: repo.ConditionStableObservations, StableObservations: n}
	}
	stableMin := func(m int64) repo.LearningCondition {
		return repo.LearningCondition{Type: repo.ConditionStableDuration, StableMinutes: m}
	}
	// bc with total observations, and the count/age at the last baseline growth.
	bc := func(count, countAtChange int64, changedAge time.Duration) repo.BaselineClass {
		b := repo.BaselineClass{ObservationCount: count, ObservationCountAtLastChange: countAtChange, LearningStartedAt: t0.Add(-24 * time.Hour)}
		if changedAge >= 0 {
			b.LastChangedAt = t0.Add(-changedAge)
		}
		return b
	}
	policy := func(mode repo.LearningMode, conds ...repo.LearningCondition) repo.LearningPolicy {
		return repo.LearningPolicy{Mode: mode, Conditions: conds}
	}

	cases := []struct {
		name   string
		policy repo.LearningPolicy
		bc     repo.BaselineClass
		want   bool
	}{
		// stable-observations: grew at count 40, so since-change = count - 40.
		{"stable obs: still growing", policy(repo.LearningModeAny, stableObs(20)), bc(45, 40, time.Minute), false},
		{"stable obs: settled", policy(repo.LearningModeAny, stableObs(20)), bc(60, 40, time.Minute), true},
		// zero observations can't be stable even if the arithmetic would allow it.
		{"stable obs: no observations", policy(repo.LearningModeAny, stableObs(1)), bc(0, 0, -1), false},
		// never grew (empty baseline): nothing settled, so it keeps learning even
		// with many observations - otherwise it would promote empty and loop.
		{"stable obs: never grew stays learning", policy(repo.LearningModeAny, stableObs(20)), bc(60, 0, -1), false},

		// stable-duration: measured from the last growth.
		{"stable dur: changed recently", policy(repo.LearningModeAny, stableMin(60)), bc(100, 90, 30*time.Minute), false},
		{"stable dur: quiet long enough", policy(repo.LearningModeAny, stableMin(60)), bc(100, 90, 2*time.Hour), true},
		// never grew (empty baseline): stability can't be met - a class that
		// learned nothing has nothing settled, so it stays in learning.
		{"stable dur: never grew stays learning", policy(repo.LearningModeAny, stableMin(60)), bc(5, 0, -1), false},

		// combined with a count floor under all: both must hold.
		{"all count+stable: not yet stable", policy(repo.LearningModeAll, repo.LearningCondition{Type: repo.ConditionMinObservations, MinObservations: 50}, stableObs(20)), bc(55, 50, time.Minute), false},
		{"all count+stable: both hold", policy(repo.LearningModeAll, repo.LearningCondition{Type: repo.ConditionMinObservations, MinObservations: 50}, stableObs(20)), bc(75, 50, time.Minute), true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := shouldPromote(c.policy, c.bc, t0); got != c.want {
				t.Errorf("shouldPromote = %v, want %v", got, c.want)
			}
		})
	}
}

func TestShouldPromote_AlreadyPromotedNeverRepromotes(t *testing.T) {
	now := time.Now()
	bc := repo.BaselineClass{ObservationCount: 1_000_000, Promoted: true}
	p := repo.LearningPolicy{
		Mode:       repo.LearningModeAny,
		Conditions: []repo.LearningCondition{{Type: repo.ConditionMinObservations, MinObservations: 1}},
	}
	if shouldPromote(p, bc, now) {
		t.Errorf("an already-promoted class must not promote again")
	}
}
