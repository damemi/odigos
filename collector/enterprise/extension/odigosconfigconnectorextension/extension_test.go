package odigosconfigconnectorextension

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/confmap"
	"go.opentelemetry.io/collector/extension/extensiontest"
	"go.opentelemetry.io/collector/pdata/pcommon"

	commonapi "github.com/odigos-io/odigos/common/api"
)

func resource(values map[string]string) pcommon.Resource {
	res := pcommon.NewResource()
	for key, value := range values {
		res.Attributes().PutStr(key, value)
	}
	return res
}

func makeExtension(t *testing.T, cfg *Config) *managedExtension {
	t.Helper()
	require.NoError(t, cfg.Validate())
	ext, err := NewFactory().Create(context.Background(), extensiontest.NewNopSettings(Type), cfg)
	require.NoError(t, err)
	require.NoError(t, ext.Start(context.Background(), componenttest.NewNopHost()))
	t.Cleanup(func() { require.NoError(t, ext.Shutdown(context.Background())) })
	return ext.(*managedExtension)
}

func scopedConfig() *Config {
	return &Config{
		CollectorConfig: map[string]any{"dbQueryTemplatization": map[string]any{"templatizeLiterals": true}},
		Workloads: []Workload{
			{ID: "checkout", ResourceAttributes: map[string]string{"cloud.region": "us-east-1", "service.name": "checkout"}},
			{ID: "payments", ResourceAttributes: map[string]string{"cloud.region": "us-east-1", "service.name": "payments"}},
		},
		Scopes: []Scope{
			{WorkloadIDs: []string{"checkout", "payments"}, CollectorConfig: map[string]any{"inferDbAttributes": map[string]any{}}},
			{Languages: []string{"java", "nodejs", "webjs"}, CollectorConfig: map[string]any{"dbQueryTemplatization": map[string]any{"removePostgresCastOperator": true}}},
			{WorkloadIDs: []string{"checkout"}, Languages: []string{"java"}, CollectorConfig: map[string]any{"urlTemplatization": map[string]any{"templatizationRules": []any{"/java/{id}"}}}},
			{Languages: []string{"python"}, CollectorConfig: map[string]any{"urlTemplatization": map[string]any{"templatizationRules": []any{"/python/{id}"}}}},
		},
	}
}

func TestGlobalWorkloadLanguageAndCombinedScopes(t *testing.T) {
	ext := makeExtension(t, scopedConfig())
	for _, tc := range []struct {
		name, region, workload, language string
		infer, cast                      bool
		template                         string
	}{
		{name: "single workload and language", region: "us-east-1", workload: "checkout", language: "java", infer: true, cast: true, template: "/java/{id}"},
		{name: "second workload in a group", region: "us-east-1", workload: "payments", language: "java", infer: true, cast: true},
		{name: "workload and independent language rules", region: "us-east-1", workload: "checkout", language: "python", infer: true, template: "/python/{id}"},
		{name: "language across unknown workloads", workload: "unknown", language: "python", template: "/python/{id}"},
		{name: "javascript server runtime", workload: "unknown", language: "nodejs", cast: true},
		{name: "javascript browser runtime", workload: "unknown", language: "webjs", cast: true},
		{name: "same service in another region is not the workload", region: "us-west-2", workload: "checkout", language: "java", cast: true},
		{name: "missing language", region: "us-east-1", workload: "checkout", infer: true},
		{name: "unconfigured language", language: "go"},
		{name: "global fallback"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := resource(map[string]string{"cloud.region": tc.region, "service.name": tc.workload, languageAttribute: tc.language})
			cfg, exists := ext.GetFromResource(res)
			require.True(t, exists)
			require.True(t, cfg.DbQueryTemplatization.TemplatizeLiterals)
			require.Equal(t, tc.cast, cfg.DbQueryTemplatization.RemovePostgresCastOperator)
			require.Equal(t, tc.infer, cfg.InferDbAttributes != nil)
			if tc.template == "" {
				require.Nil(t, cfg.UrlTemplatization)
			} else {
				require.Equal(t, []string{tc.template}, cfg.UrlTemplatization.Templates)
				require.NotNil(t, cfg.UrlTemplatization.Default)
			}
			require.Nil(t, cfg.PiiMasking)
			require.True(t, ext.IsActiveSource(res))
		})
	}
	res := resource(map[string]string{"service.name": "checkout"})
	res.Attributes().PutInt(languageAttribute, 42)
	cfg, exists := ext.GetFromResource(res)
	require.True(t, exists)
	require.False(t, cfg.DbQueryTemplatization.RemovePostgresCastOperator)
	require.Nil(t, cfg.InferDbAttributes)
}

