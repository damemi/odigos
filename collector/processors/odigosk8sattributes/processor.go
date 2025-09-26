package odigosk8sattributes

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned/typed/odigos/v1alpha1"
	odigosv1alpha1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

type k8sAttributesProcessor struct {
	logger         *zap.Logger
	config         *Config
	odigosClient   v1alpha1.OdigosV1alpha1Interface
	k8sClient      *kubernetes.Clientset
	sourceInformer cache.SharedIndexInformer
	informerStopCh chan struct{}
	mu             sync.RWMutex
}

func (p *k8sAttributesProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	// Start informer if not already started
	if err := p.startInformer(); err != nil {
		p.logger.Error("Failed to start informer", zap.Error(err))
		return td, err
	}

	// Get Sources from informer cache
	sources, err := p.getSourcesFromCache()
	if err != nil {
		p.logger.Error("Failed to get Sources from cache", zap.Error(err))
		return td, err
	}

	p.logger.Info("Retrieved Sources from cache", zap.Int("count", len(sources)))

	// Get InstrumentationConfigs for processing
	configs, err := p.getInstrumentationConfigs(ctx)
	if err != nil {
		p.logger.Error("Failed to get InstrumentationConfigs", zap.Error(err))
		return td, err
	}

	p.logger.Info("Retrieved InstrumentationConfigs", zap.Int("count", len(configs.Items)))

	// TODO: Process traces using the Sources and InstrumentationConfigs
	// This is where the actual k8s attributes processing logic would go

	return td, nil
}

// getInstrumentationConfigs retrieves all InstrumentationConfigs from the cluster
func (p *k8sAttributesProcessor) getInstrumentationConfigs(ctx context.Context) (*odigosv1alpha1.InstrumentationConfigList, error) {
	return p.odigosClient.InstrumentationConfigs("").List(ctx, metav1.ListOptions{})
}

// startInformer starts the Source informer
func (p *k8sAttributesProcessor) startInformer() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sourceInformer != nil {
		return nil // Already started
	}

	p.informerStopCh = make(chan struct{})

	// Create informer for Source objects
	p.sourceInformer = cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return p.odigosClient.Sources("").List(context.Background(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return p.odigosClient.Sources("").Watch(context.Background(), options)
			},
		},
		&odigosv1alpha1.Source{},
		0, // No resync period
		cache.Indexers{},
	)

	// Add event handlers
	p.sourceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if source, ok := obj.(*odigosv1alpha1.Source); ok {
				p.logger.Info("Source added",
					zap.String("name", source.Name),
					zap.String("namespace", source.Namespace),
					zap.String("workload", source.Spec.Workload.Name),
				)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if source, ok := newObj.(*odigosv1alpha1.Source); ok {
				p.logger.Info("Source updated",
					zap.String("name", source.Name),
					zap.String("namespace", source.Namespace),
					zap.String("workload", source.Spec.Workload.Name),
				)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if source, ok := obj.(*odigosv1alpha1.Source); ok {
				p.logger.Info("Source deleted",
					zap.String("name", source.Name),
					zap.String("namespace", source.Namespace),
					zap.String("workload", source.Spec.Workload.Name),
				)
			}
		},
	})

	// Start the informer
	go p.sourceInformer.Run(p.informerStopCh)

	// Wait for cache to sync
	if !cache.WaitForCacheSync(p.informerStopCh, p.sourceInformer.HasSynced) {
		return fmt.Errorf("failed to sync Source informer cache")
	}

	p.logger.Info("Source informer started successfully")
	return nil
}

// stopInformer stops the Source informer
func (p *k8sAttributesProcessor) stopInformer() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.sourceInformer == nil {
		return
	}

	close(p.informerStopCh)
	p.sourceInformer = nil
	p.logger.Info("Source informer stopped")
}

// getSourcesFromCache retrieves all Source objects from the informer cache
func (p *k8sAttributesProcessor) getSourcesFromCache() ([]*odigosv1alpha1.Source, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.sourceInformer == nil {
		return nil, fmt.Errorf("informer not started")
	}

	// Get all objects from the cache
	objects := p.sourceInformer.GetStore().List()
	sources := make([]*odigosv1alpha1.Source, 0, len(objects))

	for _, obj := range objects {
		if source, ok := obj.(*odigosv1alpha1.Source); ok {
			sources = append(sources, source)
		}
	}

	return sources, nil
}
