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

// ListSources lists the Source CRDs and returns each enabled one's identity plus
// its canonical SourceSpec. Sources explicitly disabled via
// spec.disableInstrumentation are excluded so the tool reflects what Odigos is
// actually instrumenting.
func (k *k8sBackend) ListSources(ctx context.Context) ([]provider.Source, error) {
	var list odigosv1.SourceList
	if err := kube.CacheClient.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]provider.Source, 0, len(list.Items))
	for i := range list.Items {
		s := list.Items[i]
		if s.Spec.DisableInstrumentation {
			continue
		}
		out = append(out, provider.Source{
			Namespace:  s.Namespace,
			Name:       s.Name,
			SourceSpec: s.Spec,
		})
	}
	return out, nil
}

// ListDestinations lists the Destination CRDs and returns each one's identity
// plus its canonical DestinationSpec.
func (k *k8sBackend) ListDestinations(ctx context.Context) ([]provider.Destination, error) {
	var list odigosv1.DestinationList
	if err := kube.CacheClient.List(ctx, &list); err != nil {
		return nil, err
	}
	out := make([]provider.Destination, 0, len(list.Items))
	for i := range list.Items {
		d := list.Items[i]
		out = append(out, provider.Destination{
			Namespace:       d.Namespace,
			Name:            d.Name,
			DestinationSpec: d.Spec,
		})
	}
	return out, nil
}
