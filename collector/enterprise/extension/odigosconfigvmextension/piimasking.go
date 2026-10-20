package odigosconfigvmextension

import (
	"sort"

	"go.uber.org/zap"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/api/actions"
)

func buildPiiMasking(logger *zap.Logger, piiActions []action, cfg *commonapi.SourceCollectorConfig) {
	cfg.PiiMasking = mergePiiMaskingConfigs(decodeActionConfigs[actions.PiiMaskingConfig](logger, piiActions))
}

// mergePiiMaskingConfigs unions categories and custom maskings across actions, the same way the k8s
// instrumentor merges PiiMasking Actions for a container. Returns nil when no action contributes a rule.
func mergePiiMaskingConfigs(configs []actions.PiiMaskingConfig) *actions.PiiMaskingConfig {
	seenCategories := make(map[actions.PiiCategory]struct{})
	seenFormats := make(map[actions.CustomFormatMasking]struct{})
	seenRegexes := make(map[string]struct{})
	merged := actions.PiiMaskingConfig{}

	for _, cfg := range configs {
		for _, category := range cfg.PiiCategories {
			if _, ok := seenCategories[category]; ok {
				continue
			}
			seenCategories[category] = struct{}{}
			merged.PiiCategories = append(merged.PiiCategories, category)
		}
		for _, masking := range cfg.CustomFormatMaskings {
			if masking.LookupKey == "" || masking.DataFormat == "" {
				continue
			}
			if _, ok := seenFormats[masking]; ok {
				continue
			}
			seenFormats[masking] = struct{}{}
			merged.CustomFormatMaskings = append(merged.CustomFormatMaskings, masking)
		}
		for _, masking := range cfg.CustomRegexMaskings {
			if masking.Regex == "" {
				continue
			}
			if _, ok := seenRegexes[masking.Regex]; ok {
				continue
			}
			seenRegexes[masking.Regex] = struct{}{}
			merged.CustomRegexMaskings = append(merged.CustomRegexMaskings, masking)
		}
	}

	if len(merged.PiiCategories) == 0 && len(merged.CustomFormatMaskings) == 0 && len(merged.CustomRegexMaskings) == 0 {
		return nil
	}
	sort.Slice(merged.PiiCategories, func(i, j int) bool {
		return merged.PiiCategories[i] < merged.PiiCategories[j]
	})
	return &merged
}
