package graph

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/central-backend/graph/model"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

// parseClusterSnapshot converts Redis snapshot data to GraphQL model
func parseClusterSnapshot(snapshot *redisclient.ClusterSnapshotData) (*model.ClusterSnapshot, error) {
	var parsed struct {
		ClusterID   string    `json:"clusterId"`
		ClusterName string    `json:"clusterName,omitempty"`
		Timestamp   time.Time `json:"timestamp"`
		Namespaces  []struct {
			Name      string `json:"name"`
			Selected  bool   `json:"selected"`
			Workloads []struct {
				Namespace   string `json:"namespace"`
				Name        string `json:"name"`
				Kind        string `json:"kind"`
				Selected    bool   `json:"selected"`
				ServiceName string `json:"serviceName,omitempty"`
				Health      string `json:"health"` // DesiredStateProgress enum as string
			} `json:"workloads"`
		} `json:"namespaces"`
	}

	if err := json.Unmarshal(snapshot.Data, &parsed); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot data: %w", err)
	}

	var timestamp int64
	if !parsed.Timestamp.IsZero() {
		timestamp = parsed.Timestamp.Unix()
	} else {
		timestamp = snapshot.Timestamp
	}

	var namespaces []*model.NamespaceSnapshot
	for _, ns := range parsed.Namespaces {
		var workloads []*model.WorkloadSnapshot
		for _, wl := range ns.Workloads {
			var otelServiceName *string
			if wl.ServiceName != "" {
				otelServiceName = &wl.ServiceName
			}

			workloads = append(workloads, &model.WorkloadSnapshot{
				Namespace:       wl.Namespace,
				Name:            wl.Name,
				Kind:            wl.Kind,
				Selected:        wl.Selected,
				OtelServiceName: otelServiceName,
				HealthStatus:    &wl.Health,
			})
		}

		nsSelected := ns.Selected
		namespaces = append(namespaces, &model.NamespaceSnapshot{
			Name:      ns.Name,
			Selected:  &nsSelected,
			Workloads: workloads,
		})
	}

	clusterName := parsed.ClusterName
	if clusterName == "" {
		clusterName = snapshot.ClusterName
	}

	return &model.ClusterSnapshot{
		ClusterID:   snapshot.ClusterID,
		ClusterName: clusterName,
		Timestamp:   int(timestamp),
		Namespaces:  namespaces,
	}, nil
}
