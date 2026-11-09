package engine

import (
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Display-only observation window used when the resolved policy has no
// stable_observations condition, so the API can always return X/Y
// observations. This is not a promotion gate.
const defaultDisplayStableObservations int64 = 50

// BaselineLearning is the computed learning/stability snapshot for one class
// against its resolved policy. Mapped onto the BaselineClass.learning wire
// field so the product UI does not reimplement the promotion gate.
type BaselineLearning struct {
	Phase                       string
	ObservationsSinceLastChange int64
	QuietMinutes                int64
	Mode                        repo.LearningMode
	ReadyToPromote              bool
	Stability                   BaselineStability
}

// BaselineStability holds both stability signals so the UI can always show
// "X / Y observations" and "grew N minutes ago" together.
type BaselineStability struct {
	Observations StabilityProgress
	Duration     StabilityProgress
}

// StabilityProgress is one dimension (observations count, or quiet minutes).
type StabilityProgress struct {
	Current         int64
	Target          int64
	DrivesPromotion bool
	Met             bool
}

// LearningOf builds the snapshot the GET /baseline handler attaches to each
// class. now is injected so tests are deterministic.
func LearningOf(bc repo.BaselineClass, policy repo.LearningPolicy, now time.Time) BaselineLearning {
	since := observationsSinceChange(bc)
	quiet := quietMinutes(bc, now)
	phase := "learning"
	switch {
	case bc.Saturated:
		phase = "saturated"
	case bc.Promoted:
		phase = "promoted"
	case !baselineGrewOnce(bc):
		phase = "empty"
	}
	stability := BaselineStability{
		Observations: observationProgress(bc, policy, now, since),
		Duration:     durationProgress(bc, policy, now, quiet),
	}
	applyPhase(phase, &stability.Observations)
	applyPhase(phase, &stability.Duration)
	return BaselineLearning{
		Phase:                       phase,
		ObservationsSinceLastChange: since,
		QuietMinutes:                quiet,
		Mode:                        normalizeLearningMode(policy.Mode),
		ReadyToPromote:              shouldPromote(policy, bc, now),
		Stability:                   stability,
	}
}

func observationProgress(bc repo.BaselineClass, policy repo.LearningPolicy, now time.Time, since int64) StabilityProgress {
	current := int64(0)
	if baselineGrewOnce(bc) {
		current = since
	}
	if target := largestStableTarget(policy, repo.ConditionStableObservations); target > 0 {
		return StabilityProgress{
			Current:         current,
			Target:          target,
			DrivesPromotion: true,
			Met:             conditionMet(repo.LearningCondition{Type: repo.ConditionStableObservations, StableObservations: target}, bc, now),
		}
	}
	return StabilityProgress{
		Current:         current,
		Target:          defaultDisplayStableObservations,
		DrivesPromotion: false,
		Met:             baselineGrewOnce(bc) && current >= defaultDisplayStableObservations,
	}
}

func durationProgress(bc repo.BaselineClass, policy repo.LearningPolicy, now time.Time, quiet int64) StabilityProgress {
	current := int64(0)
	if baselineGrewOnce(bc) {
		current = quiet
	}
	if target := largestStableTarget(policy, repo.ConditionStableDuration); target > 0 {
		return StabilityProgress{
			Current:         current,
			Target:          target,
			DrivesPromotion: true,
			Met:             conditionMet(repo.LearningCondition{Type: repo.ConditionStableDuration, StableMinutes: target}, bc, now),
		}
	}
	return StabilityProgress{Current: current}
}

func applyPhase(phase string, p *StabilityProgress) {
	switch phase {
	case "empty", "saturated":
		p.Current = 0
		p.Met = false
	case "promoted":
		p.Met = true
		if p.Target > 0 && p.Current < p.Target {
			p.Current = p.Target
		}
	}
}

func largestStableTarget(policy repo.LearningPolicy, conditionType repo.LearningConditionType) int64 {
	var target int64
	for _, condition := range policy.Conditions {
		if condition.Type != conditionType {
			continue
		}
		value := condition.StableObservations
		if conditionType == repo.ConditionStableDuration {
			value = condition.StableMinutes
		}
		if value > target {
			target = value
		}
	}
	return target
}

func quietMinutes(bc repo.BaselineClass, now time.Time) int64 {
	anchor := bc.LastChangedAt
	if !baselineGrewOnce(bc) || anchor.IsZero() || anchor.Unix() <= 0 {
		anchor = bc.LearningStartedAt
	}
	return minutesSince(anchor, now)
}

func minutesSince(anchor time.Time, now time.Time) int64 {
	if anchor.IsZero() || anchor.Unix() <= 0 {
		return 0
	}
	if m := int64(now.Sub(anchor) / time.Minute); m > 0 {
		return m
	}
	return 0
}
