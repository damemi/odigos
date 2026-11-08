package snapshot

import (
	"time"

	"github.com/odigos-io/odigos/frontend/graph/model"
)

// WorkloadSnapshot represents a minimal K8s workload for central overview lists.
// Kept compact so large clusters stay well under WebSocket message limits.
type WorkloadSnapshot struct {
	Namespace            string                     `json:"namespace"`
	Name                 string                     `json:"name"`
	Kind                 string                     `json:"kind"`
	Selected             bool                       `json:"selected"`
	Disabled             bool                       `json:"disabled,omitempty"`
	ServiceName          string                     `json:"serviceName,omitempty"`
	Health               model.DesiredStateProgress `json:"health"`
	DataStreamNames      []string                   `json:"dataStreamNames,omitempty"`
	DetectedLanguages    []string                   `json:"detectedLanguages,omitempty"`
	AgentInjectionStatus model.DesiredStateProgress `json:"agentInjectionStatus,omitempty"`
}

// NamespaceSnapshot represents a namespace with its workloads
type NamespaceSnapshot struct {
	Name      string             `json:"name"`
	Selected  bool               `json:"selected"`
	Workloads []WorkloadSnapshot `json:"workloads"`
}

// ClusterSnapshot represents the complete cluster state
type ClusterSnapshot struct {
	ClusterID   string              `json:"clusterId"`
	ClusterName string              `json:"clusterName,omitempty"`
	Timestamp   time.Time           `json:"timestamp"`
	Namespaces  []NamespaceSnapshot `json:"namespaces"`
}
