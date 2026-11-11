package odigosebpfreceiver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/odigos-io/odigos/collector/receivers/odigosebpfreceiver/internal/metadata"
)

func TestDeprecatedMetricsReceiver(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	set := receivertest.NewNopSettings(metadata.Type)
	set.Logger = zap.New(core)
	sink := new(consumertest.MetricsSink)

	factory := NewFactory()
	rcvr, err := factory.CreateMetrics(context.Background(), set, factory.CreateDefaultConfig(), sink)
	require.NoError(t, err)

	require.NoError(t, rcvr.Start(context.Background(), componenttest.NewNopHost()))
	require.NoError(t, rcvr.Shutdown(context.Background()))

	assert.Empty(t, sink.AllMetrics())
	assert.Equal(t, 1, logs.FilterMessageSnippet("deprecated").Len())
}
