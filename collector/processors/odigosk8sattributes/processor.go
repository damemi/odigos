package odigosk8sattributes

import (
	"context"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

type k8sAttributesProcessor struct {
	logger *zap.Logger
	config *Config
}

func (p *k8sAttributesProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	return td, nil
}
