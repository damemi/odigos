package snapshot

import (
	"time"

	"github.com/odigos-io/odigos/central-proxy/kube"
	"github.com/odigos-io/odigos/api/k8sconsts"
)

// Generator collects workload data from the cached K8s resources
type Generator struct {
	clusterID         string
	clusterName       string
	cache             *kube.Cache
	ignoredNamespaces map[string]bool
}

// NewGenerator creates a new snapshot generator
func NewGenerator(clusterID, clusterName string, cache *kube.Cache, ignoredNamespaces []string) *Generator {
	ignoredMap := make(map[string]bool, len(ignoredNamespaces))
	for _, ns := range ignoredNamespaces {
		ignoredMap[ns] = true
	}

	return &Generator{
		clusterID:         clusterID,
		clusterName:       clusterName,
		cache:             cache,
		ignoredNamespaces: ignoredMap,
	}
}

// isIgnoredNamespace returns true if the namespace should be excluded from snapshots
func (g *Generator) isIgnoredNamespace(namespace string) bool {
	return g.ignoredNamespaces[namespace]
}

// GenerateSnapshot collects all workloads from the cache
func (g *Generator) GenerateSnapshot() (*ClusterSnapshot, error) {
	// Get all namespaces from cache
	namespaces, err := g.cache.ListNamespaces()
	if err != nil {
		return nil, err
	}

	// Workloads excluded by their own disabled Source
	disabledWorkloads, err := g.cache.ListDisabledWorkloads()
	if err != nil {
		return nil, err
	}

	// Build a map of namespace -> workloads
	nsWorkloads := make(map[string][]WorkloadSnapshot)
	addWorkload := func(namespace, name, kind string) {
		info := g.cache.GetWorkloadInstrumentationInfo(namespace, name, kind)
		snapshot := workloadSnapshotFromInfo(namespace, name, kind, info)
		snapshot.Disabled = disabledWorkloads[k8sconsts.PodWorkload{Namespace: namespace, Name: name, Kind: k8sconsts.WorkloadKind(kind)}]
		nsWorkloads[namespace] = append(nsWorkloads[namespace], snapshot)
	}

	// Collect all deployments
	deployments, err := g.cache.ListAllDeployments()
	if err != nil {
		return nil, err
	}
	for _, dep := range deployments {
		if !g.isIgnoredNamespace(dep.Namespace) {
			addWorkload(dep.Namespace, dep.Name, "Deployment")
		}
	}

	// Collect all statefulsets
	statefulsets, err := g.cache.ListAllStatefulSets()
	if err != nil {
		return nil, err
	}
	for _, sts := range statefulsets {
		if !g.isIgnoredNamespace(sts.Namespace) {
			addWorkload(sts.Namespace, sts.Name, "StatefulSet")
		}
	}

	// Collect all daemonsets
	daemonsets, err := g.cache.ListAllDaemonSets()
	if err != nil {
		return nil, err
	}
	for _, ds := range daemonsets {
		if !g.isIgnoredNamespace(ds.Namespace) {
			addWorkload(ds.Namespace, ds.Name, "DaemonSet")
		}
	}

	// Collect all cronjobs
	cronjobs, err := g.cache.ListAllCronJobs()
	if err != nil {
		return nil, err
	}
	for _, cj := range cronjobs {
		if !g.isIgnoredNamespace(cj.Namespace) {
			addWorkload(cj.Namespace, cj.Name, "CronJob")
		}
	}

	// Collect all OpenShift DeploymentConfigs
	deploymentConfigs, err := g.cache.ListAllDeploymentConfigs()
	if err != nil {
		return nil, err
	}
	for _, dc := range deploymentConfigs {
		if !g.isIgnoredNamespace(dc.GetNamespace()) {
			addWorkload(dc.GetNamespace(), dc.GetName(), "DeploymentConfig")
		}
	}

	// Build namespace snapshots
	var nsSnapshots []NamespaceSnapshot

	for _, ns := range namespaces {
		nsName := ns.Name

		// Skip ignored namespaces
		if g.isIgnoredNamespace(nsName) {
			continue
		}

		workloads := nsWorkloads[nsName]
		if workloads == nil {
			workloads = []WorkloadSnapshot{}
		}
		nsSnapshots = append(nsSnapshots, NamespaceSnapshot{
			Name:      nsName,
			Selected:  g.cache.IsNamespaceAutoInstrumented(nsName),
			Workloads: workloads,
		})
	}

	snapshot := &ClusterSnapshot{
		ClusterID:   g.clusterID,
		ClusterName: g.clusterName,
		Timestamp:   time.Now().UTC(),
		Namespaces:  nsSnapshots,
	}

	return snapshot, nil
}

func workloadSnapshotFromInfo(namespace, name, kind string, info kube.WorkloadInstrumentationInfo) WorkloadSnapshot {
	return WorkloadSnapshot{
		Namespace:            namespace,
		Name:                 name,
		Kind:                 kind,
		Selected:             info.Selected,
		ServiceName:          info.ServiceName,
		Health:               info.Health,
		DataStreamNames:      info.DataStreamNames,
		DetectedLanguages:    info.DetectedLanguages,
		AgentInjectionStatus: info.AgentInjectionStatus,
	}
}
