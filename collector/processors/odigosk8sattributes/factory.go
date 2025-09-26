package odigosk8sattributes

import (
	"context"
	"fmt"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/processor"
	"go.opentelemetry.io/collector/processor/processorhelper"

	"k8s.io/client-go/kubernetes"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"

	"github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned/typed/odigos/v1alpha1"
)

func NewFactory() processor.Factory {
	return processor.NewFactory(
		component.MustNewType("odigosk8sattributes"),
		createDefaultConfig,
		processor.WithTraces(createTracesProcessor, component.StabilityLevelBeta),
	)
}

func createDefaultConfig() component.Config {
	return &Config{}
}

func createTracesProcessor(
	ctx context.Context,
	set processor.Settings,
	cfg component.Config,
	nextConsumer consumer.Traces) (processor.Traces, error) {

	// Create Kubernetes client and instrumentation client
	instrumentationClient, k8sClient, err := createK8sClients()
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes clients: %w", err)
	}

	proc := &k8sAttributesProcessor{
		logger:         set.Logger,
		config:         cfg.(*Config),
		odigosClient:   instrumentationClient,
		k8sClient:      k8sClient,
		sourceInformer: nil, // Will be initialized when needed
		informerStopCh: nil,
	}

	return processorhelper.NewTraces(
		ctx,
		set,
		cfg,
		nextConsumer,
		proc.processTraces,
		processorhelper.WithCapabilities(consumer.Capabilities{MutatesData: true}),
	)
}

// createK8sClients creates the Kubernetes client and returns the instrumentation client
func createK8sClients() (v1alpha1.OdigosV1alpha1Interface, *kubernetes.Clientset, error) {
	// Create in-cluster config
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create in-cluster config: %w", err)
	}

	// Create Kubernetes client
	k8sClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	// Create Odigos clientset
	odigosClientset, err := v1alpha1.NewForConfig(config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create Odigos clientset: %w", err)
	}

	// Return the instrumentation client
	return odigosClientset, k8sClient, nil
}
