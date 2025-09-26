package odigosk8sattributes

import (
	"context"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned/typed/odigos/v1alpha1"
	odigosv1alpha1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

type k8sAttributesProcessor struct {
	logger                *zap.Logger
	config                *Config
	instrumentationClient v1alpha1.OdigosV1alpha1Interface
	k8sClient             *kubernetes.Clientset
}

func (p *k8sAttributesProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	// Get InstrumentationConfigs for processing
	configs, err := p.getInstrumentationConfigs(ctx)
	if err != nil {
		p.logger.Error("Failed to get InstrumentationConfigs", zap.Error(err))
		return td, err
	}

	p.logger.Info("Retrieved InstrumentationConfigs", zap.Int("count", len(configs.Items)))

	// TODO: Process traces using the InstrumentationConfigs
	// This is where the actual k8s attributes processing logic would go

	return td, nil
}

// getInstrumentationConfigs retrieves all InstrumentationConfigs from the cluster
func (p *k8sAttributesProcessor) getInstrumentationConfigs(ctx context.Context) (*odigosv1alpha1.InstrumentationConfigList, error) {
	return p.instrumentationClient.InstrumentationConfigs("").List(ctx, metav1.ListOptions{})
}
