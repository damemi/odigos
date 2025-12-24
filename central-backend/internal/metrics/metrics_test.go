package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsInitialization(t *testing.T) {
	registry := prometheus.NewRegistry()

	for _, def := range allGauges {
		registry.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{
			Name: def.Name,
			Help: def.Options[0].Help,
		}))
	}

	for _, def := range allCounterVecs {
		registry.MustRegister(prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: def.Name,
			Help: def.Help,
		}, def.Labels))
	}
}
