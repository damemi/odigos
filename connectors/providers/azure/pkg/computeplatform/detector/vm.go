package detector

import (
	"context"
	"fmt"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/providers/azure/pkg/azureclient"
	store "github.com/odigos-io/odigos/connectors/providers/azure/pkg/computeplatform/store"
)

// VMDetector discovers Azure Virtual Machines across the subscription via list-all.
type VMDetector struct {
	store   *store.Store
	clients *azureclient.Clients
}

// NewVMDetector builds the VM detector over the compute-platform store and shared client factory.
func NewVMDetector(st *store.Store, clients *azureclient.Clients) *VMDetector {
	return &VMDetector{store: st, clients: clients}
}

func (d *VMDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	// The prune cutoff comes from the database clock, the one last_seen_at is written with.
	scanStart, err := d.store.Now(ctx)
	if err != nil {
		return baseapi.DetectResult{}, fmt.Errorf("read database clock: %w", err)
	}
	log.Info("vm detector starting", "subscription", d.clients.SubscriptionID())

	client, err := d.clients.VirtualMachines()
	if err != nil {
		return baseapi.DetectResult{}, err
	}

	pager := client.NewListAllPager(nil)
	upsertFailed := false
	count := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Error("vm list-all failed", "error", err)
			return baseapi.DetectResult{}, err
		}
		for _, vm := range page.Value {
			if vm == nil {
				continue
			}
			resourceID := ""
			if vm.ID != nil {
				resourceID = *vm.ID
			}
			name := ""
			if vm.Name != nil {
				name = *vm.Name
			}
			location := ""
			if vm.Location != nil {
				location = *vm.Location
			}
			vmID := ""
			status := ""
			meta := map[string]any{}
			if vm.Properties != nil {
				if vm.Properties.VMID != nil {
					vmID = *vm.Properties.VMID
				}
				if vm.Properties.ProvisioningState != nil {
					status = *vm.Properties.ProvisioningState
				}
				if vm.Properties.HardwareProfile != nil && vm.Properties.HardwareProfile.VMSize != nil {
					meta["vmSize"] = string(*vm.Properties.HardwareProfile.VMSize)
				}
			}
			rec := store.VirtualMachine{
				Location:      location,
				ResourceGroup: azureclient.ResourceGroupFromID(resourceID),
				ResourceID:    resourceID,
				VMName:        name,
				VMID:          vmID,
				Status:        status,
				Metadata:      meta,
			}
			if err := d.store.UpsertVirtualMachine(ctx, rec); err != nil {
				log.Error("upsert virtual machine failed", "vm", rec.VMName, "error", err)
				upsertFailed = true
				continue
			}
			count++
		}
	}

	_ = d.store.UpdateDiscoveryState(ctx, azureclient.LocationWildcard, store.KindVM, scanStart)
	if upsertFailed {
		log.Error("skipping vm prune after upsert failure")
	} else if err := d.store.PruneVM(ctx, scanStart); err != nil {
		log.Error("prune vm inventory failed", "error", err)
	}

	log.Info("vm detector complete", "vms", count)
	return baseapi.DetectResult{Authenticated: true}, nil
}
