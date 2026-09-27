// Package apimap maps the insights repo domain structs to the API wire types in
// package apitypes (generated from api/openapi.yaml, the contract's single
// source of truth). Handlers call these mappers so repo refactors never leak
// into the API, and adding a wire field is a spec change + a mapper line, never
// a hand-maintained duplicate struct. This package holds no type definitions of
// its own - only the repo<->apitypes conversions.
package apimap

import (
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// FromServiceStat maps one repo.ServiceStat to its wire type. transactionLimit
// is the max_transactions_per_service setting in effect; a service whose
// transaction count has reached it is flagged because the engine no longer
// admits its unseen operations.
func FromServiceStat(s repo.ServiceStat, transactionLimit int) apitypes.ServiceStat {
	return apitypes.ServiceStat{
		Namespace:               s.Namespace,
		Service:                 s.Service,
		TransactionCount:        s.TransactionCount,
		Volume:                  s.Volume,
		LastSeen:                s.LastSeen,
		TransactionLimit:        transactionLimit,
		TransactionLimitReached: transactionLimit > 0 && s.TransactionCount >= int64(transactionLimit),
	}
}

// ServiceStatList wraps repo.ServiceStat rows in the wire list envelope.
func ServiceStatList(in []repo.ServiceStat, transactionLimit int) apitypes.ServiceStatList {
	items := make([]apitypes.ServiceStat, len(in))
	for i, s := range in {
		items[i] = FromServiceStat(s, transactionLimit)
	}
	return apitypes.ServiceStatList{Count: len(items), Items: items}
}

// ServiceNameList wraps distinct service names in the wire list envelope.
func ServiceNameList(in []string) apitypes.ServiceNameList {
	if in == nil {
		in = []string{}
	}
	return apitypes.ServiceNameList{Count: len(in), Items: in}
}

// FromServiceProfile maps one repo.ServiceProfile to its wire type.
func FromServiceProfile(p repo.ServiceProfile) apitypes.ServiceProfile {
	callers := p.Callers
	if callers == nil {
		callers = []string{}
	}
	callees := p.Callees
	if callees == nil {
		callees = []string{}
	}
	egress := p.Egress
	if egress == nil {
		egress = []string{}
	}
	transactions := p.Transactions
	if transactions == nil {
		transactions = []string{}
	}
	return apitypes.ServiceProfile{
		Namespace:    p.Namespace,
		Service:      p.Service,
		Callers:      callers,
		Callees:      callees,
		Egress:       egress,
		Transactions: transactions,
	}
}

// FromTransaction maps one repo.Transaction to its wire type.
func FromTransaction(t repo.Transaction) apitypes.Transaction {
	name, dims := wireOperationIdentity(t.Operation)
	return apitypes.Transaction{
		Id:                 t.ID,
		Service:            t.Service,
		Namespace:          t.Namespace,
		Operation:          t.Operation,
		OperationName:      name,
		IdentityDimensions: dims,
		Kind:               apitypes.TransactionKind(t.Kind),
	}
}

// FromTransactionStat maps one repo.TransactionStat to its wire type, flattening
// the embedded Transaction.
func FromTransactionStat(t repo.TransactionStat) apitypes.TransactionStat {
	name, dims := wireOperationIdentity(t.Operation)
	return apitypes.TransactionStat{
		Id:                 t.ID,
		Service:            t.Service,
		Namespace:          t.Namespace,
		Operation:          t.Operation,
		OperationName:      name,
		IdentityDimensions: dims,
		Kind:               apitypes.TransactionKind(t.Kind),
		Volume:             t.Volume,
		LastSeen:           t.LastSeen,
		HasBaseline:        &t.HasBaseline,
		Promoted:           &t.Promoted,
		SaturatedClasses:   saturatedClasses(t.SaturatedClasses),
	}
}

func saturatedClasses(in []repo.DeviationClass) []apitypes.DeviationClass {
	if len(in) == 0 {
		return nil
	}
	out := make([]apitypes.DeviationClass, len(in))
	for i, c := range in {
		out[i] = apitypes.DeviationClass(c)
	}
	return out
}

func wireOperationIdentity(operation string) (name string, dims []apitypes.TransactionIdentityValue) {
	name, parsed := repo.SplitOperationIdentity(operation)
	if len(parsed) == 0 {
		return name, []apitypes.TransactionIdentityValue{}
	}
	out := make([]apitypes.TransactionIdentityValue, len(parsed))
	for i, d := range parsed {
		out[i] = apitypes.TransactionIdentityValue{
			Key:   d.Key,
			Value: d.Value,
		}
	}
	return name, out
}

// TransactionStatList wraps repo.TransactionStat rows in the wire list envelope.
func TransactionStatList(in []repo.TransactionStat) apitypes.TransactionStatList {
	items := make([]apitypes.TransactionStat, len(in))
	for i, t := range in {
		items[i] = FromTransactionStat(t)
	}
	return apitypes.TransactionStatList{Count: len(items), Items: items}
}

// FromBaselineClass maps one repo.BaselineClass plus its computed learning
// snapshot to the wire type. Data is the class's opaque JSON, passed through
// verbatim. Histogram is a chart-ready view for D3/D7 when data decodes.
func FromBaselineClass(b repo.BaselineClass, learning engine.BaselineLearning) apitypes.BaselineClass {
	return apitypes.BaselineClass{
		TransactionId:                b.TransactionID,
		Class:                        apitypes.DeviationClass(b.Class),
		ClassLabel:                   risk.ClassBaselineLabel(b.Class),
		ClassDescription:             risk.ClassBaselineDesc(b.Class),
		Data:                         b.Data,
		Histogram:                    HistogramPresentation(b.Class, b.Data),
		DataSchemaVersion:            b.DataSchemaVersion,
		ObservationCount:             b.ObservationCount,
		Promoted:                     b.Promoted,
		Saturated:                    b.Saturated,
		LearningStartedAt:            b.LearningStartedAt,
		LastChangedAt:                b.LastChangedAt,
		ObservationCountAtLastChange: b.ObservationCountAtLastChange,
		Learning:                     FromBaselineLearning(learning),
	}
}

// FromBaselineLearning maps the engine snapshot to the wire learning object.
func FromBaselineLearning(v engine.BaselineLearning) apitypes.BaselineLearning {
	return apitypes.BaselineLearning{
		Phase:                       apitypes.BaselineLearningPhase(v.Phase),
		ObservationsSinceLastChange: v.ObservationsSinceLastChange,
		QuietMinutes:                v.QuietMinutes,
		Mode:                        apitypes.LearningMode(v.Mode),
		ReadyToPromote:              v.ReadyToPromote,
		Stability: apitypes.BaselineLearningStability{
			Observations: fromStabilityProgress(v.Stability.Observations),
			Duration:     fromStabilityProgress(v.Stability.Duration),
		},
	}
}

func fromStabilityProgress(p engine.StabilityProgress) apitypes.BaselineStabilityProgress {
	return apitypes.BaselineStabilityProgress{
		Current:         p.Current,
		Target:          p.Target,
		DrivesPromotion: p.DrivesPromotion,
		Met:             p.Met,
	}
}

// BaselineClassList wraps repo.BaselineClass rows in the wire list envelope,
// attaching a computed learning snapshot per class.
func BaselineClassList(in []repo.BaselineClass, learning []engine.BaselineLearning) apitypes.BaselineClassList {
	items := make([]apitypes.BaselineClass, len(in))
	for i, b := range in {
		var snap engine.BaselineLearning
		if i < len(learning) {
			snap = learning[i]
		}
		items[i] = FromBaselineClass(b, snap)
	}
	return apitypes.BaselineClassList{Count: len(items), Items: items}
}

// FromObservationSummary maps one repo.Observation to the list wire type
// (metadata only - never includes raw_otlp).
func FromObservationSummary(o repo.Observation) apitypes.ObservationSummary {
	return apitypes.ObservationSummary{
		TraceId:       o.TraceID,
		TransactionId: o.TransactionID,
		ObservedAt:    o.ObservedAt,
		DurationMs:    o.DurationMs,
		SampleReason:  apitypes.SampleReason(o.SampleReason),
	}
}

// FromObservation maps one repo.Observation to the detail wire type, including
// raw_otlp. Used by getObservation only.
func FromObservation(o repo.Observation) apitypes.Observation {
	return apitypes.Observation{
		TraceId:       o.TraceID,
		TransactionId: o.TransactionID,
		ObservedAt:    o.ObservedAt,
		DurationMs:    o.DurationMs,
		SampleReason:  apitypes.SampleReason(o.SampleReason),
		RawOtlp:       o.RawOTLP,
	}
}

// ObservationList wraps repo.Observation rows as ObservationSummary items.
func ObservationList(in []repo.Observation) apitypes.ObservationList {
	items := make([]apitypes.ObservationSummary, len(in))
	for i, o := range in {
		items[i] = FromObservationSummary(o)
	}
	return apitypes.ObservationList{Count: len(items), Items: items}
}

// FromPolicy maps one repo.Policy to its wire type.
func FromPolicy(p repo.Policy) apitypes.Policy {
	var weights map[string]int
	if len(p.SignalWeights) > 0 {
		weights = make(map[string]int, len(p.SignalWeights))
		for k, v := range p.SignalWeights {
			weights[string(k)] = v
		}
	}
	var lists map[string][]string
	if len(p.EnricherLists) > 0 {
		lists = make(map[string][]string, len(p.EnricherLists))
		for k, v := range p.EnricherLists {
			lists[string(k)] = v
		}
	}
	return apitypes.Policy{
		Id:            p.ID,
		Name:          p.Name,
		Enabled:       p.Enabled,
		FireAtScore:   p.FireAtScore,
		SignalWeights: weights,
		EnricherLists: lists,
		Scope:         apitypes.PolicyScope(repo.NormalizePolicyScope(p.Scope)),
		ScopeKey:      p.ScopeKey,
	}
}

// PolicyList wraps repo.Policy rows in the wire list envelope.
func PolicyList(in []repo.Policy) apitypes.PolicyList {
	items := make([]apitypes.Policy, len(in))
	for i, p := range in {
		items[i] = FromPolicy(p)
	}
	return apitypes.PolicyList{Count: len(items), Items: items}
}

// FromBlastRadiusNode maps one repo.BlastRadiusNode to its wire type.
func FromBlastRadiusNode(n repo.BlastRadiusNode) apitypes.BlastRadiusNode {
	return apitypes.BlastRadiusNode{
		Namespace: n.Namespace,
		Service:   n.Service,
		IsVirtual: n.IsVirtual,
	}
}

// FromBlastRadiusEdge maps one repo.BlastRadiusEdge to its wire type. Counts are
// summed-across-pods uint64s in the repo; they comfortably fit int64 on the wire.
func FromBlastRadiusEdge(e repo.BlastRadiusEdge) apitypes.BlastRadiusEdge {
	return apitypes.BlastRadiusEdge{
		ClientNamespace: e.ClientNamespace,
		ClientService:   e.ClientService,
		ServerNamespace: e.ServerNamespace,
		ServerService:   e.ServerService,
		ConnectionType:  e.ConnectionType,
		RequestCount:    int64(e.RequestCount),
		FailedCount:     int64(e.FailedCount),
		LastSeen:        e.LastSeen,
	}
}

// FromBlastRadiusSubgraph maps a repo.BlastRadiusSubgraph to its wire type.
// nodes/edges always serialize as arrays (never null) so the frontend can render
// an anchor with no recorded edges without a nil guard.
func FromBlastRadiusSubgraph(s repo.BlastRadiusSubgraph) apitypes.BlastRadiusSubgraph {
	nodes := make([]apitypes.BlastRadiusNode, len(s.Nodes))
	for i, n := range s.Nodes {
		nodes[i] = FromBlastRadiusNode(n)
	}
	edges := make([]apitypes.BlastRadiusEdge, len(s.Edges))
	for i, e := range s.Edges {
		edges[i] = FromBlastRadiusEdge(e)
	}
	return apitypes.BlastRadiusSubgraph{
		Root:  FromBlastRadiusNode(s.Root),
		Nodes: nodes,
		Edges: edges,
	}
}
