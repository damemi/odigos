package kube

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/odigos-io/central-proxy/internal/logger"
	odigosinformers "github.com/odigos-io/odigos/api/generated/odigos/informers/externalversions"
	odigosv1alpha1informer "github.com/odigos-io/odigos/api/generated/odigos/informers/externalversions/odigos/v1alpha1"
	odigoslisters "github.com/odigos-io/odigos/api/generated/odigos/listers/odigos/v1alpha1"
	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/frontend/graph/model"
	"github.com/odigos-io/odigos/frontend/graph/status"
	"github.com/odigos-io/odigos/k8sutils/pkg/workload"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	appsinformers "k8s.io/client-go/informers/apps/v1"
	batchinformers "k8s.io/client-go/informers/batch/v1"
	coreinformers "k8s.io/client-go/informers/core/v1"
	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const (
	// DefaultResyncPeriod is how often the informers will resync with the API server
	DefaultResyncPeriod = 30 * time.Second
)

// Cache provides cached access to Kubernetes resources using informers
type Cache struct {
	factory        informers.SharedInformerFactory
	odigosFactory  odigosinformers.SharedInformerFactory
	dynamicFactory dynamicinformer.DynamicSharedInformerFactory

	// Informers
	namespaceInformer             coreinformers.NamespaceInformer
	deploymentInformer            appsinformers.DeploymentInformer
	statefulsetInformer           appsinformers.StatefulSetInformer
	daemonsetInformer             appsinformers.DaemonSetInformer
	cronjobInformer               batchinformers.CronJobInformer
	deploymentConfigInformer      cache.SharedIndexInformer
	instrumentationConfigInformer odigosv1alpha1informer.InstrumentationConfigInformer
	sourceInformer                odigosv1alpha1informer.SourceInformer

	// Listers (read from cache)
	namespaceLister             corelisters.NamespaceLister
	deploymentLister            appslisters.DeploymentLister
	statefulsetLister           appslisters.StatefulSetLister
	daemonsetLister             appslisters.DaemonSetLister
	cronjobLister               batchlisters.CronJobLister
	deploymentConfigLister      cache.GenericLister
	instrumentationConfigLister odigoslisters.InstrumentationConfigLister
	sourceLister                odigoslisters.SourceLister

	// Synced functions to check if caches are ready
	hasSynced []cache.InformerSynced

	// dirty tracks whether there have been changes since the last snapshot
	dirty atomic.Bool

	// dirtyNotify is signaled (non-blocking, coalesced) whenever markDirty runs so
	// the snapshot runner can flush promptly. Only a wake-up bit — never queues
	// per-object events (avoids unbounded batcher memory under informer churn).
	dirtyNotify chan struct{}
}

