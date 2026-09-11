package clustercollector

import (
	"testing"

	commonconf "github.com/odigos-io/odigos/autoscaler/controllers/common"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/config"
	pipelinegen "github.com/odigos-io/odigos/common/pipelinegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func configWithProfilesPipeline() *config.Config {
	return &config.Config{
		Exporters: config.GenericMap{
			commonconf.ProfilingGatewayToUIExporter: config.GenericMap{},
		},
		Service: config.Service{
			Pipelines: map[string]config.Pipeline{
				gatewayProfilesPipeline: {
					Receivers: []string{"otlp"},
					Exporters: []string{commonconf.ProfilingGatewayToUIExporter},
				},
			},
		},
	}
}

func configWithTracesRootPipeline() *config.Config {
	rootName := pipelinegen.GetTelemetryRootPipelineName(common.TracesObservabilitySignal)
	return &config.Config{
		Exporters: config.GenericMap{
			"odigosrouterconnector/traces": config.GenericMap{},
		},
		Service: config.Service{
			Pipelines: map[string]config.Pipeline{
				rootName: {
					Receivers:  []string{"otlp"},
					Exporters:  []string{"odigosrouterconnector/traces"},
				},
			},
		},
	}
}

func TestAddInterrogationExporters_Disabled(t *testing.T) {
	t.Run("nil_config_noop", func(t *testing.T) {
		c := configWithProfilesPipeline()
		require.NoError(t, addInterrogationExporters(c, "odigos-system", nil, nil))

		_, hasExp := c.Exporters[commonconf.InterrogationProfilesExporter]
		assert.False(t, hasExp)

		pl := c.Service.Pipelines[gatewayProfilesPipeline]
		assert.Equal(t, []string{commonconf.ProfilingGatewayToUIExporter}, pl.Exporters)
	})

	t.Run("explicit_false_noop", func(t *testing.T) {
		off := false
		c := configWithProfilesPipeline()
		require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{Enabled: &off}, nil))

		_, hasExp := c.Exporters[commonconf.InterrogationProfilesExporter]
		assert.False(t, hasExp)
	})
}

func TestAddInterrogationExporters_NoPipelinesNoop(t *testing.T) {
	on := true
	c := &config.Config{Service: config.Service{Pipelines: map[string]config.Pipeline{}}}
	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{Enabled: &on}, nil))

	_, hasProfiles := c.Exporters[commonconf.InterrogationProfilesExporter]
	_, hasTraces := c.Exporters[commonconf.InterrogationTracesExporter]
	assert.False(t, hasProfiles)
	assert.False(t, hasTraces)
}

func TestAddInterrogationExporters_EnabledAppendsToProfilesPipeline(t *testing.T) {
	on := true
	c := configWithProfilesPipeline()
	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{Enabled: &on}, nil))

	exp, ok := c.Exporters[commonconf.InterrogationProfilesExporter].(config.GenericMap)
	require.True(t, ok, "profiles exporter must be registered")
	assert.Equal(t, commonconf.InterrogationCacheExtension, exp["interrogation_cache_extension"])

	_, hasExt := c.Extensions[commonconf.InterrogationCacheExtension]
	assert.True(t, hasExt, "cache extension must be registered")
	assert.Contains(t, c.Service.Extensions, commonconf.InterrogationCacheExtension)

	pl := c.Service.Pipelines[gatewayProfilesPipeline]
	assert.Equal(t, []string{"otlp"}, pl.Receivers)
	assert.Equal(t, []string{commonconf.ProfilingGatewayToUIExporter, commonconf.InterrogationProfilesExporter}, pl.Exporters)

	_, hasTraces := c.Exporters[commonconf.InterrogationTracesExporter]
	assert.False(t, hasTraces, "traces exporter requires a root traces pipeline")
}

