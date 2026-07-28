// Package reconciler holds the GCP compute-platform reconcilers. Installing a separate Odigos agent onto
// a discovered compute platform (Odigos for Kubernetes on GKE, the Odigos VM agent on Compute Engine) is
// not built yet, so these are inert no-ops. The spec declares no installation types, so the base never
// runs them; they hold the seam so enabling a capability is a no-op rather than an error.
package reconciler

import (
	"context"

	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var log = connlog.Subsystem("gcp-computeplatform-reconciler")

// StubComputePlatformReconciler is a placeholder base.ComputePlatformReconciler for compute-platform
// types whose assign/install flow is not built yet (GKE / GCE). Replace with a real reconciler when the
// connector owns the install flow for the type.
type StubComputePlatformReconciler struct{ typeName string }

// NewStubComputePlatform builds a no-op compute-platform reconciler labelled with its spec type name.
func NewStubComputePlatform(typeName string) *StubComputePlatformReconciler {
	return &StubComputePlatformReconciler{typeName: typeName}
}

func (r *StubComputePlatformReconciler) Reconcile(context.Context) error {
	log.Debug("compute-platform reconcile not implemented; skipping", "type", r.typeName)
	return nil
}
