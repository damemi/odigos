package mcp

import (
	"context"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/frontend/kube"

	"github.com/odigos-io/odigos-enterprise/mcp/provider"
)

// k8sBackend implements provider.Backend for the enterprise k8s UI. It reads the
// Odigos CRDs from the shared informer cache (kube.CacheClient) and returns
// their canonical Spec directly — no GraphQL, no re-shaping. The CRD spec IS the
// source of truth, so the MCP output cannot drift from odigos.
type k8sBackend struct{}

func newK8sBackend() *k8sBackend { return &k8sBackend{} }

// ListSources returns every Source with its canonical SourceSpec. It deliberately
// does NOT filter out disabled sources: the read catalog mirrors full cluster state
// so an agent can answer "why isn't workload X instrumented?" — the answer lives in
// spec.disableInstrumentation. There is no separate enabled/disabled list; callers
// read the flag off each row. Matches ListDestinations/ListActions/
// ListInstrumentationRules below, none of which filter.
func (k *k8sBackend) ListSources(ctx context.Context) ([]provider.Source, error) {
	var list odigosv1.SourceList
	if err := kube.CacheClient.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]provider.Source, 0, len(list.Items))
	for i := range list.Items {
		s := list.Items[i]
		out = append(out, provider.Source{Namespace: s.Namespace, Name: s.Name, SourceSpec: s.Spec})
	}
	return out, nil
}

// ListDestinations returns every Destination with its canonical DestinationSpec
// (type, exported signals, config).
func (k *k8sBackend) ListDestinations(ctx context.Context) ([]provider.Destination, error) {
	var list odigosv1.DestinationList
	if err := kube.CacheClient.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]provider.Destination, 0, len(list.Items))
	for i := range list.Items {
		d := list.Items[i]
		out = append(out, provider.Destination{Namespace: d.Namespace, Name: d.Name, DestinationSpec: d.Spec})
	}
	return out, nil
}

// ListActions returns every Action with its canonical ActionSpec; the populated
// sub-config identifies which kind of telemetry processing it performs.
func (k *k8sBackend) ListActions(ctx context.Context) ([]provider.Action, error) {
	var list odigosv1.ActionList
	if err := kube.CacheClient.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]provider.Action, 0, len(list.Items))
	for i := range list.Items {
		a := list.Items[i]
		out = append(out, provider.Action{Namespace: a.Namespace, Name: a.Name, ActionSpec: a.Spec})
	}
	return out, nil
}

// ListInstrumentationRules returns every InstrumentationRule with its canonical
// InstrumentationRuleSpec.
func (k *k8sBackend) ListInstrumentationRules(ctx context.Context) ([]provider.InstrumentationRule, error) {
	var list odigosv1.InstrumentationRuleList
	if err := kube.CacheClient.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]provider.InstrumentationRule, 0, len(list.Items))
	for i := range list.Items {
		r := list.Items[i]
		out = append(out, provider.InstrumentationRule{Namespace: r.Namespace, Name: r.Name, InstrumentationRuleSpec: r.Spec})
	}
	return out, nil
}
