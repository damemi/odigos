package metrics

import (
	"fmt"
	"net/http"

	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func InitMetrics() {
	registry := prometheus.NewRegistry()

	// Create all metrics via iteration
	for _, def := range allGauges {
		gauge := prometheus.NewGauge(prometheus.GaugeOpts{
			Name: def.Name,
			Help: def.Options[0].Help,
		})
		*def.Target = gauge
		registry.MustRegister(gauge)
	}

	for _, def := range allCounterVecs {
		counterVec := prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: def.Name,
			Help: def.Help,
		}, def.Labels)
		*def.Target = counterVec
		registry.MustRegister(counterVec)
	}

	go serveMetrics(registry)
}

func serveMetrics(registry *prometheus.Registry) {
	addr := ":" + config.Config.MetricsPort

	// Serve metrics request in /metrics endpoint
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	}))

	logger.Log.Info(fmt.Sprintf("Starting Prometheus metrics server on %s/metrics", addr))
	if err := http.ListenAndServe(addr, mux); err != nil && err != http.ErrServerClosed {
		logger.Log.Error("Failed to start metrics server", "error", err)
	}
}
