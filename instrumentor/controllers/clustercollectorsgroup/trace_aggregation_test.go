package clustercollectorsgroup

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"

	"github.com/odigos-io/odigos/common"
	"github.com/odigos-io/odigos/common/api/sampling"
	commonlogger "github.com/odigos-io/odigos/common/logger"
)

func ptr[T any](v T) *T { return &v }

func TestResolveTraceAggregation(t *testing.T) {
	gateway := func(ta *common.TraceAggregationConfiguration) *common.OdigosConfiguration {
		return &common.OdigosConfiguration{CollectorGateway: &common.CollectorGatewayConfiguration{TraceAggregation: ta}}
	}
	withLegacy := func(cfg *common.OdigosConfiguration, wait string) *common.OdigosConfiguration {
		cfg.Sampling = &common.SamplingConfiguration{TailSampling: &sampling.TailSamplingConfiguration{TraceAggregationWaitDuration: &wait}}
		return cfg
	}

	tests := []struct {
		name string
		cfg  *common.OdigosConfiguration
		want *common.TraceAggregationConfiguration
	}{
		{
			name: "nothing set keeps every processor default",
			cfg:  &common.OdigosConfiguration{},
		},
		{
			name: "set fields pass through",
			cfg: gateway(&common.TraceAggregationConfiguration{
				SyncWait: ptr("20s"), AsyncWait: ptr("45s"), RootSettle: ptr("5s"), MaxTraces: ptr(1000), LateSpansMaxMemoryMiB: ptr(16),
			}),
			want: &common.TraceAggregationConfiguration{
				SyncWait: ptr("20s"), AsyncWait: ptr("45s"), RootSettle: ptr("5s"), MaxTraces: ptr(1000), LateSpansMaxMemoryMiB: ptr(16),
			},
		},
		{
			name: "deprecated wait is ignored",
			cfg:  withLegacy(&common.OdigosConfiguration{}, "2s"),
		},
		{
			name: "deprecated wait does not fill unset waits",
			cfg:  withLegacy(gateway(&common.TraceAggregationConfiguration{SyncWait: ptr("10s")}), "2s"),
			want: &common.TraceAggregationConfiguration{SyncWait: ptr("10s")},
		},
		{
			name: "invalid values fall back to the defaults",
			cfg: gateway(&common.TraceAggregationConfiguration{
				SyncWait: ptr("soon"), AsyncWait: ptr("0s"), RootSettle: ptr("-1s"), MaxTraces: ptr(0), LateSpansMaxMemoryMiB: ptr(-1),
			}),
		},
		{
			name: "zero root settle and late span memory disable them",
			cfg:  gateway(&common.TraceAggregationConfiguration{RootSettle: ptr("0s"), LateSpansMaxMemoryMiB: ptr(0)}),
			want: &common.TraceAggregationConfiguration{RootSettle: ptr("0s"), LateSpansMaxMemoryMiB: ptr(0)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resolveTraceAggregation(&commonlogger.ContextLogger{Logger: logr.Discard()}, tt.cfg))
		})
	}
}

func TestResolveTailSamplingConfigDropsWait(t *testing.T) {
	got := resolveTailSamplingConfig(true)
	//nolint:staticcheck // asserting the deprecated field is no longer written
	assert.Nil(t, got.TraceAggregationWaitDuration)
	assert.False(t, *got.Disabled)
}
