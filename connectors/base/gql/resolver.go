// Package gql wires the agents-api generated GraphQL schema to the connector. RootResolver implements
// generated.ResolverRoot: discovery, sources, destinations and workloadReports are backed by the
// coordinator; actions, rules, profiling and pipeline counters return ErrUnsupported.
package gql

import (
	"context"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/generated"
	"github.com/odigos-io/agents-api/go/server/graph/model"

	centralapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/computeplatformreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/instrumentationplan"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/workloadreport"
	gqlapi "github.com/odigos-io/odigos-enterprise/connectors/base/gql/api"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var resolverLog = connlog.Subsystem("gql-resolver")

// logUnsupported records, at warn level, that Central/odictl invoked an operation this connector does not
// implement. The connector intentionally returns a null result (not an error) to stay compatible with the
// shared agents-api schema, but the warn log makes the dropped call visible instead of silent.
func logUnsupported(op string) {
	resolverLog.Warn("unsupported operation invoked; returning null (no-op)", "operation", op)
}

// RootResolver implements generated.ResolverRoot.
type RootResolver struct {
	Central centralapi.CentralInterface
}

func (r *RootResolver) Query() generated.QueryResolver       { return &queryResolver{r} }
func (r *RootResolver) Mutation() generated.MutationResolver { return &mutationResolver{r} }

type queryResolver struct{ *RootResolver }
type mutationResolver struct{ *RootResolver }

// --- Connector queries ---

func (r *queryResolver) SupportedSourceKinds(ctx context.Context) ([]string, error) {
	return r.Central.SupportedSourceKinds(ctx)
}

func (r *queryResolver) ConnectorRuntimeHealth(ctx context.Context) (*model.ConnectorRuntimeHealth, error) {
	h, err := r.Central.RuntimeHealth(ctx)
	if err != nil {
		return nil, err
	}
	return toHealthModel(h), nil
}

func (r *queryResolver) ConnectorCollectorStatuses(ctx context.Context) ([]*model.ConnectorCollectorStatus, error) {
	statuses, err := r.Central.CollectorStatuses(ctx)
	if err != nil {
		return nil, err
	}
	return toCollectorModels(statuses), nil
}

// --- WorkloadReports ---

func (r *queryResolver) WorkloadReports(ctx context.Context) ([]*model.WorkloadReport, error) {
	resolverLog.Debug("workloadReports query")
	workloads, err := r.Central.WorkloadReports(ctx)
	if err != nil {
		resolverLog.Error("workloadReports failed", "error", err)
		return nil, err
	}
	out := make([]*model.WorkloadReport, 0, len(workloads))
	for _, w := range workloads {
		out = append(out, workloadreport.ToModel(w))
	}
	return out, nil
}

func (r *queryResolver) WorkloadReport(ctx context.Context, serviceName string, kind string, region *string) (*model.WorkloadReport, error) {
	reg := ""
	if region != nil {
		reg = *region
	}
	w, err := r.Central.WorkloadReport(ctx, serviceName, kind, reg)
	if err != nil || w == nil {
		return nil, err
	}
	return workloadreport.ToModel(*w), nil
}

// --- InstrumentationInstructions ---

// InstrumentationInstructions serves what a customer has to apply to instrument one workload.
//
// The coordinator computes a PLAN — the same desired state the connector's own injector would produce — and
// instrumentationplan renders it into documents. The two names differ on purpose: the plan is the internal
// model the observer also judges a workload against, and only its rendering crosses the wire, so nothing a
// client sees can drift from what the connector reports drift against.
//
// A null result is a normal answer, not an error: the workload is unknown, or its type has no instrumentation
// defined on this connector, or the provider registers no planner for it. Each of those is already explained
// by the workload report, so this returns null rather than an error a client would have to special-case.
func (r *queryResolver) InstrumentationInstructions(ctx context.Context, serviceName string, kind string, region *string) (*model.InstrumentationInstructions, error) {
	reg := ""
	if region != nil {
		reg = *region
	}
	resolverLog.Debug("instrumentationInstructions query", "service", serviceName, "kind", kind, "region", reg)
	res, err := r.Central.InstrumentationPlan(ctx, serviceName, kind, reg)
	if err != nil {
		resolverLog.Error("instrumentationInstructions failed", "service", serviceName, "kind", kind, "error", err)
		return nil, err
	}
	if res == nil {
		return nil, nil
	}
	return instrumentationplan.ToModel(*res, time.Now()), nil
}

// --- ComputePlatformReports (managed install targets; distinct from WorkloadReports) ---

func (r *queryResolver) ComputePlatformReports(ctx context.Context) ([]*model.ComputePlatformReport, error) {
	resolverLog.Debug("computePlatformReports query")
	services, err := r.Central.ComputePlatformReports(ctx)
	if err != nil {
		resolverLog.Error("computePlatformReports failed", "error", err)
		return nil, err
	}
	out := make([]*model.ComputePlatformReport, 0, len(services))
	for _, s := range services {
		out = append(out, computeplatformreport.ToModel(s))
	}
	return out, nil
}

func (r *queryResolver) ComputePlatformReport(ctx context.Context, name string, kind string, region *string) (*model.ComputePlatformReport, error) {
	reg := ""
	if region != nil {
		reg = *region
	}
	s, err := r.Central.ComputePlatformReport(ctx, name, kind, reg)
	if err != nil || s == nil {
		return nil, err
	}
	return computeplatformreport.ToModel(*s), nil
}

// --- Sources ---

func (r *queryResolver) Sources(ctx context.Context) ([]*model.Source, error) {
	return (&gqlapi.SourceAPI{Central: r.Central}).Sources(ctx)
}

func (r *queryResolver) Source(ctx context.Context, name string) (*model.Source, error) {
	return (&gqlapi.SourceAPI{Central: r.Central}).Source(ctx, name)
}

// --- Destinations ---

func (r *queryResolver) Destinations(ctx context.Context) ([]*model.Destination, error) {
	return (&gqlapi.DestinationAPI{Central: r.Central}).Destinations(ctx)
}

func (r *queryResolver) Destination(ctx context.Context, name string) (*model.Destination, error) {
	return (&gqlapi.DestinationAPI{Central: r.Central}).Destination(ctx, name)
}

func (r *queryResolver) DestinationTypes(ctx context.Context) ([]*model.DestinationType, error) {
	return (&gqlapi.DestinationTypeAPI{Central: r.Central}).DestinationTypes(ctx)
}

// --- Unsupported queries ---

func (r *queryResolver) ActionTypes(context.Context) ([]*model.ActionTypeOption, error) {
	logUnsupported("actionTypes")
	return nil, nil
}
func (r *queryResolver) Action(context.Context, string) (model.Action, error) {
	logUnsupported("action")
	return nil, nil
}
func (r *queryResolver) Actions(context.Context) ([]model.Action, error) {
	logUnsupported("actions")
	return nil, nil
}
func (r *queryResolver) Config(ctx context.Context) (map[string]any, error) {
	return r.Central.Config(ctx)
}
func (r *queryResolver) ConfigDefaults(ctx context.Context) (map[string]any, error) {
	return r.Central.ConfigDefaults(ctx)
}
func (r *queryResolver) PipelineCounters(context.Context) (*model.PipelineCounters, error) {
	logUnsupported("pipelineCounters")
	return nil, nil
}
func (r *queryResolver) ProfileHotFunctions(context.Context, *int) (*model.HotFunctionsResult, error) {
	logUnsupported("profileHotFunctions")
	return nil, nil
}
func (r *queryResolver) ProfileBundleStatus(context.Context, string) (*model.ProfileBundleStatus, error) {
	logUnsupported("profileBundleStatus")
	return nil, nil
}

// Per-source profiling reads the agent's in-process eBPF profile cache. A cloud connector has no
// such cache — it rewrites provider configuration and never runs alongside the workload — so these
// join the rest of the profiling surface as no-ops.
func (r *queryResolver) ProfilingSlots(context.Context) (*model.ProfilingSlots, error) {
	logUnsupported("profilingSlots")
	return nil, nil
}
func (r *queryResolver) SourceProfile(context.Context, string) (*model.SourceProfilingResult, error) {
	logUnsupported("sourceProfile")
	return nil, nil
}
func (r *queryResolver) InstrumentationRule(context.Context, string) (model.InstrumentationRule, error) {
	logUnsupported("instrumentationRule")
	return nil, nil
}
func (r *queryResolver) InstrumentationRules(context.Context) ([]model.InstrumentationRule, error) {
	logUnsupported("instrumentationRules")
	return nil, nil
}
func (r *queryResolver) InstrumentationRuleTypes(context.Context) ([]*model.InstrumentationRuleTypeOption, error) {
	logUnsupported("instrumentationRuleTypes")
	return nil, nil
}

// --- Connector mutations ---

func (r *mutationResolver) RefreshDiscovery(ctx context.Context) (bool, error) {
	resolverLog.Info("refreshDiscovery mutation")
	if err := r.Central.RefreshDiscovery(ctx); err != nil {
		resolverLog.Error("refreshDiscovery failed", "error", err)
		return false, err
	}
	return true, nil
}

func (r *mutationResolver) TriggerRollback(ctx context.Context, id string) (bool, error) {
	return false, r.Central.TriggerRollback(ctx, id)
}

func (r *mutationResolver) RequestCleanup(ctx context.Context, id string) (bool, error) {
	// RequestCleanup now rolls back instrumentation inline, so a nil error means cleanup actually
	// completed — return true rather than the previous always-false placeholder.
	if err := r.Central.RequestCleanup(ctx, id); err != nil {
		resolverLog.Error("requestCleanup failed", "error", err)
		return false, err
	}
	return true, nil
}

// --- Sources / destinations ---

func (r *mutationResolver) CreateSource(ctx context.Context, input model.CreateSourceInput) (*model.Source, error) {
	return (&gqlapi.SourceAPI{Central: r.Central}).CreateSource(ctx, input)
}

func (r *mutationResolver) UpdateSource(ctx context.Context, input model.UpdateSourceInput) (*model.Source, error) {
	return (&gqlapi.SourceAPI{Central: r.Central}).UpdateSource(ctx, input)
}

func (r *mutationResolver) DeleteSource(ctx context.Context, input model.DeleteSourceInput) (*model.DeleteSourcePayload, error) {
	return (&gqlapi.SourceAPI{Central: r.Central}).DeleteSource(ctx, input)
}

func (r *mutationResolver) RestartSource(ctx context.Context, input model.RestartSourceInput) (*model.RestartSourcePayload, error) {
	return (&gqlapi.SourceAPI{Central: r.Central}).RestartSource(ctx, input)
}

func (r *mutationResolver) CreateDestination(ctx context.Context, input model.CreateDestinationInput) (*model.Destination, error) {
	return (&gqlapi.DestinationAPI{Central: r.Central}).CreateDestination(ctx, input)
}

func (r *mutationResolver) UpdateDestination(ctx context.Context, input model.UpdateDestinationInput) (*model.Destination, error) {
	return (&gqlapi.DestinationAPI{Central: r.Central}).UpdateDestination(ctx, input)
}

func (r *mutationResolver) DeleteDestination(ctx context.Context, input model.DeleteDestinationInput) (*model.DeleteDestinationPayload, error) {
	return (&gqlapi.DestinationAPI{Central: r.Central}).DeleteDestination(ctx, input)
}

func (r *mutationResolver) TestConnectionForDestination(ctx context.Context, input model.TestConnectionDestinationInput) (*model.TestConnectionResponse, error) {
	return (&gqlapi.DestinationAPI{Central: r.Central}).TestConnectionForDestination(ctx, input)
}

// --- Unsupported mutations ---

func (r *mutationResolver) CreateAction(context.Context, model.CreateActionInput) (model.Action, error) {
	logUnsupported("createAction")
	return nil, nil
}
func (r *mutationResolver) UpdateAction(context.Context, model.UpdateActionInput) (model.Action, error) {
	logUnsupported("updateAction")
	return nil, nil
}
func (r *mutationResolver) DeleteAction(context.Context, model.DeleteActionInput) (*model.DeleteActionPayload, error) {
	logUnsupported("deleteAction")
	return nil, nil
}
func (r *mutationResolver) UpdateConfig(ctx context.Context, input model.UpdateConfigInput) (*model.UpdateConfigPayload, error) {
	eff, err := r.Central.UpdateConfig(ctx, input.Config)
	if err != nil {
		resolverLog.Error("updateConfig failed", "error", err)
		return nil, err
	}
	return &model.UpdateConfigPayload{Config: eff}, nil
}
func (r *mutationResolver) GenerateProfileBundle(context.Context, *int, *string) (*model.ProfileBundle, error) {
	logUnsupported("generateProfileBundle")
	return nil, nil
}
func (r *mutationResolver) StartBackgroundProfileBundle(context.Context, *int, *string) (*model.ProfileBundleStatus, error) {
	logUnsupported("startBackgroundProfileBundle")
	return nil, nil
}
func (r *mutationResolver) CancelProfileBundle(context.Context, string) (bool, error) {
	logUnsupported("cancelProfileBundle")
	return false, nil
}
func (r *mutationResolver) ClearProfileCache(context.Context) (bool, error) {
	logUnsupported("clearProfileCache")
	return false, nil
}
func (r *mutationResolver) EnableSourceProfiling(context.Context, string) (*model.SlotResult, error) {
	logUnsupported("enableSourceProfiling")
	return nil, nil
}
func (r *mutationResolver) DisableSourceProfiling(context.Context, string) (*model.SlotResult, error) {
	logUnsupported("disableSourceProfiling")
	return nil, nil
}
func (r *mutationResolver) ClearSourceProfilingBuffer(context.Context, string) (*model.SlotResult, error) {
	logUnsupported("clearSourceProfilingBuffer")
	return nil, nil
}
func (r *mutationResolver) ConfigureProfilingCache(context.Context, *int, *int, *int) (*model.ProfilingSlots, error) {
	logUnsupported("configureProfilingCache")
	return nil, nil
}
func (r *mutationResolver) CreateInstrumentationRule(context.Context, model.CreateInstrumentationRuleInput) (model.InstrumentationRule, error) {
	logUnsupported("createInstrumentationRule")
	return nil, nil
}
func (r *mutationResolver) UpdateInstrumentationRule(context.Context, model.UpdateInstrumentationRuleInput) (model.InstrumentationRule, error) {
	logUnsupported("updateInstrumentationRule")
	return nil, nil
}
func (r *mutationResolver) DeleteInstrumentationRule(context.Context, model.DeleteInstrumentationRuleInput) (*model.DeleteInstrumentationRulePayload, error) {
	logUnsupported("deleteInstrumentationRule")
	return nil, nil
}

// --- converters ---

func toHealthModel(h centralapi.Health) *model.ConnectorRuntimeHealth {
	m := &model.ConnectorRuntimeHealth{
		IsRegistered:          h.Registered,
		HasSchemaVersionMatch: h.SchemaVersionOK,
		IsAuthenticated:       h.Authenticated,
		IsDiscoveryReady:      h.DiscoveryReady,
	}
	if h.LastHeartbeatAt != nil {
		s := h.LastHeartbeatAt.UTC().Format(time.RFC3339)
		m.LastHeartbeatAt = &s
	}
	if h.Message != "" {
		msg := h.Message
		m.Message = &msg
	}
	return m
}

func toCollectorModels(cs []centralapi.CollectorStatus) []*model.ConnectorCollectorStatus {
	out := make([]*model.ConnectorCollectorStatus, 0, len(cs))
	for _, c := range cs {
		var msg *string
		if c.Message != "" {
			m := c.Message
			msg = &m
		}
		out = append(out, &model.ConnectorCollectorStatus{Name: c.Name, IsHealthy: c.Healthy, Message: msg})
	}
	return out
}
