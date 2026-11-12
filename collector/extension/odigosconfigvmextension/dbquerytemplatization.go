package odigosconfigvmextension

import (
	"go.uber.org/zap"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/api/actions"
)

func buildDbQueryTemplatization(logger *zap.Logger, dbActions []action, cfg *commonapi.SourceCollectorConfig) {
	cfg.DbQueryTemplatization = mergeDbQueryTemplatizationConfigs(decodeActionConfigs[actions.DbQueryTemplatizationConfig](logger, dbActions))
}

// mergeDbQueryTemplatizationConfigs ORs the options across actions, the same way the k8s instrumentor
// merges DbQueryTemplatization Actions for a container. Returns nil when no action enables an option.
func mergeDbQueryTemplatizationConfigs(configs []actions.DbQueryTemplatizationConfig) *actions.DbQueryTemplatizationConfig {
	merged := actions.DbQueryTemplatizationConfig{}
	for _, cfg := range configs {
		merged.TemplatizeLiterals = merged.TemplatizeLiterals || cfg.TemplatizeLiterals
		merged.RemovePostgresCastOperator = merged.RemovePostgresCastOperator || cfg.RemovePostgresCastOperator
	}
	if !merged.TemplatizeLiterals && !merged.RemovePostgresCastOperator {
		return nil
	}
	return &merged
}
