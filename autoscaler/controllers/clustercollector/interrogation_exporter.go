package clustercollector

import (
	"slices"

	"github.com/odigos-io/odigos/api/k8sconsts"
	commonconf "github.com/odigos-io/odigos/autoscaler/controllers/common"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/config"
	pipelinegen "github.com/odigos-io/odigos/common/pipelinegen"
)

const gatewayProfilesPipeline = "profiles"

// addInterrogationExporters enables the interrogation bounding-join exporters and
// the shared cache extension when interrogation is on. Profiles exporter is
// appended only when the gateway profiles pipeline exists. Traces exporter is
// appended to the root traces pipeline (traces/in), which is post-groupbytrace —
// the same attachment point as insights and service I/O correlations.
// transactionIdentity supplies optional dimensions for the traces exporter config
// (from OdigosConfiguration.transactionIdentity).
func addInterrogationExporters(c *config.Config, odigosNs string, interrogation *common.InterrogationConfiguration, transactionIdentity *common.TransactionIdentityConfiguration) error {
	if !common.InterrogationActive(interrogation) {
		return nil
	}

	profilesPipeline, hasProfiles := c.Service.Pipelines[gatewayProfilesPipeline]
	rootPipelineName := pipelinegen.GetTelemetryRootPipelineName(common.TracesObservabilitySignal)
	rootPipeline, hasTraces := c.Service.Pipelines[rootPipelineName]
	if !hasProfiles && !hasTraces {
		return nil
	}

	ensureInterrogationCacheExtension(c)

	if c.Exporters == nil {
		c.Exporters = config.GenericMap{}
	}

	if hasProfiles {
		c.Exporters[commonconf.InterrogationProfilesExporter] = config.GenericMap{
			"interrogation_cache_extension": commonconf.InterrogationCacheExtension,
			"clickhouse_endpoint":           k8sconsts.InsightsClickHouseEndpoint(odigosNs),
			"clickhouse_password":           "${" + k8sconsts.OdigosInsightsClickHousePasswordEnv + "}",
		}
		profilesPipeline.Exporters = append(profilesPipeline.Exporters, commonconf.InterrogationProfilesExporter)
		c.Service.Pipelines[gatewayProfilesPipeline] = profilesPipeline
	}

	if hasTraces {
		tracesExp := config.GenericMap{
			"interrogation_cache_extension": commonconf.InterrogationCacheExtension,
			"redis_endpoint":                k8sconsts.InterrogationRedisEndpoint(odigosNs),
		}
		if dims := transactionIdentityDimensions(transactionIdentity); len(dims) > 0 {
			tracesExp["transaction_identity_dimensions"] = dims
		}
		if llm := interrogationLLMExporterConfig(interrogation); llm != nil {
			tracesExp["llm"] = llm
		}
		c.Exporters[commonconf.InterrogationTracesExporter] = tracesExp
		rootPipeline.Exporters = append(rootPipeline.Exporters, commonconf.InterrogationTracesExporter)
		c.Service.Pipelines[rootPipelineName] = rootPipeline
	}

	return nil
}

func transactionIdentityDimensions(cfg *common.TransactionIdentityConfiguration) []string {
	if cfg == nil || len(cfg.Dimensions) == 0 {
		return nil
	}
	return cfg.Dimensions
}

func interrogationLLMExporterConfig(interrogation *common.InterrogationConfiguration) config.GenericMap {
	if interrogation == nil || interrogation.LLM == nil {
		return nil
	}
	llm := interrogation.LLM
	if llm.APIKey == "" {
		return nil
	}
	out := config.GenericMap{
		"api_key": llm.APIKey,
	}
	if llm.Provider != "" {
		out["provider"] = llm.Provider
	}
	if llm.Model != "" {
		out["model"] = llm.Model
	}
	if llm.BaseURL != "" {
		out["base_url"] = llm.BaseURL
	}
	return out
}

func ensureInterrogationCacheExtension(c *config.Config) {
	if c.Extensions == nil {
		c.Extensions = config.GenericMap{}
	}
	c.Extensions[commonconf.InterrogationCacheExtension] = config.GenericMap{}
	if !slices.Contains(c.Service.Extensions, commonconf.InterrogationCacheExtension) {
		c.Service.Extensions = append(c.Service.Extensions, commonconf.InterrogationCacheExtension)
	}
}
