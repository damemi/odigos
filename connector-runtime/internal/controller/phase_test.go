package controller

import (
	"testing"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos/connector-runtime/internal/heartbeat"
	cpg "github.com/odigos-io/odigos/connectors/postgres"
)

func TestComputePhase(t *testing.T) {
	cases := []struct {
		name      string
		ready     bool
		hb        heartbeat.Snapshot
		stale     bool
		wantPhase cloudconnectorsv1.CloudConnectorPhase
	}{
		{"deployment not ready", false, heartbeat.Snapshot{}, false, cloudconnectorsv1.CloudConnectorPhaseStarting},
		{"ready, no heartbeat", true, heartbeat.Snapshot{}, false, cloudconnectorsv1.CloudConnectorPhaseStarting},
		{"ready, connected", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusConnected}, false, cloudconnectorsv1.CloudConnectorPhaseConnected},
		{"ready, connected but stale -> degraded", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusConnected}, true, cloudconnectorsv1.CloudConnectorPhaseDegraded},
		{"ready, degraded", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusDegraded}, false, cloudconnectorsv1.CloudConnectorPhaseDegraded},
		{"ready, error", true, heartbeat.Snapshot{Found: true, Status: cpg.HeartbeatStatusError}, false, cloudconnectorsv1.CloudConnectorPhaseError},
		{"ready, unknown status", true, heartbeat.Snapshot{Found: true, Status: "Bogus"}, false, cloudconnectorsv1.CloudConnectorPhaseStarting},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := computePhase(tc.ready, tc.hb, tc.stale); got != tc.wantPhase {
				t.Errorf("computePhase = %q, want %q", got, tc.wantPhase)
			}
		})
	}
}
