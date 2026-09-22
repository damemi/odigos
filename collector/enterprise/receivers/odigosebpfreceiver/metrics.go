package odigosebpfreceiver

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"
)

// createMetricsReceiver keeps configs that still list odigosebpf in a metrics
// pipeline loadable for one release. JVM metrics now reach the collector over
// OTLP, so the receiver emits nothing.
func createMetricsReceiver(
	_ context.Context,
	set receiver.Settings,
	_ component.Config,
	_ consumer.Metrics,
) (receiver.Metrics, error) {
	return &deprecatedMetricsReceiver{logger: set.Logger}, nil
}

type deprecatedMetricsReceiver struct {
	logger *zap.Logger
}

func (r *deprecatedMetricsReceiver) Start(context.Context, component.Host) error {
	r.logger.Warn("the odigosebpf metrics receiver is deprecated and emits nothing; JVM metrics arrive over OTLP. Remove odigosebpf from metrics pipelines")
	return nil
}

func (r *deprecatedMetricsReceiver) Shutdown(context.Context) error {
	return nil
}
