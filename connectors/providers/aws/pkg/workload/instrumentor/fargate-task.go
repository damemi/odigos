package instrumentor

import "context"

// ECSFargateInstrumentor is the seam for Fargate instrumentation (a per-task sidecar injected into the
// task definition). Not implemented yet — Reconcile is a no-op so that enabling the capability is inert
// rather than erroring. The spec keeps fargate-task instrumentation disabled until this is built.
type ECSFargateInstrumentor struct{}

func NewECSFargate() *ECSFargateInstrumentor { return &ECSFargateInstrumentor{} }

func (i *ECSFargateInstrumentor) Reconcile(_ context.Context) error {
	log.Debug("fargate-task instrumentation not implemented; skipping")
	return nil
}
