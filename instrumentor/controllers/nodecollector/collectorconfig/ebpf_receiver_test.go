package collectorconfig

import (
	"slices"
	"testing"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The eBPF receiver carries traces and logs only. JVM runtime metrics reach the node collector
// over OTLP, so the receiver is never wired into a metrics pipeline.
func TestMetricsConfig_EbpfReceiverNotInMetricsPipelines(t *testing.T) {
	// kubeletstats keeps the pipeline alive, so the assertion below is about the eBPF receiver
	// only and not about the pipeline being dropped.
	cfg := MetricsConfig(&odigosv1.CollectorsGroup{}, MetricsConfigOptions{
		MetricsConfigSettings: &odigosv1.CollectorsGroupMetricsCollectionSettings{
			KubeletStats: &common.MetricsSourceKubeletStatsConfiguration{},
		},
	})

	pl, ok := cfg.Service.Pipelines[odigosMetricsPipelineName]
	require.True(t, ok, "expected a metrics pipeline")
	require.True(t, contains(pl.Receivers, kubeletstatsReceiverName),
		"precondition: kubeletstats should be wired")
	for name, pl := range cfg.Service.Pipelines {
		assert.False(t, contains(pl.Receivers, odigosEbpfReceiverName),
			"pipeline %s receivers %v", name, pl.Receivers)
	}
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