func TestAddInterrogationExporters_EnabledAppendsToTracesPipeline(t *testing.T) {
	on := true
	c := configWithTracesRootPipeline()
	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{Enabled: &on}, nil))

	exp, ok := c.Exporters[commonconf.InterrogationTracesExporter].(config.GenericMap)
	require.True(t, ok, "traces exporter must be registered")
	assert.Equal(t, commonconf.InterrogationCacheExtension, exp["interrogation_cache_extension"])
	assert.Equal(t, "odigos-interrogation-redis.odigos-system:6379", exp["redis_endpoint"])

	_, hasExt := c.Extensions[commonconf.InterrogationCacheExtension]
	assert.True(t, hasExt)
	assert.Contains(t, c.Service.Extensions, commonconf.InterrogationCacheExtension)

	rootName := pipelinegen.GetTelemetryRootPipelineName(common.TracesObservabilitySignal)
	pl := c.Service.Pipelines[rootName]
	assert.Equal(t, []string{"odigosrouterconnector/traces", commonconf.InterrogationTracesExporter}, pl.Exporters)

	_, hasProfiles := c.Exporters[commonconf.InterrogationProfilesExporter]
	assert.False(t, hasProfiles, "profiles exporter requires a profiles pipeline")
}

func TestAddInterrogationExporters_BothPipelines(t *testing.T) {
	on := true
	c := configWithProfilesPipeline()
	rootName := pipelinegen.GetTelemetryRootPipelineName(common.TracesObservabilitySignal)
	c.Service.Pipelines[rootName] = config.Pipeline{
		Receivers: []string{"otlp"},
		Exporters: []string{"odigosrouterconnector/traces"},
	}

	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{Enabled: &on}, nil))

	_, hasProfiles := c.Exporters[commonconf.InterrogationProfilesExporter]
	_, hasTraces := c.Exporters[commonconf.InterrogationTracesExporter]
	assert.True(t, hasProfiles)
	assert.True(t, hasTraces)
}

func TestAddInterrogationExporters_LLMConfigOnTracesExporter(t *testing.T) {
	on := true
	c := configWithTracesRootPipeline()
	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{
		Enabled: &on,
		LLM: &common.InterrogationLLMConfiguration{
			Provider: "openai",
			Model:    "gpt-4o-mini",
			APIKey:   "sk-test",
			BaseURL:  "https://api.openai.com/v1",
		},
	}, nil))

	exp, ok := c.Exporters[commonconf.InterrogationTracesExporter].(config.GenericMap)
	require.True(t, ok)
	llm, ok := exp["llm"].(config.GenericMap)
	require.True(t, ok)
	assert.Equal(t, "openai", llm["provider"])
	assert.Equal(t, "gpt-4o-mini", llm["model"])
	assert.Equal(t, "sk-test", llm["api_key"])
	assert.Equal(t, "https://api.openai.com/v1", llm["base_url"])
}

func TestAddInterrogationExporters_LLMSkippedWithoutAPIKey(t *testing.T) {
	on := true
	c := configWithTracesRootPipeline()
	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{
		Enabled: &on,
		LLM: &common.InterrogationLLMConfiguration{
			Provider: "openai",
			Model:    "gpt-4o-mini",
		},
	}, nil))

	exp := c.Exporters[commonconf.InterrogationTracesExporter].(config.GenericMap)
	_, hasLLM := exp["llm"]
	assert.False(t, hasLLM)
}

func TestAddInterrogationExporters_TransactionIdentityDimensions(t *testing.T) {
	on := true
	c := configWithTracesRootPipeline()
	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{Enabled: &on},
		&common.TransactionIdentityConfiguration{
			Dimensions: []string{"http.response.status_code", "rpc.grpc.status_code"},
		}))

	exp, ok := c.Exporters[commonconf.InterrogationTracesExporter].(config.GenericMap)
	require.True(t, ok)
	assert.Equal(t, []string{"http.response.status_code", "rpc.grpc.status_code"}, exp["transaction_identity_dimensions"])
}

func TestAddInterrogationExporters_TransactionIdentityDimensionsOmittedWhenEmpty(t *testing.T) {
	on := true
	c := configWithTracesRootPipeline()
	require.NoError(t, addInterrogationExporters(c, "odigos-system", &common.InterrogationConfiguration{Enabled: &on},
		&common.TransactionIdentityConfiguration{Dimensions: nil}))

	exp := c.Exporters[commonconf.InterrogationTracesExporter].(config.GenericMap)
	_, hasDims := exp["transaction_identity_dimensions"]
	assert.False(t, hasDims)
}
