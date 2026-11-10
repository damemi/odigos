package odigosebpfreceiver

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/receiver"
)

//go:generate mdatagen metadata.yaml

const TypeStr = "odigosebpf"

func NewFactory() receiver.Factory {
	return receiver.NewFactory(
		component.MustNewType(TypeStr),
		createDefaultConfig,
		receiver.WithTraces(createTracesReceiver, component.StabilityLevelBeta),
		receiver.WithLogs(createLogsReceiver, component.StabilityLevelBeta),
		receiver.WithMetrics(createMetricsReceiver, component.StabilityLevelDeprecated),
	)
}

func createDefaultConfig() component.Config {
	return &Config{}
}

func createTracesReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	next consumer.Traces,
) (receiver.Traces, error) {
	return &ebpfReceiver{
		config:       cfg.(*Config),
		receiverType: ReceiverTypeTraces,
		nextTraces:   next,
		logger:       set.Logger,
		settings:     set,
	}, nil
}

func createLogsReceiver(
	_ context.Context,
	set receiver.Settings,
	cfg component.Config,
	next consumer.Logs,
) (receiver.Logs, error) {
	return &ebpfReceiver{
		config:       cfg.(*Config),
		receiverType: ReceiverTypeLogs,
		nextLogs:     next,
		logger:       set.Logger,
		settings:     set,
	}, nil
}