// NewCache creates a new Cache with informers for all watched resources
func NewCache(ctx context.Context, client *Client) *Cache {
	factory := informers.NewSharedInformerFactory(client.Interface, DefaultResyncPeriod)
	odigosFactory := odigosinformers.NewSharedInformerFactory(client.OdigosClient, DefaultResyncPeriod)

	namespaceInformer := factory.Core().V1().Namespaces()
	deploymentInformer := factory.Apps().V1().Deployments()
	statefulsetInformer := factory.Apps().V1().StatefulSets()
	daemonsetInformer := factory.Apps().V1().DaemonSets()
	cronjobInformer := factory.Batch().V1().CronJobs()
	instrumentationConfigInformer := odigosFactory.Odigos().V1alpha1().InstrumentationConfigs()
	sourceInformer := odigosFactory.Odigos().V1alpha1().Sources()

	c := &Cache{
		factory:       factory,
		odigosFactory: odigosFactory,
		dirtyNotify:   make(chan struct{}, 1),

		namespaceInformer:             namespaceInformer,
		deploymentInformer:            deploymentInformer,
		statefulsetInformer:           statefulsetInformer,
		daemonsetInformer:             daemonsetInformer,
		cronjobInformer:               cronjobInformer,
		instrumentationConfigInformer: instrumentationConfigInformer,
		sourceInformer:                sourceInformer,

		namespaceLister:             namespaceInformer.Lister(),
		deploymentLister:            deploymentInformer.Lister(),
		statefulsetLister:           statefulsetInformer.Lister(),
		daemonsetLister:             daemonsetInformer.Lister(),
		cronjobLister:               cronjobInformer.Lister(),
		instrumentationConfigLister: instrumentationConfigInformer.Lister(),
		sourceLister:                sourceInformer.Lister(),

		hasSynced: []cache.InformerSynced{
			namespaceInformer.Informer().HasSynced,
			deploymentInformer.Informer().HasSynced,
			statefulsetInformer.Informer().HasSynced,
			daemonsetInformer.Informer().HasSynced,
			cronjobInformer.Informer().HasSynced,
			instrumentationConfigInformer.Informer().HasSynced,
			sourceInformer.Informer().HasSynced,
		},
	}

	if client.IsDeploymentConfigAvailable(ctx) {
		c.dynamicFactory = dynamicinformer.NewDynamicSharedInformerFactory(client.DynamicClient, DefaultResyncPeriod)
		deploymentConfigInformer := c.dynamicFactory.ForResource(deploymentConfigGVR)
		c.deploymentConfigInformer = deploymentConfigInformer.Informer()
		c.deploymentConfigLister = deploymentConfigInformer.Lister()
		c.hasSynced = append(c.hasSynced, c.deploymentConfigInformer.HasSynced)
		logger.Log.Info("DeploymentConfig snapshots enabled")
	}

	// Slim cached objects before they enter the store. On large clusters the default
	// full-object cache exceeds the central-proxy memory limit (see PLAT-1302).
	c.setInformerTransforms()

	// Register event handlers
	c.registerEventHandlers()

	return c
}

func (c *Cache) setInformerTransforms() {
	_ = c.namespaceInformer.Informer().SetTransform(namespacesTransformFunc)
	_ = c.deploymentInformer.Informer().SetTransform(deploymentsTransformFunc)
	_ = c.statefulsetInformer.Informer().SetTransform(statefulsetsTransformFunc)
	_ = c.daemonsetInformer.Informer().SetTransform(daemonsetsTransformFunc)
	_ = c.cronjobInformer.Informer().SetTransform(cronjobsTransformFunc)
	if c.deploymentConfigInformer != nil {
		_ = c.deploymentConfigInformer.SetTransform(deploymentConfigsTransformFunc)
	}
	_ = c.instrumentationConfigInformer.Informer().SetTransform(instrumentationConfigsTransformFunc)
	_ = c.sourceInformer.Informer().SetTransform(sourcesTransformFunc)
}

// registerEventHandlers adds event handlers to all informers to track changes
func (c *Cache) registerEventHandlers() {
	handler := cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			c.markDirty()
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			// Only mark dirty if the resource actually changed.
			// Informer resyncs trigger UpdateFunc but with same ResourceVersion.
			oldMeta, err1 := meta.Accessor(oldObj)
			newMeta, err2 := meta.Accessor(newObj)
			if err1 != nil || err2 != nil {
				c.markDirty() // Fallback: mark dirty if we can't compare
				return
			}
			if oldMeta.GetResourceVersion() != newMeta.GetResourceVersion() {
				c.markDirty()
			}
		},
		DeleteFunc: func(obj interface{}) {
			c.markDirty()
		},
	}

	c.namespaceInformer.Informer().AddEventHandler(handler)
	c.deploymentInformer.Informer().AddEventHandler(handler)
	c.statefulsetInformer.Informer().AddEventHandler(handler)
	c.daemonsetInformer.Informer().AddEventHandler(handler)
	c.cronjobInformer.Informer().AddEventHandler(handler)
	if c.deploymentConfigInformer != nil {
		c.deploymentConfigInformer.AddEventHandler(handler)
	}
	c.instrumentationConfigInformer.Informer().AddEventHandler(handler)
	c.sourceInformer.Informer().AddEventHandler(handler)
}

// markDirty sets the dirty flag indicating changes have occurred
func (c *Cache) markDirty() {
	c.dirty.Store(true)
	// Coalesce bursts of informer events into a single wake-up.
	select {
	case c.dirtyNotify <- struct{}{}:
	default:
	}
}

