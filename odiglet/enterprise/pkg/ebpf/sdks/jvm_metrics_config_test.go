package sdks

import (
	"testing"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/api/agentsignalconfig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJVMMetricsConfig(t *testing.T) {
	enabled, disabled := false, true

	t.Run("metrics signal disabled turns JVM metrics off", func(t *testing.T) {
		cfg := jvmMetricsConfig(&odigosv1.ContainerAgentConfig{})
		require.NotNil(t, cfg.Disabled)
		assert.True(t, *cfg.Disabled)
	})

	t.Run("metrics signal enabled without runtime metrics config leaves them on", func(t *testing.T) {
		cfg := jvmMetricsConfig(&odigosv1.ContainerAgentConfig{Metrics: &agentsignalconfig.AgentMetricsConfig{}})
		assert.Nil(t, cfg.Disabled)
		assert.Empty(t, cfg.Metrics)
	})

	t.Run("runtime metrics config is passed through", func(t *testing.T) {
		for _, d := range []*bool{&enabled, &disabled} {
			cfg := jvmMetricsConfig(&odigosv1.ContainerAgentConfig{Metrics: &agentsignalconfig.AgentMetricsConfig{
				RuntimeMetrics: &common.MetricsSourceAgentRuntimeMetricsConfiguration{
					Java: &common.MetricsSourceAgentJavaRuntimeMetricsConfiguration{Disabled: d},
				},
			}})
			require.NotNil(t, cfg.Disabled)
			assert.Equal(t, *d, *cfg.Disabled)
		}
	})
}
