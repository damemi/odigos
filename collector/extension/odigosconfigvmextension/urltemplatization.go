package odigosconfigvmextension

import (
	"go.uber.org/zap"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/api/actions"
)

func buildUrlTemplatization(logger *zap.Logger, urlActions []action, cfg *commonapi.SourceCollectorConfig) {
	cfg.UrlTemplatization = mergeUrlTemplatizationConfigs(decodeActionConfigs[actions.UrlTemplatizationConfig](logger, urlActions))
}

// mergeUrlTemplatizationConfigs unions templates and merges default templatization configs across
// actions, the same way the k8s instrumentor merges URLTemplatization Actions for a container. VM
// actions have no scopes, so every action applies to all sources. Returns nil when there are no actions.
func mergeUrlTemplatizationConfigs(configs []actions.UrlTemplatizationConfig) *actions.UrlTemplatizationConfig {
	if len(configs) == 0 {
		return nil
	}

	var templates []string
	var defaultConfig *actions.DefaultTemplatizationConfig
	for _, cfg := range configs {
		templates = append(templates, cfg.Templates...)
		defaultConfig = actions.MergeDefaultTemplatizationConfigs(defaultConfig, cfg.Default)
	}
	return actions.BuildUrlTemplatizationConfig(templates, defaultConfig, true)
}
