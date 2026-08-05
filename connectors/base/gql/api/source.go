package gqlapi

import (
	"context"
	"fmt"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	centralapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var sourceAPILog = connlog.Subsystem("gql-source")

// SourceAPI maps GraphQL Source operations to the coordinator.
type SourceAPI struct {
	Central centralapi.CentralInterface
}

func (a *SourceAPI) Sources(ctx context.Context) ([]*model.Source, error) {
	sourceAPILog.Debug("list sources")
	list, err := a.Central.GetSources(ctx)
	if err != nil {
		sourceAPILog.Error("list sources failed", "error", err)
		return nil, fmt.Errorf("sources: %w", err)
	}
	out := make([]*model.Source, 0, len(list))
	for i := range list {
		s := list[i]
		out = append(out, (&s).ToSourceModel())
	}
	sourceAPILog.Debug("list sources complete", "count", len(out))
	return out, nil
}

func (a *SourceAPI) Source(ctx context.Context, name string) (*model.Source, error) {
	sourceAPILog.Debug("get source", "name", name)
	s, err := a.Central.GetSource(ctx, name, "", "")
	if err != nil {
		sourceAPILog.Error("get source failed", "name", name, "error", err)
		return nil, fmt.Errorf("source: %w", err)
	}
	if s == nil {
		sourceAPILog.Debug("source not found", "name", name)
		return nil, nil
	}
	return s.ToSourceModel(), nil
}

func (a *SourceAPI) CreateSource(ctx context.Context, input model.CreateSourceInput) (*model.Source, error) {
	sourceAPILog.Info("create source", "name", input.Name, "kind", input.Kind)
	if err := domainsrc.ValidateConfigInput(input.Kind, input.Config); err != nil {
		sourceAPILog.Error("create source rejected", "name", input.Name, "kind", input.Kind, "error", err)
		return nil, fmt.Errorf("createSource: %w", err)
	}
	src := domainsrc.SourceFromCreateInput(input)
	if err := a.Central.CreateSource(ctx, *src); err != nil {
		sourceAPILog.Error("create source failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("createSource: %w", err)
	}
	sourceAPILog.Info("create source complete", "name", input.Name)
	return src.ToSourceModel(), nil
}

func (a *SourceAPI) UpdateSource(ctx context.Context, input model.UpdateSourceInput) (*model.Source, error) {
	sourceAPILog.Info("update source", "name", input.Name)
	// Resolve the exact existing row using the input's identity (kind/region) so the same name in two
	// regions is never confused. service_name is part of the identity but is what the update may change,
	// so it is deliberately not used to look up the current row — the source_name handle + kind + region
	// is.
	current, err := a.Central.GetSource(ctx, input.Name, strDeref(input.Kind), strDeref(input.Region))
	if err != nil {
		sourceAPILog.Error("update source lookup failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("updateSource: %w", err)
	}
	if current == nil {
		sourceAPILog.Warn("update source not found", "name", input.Name)
		return nil, fmt.Errorf("updateSource: source not found: name=%s", input.Name)
	}
	updated := *current
	updated.ApplyUpdateInput(input)
	// Validate the branch against the POST-update kind: one call may change both the kind and the
	// config, and the branch has to agree with what the source ends up being.
	if err := domainsrc.ValidateConfigInput(updated.Kind, input.Config); err != nil {
		sourceAPILog.Error("update source rejected", "name", input.Name, "kind", updated.Kind, "error", err)
		return nil, fmt.Errorf("updateSource: %w", err)
	}
	if err := a.Central.UpdateSource(ctx, updated); err != nil {
		sourceAPILog.Error("update source failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("updateSource: %w", err)
	}
	sourceAPILog.Info("update source complete", "name", input.Name)
	return (&updated).ToSourceModel(), nil
}

func (a *SourceAPI) DeleteSource(ctx context.Context, input model.DeleteSourceInput) (*model.DeleteSourcePayload, error) {
	sourceAPILog.Info("delete source", "name", input.Name)
	kind, region := strDeref(input.Kind), strDeref(input.Region)
	current, err := a.Central.GetSource(ctx, input.Name, kind, region)
	if err != nil {
		sourceAPILog.Error("delete source lookup failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("deleteSource: %w", err)
	}
	if err := a.Central.DeleteSource(ctx, input.Name, kind, region); err != nil {
		sourceAPILog.Error("delete source failed", "name", input.Name, "error", err)
		return nil, fmt.Errorf("deleteSource: %w", err)
	}
	sourceAPILog.Info("delete source complete", "name", input.Name)
	if current == nil {
		return (&domainsrc.Source{Name: input.Name}).ToDeletePayload(), nil
	}
	return current.ToDeletePayload(), nil
}

func (a *SourceAPI) RestartSource(context.Context, model.RestartSourceInput) (*model.RestartSourcePayload, error) {
	return nil, centralapi.ErrUnsupported
}

// strDeref returns the pointed-to string, or "" when nil. Optional GraphQL identity fields (kind, region)
// arrive as *string; "" means "unconstrained" to the resolver/store.
func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
