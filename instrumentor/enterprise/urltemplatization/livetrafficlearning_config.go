package urltemplatization

import (
	"context"
	"time"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/k8sutils/pkg/utils"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

const defaultLiveTrafficLearningInterval = 30 * time.Second

// liveTrafficLearningParams holds resolved settings from effective config.
type liveTrafficLearningParams struct {
	interval              time.Duration
	automaticRulesEnabled bool
	learning.LiveTrafficLearningParams
}

func (j *LiveTrafficLearningJob) loadLearningParams(ctx context.Context) liveTrafficLearningParams {
	params := liveTrafficLearningParams{
		interval: defaultLiveTrafficLearningInterval,
	}
	if j.Client == nil {
		return params
	}
	cfg, err := utils.GetCurrentOdigosConfiguration(ctx, j.Client)
	if err != nil {
		return params
	}
	if d := learningIntervalFromConfig(&cfg); d > 0 {
		params.interval = d
	}
	params.LiveTrafficLearningParams = learningParamsFromConfig(&cfg)
	params.automaticRulesEnabled = automaticRulesEnabled(automaticRulesFromConfig(&cfg))
	return params
}

func learningIntervalFromConfig(cfg *common.OdigosConfiguration) time.Duration {
	ltl := liveTrafficLearningFromConfig(cfg)
	if ltl == nil || ltl.LearningInterval == "" {
		return 0
	}
	d, err := time.ParseDuration(ltl.LearningInterval)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func learningParamsFromConfig(cfg *common.OdigosConfiguration) learning.LiveTrafficLearningParams {
	ltl := liveTrafficLearningFromConfig(cfg)
	if ltl == nil || ltl.RuleComputation == nil {
		return learning.LiveTrafficLearningParams{}
	}
	rc := ltl.RuleComputation
	params := learning.LiveTrafficLearningParams{}
	if rc.MinObservationsForRule != nil && *rc.MinObservationsForRule > 0 {
		minObs := int64(*rc.MinObservationsForRule)
		params.MinObservationsForRule = &minObs
	}
	if rc.MinCardinalityForTemplating != nil && *rc.MinCardinalityForTemplating > 0 {
		minCard := *rc.MinCardinalityForTemplating
		params.MinCardinalityForTemplating = &minCard
	}
	return params
}

func automaticRulesFromConfig(cfg *common.OdigosConfiguration) *common.LiveTrafficLearningAutomaticRulesConfiguration {
	ltl := liveTrafficLearningFromConfig(cfg)
	if ltl == nil {
		return nil
	}
	return ltl.AutomaticRules
}

func automaticRulesEnabled(ar *common.LiveTrafficLearningAutomaticRulesConfiguration) bool {
	return ar != nil && ar.Enabled != nil && *ar.Enabled
}

func liveTrafficLearningFromConfig(cfg *common.OdigosConfiguration) *common.LiveTrafficLearningConfiguration {
	if cfg == nil ||
		cfg.CardinalityControl == nil ||
		cfg.CardinalityControl.UrlTemplatization == nil {
		return nil
	}
	return cfg.CardinalityControl.UrlTemplatization.LiveTrafficLearning
}
