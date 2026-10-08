package gqlapi

import (
	"context"
	"fmt"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	centralapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectordestination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var collectorDestinationAPILog = connlog.Subsystem("gql-collector-destination")

// ManagedCollectorDestinationAPI maps the destinations a managed Collector owns to the coordinator.
//
// Not DestinationAPI: that one serves the workload destinations, which say where workloads send. These
// say where one region's Collector sends what it receives, and are addressed by (region, name).
type ManagedCollectorDestinationAPI struct {
	Central centralapi.CentralInterface
}

// GetManagedCollectorDestinationTypes is the catalog a managed Collector can export to. It needs no
// connector state: it is the Odigos catalog this binary was built with, limited to what the Collector
// image can run.
func (a *ManagedCollectorDestinationAPI) GetManagedCollectorDestinationTypes(context.Context) ([]*model.DestinationType, error) {
	out, err := collectordestination.Types()
	if err != nil {
		return nil, fmt.Errorf("getManagedCollectorDestinationTypes: %w", err)
	}
	return out, nil
}

func (a *ManagedCollectorDestinationAPI) ListManagedCollectorDestinations(ctx context.Context, region string) ([]*model.Destination, error) {
	collectorDestinationAPILog.Debug("list collector destinations", "region", region)
	dests, err := a.Central.ManagedCollectorDestinations(ctx, region)
	if err != nil {
		collectorDestinationAPILog.Error("list collector destinations failed", "region", region, "error", err)
		return nil, fmt.Errorf("managedCollectorDestinations: %w", err)
	}
	out := make([]*model.Destination, 0, len(dests))
	for i := range dests {
		out = append(out, dests[i].ToDestinationModel())
	}
	return out, nil
}

func (a *ManagedCollectorDestinationAPI) GetManagedCollectorDestination(ctx context.Context, region, name string) (*model.Destination, error) {
	collectorDestinationAPILog.Debug("get collector destination", "region", region, "name", name)
	dest, err := a.Central.ManagedCollectorDestination(ctx, region, name)
	if err != nil {
		return nil, fmt.Errorf("managedCollectorDestination: %w", err)
	}
	if dest == nil {
		return nil, nil
	}
	return dest.ToDestinationModel(), nil
}

func (a *ManagedCollectorDestinationAPI) CreateManagedCollectorDestination(ctx context.Context, input model.CreateManagedCollectorDestinationInput) (*model.Destination, error) {
	dest := collectordestination.FromCreateInput(input)
	if err := a.Central.CreateManagedCollectorDestination(ctx, dest); err != nil {
		collectorDestinationAPILog.Error("create collector destination failed",
			"region", input.Region, "name", input.Name, "error", err)
		return nil, err
	}
	return dest.ToDestinationModel(), nil
}

// UpdateManagedCollectorDestination reads the stored destination, merges the partial update onto it and
// writes the whole result — so an omitted field keeps its value rather than being cleared.
func (a *ManagedCollectorDestinationAPI) UpdateManagedCollectorDestination(ctx context.Context, input model.UpdateManagedCollectorDestinationInput) (*model.Destination, error) {
	existing, err := a.Central.ManagedCollectorDestination(ctx, input.Region, input.Name)
	if err != nil {
		return nil, fmt.Errorf("updateManagedCollectorDestination: %w", err)
	}
	if existing == nil {
		return nil, fmt.Errorf("updateManagedCollectorDestination: region %s has no destination named %q",
			input.Region, input.Name)
	}
	dest := collectordestination.ApplyUpdateInput(*existing, input)
	if err := a.Central.UpdateManagedCollectorDestination(ctx, dest); err != nil {
		collectorDestinationAPILog.Error("update collector destination failed",
			"region", input.Region, "name", input.Name, "error", err)
		return nil, err
	}
	return dest.ToDestinationModel(), nil
}

func (a *ManagedCollectorDestinationAPI) DestroyManagedCollectorDestination(ctx context.Context, input model.DestroyManagedCollectorDestinationInput) (*model.DestroyManagedCollectorDestinationPayload, error) {
	deleted, err := a.Central.DeleteManagedCollectorDestination(ctx, input.Region, input.Name)
	if err != nil {
		collectorDestinationAPILog.Error("delete collector destination failed",
			"region", input.Region, "name", input.Name, "error", err)
		return nil, fmt.Errorf("destroyManagedCollectorDestination: %w", err)
	}
	return collectordestination.ToDestroyPayload(input.Region, input.Name, deleted), nil
}
