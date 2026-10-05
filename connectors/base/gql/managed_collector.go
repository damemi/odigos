package gql

import (
	"context"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"
)

// Managed-collector GraphQL ops were added in agents-api v0.24. Connectors already manage collectors
// through the coordinator/store; these schema methods are stubbed until they are wired to that path.

func (r *queryResolver) ManagedCollectorReport(context.Context, string) (*model.ManagedCollectorReport, error) {
	logUnsupported("managedCollectorReport")
	return nil, nil
}

func (r *queryResolver) ManagedCollectorReports(context.Context) ([]*model.ManagedCollectorReport, error) {
	logUnsupported("managedCollectorReports")
	return []*model.ManagedCollectorReport{}, nil
}

func (r *queryResolver) ManagedCollectorInstructions(_ context.Context, region string) (*model.ManagedCollectorInstructions, error) {
	logUnsupported("managedCollectorInstructions")
	return &model.ManagedCollectorInstructions{
		Region:    region,
		Mode:      "automatic",
		Documents: []*model.InstructionDocument{},
		Notes:     []string{},
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

func (r *queryResolver) ManagedCollectorMetrics(_ context.Context, region string, _ *bool) (*model.ManagedCollectorMetrics, error) {
	logUnsupported("managedCollectorMetrics")
	return &model.ManagedCollectorMetrics{
		Region:     region,
		ObservedAt: time.Now().UTC().Format(time.RFC3339),
		Live:       false,
	}, nil
}

func (r *queryResolver) GetManagedCollectorDestinationTypes(context.Context) ([]*model.DestinationType, error) {
	logUnsupported("getManagedCollectorDestinationTypes")
	return []*model.DestinationType{}, nil
}

func (r *queryResolver) GetManagedCollectorDestination(context.Context, string, string) (*model.Destination, error) {
	logUnsupported("getManagedCollectorDestination")
	return nil, nil
}

func (r *queryResolver) ListManagedCollectorDestinations(context.Context, string) ([]*model.Destination, error) {
	logUnsupported("listManagedCollectorDestinations")
	return []*model.Destination{}, nil
}

func (r *mutationResolver) CreateManagedCollector(context.Context, model.CreateManagedCollectorInput) (*model.ManagedCollectorReport, error) {
	logUnsupported("createManagedCollector")
	return nil, nil
}

func (r *mutationResolver) UpdateManagedCollector(context.Context, model.UpdateManagedCollectorInput) (*model.ManagedCollectorReport, error) {
	logUnsupported("updateManagedCollector")
	return nil, nil
}

func (r *mutationResolver) TeardownManagedCollector(_ context.Context, input model.TeardownManagedCollectorInput) (*model.TeardownManagedCollectorPayload, error) {
	logUnsupported("teardownManagedCollector")
	return &model.TeardownManagedCollectorPayload{
		Region:   input.Region,
		Deleted:  false,
		Retained: []string{},
	}, nil
}

func (r *mutationResolver) CreateManagedCollectorDestination(context.Context, model.CreateManagedCollectorDestinationInput) (*model.Destination, error) {
	logUnsupported("createManagedCollectorDestination")
	return nil, nil
}

func (r *mutationResolver) UpdateManagedCollectorDestination(context.Context, model.UpdateManagedCollectorDestinationInput) (*model.Destination, error) {
	logUnsupported("updateManagedCollectorDestination")
	return nil, nil
}

func (r *mutationResolver) DestroyManagedCollectorDestination(_ context.Context, input model.DestroyManagedCollectorDestinationInput) (*model.DestroyManagedCollectorDestinationPayload, error) {
	logUnsupported("destroyManagedCollectorDestination")
	return &model.DestroyManagedCollectorDestinationPayload{
		Region:  input.Region,
		Name:    input.Name,
		Deleted: false,
	}, nil
}

func (r *mutationResolver) GenerateDiagnoseBundle(context.Context, *bool) (*model.DiagnoseBundle, error) {
	logUnsupported("generateDiagnoseBundle")
	return nil, nil
}

func (r *mutationResolver) UpdateOnPremLicense(context.Context, string) (bool, error) {
	logUnsupported("updateOnPremLicense")
	return false, nil
}
