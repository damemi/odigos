package instrumentor

import (
	"errors"
	"testing"

	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

// TestCutoverFailureSurvivesAHeldBackPass. A workload held on direct export while it waits for a batch
// slot is re-applied on that route, which succeeds as a no-op. When that success also cleared its failure,
// the rollout saw it next pass as never attempted, and enough such workloads filled every batch the
// never-attempted were promised — the region's rollout stalled with nothing reporting it.
func TestCutoverFailureSurvivesAHeldBackPass(t *testing.T) {
	collector := domaindest.Routing{Via: domaindest.RouteCollector}
	heldBack := domaindest.Routing{Via: domaindest.RouteDirect, Reason: domaindest.ReasonCollectorRolloutPending}
	direct := domaindest.Routing{Via: domaindest.RouteDirect, Reason: domaindest.ReasonNoCollectorForRegion}
	denied := errors.New("register instrumented task definition: AccessDenied")
	viaDirect, viaCollector := string(domaindest.RouteDirect), string(domaindest.RouteCollector)

	for _, tc := range []struct {
		name      string
		routing   domaindest.Routing
		routedVia string
		err       error
		want      cutoverMark
	}{
		{"a re-point that fails is recorded", collector, viaDirect, denied, cutoverRecord},
		{"a held-back pass that succeeds keeps it", heldBack, viaDirect, nil, cutoverKeep},
		{"a held-back pass that fails keeps it", heldBack, viaDirect, denied, cutoverKeep},
		{"a re-point that succeeds clears it", collector, viaDirect, nil, cutoverClear},
		{"a workload no longer waiting on a rollout clears it", direct, viaDirect, nil, cutoverClear},
		{"a failure on direct export is not a cutover failure", direct, viaDirect, denied, cutoverKeep},
		{"a failure after the move is not a cutover failure", collector, viaCollector, denied, cutoverKeep},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cutoverMarkAfter(tc.routing, tc.routedVia, tc.err); got != tc.want {
				t.Errorf("cutoverMarkAfter = %v, want %v", got, tc.want)
			}
		})
	}
}
