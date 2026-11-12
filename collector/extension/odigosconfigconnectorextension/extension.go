package odigosconfigconnectorextension

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"

	commonapi "github.com/odigos-io/odigos/common/api"
	"github.com/odigos-io/odigos/common/api/actions"
	"github.com/odigos-io/odigos/common/collector"
)

const languageAttribute = "telemetry.sdk.language"

type compiledScope struct {
	config    *commonapi.SourceCollectorConfig
	workloads map[int]bool
	languages map[string]bool
}

type managedConfig struct {
	workloads []Workload
	languages []string
	configs   map[string]*commonapi.SourceCollectorConfig
}

func (c *managedConfig) addLanguage(language string) {
	for _, existing := range c.languages {
		if existing == language {
			return
		}
	}
	c.languages = append(c.languages, language)
}

func cacheKey(workload, language int) string {
	return fmt.Sprintf("workload/%d/language/%d", workload, language)
}

type managedExtension struct {
	config    *managedConfig
	mu        sync.Mutex
	callbacks map[collector.WorkloadConfigCacheCallback]struct{}
}

var _ collector.OdigosConfigExtension = (*managedExtension)(nil)

func (*managedExtension) Start(context.Context, component.Host) error { return nil }

func (e *managedExtension) Shutdown(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	clear(e.callbacks)
	return nil
}

func (e *managedExtension) resolve(resource pcommon.Resource) (int, int) {
	workload, language := -1, -1
	for i, selector := range e.config.workloads {
		matched := true
		for key, expected := range selector.ResourceAttributes {
			value, exists := resource.Attributes().Get(key)
			if !exists || value.Type() != pcommon.ValueTypeStr || value.Str() != expected {
				matched = false
				break
			}
		}
		if matched {
			workload = i
			break
		}
	}
	if value, exists := resource.Attributes().Get(languageAttribute); exists && value.Type() == pcommon.ValueTypeStr {
		for i, configured := range e.config.languages {
			if value.Str() == configured {
				language = i
				break
			}
		}
	}
	return workload, language
}

func (e *managedExtension) GetFromResource(resource pcommon.Resource) (*commonapi.SourceCollectorConfig, bool) {
	workload, language := e.resolve(resource)
	return e.config.configs[cacheKey(workload, language)], true
}

func (*managedExtension) IsActiveSource(pcommon.Resource) bool { return true }

func (e *managedExtension) GetWorkloadCacheKey(resource pcommon.Resource) (string, error) {
	workload, language := e.resolve(resource)
	return cacheKey(workload, language), nil
}

func (e *managedExtension) GetWorkloadIdentityFromResource(resource pcommon.Resource) (string, pcommon.Map, error) {
	workload, language := e.resolve(resource)
	attrs := pcommon.NewMap()
	if workload >= 0 {
		for key, value := range e.config.workloads[workload].ResourceAttributes {
			attrs.PutStr(key, value)
		}
	}
	if language >= 0 {
		attrs.PutStr(languageAttribute, e.config.languages[language])
	}
	return cacheKey(workload, language), attrs, nil
}

func (e *managedExtension) RegisterWorkloadConfigCacheCallback(callback collector.WorkloadConfigCacheCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.callbacks[callback] = struct{}{}
	for key, cfg := range e.config.configs {
		callback.OnSet(key, cfg)
	}
}

func (e *managedExtension) UnregisterWorkloadConfigCacheCallback(callback collector.WorkloadConfigCacheCallback) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.callbacks, callback)
}

func (*managedExtension) WaitForCacheSync(context.Context) bool { return true }

func (*managedExtension) GetDataStreamsForWorkload(pcommon.Resource) ([]string, bool) {
	return nil, false
}

func mergeConfig(target, addition *commonapi.SourceCollectorConfig) {
	copy := addition.DeepCopy()
	if copy.UrlTemplatization != nil {
		if target.UrlTemplatization == nil {
			target.UrlTemplatization = copy.UrlTemplatization
		} else {
			target.UrlTemplatization.Templates = append(target.UrlTemplatization.Templates, copy.UrlTemplatization.Templates...)
			target.UrlTemplatization.Default = actions.MergeDefaultTemplatizationConfigs(target.UrlTemplatization.Default, copy.UrlTemplatization.Default)
		}
	}
	if copy.DbQueryTemplatization != nil {
		if target.DbQueryTemplatization == nil {
			target.DbQueryTemplatization = copy.DbQueryTemplatization
		} else {
			target.DbQueryTemplatization.TemplatizeLiterals = target.DbQueryTemplatization.TemplatizeLiterals || copy.DbQueryTemplatization.TemplatizeLiterals
			target.DbQueryTemplatization.RemovePostgresCastOperator = target.DbQueryTemplatization.RemovePostgresCastOperator || copy.DbQueryTemplatization.RemovePostgresCastOperator
		}
	}
	if copy.InferDbAttributes != nil {
		target.InferDbAttributes = copy.InferDbAttributes
	}
}

func normalizeConfig(cfg *commonapi.SourceCollectorConfig) *commonapi.SourceCollectorConfig {
	if url := cfg.UrlTemplatization; url != nil {
		cfg.UrlTemplatization = actions.BuildUrlTemplatizationConfig(url.Templates, url.Default, len(url.Templates) != 0)
	}
	if db := cfg.DbQueryTemplatization; db != nil && !db.TemplatizeLiterals && !db.RemovePostgresCastOperator {
		cfg.DbQueryTemplatization = nil
	}
	return cfg
}
