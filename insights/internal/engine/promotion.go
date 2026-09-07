package engine

import (
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// DefaultMinObservations is used when a policy has no conditions, so an
// unconfigured class still learns instead of promoting on its first trace.
const DefaultMinObservations int64 = 50

// shouldPromote reports whether a learning class has met its promotion bar.
// now is passed in so time-based conditions can be tested.
//
// A policy with no conditions degrades to the count-only default. Otherwise
// each condition is evaluated independently and combined per the policy mode:
// "all" needs every condition, "any" needs one, "k_of" needs MinMatches. An
// unknown condition type is treated as unmet, so it can never satisfy "any"
// and always blocks "all" - the safe direction (never promote on a criterion
// this build can't evaluate).
func shouldPromote(p repo.LearningPolicy, bc repo.BaselineClass, now time.Time) bool {
	if bc.Promoted {
		return false
	}
	if len(p.Conditions) == 0 {
		return bc.ObservationCount >= DefaultMinObservations
	}

	met := 0
	for i := range p.Conditions {
		if conditionMet(p.Conditions[i], bc, now) {
			met++
		}
	}

	switch normalizeLearningMode(p.Mode) {
	case repo.LearningModeAll:
		return met == len(p.Conditions)
	case repo.LearningModeKOf:
		need := p.MinMatches
		if need < 1 {
			need = 1
		}
		return met >= need
	default: // LearningModeAny
		return met >= 1
	}
}

// conditionMet evaluates one condition against a class's learning state.
func conditionMet(c repo.LearningCondition, bc repo.BaselineClass, now time.Time) bool {
	switch c.Type {
	case repo.ConditionMinObservations:
		return c.MinObservations > 0 && bc.ObservationCount >= c.MinObservations
	case repo.ConditionMinDuration:
		if c.MinDurationMinutes <= 0 {
			return false
		}
		return now.Sub(bc.LearningStartedAt) >= time.Duration(c.MinDurationMinutes)*time.Minute
	case repo.ConditionStableObservations:
		// Stability is only meaningful once the baseline has actually grown at
		// least once. A class that has learned nothing (still empty) has nothing
		// settled to lock in, and promoting it empty would leave a frozen,
		// unusable class the health guard then resets - an endless reset loop.
		// So an empty class keeps learning until it observes something.
		if c.StableObservations <= 0 || !baselineGrewOnce(bc) {
			return false
		}
		return observationsSinceChange(bc) >= c.StableObservations
	case repo.ConditionStableDuration:
		// Quiet time is measured from the last growth. Never grown (still empty)
		// is not "stable" - same reasoning as ConditionStableObservations.
		if c.StableMinutes <= 0 || !baselineGrewOnce(bc) {
			return false
		}
		return now.Sub(bc.LastChangedAt) >= time.Duration(c.StableMinutes)*time.Minute
	default:
		return false
	}
}

// baselineGrewOnce reports whether the baseline has ever grown (learned some
// structure). ObservationCountAtLastChange is stamped with the observation count
// each time the baseline grows and starts at zero, so a positive value is the
// reliable "has grown at least once" marker (more robust than the timestamp,
// which a store may return as the epoch rather than the Go zero time).
func baselineGrewOnce(bc repo.BaselineClass) bool {
	return bc.ObservationCountAtLastChange > 0
}

// observationsSinceChange is how many observations have been folded in since the
// baseline last grew. Clamped at zero: cross-replica adoption can briefly leave
// the last-change marker ahead of this replica's local count.
func observationsSinceChange(bc repo.BaselineClass) int64 {
	since := bc.ObservationCount - bc.ObservationCountAtLastChange
	if since < 0 {
		return 0
	}
	return since
}

// normalizeLearningMode treats anything that isn't a known mode as "any", so a
// bad stored value can't accidentally disable promotion.
func normalizeLearningMode(m repo.LearningMode) repo.LearningMode {
	switch m {
	case repo.LearningModeAll, repo.LearningModeKOf:
		return m
	default:
		return repo.LearningModeAny
	}
}
