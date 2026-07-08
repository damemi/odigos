package controller

import (
	"testing"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connector-runtime/internal/heartbeat"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

func TestComputePhase(t *testing.T) {
	cases := []struct {
		name      string
		ready     bool
		hb        heartbeat.Snapshot
		stale     bool
		wantPhase odigosv1.CloudConnectorPhase
	}{
		{"deployment not ready", false, heartbeat.Snapshot{}, false, odigosv1.CloudConnectorPhaseStarting},
		{"ready, no heartbeat", true, heartbeat.Snapshot{}, false, odigosv1.CloudConnectorPhaseStarting},
		{"ready, connected", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusConnected}, false, odigosv1.CloudConnectorPhaseConnected},
		{"ready, connected but stale -> degraded", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusConnected}, true, odigosv1.CloudConnectorPhaseDegraded},
		{"ready, degraded", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusDegraded}, false, odigosv1.CloudConnectorPhaseDegraded},
		{"ready, error", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusError}, false, odigosv1.CloudConnectorPhaseError},
		{"ready, unknown status", true, heartbeat.Snapshot{Found: true, Status: "Bogus"}, false, odigosv1.CloudConnectorPhaseStarting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computePhase(tc.ready, tc.hb, tc.stale); got != tc.wantPhase {
				t.Errorf("computePhase = %q, want %q", got, tc.wantPhase)
			}
		})
	}
}
