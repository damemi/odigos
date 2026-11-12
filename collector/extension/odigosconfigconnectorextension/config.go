package odigosconfigconnectorextension

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/urltemplate"
)

// Config is part of the Collector configuration delivered by the OpAMP supervisor.
// Workload identities are explicit selectors, not inferred from mutable task/instance IDs.
type Config struct {
	CollectorConfig map[string]any `mapstructure:"collector_config"`
	Workloads       []Workload     `mapstructure:"workloads"`
	Scopes          []Scope        `mapstructure:"scopes"`
}

type Workload struct {
	ID                 string            `mapstructure:"id"`
	ResourceAttributes map[string]string `mapstructure:"resource_attributes"`
}

// Values within a filter are ORed; workload and language filters are ANDed.
type Scope struct {
	WorkloadIDs     []string       `mapstructure:"workload_ids"`
	Languages       []string       `mapstructure:"languages"`
	CollectorConfig map[string]any `mapstructure:"collector_config"`
}

func (c *Config) Validate() error {
	_, err := c.compile()
	return err
}

func decodeConfig(raw map[string]any) (*commonapi.SourceCollectorConfig, error) {
	for key := range raw {
		switch key {
		case "urlTemplatization", "dbQueryTemplatization", "inferDbAttributes":
		default:
			return nil, fmt.Errorf("unsupported managed action setting %q", key)
		}
	}
	out := &commonapi.SourceCollectorConfig{}
	if raw == nil {
		return out, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return nil, err
	}
	if out.UrlTemplatization != nil {
		for _, rule := range out.UrlTemplatization.Templates {
			if _, err := urltemplate.ParseUserInputRuleString(rule, false); err != nil {
				return nil, fmt.Errorf("invalid URL template %q: %w", rule, err)
			}
		}
	}
	return out, nil
}

func (c *Config) compile() (*managedConfig, error) {
	global, err := decodeConfig(c.CollectorConfig)
	if err != nil {
		return nil, fmt.Errorf("collector_config: %w", err)
	}
	out := &managedConfig{configs: map[string]*commonapi.SourceCollectorConfig{}}
	ids := map[string]int{}
	for i, workload := range c.Workloads {
		if strings.TrimSpace(workload.ID) == "" {
			return nil, fmt.Errorf("workloads[%d]: id is required", i)
		}
		if _, exists := ids[workload.ID]; exists {
			return nil, fmt.Errorf("duplicate workload id %q", workload.ID)
		}
		if len(workload.ResourceAttributes) == 0 {
			return nil, fmt.Errorf("workload %q needs resource_attributes", workload.ID)
		}
		selector := map[string]string{}
		for key, value := range workload.ResourceAttributes {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("workload %q has a blank resource attribute", workload.ID)
			}
			selector[key] = value
		}
		for _, previous := range out.workloads {
			if selectorsOverlap(previous.ResourceAttributes, selector) {
				return nil, fmt.Errorf("workload selectors %q and %q can match the same resource", previous.ID, workload.ID)
			}
		}
		ids[workload.ID] = i
		out.workloads = append(out.workloads, Workload{ID: workload.ID, ResourceAttributes: selector})
	}
	var scopes []compiledScope
	for i, scope := range c.Scopes {
		if len(scope.WorkloadIDs) == 0 && len(scope.Languages) == 0 {
			return nil, fmt.Errorf("scopes[%d] needs workload_ids or languages; use collector_config for global settings", i)
		}
		cfg, err := decodeConfig(scope.CollectorConfig)
		if err != nil {
			return nil, fmt.Errorf("scopes[%d].collector_config: %w", i, err)
		}
		compiled := compiledScope{config: cfg, workloads: map[int]bool{}, languages: map[string]bool{}}
		for _, id := range scope.WorkloadIDs {
			index, exists := ids[id]
			if !exists {
				return nil, fmt.Errorf("scopes[%d] references unknown workload %q", i, id)
			}
			compiled.workloads[index] = true
		}
		for _, language := range scope.Languages {
			if strings.TrimSpace(language) == "" || language != strings.TrimSpace(language) || language != strings.ToLower(language) {
				return nil, fmt.Errorf("scopes[%d]: language must be a non-empty lowercase telemetry.sdk.language value", i)
			}
			compiled.languages[language] = true
			out.addLanguage(language)
		}
		scopes = append(scopes, compiled)
	}
	// Cache cardinality depends only on configuration, never task IDs or arbitrary incoming languages.
	for workload := -1; workload < len(out.workloads); workload++ {
		for language := -1; language < len(out.languages); language++ {
			cfg := global.DeepCopy()
			for _, scope := range scopes {
				if len(scope.workloads) != 0 && !scope.workloads[workload] {
					continue
				}
				if len(scope.languages) != 0 && (language < 0 || !scope.languages[out.languages[language]]) {
					continue
				}
				mergeConfig(cfg, scope.config)
			}
			out.configs[cacheKey(workload, language)] = normalizeConfig(cfg)
		}
	}
	return out, nil
}

// Selectors overlap unless a shared attribute has conflicting values. Rejecting overlap avoids
// order-dependent selection when, for example, two clusters reuse the same service name.
func selectorsOverlap(left, right map[string]string) bool {
	for key, value := range left {
		if other, exists := right[key]; exists && other != value {
			return false
		}
	}
	return true
}
