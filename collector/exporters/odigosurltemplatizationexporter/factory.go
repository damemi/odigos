package odigosurltemplatizationexporter

//go:generate mdatagen metadata.yaml

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/exporter"
	"go.opentelemetry.io/collector/exporter/exporterhelper"

	"github.com/odigos-io/odigos/collector/exporters/odigosurltemplatizationexporter/internal/metadata"
)

func NewFactory() exporter.Factory {
	return exporter.NewFactory(
		metadata.Type,
		createDefaultConfig,
		exporter.WithTraces(createTracesExporter, metadata.TracesStability),
	)
}

func createDefaultConfig() component.Config {
	return &Config{
		FlushInterval:  defaultFlushInterval,
		FlushThreshold: defaultFlushThreshold,
	}
}

func createTracesExporter(
	ctx context.Context,
	set exporter.Settings,
	cfg component.Config,
) (exporter.Traces, error) {
	oCfg := cfg.(*Config)
	e := newExporter(set.Logger, oCfg)
	return exporterhelper.NewTraces(
		ctx,
		set,
		cfg,
		e.consumeTraces,
		exporterhelper.WithCapabilities(consumer.Capabilities{MutatesData: false}),
		exporterhelper.WithStart(e.Start),
		exporterhelper.WithShutdown(e.Shutdown),
	)
}
