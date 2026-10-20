package odigosconfigvmextension

import (
	"go.uber.org/zap"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/api/actions"
)

// buildInferDbAttributes enables inferring db attributes when any enabled inferDbAttributes action
// exists. The config has no options yet, so the actions' config is not read.
func buildInferDbAttributes(_ *zap.Logger, inferActions []action, cfg *commonapi.SourceCollectorConfig) {
	if len(inferActions) > 0 {
		cfg.InferDbAttributes = &actions.InferDbAttributesConfig{}
	}
}