func TestArbitraryLanguageValuesAreSupported(t *testing.T) {
	ext := makeExtension(t, &Config{Scopes: []Scope{{Languages: []string{"dotnet", "ruby", "future-language"}, CollectorConfig: map[string]any{"inferDbAttributes": map[string]any{}}}}})
	for _, language := range []string{"dotnet", "ruby", "future-language"} {
		cfg, _ := ext.GetFromResource(resource(map[string]string{languageAttribute: language}))
		require.NotNil(t, cfg.InferDbAttributes)
	}
	cfg, _ := ext.GetFromResource(resource(map[string]string{languageAttribute: "java"}))
	require.Nil(t, cfg.InferDbAttributes)
}

type recordingCallback struct {
	configs map[string]*commonapi.SourceCollectorConfig
}

func (c *recordingCallback) OnSet(key string, cfg *commonapi.SourceCollectorConfig) {
	c.configs[key] = cfg
}
func (c *recordingCallback) OnDeleteKey(key string) { delete(c.configs, key) }

func TestCallbackKeysMatchEveryWorkloadAndLanguageAndStayBounded(t *testing.T) {
	ext := makeExtension(t, scopedConfig())
	callback := &recordingCallback{configs: map[string]*commonapi.SourceCollectorConfig{}}
	ext.RegisterWorkloadConfigCacheCallback(callback)
	// Two configured workloads plus fallback, four configured languages plus fallback.
	require.Len(t, callback.configs, 3*5)
	for i := 0; i < 100; i++ {
		for _, service := range []string{"checkout", "payments", "unknown"} {
			for _, language := range []string{"java", "python", "nodejs", "webjs", fmt.Sprint(i)} {
				res := resource(map[string]string{"cloud.region": "us-east-1", "service.name": service, languageAttribute: language, "service.instance.id": fmt.Sprint(i)})
				key, err := ext.GetWorkloadCacheKey(res)
				require.NoError(t, err)
				cfg, _ := ext.GetFromResource(res)
				require.Same(t, callback.configs[key], cfg)
			}
		}
	}
	require.Len(t, ext.config.configs, 15)
	res := resource(map[string]string{"cloud.region": "us-east-1", "service.name": "checkout", languageAttribute: "java"})
	key, attrs, err := ext.GetWorkloadIdentityFromResource(res)
	require.NoError(t, err)
	require.Contains(t, callback.configs, key)
	require.Equal(t, map[string]any{"cloud.region": "us-east-1", "service.name": "checkout", languageAttribute: "java"}, attrs.AsRaw())
	require.True(t, ext.WaitForCacheSync(context.Background()))
	streams, exists := ext.GetDataStreamsForWorkload(res)
	require.Nil(t, streams)
	require.False(t, exists)
	ext.UnregisterWorkloadConfigCacheCallback(callback)
	require.Empty(t, ext.callbacks)
}

