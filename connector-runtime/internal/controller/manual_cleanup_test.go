package controller

import (
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connector-runtime/internal/heartbeat"
)

// TestManualCleanupWaitingMessage pins the sentence a customer sees while deletion is held for manual-mode
// leftovers. It must say what remains, why Odigos cannot remove it, what to do, and that delete will finish
// once discovery sees them uninstrumented.
func TestManualCleanupWaitingMessage(t *testing.T) {
	hb := heartbeat.Snapshot{
		ManualInstrumentationRetained:  3,
		ManualInstrumentationWorkloads: []string{"gcp.cloud-run/checkout", "aws.lambda/orders"},
	}
	msg := manualCleanupWaitingMessage(hb)

	for _, want := range []string{
		"Waiting to finish deleting",
		"3 workload(s)",
		"manual (infrastructure-as-code)",
		"remove it from your own deployment definitions",
		"gcp.cloud-run/checkout",
		"aws.lambda/orders",
		"Deletion will complete once discovery reports them uninstrumented",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("waiting message is missing %q:\n%s", want, msg)
		}
	}

	bare := manualCleanupWaitingMessage(heartbeat.Snapshot{ManualInstrumentationRetained: 1})
	if strings.Contains(bare, ": gcp") || strings.HasSuffix(strings.TrimSuffix(bare, "."), ":") {
		t.Errorf("message with no sample should not dangle a colon:\n%s", bare)
	}
	if !strings.Contains(bare, "1 workload(s)") {
		t.Errorf("message lost the count:\n%s", bare)
	}
}

// TestManualCleanupTimedOutMessage pins the durable Event text when the cleanup timeout fires with
// manual-mode instrumentation still live — the CR is about to disappear.
func TestManualCleanupTimedOutMessage(t *testing.T) {
	hb := heartbeat.Snapshot{
		ManualInstrumentationRetained:  2,
		ManualInstrumentationWorkloads: []string{"aws.lambda/orders"},
	}
	msg := manualCleanupTimedOutMessage(hb)
	for _, want := range []string{
		"Cleanup timed out",
		"2 workload(s)",
		"manual (infrastructure-as-code)",
		"aws.lambda/orders",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("timeout message is missing %q:\n%s", want, msg)
		}
	}
}