// DirtyNotify returns a channel that receives when the cache is marked dirty.
// Receives are coalesced (buffer size 1); callers should still check HasChanges.
func (c *Cache) DirtyNotify() <-chan struct{} {
	return c.dirtyNotify
}

// HasChanges returns true if there have been changes since the last call to ResetChanges
func (c *Cache) HasChanges() bool {
	return c.dirty.Load()
}

// ResetChanges clears the dirty flag
func (c *Cache) ResetChanges() {
	c.dirty.Store(false)
}

// Start begins the informers and waits for them to sync
func (c *Cache) Start(ctx context.Context) error {
	logger.Log.Info("Starting informer cache")

	// Start all informers
	c.factory.Start(ctx.Done())
	c.odigosFactory.Start(ctx.Done())
	if c.dynamicFactory != nil {
		c.dynamicFactory.Start(ctx.Done())
	}

	// Wait for all caches to sync
	logger.Log.Info("Waiting for informer caches to sync")
	if !cache.WaitForCacheSync(ctx.Done(), c.hasSynced...) {
		return ctx.Err()
	}

	logger.Log.Info("Informer caches synced successfully")

	// Mark as dirty after initial sync so we send the first snapshot
	c.dirty.Store(true)

	return nil
}

// IsNamespaceAutoInstrumented checks if a namespace has a Source CRD with kind=Namespace,
// indicating that the namespace is configured for automatic instrumentation of future workloads.
func (c *Cache) IsNamespaceAutoInstrumented(namespace string) bool {
	sources, err := c.sourceLister.Sources(namespace).List(labels.Everything())
	if err != nil {
		logger.Log.Error("Error listing Source CRDs", "error", err, "namespace", namespace)
		return false
	}

	for _, source := range sources {
		if source.Spec.Workload.Kind == k8sconsts.WorkloadKindNamespace &&
			source.Spec.Workload.Name == namespace {
			return true
		}
	}

	return false
}

// WorkloadInstrumentationInfo contains instrumentation details for a workload
type WorkloadInstrumentationInfo struct {
	Selected             bool
	ServiceName          string
	Health               model.DesiredStateProgress
	DataStreamNames      []string
	DetectedLanguages    []string
	AgentInjectionStatus model.DesiredStateProgress
}

// GetWorkloadInstrumentationInfo returns instrumentation info for a workload
func (c *Cache) GetWorkloadInstrumentationInfo(namespace, name, kind string) WorkloadInstrumentationInfo {
	// Calculate the InstrumentationConfig name using the workload helper
	icName := workload.CalculateWorkloadRuntimeObjectName(name, kind)

	// Try to get the InstrumentationConfig from cache
	ic, err := c.instrumentationConfigLister.InstrumentationConfigs(namespace).Get(icName)
	if err != nil {
		if !errors.IsNotFound(err) {
			// Log unexpected errors
			logger.Log.Error("Error checking InstrumentationConfig", "error", err, "namespace", namespace, "name", icName)
		}
		return WorkloadInstrumentationInfo{
			Selected:             false,
			Health:               model.DesiredStateProgressDisabled,
			AgentInjectionStatus: model.DesiredStateProgressDisabled,
		}
	}

	// Calculate health using odigos frontend status functions
	agentInjection := status.CalculateAgentInjectionEnabledStatus(ic)
	conditions := []*model.DesiredConditionStatus{
		status.CalculateRuntimeInspectionStatus(ic),
		agentInjection,
		status.CalculateRolloutStatus(ic),
	}

	// Get the most severe condition
	health := aggregateConditionsBySeverity(conditions)

	agentStatus := model.DesiredStateProgressUnknown
	if agentInjection != nil {
		agentStatus = agentInjection.Status
	}

	return WorkloadInstrumentationInfo{
		Selected:             true,
		ServiceName:          ic.Spec.ServiceName,
		Health:               health,
		DataStreamNames:      dataStreamNamesFromIC(ic),
		DetectedLanguages:    detectedLanguagesFromIC(ic),
		AgentInjectionStatus: agentStatus,
	}
}

