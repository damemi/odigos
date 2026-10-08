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
