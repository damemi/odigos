package instrumentor

import (
	"context"
	"time"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

// cutoverMark is what one pass means for a workload's recorded cutover failure (store.CutoverFailure).
type cutoverMark int

const (
	cutoverKeep cutoverMark = iota
	cutoverRecord
	cutoverClear
)

// cutoverMarkAfter decides it from the route the pass applied, the route the workload was on before, and
// how the apply went.
//
// Recorded when an attempt to move the workload onto its Collector fails. Kept when the pass held the
// workload on direct export to wait for a batch slot, whatever happened: that pass says nothing about the
// Collector, and forgetting the failure there is what handed held-back failures to the rollout as never
// attempted, where enough of them stalled the region (see collectorcutover.Decide). Cleared by any other
// pass that succeeds — the workload is on its Collector now, or its route no longer waits on a rollout.
func cutoverMarkAfter(routing domaindest.Routing, routedVia string, err error) cutoverMark {
	if routing.Reason == domaindest.ReasonCollectorRolloutPending {
		return cutoverKeep
	}
	if err != nil {
		if routing.Via == domaindest.RouteCollector && routedVia != string(domaindest.RouteCollector) {
			return cutoverRecord
		}
		return cutoverKeep
	}
	return cutoverClear
}

// recordCutover keeps a service's cutover failure in step with this pass — see cutoverMarkAfter.
func (i *ECSFargateInstrumentor) recordCutover(ctx context.Context, svc store.ECSService, routing domaindest.Routing, err error) {
	var serr error
	switch cutoverMarkAfter(routing, svc.RoutingVia, err) {
	case cutoverRecord:
		serr = i.store.SetECSServiceCutoverFailure(ctx, svc.ClusterARN, svc.ServiceName,
			store.CutoverFailure{Reason: err.Error(), At: time.Now()})
	case cutoverClear:
		if svc.CutoverFailure.Reason != "" {
			serr = i.store.ClearECSServiceCutoverFailure(ctx, svc.ClusterARN, svc.ServiceName)
		}
	}
	if serr != nil {
		log.Warn("record fargate cutover failure failed", "service", svc.ServiceName, "error", serr)
	}
}

// recordCutover keeps a function's cutover failure in step with this pass — see cutoverMarkAfter.
func (i *LambdaInstrumentor) recordCutover(ctx context.Context, fn store.LambdaFunction, routing domaindest.Routing, err error) {
	var serr error
	switch cutoverMarkAfter(routing, fn.RoutingVia, err) {
	case cutoverRecord:
		serr = i.store.SetLambdaCutoverFailure(ctx, fn.FunctionARN, store.CutoverFailure{Reason: err.Error(), At: time.Now()})
	case cutoverClear:
		if fn.CutoverFailure.Reason != "" {
			serr = i.store.ClearLambdaCutoverFailure(ctx, fn.FunctionARN)
		}
	}
	if serr != nil {
		log.Warn("record lambda cutover failure failed", "function", fn.FunctionName, "error", serr)
	}
}
