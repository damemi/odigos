package snapshot

import (
	"time"

	"github.com/odigos-io/central-proxy/kube"
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

	// Build a map of namespace -> workloads
	nsWorkloads := make(map[string][]WorkloadSnapshot)

	// Collect all deployments
	deployments, err := g.cache.ListAllDeployments()
	if err != nil {
		return nil, err
	}
	for _, dep := range deployments {
		if !g.isIgnoredNamespace(dep.Namespace) {
			info := g.cache.GetWorkloadInstrumentationInfo(dep.Namespace, dep.Name, "Deployment")
			nsWorkloads[dep.Namespace] = append(nsWorkloads[dep.Namespace], workloadSnapshotFromInfo(dep.Namespace, dep.Name, "Deployment", info))
		}
	}

	// Collect all statefulsets
	statefulsets, err := g.cache.ListAllStatefulSets()
	if err != nil {
		return nil, err
	}
	for _, sts := range statefulsets {
		if !g.isIgnoredNamespace(sts.Namespace) {
			info := g.cache.GetWorkloadInstrumentationInfo(sts.Namespace, sts.Name, "StatefulSet")
			nsWorkloads[sts.Namespace] = append(nsWorkloads[sts.Namespace], workloadSnapshotFromInfo(sts.Namespace, sts.Name, "StatefulSet", info))
		}
	}

	// Collect all daemonsets
	daemonsets, err := g.cache.ListAllDaemonSets()
	if err != nil {
		return nil, err
	}
	for _, ds := range daemonsets {
		if !g.isIgnoredNamespace(ds.Namespace) {
			info := g.cache.GetWorkloadInstrumentationInfo(ds.Namespace, ds.Name, "DaemonSet")
			nsWorkloads[ds.Namespace] = append(nsWorkloads[ds.Namespace], workloadSnapshotFromInfo(ds.Namespace, ds.Name, "DaemonSet", info))
		}
	}

	// Collect all cronjobs
	cronjobs, err := g.cache.ListAllCronJobs()
	if err != nil {
		return nil, err
	}
	for _, cj := range cronjobs {
		if !g.isIgnoredNamespace(cj.Namespace) {
			info := g.cache.GetWorkloadInstrumentationInfo(cj.Namespace, cj.Name, "CronJob")
			nsWorkloads[cj.Namespace] = append(nsWorkloads[cj.Namespace], workloadSnapshotFromInfo(cj.Namespace, cj.Name, "CronJob", info))
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
