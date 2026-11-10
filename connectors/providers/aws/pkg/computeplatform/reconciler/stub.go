package reconciler

import "context"

// StubComputePlatformReconciler is a placeholder base.ComputePlatformReconciler for compute-platform types whose assign/install
// flow is not built yet — currently EKS (Odigos for Kubernetes / odiglet) and EC2 (the Odigos VM agent).
// For these the agent is installed externally and the central UI links to it; this holds the seam so
// enabling the capability is inert (a no-op) rather than erroring. Replace with a real reconciler when
// the connector owns the assign/install flow for the type.
type StubComputePlatformReconciler struct{ typeName string }

// NewStubComputePlatform builds a no-op compute-platform reconciler labelled with its spec compute-platform type name.
func NewStubComputePlatform(typeName string) *StubComputePlatformReconciler {
	return &StubComputePlatformReconciler{typeName: typeName}
}

func (r *StubComputePlatformReconciler) Reconcile(context.Context) error {
	log.Debug("compute-platform reconcile not implemented; skipping", "type", r.typeName)
	return nil
}
