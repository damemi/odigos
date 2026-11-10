// Package detector implements COMPUTE-PLATFORM discovery for the GCP connector — managed infra Odigos is
// installed on (GKE clusters, Compute Engine instances). Each detector enumerates one type across the
// whole project (locations/zones wildcard) and persists the compute-platform store inventory the base
// reports as ComputePlatformReports. Workload (Cloud Run) discovery lives in the sibling
// workload/detector package.
package detector

import (
	"context"
	"fmt"

	containerpb "cloud.google.com/go/container/apiv1/containerpb"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	store "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/computeplatform/store"
	"github.com/odigos-io/odigos/connectors/providers/gcp/pkg/gcpclient"
)

var log = connlog.Subsystem("gcp-computeplatform-detector")

// GKEClusterDetector discovers GKE clusters across every location in the project.
type GKEClusterDetector struct {
	store   *store.Store
	clients *gcpclient.Clients
}

// NewGKEClusterDetector builds the GKE detector over the compute-platform store and shared client factory.
func NewGKEClusterDetector(st *store.Store, clients *gcpclient.Clients) *GKEClusterDetector {
	return &GKEClusterDetector{store: st, clients: clients}
}

func (d *GKEClusterDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	// The prune cutoff comes from the database clock, the one last_seen_at is written with.
	scanStart, err := d.store.Now(ctx)
	if err != nil {
		return baseapi.DetectResult{}, fmt.Errorf("read database clock: %w", err)
	}
	log.Info("gke cluster detector starting", "project", d.clients.ProjectID())

	client, err := d.clients.GKEClusters(ctx)
	if err != nil {
		return baseapi.DetectResult{}, err
	}
	defer client.Close()

	resp, err := client.ListClusters(ctx, &containerpb.ListClustersRequest{Parent: d.clients.GKEParent()})
	if err != nil {
		log.Error("gke list clusters failed", "error", err)
		return baseapi.DetectResult{}, err
	}

	upsertFailed := false
	for _, c := range resp.GetClusters() {
		rec := store.GKECluster{
			Location:     c.GetLocation(),
			ResourceName: fmt.Sprintf("projects/%s/locations/%s/clusters/%s", d.clients.ProjectID(), c.GetLocation(), c.GetName()),
			ClusterName:  c.GetName(),
			Status:       c.GetStatus().String(),
			Metadata: map[string]any{
				"endpoint":             c.GetEndpoint(),
				"currentMasterVersion": c.GetCurrentMasterVersion(),
				"currentNodeVersion":   c.GetCurrentNodeVersion(),
				"autopilot":            c.GetAutopilot().GetEnabled(),
			},
		}
		if err := d.store.UpsertGKECluster(ctx, rec); err != nil {
			log.Error("upsert gke cluster failed", "cluster", rec.ClusterName, "error", err)
			upsertFailed = true
		}
	}

	_ = d.store.UpdateDiscoveryState(ctx, gcpclient.LocationWildcard, store.KindGKE, scanStart)
	if upsertFailed {
		log.Error("skipping gke prune after upsert failure")
	} else if err := d.store.PruneGKE(ctx, scanStart); err != nil {
		log.Error("prune gke inventory failed", "error", err)
	}

	log.Info("gke cluster detector complete", "clusters", len(resp.GetClusters()))
	return baseapi.DetectResult{Authenticated: true}, nil
}
