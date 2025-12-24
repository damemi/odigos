package ssehub

import (
	"testing"

	"github.com/odigos-io/central-backend/internal/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSSEMetricIncrement(t *testing.T) {
	registry := prometheus.NewRegistry()

	counterVec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metrics.SSEBroadcastRateDef.Name,
		Help: metrics.SSEBroadcastRateDef.Help,
	}, metrics.SSEBroadcastRateDef.Labels)
	registry.MustRegister(counterVec)

	metrics.SSEBroadcastRate = counterVec

	metrics.SSEBroadcastRate.WithLabelValues("cluster-1", "test-cluster").Inc()
	metrics.SSEBroadcastRate.WithLabelValues("cluster-1", "test-cluster").Inc()
	metrics.SSEBroadcastRate.WithLabelValues("cluster-1", "test-cluster").Inc()

	value := testutil.ToFloat64(metrics.SSEBroadcastRate.WithLabelValues("cluster-1", "test-cluster"))
	if value != 3 {
		t.Errorf("Expected metric value 3, got %f", value)
	}
}

func TestSSEMetricIncrementMultipleClusters(t *testing.T) {
	registry := prometheus.NewRegistry()

	counterVec := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: metrics.SSEBroadcastRateDef.Name,
		Help: metrics.SSEBroadcastRateDef.Help,
	}, metrics.SSEBroadcastRateDef.Labels)
	registry.MustRegister(counterVec)

	metrics.SSEBroadcastRate = counterVec

	metrics.SSEBroadcastRate.WithLabelValues("cluster-1", "production").Inc()
	metrics.SSEBroadcastRate.WithLabelValues("cluster-1", "production").Inc()
	metrics.SSEBroadcastRate.WithLabelValues("cluster-2", "staging").Inc()

	prodValue := testutil.ToFloat64(metrics.SSEBroadcastRate.WithLabelValues("cluster-1", "production"))
	if prodValue != 2 {
		t.Errorf("Expected production cluster value 2, got %f", prodValue)
	}

	stagingValue := testutil.ToFloat64(metrics.SSEBroadcastRate.WithLabelValues("cluster-2", "staging"))
	if stagingValue != 1 {
		t.Errorf("Expected staging cluster value 1, got %f", stagingValue)
	}
}
