package odigosgroupbytraceprocessor

//go:generate mdatagen metadata.yaml

import (
	"context"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"

	"github.com/odigos-io/odigos/collector/processors/odigosgroupbytraceprocessor/internal/metadata"
)

// NewFactory returns a traces processor factory.
func NewFactory() processor.Factory {
	return processor.NewFactory(
		metadata.Type,
		func() component.Config { return createDefaultConfig() },
		processor.WithTraces(createTracesProcessor, metadata.TracesStability),
	)
}

func createTracesProcessor(
	_ context.Context,
	set processor.Settings,
	cfg component.Config,
	next consumer.Traces,
) (processor.Traces, error) {
	return newGroupByTraceProcessor(set, cfg.(*Config), next)
}
