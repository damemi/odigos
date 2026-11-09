package urltemplatization

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/common"
)

func TestLearningIntervalFromConfig(t *testing.T) {
	if got := learningIntervalFromConfig(nil); got != 0 {
		t.Fatalf("nil config: got %v, want 0", got)
	}
	if got := learningIntervalFromConfig(&common.OdigosConfiguration{}); got != 0 {
		t.Fatalf("empty config: got %v, want 0", got)
	}

	cfg := &common.OdigosConfiguration{
		CardinalityControl: &common.CardinalityControlConfiguration{
			UrlTemplatization: &common.UrlTemplatizationCardinalityControlConfiguration{
				LiveTrafficLearning: &common.LiveTrafficLearningConfiguration{
					LearningInterval: "45s",
				},
			},
		},
	}
	if got := learningIntervalFromConfig(cfg); got != 45*time.Second {
		t.Fatalf("45s: got %v, want 45s", got)
	}

	cfg.CardinalityControl.UrlTemplatization.LiveTrafficLearning.LearningInterval = "bad"
	if got := learningIntervalFromConfig(cfg); got != 0 {
		t.Fatalf("invalid duration: got %v, want 0", got)
	}

	cfg.CardinalityControl.UrlTemplatization.LiveTrafficLearning.LearningInterval = "0s"
	if got := learningIntervalFromConfig(cfg); got != 0 {
		t.Fatalf("zero duration: got %v, want 0", got)
	}
}

func TestAutomaticRulesEnabled(t *testing.T) {
	if automaticRulesEnabled(nil) {
		t.Fatal("nil config should be disabled")
	}
	enabled := true
	if !automaticRulesEnabled(&common.LiveTrafficLearningAutomaticRulesConfiguration{Enabled: &enabled}) {
		t.Fatal("Enabled=true should be enabled")
	}
	disabled := false
	if automaticRulesEnabled(&common.LiveTrafficLearningAutomaticRulesConfiguration{Enabled: &disabled}) {
		t.Fatal("Enabled=false should be disabled")
	}
}

func TestLearningParamsFromConfig(t *testing.T) {
	params := learningParamsFromConfig(nil)
	if params.MinObservationsForRule != nil || params.MinCardinalityForTemplating != nil {
		t.Fatalf("nil config should leave learning params unset, got %+v", params)
	}

	obs, card := 50, 10
	cfg := &common.OdigosConfiguration{
		CardinalityControl: &common.CardinalityControlConfiguration{
			UrlTemplatization: &common.UrlTemplatizationCardinalityControlConfiguration{
				LiveTrafficLearning: &common.LiveTrafficLearningConfiguration{
					RuleComputation: &common.LiveTrafficLearningRuleComputationConfiguration{
						MinObservationsForRule:      &obs,
						MinCardinalityForTemplating: &card,
					},
				},
			},
		},
	}
	params = learningParamsFromConfig(cfg)
	if params.MinObservationsForRule == nil || *params.MinObservationsForRule != 50 {
		t.Fatalf("MinObservationsForRule=%v, want 50", params.MinObservationsForRule)
	}
	if params.MinCardinalityForTemplating == nil || *params.MinCardinalityForTemplating != 10 {
		t.Fatalf("MinCardinalityForTemplating=%v, want 10", params.MinCardinalityForTemplating)
	}
}
