package clustercollector

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/odigos-io/odigos/api/k8sconsts"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/config"
	odigosconsts "github.com/odigos-io/odigos/common/consts"
	pipelinegen "github.com/odigos-io/odigos/common/pipelinegen"
)

func ptr[T any](v T) *T { return &v }

func TestGatewayConfig_GroupByWritesOnlySetFields(t *testing.T) {
	on := true
	tier := common.OnPremOdigosTier
	insightsCfg := effectiveInsightsConfig(&common.InsightsConfiguration{Enabled: &on}, tier)
	ext := k8sconsts.OdigosConfigK8sExtensionType
	tailSampling := false
	gatewayOptions := pipelinegen.GatewayConfigOptions{
		OdigosNamespace:           "odigos-system",
		OdigosConfigExtensionName: &ext,
		TailSamplingEnabled:       &tailSampling,
		Insights:                  insightsCfg,
		InsightsOtlpEndpoint:      k8sconsts.InsightsOtlpGrpcDNSEndpoint("odigos-system"),
		TraceAggregation:          &common.TraceAggregationConfiguration{RootSettle: ptr("3s"), MaxTraces: ptr(5000), LateSpansMaxMemoryMiB: ptr(0)},
	}

	cfg, err, _, _ := pipelinegen.CalculateGatewayConfig(nil, nil,
		func(c *config.Config, destinationPipelineNames []string, signalsRootPipelines []string) error {
			if err := addSelfTelemetryPipeline(c, 8888, destinationPipelineNames, signalsRootPipelines); err != nil {
				return err
			}
			return addInsightsGatewayExporter(c, "odigos-system", insightsCfg)
		},
		nil, &gatewayOptions)
	require.NoError(t, err)

	assert.Equal(t, config.GenericMap{"root_settle": "3s", "max_traces": 5000, "late_spans_max_memory_mib": 0}, cfg.Processors[odigosconsts.GroupByTraceProcessor])
}
