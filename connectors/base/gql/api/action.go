package gqlapi

import (
	"context"
	"fmt"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	centralapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectoraction"
)

// CollectorActionAPI adapts the generic shared Action contract to regional
// managed Collectors. The shared API is also used by vm-agent, so the connector
// owns the meaning of its optional region field here.
type CollectorActionAPI struct{ Central centralapi.CentralInterface }

// allCollectorActionsReader is intentionally narrower than CentralInterface:
// existing central API fakes and older callers remain compatible, while the
// connector coordinator can expose the shared, unscoped Action list to the
// generic Central overview.
type allCollectorActionsReader interface {
	AllManagedCollectorActions(context.Context) ([]collectoraction.Action, error)
}

func (a *CollectorActionAPI) ActionTypes(context.Context) ([]*model.ActionTypeOption, error) {
	return collectoraction.Types(), nil
}

func (a *CollectorActionAPI) Action(ctx context.Context, name string, region *string) (model.Action, error) {
	if region != nil {
		action, err := a.Central.ManagedCollectorAction(ctx, *region, name)
		if err != nil || action == nil {
			return nil, err
		}
		return collectoraction.ToModel(*action)
	}
	actions, err := a.all(ctx)
	if err != nil {
		return nil, err
	}
	var found *collectoraction.Action
	for i := range actions {
		if actions[i].Name != name {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("action %q exists in multiple collector regions; specify region", name)
		}
		found = &actions[i]
	}
	if found == nil {
		return nil, nil
	}
	return collectoraction.ToModel(*found)
}

func (a *CollectorActionAPI) Actions(ctx context.Context, region *string) ([]model.Action, error) {
	var (
		actions []collectoraction.Action
		err     error
	)
	if region == nil {
		actions, err = a.all(ctx)
	} else {
		actions, err = a.Central.ManagedCollectorActions(ctx, *region)
	}
	if err != nil {
		return nil, err
	}
	out := make([]model.Action, 0, len(actions))
	for _, action := range actions {
		rendered, err := collectoraction.ToModel(action)
		if err != nil {
			return nil, err
		}
		out = append(out, rendered)
	}
	return out, nil
}

func (a *CollectorActionAPI) all(ctx context.Context) ([]collectoraction.Action, error) {
	reader, ok := a.Central.(allCollectorActionsReader)
	if !ok {
		return nil, fmt.Errorf("listing actions without a region is not supported by this connector")
	}
	return reader.AllManagedCollectorActions(ctx)
}

func (a *CollectorActionAPI) Create(ctx context.Context, input model.CreateActionInput) (model.Action, error) {
	if input.Region == nil {
		reports, err := a.Central.ManagedCollectorReports(ctx)
		if err != nil {
			return nil, err
		}
		if len(reports) != 1 {
			return nil, fmt.Errorf("createAction: region is required when the connector manages %d collectors", len(reports))
		}
		region := reports[0].Region
		input.Region = &region
	}
	action, err := collectoraction.FromCreateInput(input)
	if err != nil {
		return nil, err
	}
	if err := a.Central.CreateManagedCollectorAction(ctx, action); err != nil {
		return nil, err
	}
	return collectoraction.ToModel(action)
}

func (a *CollectorActionAPI) Update(ctx context.Context, input model.UpdateActionInput) (model.Action, error) {
	if input.Region == nil {
		return nil, fmt.Errorf("updateAction: region is required for cloud connectors")
	}
	current, err := a.Central.ManagedCollectorAction(ctx, *input.Region, input.Name)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("updateAction: region %s has no action named %q", *input.Region, input.Name)
	}
	action, err := collectoraction.ApplyUpdateInput(*current, input)
	if err != nil {
		return nil, err
	}
	if err := a.Central.UpdateManagedCollectorAction(ctx, action); err != nil {
		return nil, err
	}
	return collectoraction.ToModel(action)
}

func (a *CollectorActionAPI) Delete(ctx context.Context, input model.DeleteActionInput) (*model.DeleteActionPayload, error) {
	if input.Region == nil {
		return nil, fmt.Errorf("deleteAction: region is required for cloud connectors")
	}
	deleted, err := a.Central.DeleteManagedCollectorAction(ctx, *input.Region, input.Name)
	if err != nil {
		return nil, err
	}
	return &model.DeleteActionPayload{Name: input.Name, Deleted: deleted}, nil
}
