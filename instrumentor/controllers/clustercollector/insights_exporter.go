package clustercollector

import (
	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/config"
	pipelinegen "github.com/odigos-io/odigos/common/pipelinegen"
	"github.com/odigos-io/odigos/instrumentor/controllers/pipeline"
)

// addInsightsGatewayExporter appends an OTLP gRPC exporter to the gateway's
// root traces pipeline so every processed span fans out to the in-cluster
// sidecar alongside the destination router. Noop when disabled or when no
// root traces pipeline exists.
func addInsightsGatewayExporter(c *config.Config, odigosNs string, insights *common.InsightsConfiguration) error {
	if !common.InsightsPipelineActive(insights) {
		return nil
	}

	rootPipelineName := pipelinegen.GetTelemetryRootPipelineName(common.TracesObservabilitySignal)
	rootPipeline, hasRoot := c.Service.Pipelines[rootPipelineName]
	if !hasRoot {
		return nil
	}

	if c.Exporters == nil {
		c.Exporters = config.GenericMap{}
	}

	// Target the headless insights Service via the dns:/// resolver and enable
	// round_robin so the gateway's gRPC client fans out across every insights
	// pod instead of pinning one behind the ClusterIP VIP.
	c.Exporters[pipeline.InsightsGatewayExporter] = config.GenericMap{
		"endpoint":      k8sconsts.InsightsOtlpGrpcDNSEndpoint(odigosNs),
		"balancer_name": "round_robin",
		"tls":           config.GenericMap{"insecure": true},
		"compression":   "none",
		"retry_on_failure": config.GenericMap{
			"enabled": false,
		},
		// groupbytrace emits one trace per call; batch them into fewer Exports.
		// min_size is below the 8192 default because insights handles each Export
		// serially. max_size stays unset so a trace is never split across Exports.
		"sending_queue": config.GenericMap{
			"batch": config.GenericMap{
				"min_size": 1000,
			},
		},
	}

	rootPipeline.Exporters = append(rootPipeline.Exporters, pipeline.InsightsGatewayExporter)
	c.Service.Pipelines[rootPipelineName] = rootPipeline

	return nil
}