func TestConfigurationDecodingMergingAndIsolation(t *testing.T) {
	raw := map[string]any{
		"collector_config": map[string]any{"urlTemplatization": map[string]any{"templatizationRules": []any{"/global/{id}"}}},
		"workloads":        []any{map[string]any{"id": "checkout", "resource_attributes": map[string]any{"service.name": "checkout"}}},
		"scopes":           []any{map[string]any{"workload_ids": []any{"checkout"}, "languages": []any{"python"}, "collector_config": map[string]any{"urlTemplatization": map[string]any{"templatizationRules": []any{"/scoped/{id}", "/global/{id}"}, "default": map[string]any{"disabled": true}}}}},
	}
	var cfg Config
	require.NoError(t, confmap.NewFromStringMap(raw).Unmarshal(&cfg))
	ext := makeExtension(t, &cfg)
	// Compiling snapshots caller-owned configuration, including selector maps.
	cfg.Workloads[0].ResourceAttributes["service.name"] = "different"
	matched, _ := ext.GetFromResource(resource(map[string]string{"service.name": "checkout", languageAttribute: "python"}))
	require.Equal(t, []string{"/global/{id}", "/scoped/{id}"}, matched.UrlTemplatization.Templates)
	require.Nil(t, matched.UrlTemplatization.Default)
	global, _ := ext.GetFromResource(pcommon.NewResource())
	require.Equal(t, []string{"/global/{id}"}, global.UrlTemplatization.Templates)
	require.NotNil(t, global.UrlTemplatization.Default)
	// A fresh extension must not retain callbacks or scopes from a previous configuration.
	empty := makeExtension(t, &Config{})
	none, exists := empty.GetFromResource(pcommon.NewResource())
	require.True(t, exists)
	require.Equal(t, &commonapi.SourceCollectorConfig{}, none)
}

func TestInvalidConfigurationsAreRejected(t *testing.T) {
	for name, cfg := range map[string]*Config{
		"unknown setting":                 {CollectorConfig: map[string]any{"somethingElse": true}},
		"PII excluded":                    {CollectorConfig: map[string]any{"piiMasking": map[string]any{}}},
		"incorrect type":                  {CollectorConfig: map[string]any{"dbQueryTemplatization": "enabled"}},
		"typo in nested field":            {CollectorConfig: map[string]any{"dbQueryTemplatization": map[string]any{"templatizeLiteral": true}}},
		"missing id":                      {Workloads: []Workload{{ResourceAttributes: map[string]string{"service.name": "checkout"}}}},
		"missing selector":                {Workloads: []Workload{{ID: "checkout"}}},
		"blank selector":                  {Workloads: []Workload{{ID: "checkout", ResourceAttributes: map[string]string{"": "checkout"}}}},
		"duplicate id":                    {Workloads: []Workload{{ID: "same", ResourceAttributes: map[string]string{"service.name": "a"}}, {ID: "same", ResourceAttributes: map[string]string{"service.name": "b"}}}},
		"overlapping selectors":           {Workloads: []Workload{{ID: "a", ResourceAttributes: map[string]string{"service.name": "checkout"}}, {ID: "b", ResourceAttributes: map[string]string{"service.name": "checkout", "cloud.region": "us-east-1"}}}},
		"unrelated selectors can overlap": {Workloads: []Workload{{ID: "a", ResourceAttributes: map[string]string{"service.name": "checkout"}}, {ID: "b", ResourceAttributes: map[string]string{"cloud.region": "us-east-1"}}}},
		"unknown workload":                {Scopes: []Scope{{WorkloadIDs: []string{"missing"}}}},
		"missing scope filters":           {Scopes: []Scope{{}}},
		"blank language":                  {Scopes: []Scope{{Languages: []string{""}}}},
		"non canonical language":          {Scopes: []Scope{{Languages: []string{"Java"}}}},
		"invalid scoped config":           {Scopes: []Scope{{Languages: []string{"python"}, CollectorConfig: map[string]any{"inferDbAttributes": "yes"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			require.Error(t, cfg.Validate())
			_, err := NewFactory().Create(context.Background(), extensiontest.NewNopSettings(Type), cfg)
			require.Error(t, err)
		})
	}
}
