package gqlapi

import (
	"context"
	"fmt"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	centralapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var destinationAPILog = connlog.Subsystem("gql-destination")

// DestinationAPI maps GraphQL Destination operations to the coordinator.
type DestinationAPI struct {
	Central centralapi.CentralInterface
}

func (a *DestinationAPI) Destinations(ctx context.Context) ([]*model.Destination, error) {
	destinationAPILog.Debug("list destinations")
	list, err := a.Central.GetDestinations(ctx)
	if err != nil {
		destinationAPILog.Error("list destinations failed", "error", err)
		return nil, fmt.Errorf("destinations: %w", err)
	}
	out := make([]*model.Destination, 0, len(list))
	for i := range list {
		out = append(out, list[i].ToDestinationModel())
	}
	destinationAPILog.Debug("list destinations complete", "count", len(out))
	return out, nil
}

func (a *DestinationAPI) Destination(ctx context.Context, name string) (*model.Destination, error) {
	destinationAPILog.Debug("get destination", "name", name)
	d, err := a.Central.GetDestination(ctx, name)
	if err != nil {
		destinationAPILog.Error("get destination failed", "name", name, "error", err)
		return nil, fmt.Errorf("destination: %w", err)
	}
	if d == nil {
		destinationAPILog.Debug("destination not found", "name", name)
		return nil, nil
	}
	return d.ToDestinationModel(), nil
}

func (a *DestinationAPI) CreateDestination(ctx context.Context, input model.CreateDestinationInput) (*model.Destination, error) {
	destinationAPILog.Info("create destination", "name", input.Name, "type", input.Type)
	dest := domaindest.DestinationFromCreateInput(input)
	if err := a.Central.CreateDestination(ctx, dest); err != nil {
		destinationAPILog.Error("create destination failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("createDestination: %w", err)
	}
	destinationAPILog.Info("create destination complete", "name", input.Name)
	return dest.ToDestinationModel(), nil
}

func (a *DestinationAPI) UpdateDestination(ctx context.Context, input model.UpdateDestinationInput) (*model.Destination, error) {
	destinationAPILog.Info("update destination", "name", input.Name)
	current, err := a.Central.GetDestination(ctx, input.Name)
	if err != nil {
		destinationAPILog.Error("update destination lookup failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("updateDestination: %w", err)
	}
	if current == nil {
		destinationAPILog.Warn("update destination not found", "name", input.Name)
		return nil, fmt.Errorf("updateDestination: destination not found: name=%s", input.Name)
	}
	updated := *current
	updated.ApplyUpdateInput(input)
	if err := a.Central.UpdateDestination(ctx, updated); err != nil {
		destinationAPILog.Error("update destination failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("updateDestination: %w", err)
	}
	destinationAPILog.Info("update destination complete", "name", input.Name)
	return updated.ToDestinationModel(), nil
}

func (a *DestinationAPI) DeleteDestination(ctx context.Context, input model.DeleteDestinationInput) (*model.DeleteDestinationPayload, error) {
	destinationAPILog.Info("delete destination", "name", input.Name)
	current, err := a.Central.GetDestination(ctx, input.Name)
	if err != nil {
		destinationAPILog.Error("delete destination lookup failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("deleteDestination: %w", err)
	}
	if current == nil {
		destinationAPILog.Debug("delete destination not found", "name", input.Name)
		return domaindest.ToDeletePayload(input.Name, false), nil
	}
	if err := a.Central.DeleteDestination(ctx, input.Name); err != nil {
		destinationAPILog.Error("delete destination failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("deleteDestination: %w", err)
	}
	destinationAPILog.Info("delete destination complete", "name", input.Name)
	return domaindest.ToDeletePayload(input.Name, true), nil
}

func (a *DestinationAPI) TestConnectionForDestination(ctx context.Context, input model.TestConnectionDestinationInput) (*model.TestConnectionResponse, error) {
	destinationAPILog.Debug("test destination connection not implemented", "type", input.Type)
	// Retesting a destination is also an explicit request to reconsider routing.
	// In particular, a managed-Collector marker may have become usable since the
	// last pass. Automatic workloads will converge their export endpoint; manual
	// workloads will be observed and surface collectorExport drift instead.
	if revalidator, ok := a.Central.(interface{ RevalidateWorkloads(context.Context) error }); ok {
		if err := revalidator.RevalidateWorkloads(ctx); err != nil {
			return nil, fmt.Errorf("queue workload route validation: %w", err)
		}
	}
	destType := input.Type
	return &model.TestConnectionResponse{
		DestinationType: &destType,
	}, nil
}
