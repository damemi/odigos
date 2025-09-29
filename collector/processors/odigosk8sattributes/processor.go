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
	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1alpha1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/collector/processors/odigosk8sattributes/internal/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"
)

type k8sAttributesProcessor struct {
	logger         *zap.Logger
	config         *Config
	odigosClient   v1alpha1.OdigosV1alpha1Interface
	k8sClient      *kubernetes.Clientset
	icInformer     cache.SharedIndexInformer
	informerStopCh chan struct{}
	mu             sync.RWMutex
	// workloadCache maps PodWorkload to Pod
	workloadCache map[k8sconsts.PodWorkload]*kube.Pod
}

func (p *k8sAttributesProcessor) processTraces(ctx context.Context, td ptrace.Traces) (ptrace.Traces, error) {
	// Start informer if not already started
	if err := p.startInformer(); err != nil {
		p.logger.Error("Failed to start informer", zap.Error(err))
		return td, err
	}

	// Get InstrumentationConfigs from informer cache
	configs, err := p.getInstrumentationConfigsFromCache()
	if err != nil {
		p.logger.Error("Failed to get InstrumentationConfigs from cache", zap.Error(err))
		return td, err
	}

	p.logger.Info("Retrieved InstrumentationConfigs from cache", zap.Int("count", len(configs)))

	// Get workloads from cache
	workloads := p.getWorkloadsFromCache()
	p.logger.Info("Retrieved workloads from cache", zap.Int("count", len(workloads)))

	// TODO: Process traces using the InstrumentationConfigs and workloads
	// This is where the actual k8s attributes processing logic would go

	return td, nil
}

// getInstrumentationConfigs retrieves all InstrumentationConfigs from the cluster
func (p *k8sAttributesProcessor) getInstrumentationConfigs(ctx context.Context) (*odigosv1alpha1.InstrumentationConfigList, error) {
	return p.odigosClient.InstrumentationConfigs("").List(ctx, metav1.ListOptions{})
}

// startInformer starts the InstrumentationConfig informer
func (p *k8sAttributesProcessor) startInformer() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.icInformer != nil {
		return nil // Already started
	}

	p.informerStopCh = make(chan struct{})

	// Create informer for InstrumentationConfig objects
	p.icInformer = cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				return p.odigosClient.InstrumentationConfigs("").List(context.Background(), options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				return p.odigosClient.InstrumentationConfigs("").Watch(context.Background(), options)
			},
		},
		&odigosv1alpha1.InstrumentationConfig{},
		0, // No resync period
		cache.Indexers{},
	)

	// Add event handlers
	p.icInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			if ic, ok := obj.(*odigosv1alpha1.InstrumentationConfig); ok {
				p.logger.Info("InstrumentationConfig added",
					zap.String("name", ic.Name),
					zap.String("namespace", ic.Namespace),
				)
				// Extract workload info and add to cache
				p.handleInstrumentationConfigEvent(ic)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if ic, ok := newObj.(*odigosv1alpha1.InstrumentationConfig); ok {
				p.logger.Info("InstrumentationConfig updated",
					zap.String("name", ic.Name),
					zap.String("namespace", ic.Namespace),
				)
				// Extract workload info and update cache
				p.handleInstrumentationConfigEvent(ic)
			}
		},
		DeleteFunc: func(obj interface{}) {
			if ic, ok := obj.(*odigosv1alpha1.InstrumentationConfig); ok {
				p.logger.Info("InstrumentationConfig deleted",
					zap.String("name", ic.Name),
					zap.String("namespace", ic.Namespace),
				)
				// Extract workload info and remove from cache
				p.handleInstrumentationConfigDeletion(ic)
			}
		},
	})

	// Start the informer
	go p.icInformer.Run(p.informerStopCh)

	// Wait for cache to sync
	if !cache.WaitForCacheSync(p.informerStopCh, p.icInformer.HasSynced) {
		return fmt.Errorf("failed to sync InstrumentationConfig informer cache")
	}

	p.logger.Info("InstrumentationConfig informer started successfully")
	return nil
}

