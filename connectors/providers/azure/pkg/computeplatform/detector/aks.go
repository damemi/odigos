// Package detector implements COMPUTE-PLATFORM discovery for the Azure connector — managed infra
// Odigos is installed on (AKS clusters, Virtual Machines). Each detector enumerates one type across the
// whole subscription and persists the compute-platform store inventory the base reports as
// ComputePlatformReports. Workload (Container Apps) discovery lives in the sibling workload/detector
// package.
package detector

import (
	"context"
	"time"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	"github.com/odigos-io/odigos/connectors/providers/azure/pkg/azureclient"
	store "github.com/odigos-io/odigos/connectors/providers/azure/pkg/computeplatform/store"
)

var log = connlog.Subsystem("azure-computeplatform-detector")

// AKSClusterDetector discovers AKS clusters across the subscription.
type AKSClusterDetector struct {
	store   *store.Store
	clients *azureclient.Clients
}

// NewAKSClusterDetector builds the AKS detector over the compute-platform store and shared client factory.
func NewAKSClusterDetector(st *store.Store, clients *azureclient.Clients) *AKSClusterDetector {
	return &AKSClusterDetector{store: st, clients: clients}
}

func (d *AKSClusterDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	scanStart := time.Now()
	log.Info("aks cluster detector starting", "subscription", d.clients.SubscriptionID())

	client, err := d.clients.AKSClusters()
	if err != nil {
		return baseapi.DetectResult{}, err
	}

	pager := client.NewListPager(nil)
	upsertFailed := false
	count := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Error("aks list clusters failed", "error", err)
			return baseapi.DetectResult{}, err
		}
		for _, c := range page.Value {
			if c == nil {
				continue
			}
			resourceID := ""
			if c.ID != nil {
				resourceID = *c.ID
			}
			name := ""
			if c.Name != nil {
				name = *c.Name
			}
			location := ""
			if c.Location != nil {
				location = *c.Location
			}
			status := ""
			meta := map[string]any{}
			if c.Properties != nil {
				if c.Properties.ProvisioningState != nil {
					status = *c.Properties.ProvisioningState
				}
				if c.Properties.KubernetesVersion != nil {
					meta["kubernetesVersion"] = *c.Properties.KubernetesVersion
				}
				if c.Properties.Fqdn != nil {
					meta["fqdn"] = *c.Properties.Fqdn
				}
			}
			rec := store.AKSCluster{
				Location:      location,
				ResourceGroup: azureclient.ResourceGroupFromID(resourceID),
				ResourceID:    resourceID,
				ClusterName:   name,
				Status:        status,
				Metadata:      meta,
			}
			if err := d.store.UpsertAKSCluster(ctx, rec); err != nil {
				log.Error("upsert aks cluster failed", "cluster", rec.ClusterName, "error", err)
				upsertFailed = true
				continue
			}
			count++
		}
	}

	_ = d.store.UpdateDiscoveryState(ctx, azureclient.LocationWildcard, store.KindAKS, scanStart)
	if upsertFailed {
		log.Error("skipping aks prune after upsert failure")
	} else if err := d.store.PruneAKS(ctx, scanStart); err != nil {
		log.Error("prune aks inventory failed", "error", err)
	}

	log.Info("aks cluster detector complete", "clusters", count)
	return baseapi.DetectResult{Authenticated: true}, nil
}