// aggregateConditionsBySeverity returns the most severe status from a list of conditions
func aggregateConditionsBySeverity(conditions []*model.DesiredConditionStatus) model.DesiredStateProgress {
	var mostSevere model.DesiredStateProgress = model.DesiredStateProgressUnknown
	mostSevereSeverity := desiredStateProgressSeverity(mostSevere)

	for _, condition := range conditions {
		if condition == nil {
			continue
		}
		severity := desiredStateProgressSeverity(condition.Status)
		if severity < mostSevereSeverity {
			mostSevere = condition.Status
			mostSevereSeverity = severity
		}
	}

	return mostSevere
}

// desiredStateProgressSeverity returns severity score (lower = more severe)
// Matches the ordering from odigos frontend/graph/utils.go
func desiredStateProgressSeverity(status model.DesiredStateProgress) int {
	switch status {
	case model.DesiredStateProgressError:
		return 0
	case model.DesiredStateProgressFailure:
		return 10
	case model.DesiredStateProgressNotice:
		return 20
	case model.DesiredStateProgressPending:
		return 30
	case model.DesiredStateProgressWaiting:
		return 40
	case model.DesiredStateProgressUnsupported:
		return 50
	case model.DesiredStateProgressDisabled:
		return 60
	case model.DesiredStateProgressSuccess:
		return 70
	case model.DesiredStateProgressIrrelevant:
		return 80
	case model.DesiredStateProgressUnknown:
		return 90
	}
	return 1000
}

// ListNamespaces returns all namespaces from cache
func (c *Cache) ListNamespaces() ([]*corev1.Namespace, error) {
	return c.namespaceLister.List(labels.Everything())
}

// ListAllDeployments returns all deployments across all namespaces from cache
func (c *Cache) ListAllDeployments() ([]*appsv1.Deployment, error) {
	return c.deploymentLister.List(labels.Everything())
}

// ListAllStatefulSets returns all statefulsets across all namespaces from cache
func (c *Cache) ListAllStatefulSets() ([]*appsv1.StatefulSet, error) {
	return c.statefulsetLister.List(labels.Everything())
}

// ListAllDaemonSets returns all daemonsets across all namespaces from cache
func (c *Cache) ListAllDaemonSets() ([]*appsv1.DaemonSet, error) {
	return c.daemonsetLister.List(labels.Everything())
}

// ListAllCronJobs returns all cronjobs across all namespaces from cache
func (c *Cache) ListAllCronJobs() ([]*batchv1.CronJob, error) {
	return c.cronjobLister.List(labels.Everything())
}

// ListAllDeploymentConfigs returns all DeploymentConfigs when the OpenShift API is available.
func (c *Cache) ListAllDeploymentConfigs() ([]*unstructured.Unstructured, error) {
	if c.deploymentConfigLister == nil {
		return nil, nil
	}

	objects, err := c.deploymentConfigLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	deploymentConfigs := make([]*unstructured.Unstructured, 0, len(objects))
	for _, object := range objects {
		deploymentConfig, ok := object.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("expected a DeploymentConfig, got %T", object)
		}
		deploymentConfigs = append(deploymentConfigs, deploymentConfig)
	}
	return deploymentConfigs, nil
}

func dataStreamNamesFromIC(ic *odigosv1.InstrumentationConfig) []string {
	if ic == nil || len(ic.Labels) == 0 {
		return nil
	}
	names := make([]string, 0, 1)
	for k, v := range ic.Labels {
		if !strings.HasPrefix(k, k8sconsts.SourceDataStreamLabelPrefix) || v == "false" {
			continue
		}
		names = append(names, strings.TrimPrefix(k, k8sconsts.SourceDataStreamLabelPrefix))
	}
	return names
}

func detectedLanguagesFromIC(ic *odigosv1.InstrumentationConfig) []string {
	if ic == nil || len(ic.Status.RuntimeDetailsByContainer) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(ic.Status.RuntimeDetailsByContainer))
	langs := make([]string, 0, len(ic.Status.RuntimeDetailsByContainer))
	for _, d := range ic.Status.RuntimeDetailsByContainer {
		lang := string(d.Language)
		if lang == "" {
			continue
		}
		if _, ok := seen[lang]; ok {
			continue
		}
		seen[lang] = struct{}{}
		langs = append(langs, lang)
	}
	return langs
}