// stopInformer stops the InstrumentationConfig informer
func (p *k8sAttributesProcessor) stopInformer() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.icInformer == nil {
		return
	}

	close(p.informerStopCh)
	p.icInformer = nil
	p.logger.Info("InstrumentationConfig informer stopped")
}

// handleInstrumentationConfigEvent handles add/update events for InstrumentationConfig
func (p *k8sAttributesProcessor) handleInstrumentationConfigEvent(ic *odigosv1alpha1.InstrumentationConfig) {
	// Extract workload info from the InstrumentationConfig name
	workloadInfo, err := workload.ExtractWorkloadInfoFromRuntimeObjectName(ic.Name, ic.Namespace)
	if err != nil {
		p.logger.Error("Failed to extract workload info from InstrumentationConfig",
			zap.String("name", ic.Name),
			zap.String("namespace", ic.Namespace),
			zap.Error(err),
		)
		return
	}

	// Add workload to cache with empty Pod
	p.addWorkloadToCache(workloadInfo, &kube.Pod{})
}

// handleInstrumentationConfigDeletion handles delete events for InstrumentationConfig
func (p *k8sAttributesProcessor) handleInstrumentationConfigDeletion(ic *odigosv1alpha1.InstrumentationConfig) {
	// Extract workload info from the InstrumentationConfig name
	workloadInfo, err := workload.ExtractWorkloadInfoFromRuntimeObjectName(ic.Name, ic.Namespace)
	if err != nil {
		p.logger.Error("Failed to extract workload info from InstrumentationConfig",
			zap.String("name", ic.Name),
			zap.String("namespace", ic.Namespace),
			zap.Error(err),
		)
		return
	}

	// Remove workload from cache
	p.removeWorkloadFromCache(workloadInfo)
}

// getInstrumentationConfigsFromCache retrieves all InstrumentationConfig objects from the informer cache
func (p *k8sAttributesProcessor) getInstrumentationConfigsFromCache() ([]*odigosv1alpha1.InstrumentationConfig, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.icInformer == nil {
		return nil, fmt.Errorf("informer not started")
	}

	// Get all objects from the cache
	objects := p.icInformer.GetStore().List()
	configs := make([]*odigosv1alpha1.InstrumentationConfig, 0, len(objects))

	for _, obj := range objects {
		if ic, ok := obj.(*odigosv1alpha1.InstrumentationConfig); ok {
			configs = append(configs, ic)
		}
	}

	return configs, nil
}

// addWorkloadToCache adds a workload to the cache
func (p *k8sAttributesProcessor) addWorkloadToCache(workload k8sconsts.PodWorkload, pod *kube.Pod) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.workloadCache == nil {
		p.workloadCache = make(map[k8sconsts.PodWorkload]*kube.Pod)
	}

	p.workloadCache[workload] = pod
	p.logger.Debug("Added workload to cache",
		zap.String("name", workload.Name),
		zap.String("namespace", workload.Namespace),
		zap.String("kind", string(workload.Kind)),
	)
}

// removeWorkloadFromCache removes a workload from the cache
func (p *k8sAttributesProcessor) removeWorkloadFromCache(workload k8sconsts.PodWorkload) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.workloadCache == nil {
		return
	}

	delete(p.workloadCache, workload)
	p.logger.Debug("Removed workload from cache",
		zap.String("name", workload.Name),
		zap.String("namespace", workload.Namespace),
		zap.String("kind", string(workload.Kind)),
	)
}

// getWorkloadsFromCache returns all workloads from the cache
func (p *k8sAttributesProcessor) getWorkloadsFromCache() []k8sconsts.PodWorkload {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.workloadCache == nil {
		return nil
	}

	workloads := make([]k8sconsts.PodWorkload, 0, len(p.workloadCache))
	for workload := range p.workloadCache {
		workloads = append(workloads, workload)
	}

	return workloads
}

// getPodFromCache returns a specific pod from the cache by workload
func (p *k8sAttributesProcessor) getPodFromCache(workload k8sconsts.PodWorkload) *kube.Pod {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.workloadCache == nil {
		return nil
	}

	return p.workloadCache[workload]
}
