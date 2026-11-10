package coordinator

import (
	"os"
	"strings"
	"testing"

	cpg "github.com/odigos-io/odigos/connectors/postgres"
)

// Only a manual cleanup leaves the connector running. Anything else, including a reason this runtime does
// not know, is a deletion: that is what every drain meant before the two were told apart, and guessing
// "cleanup" for an unknown reason would hold a deletion open until the controller's timeout.
func TestOnlyAManualCleanupLeavesTheConnectorRunning(t *testing.T) {
	for reason, want := range map[string]bool{
		cpg.DrainReasonConnectorDeleted: true,
		cpg.DrainReasonManualCleanup:    false,
		"":                              true,
		"something_newer":               true,
	} {
		if got := endsConnector(reason); got != want {
			t.Errorf("endsConnector(%q) = %v, want %v", reason, got, want)
		}
	}
}

// A manual cleanup that the heartbeat finishes must not do what a deletion does. Driving observeDrain needs a
// live pool (the store is concrete), so this pins the consequence in the source, the way the Collector
// teardown tests do: the manual path never reaches the three calls that end a connector.
func TestAManualCleanupNeverEndsTheConnector(t *testing.T) {
	raw, err := os.ReadFile("shutdown.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	body := func(fn string) string {
		start := strings.Index(src, "func (c *Coordinator) "+fn+"(")
		if start < 0 {
			t.Fatalf("could not find %s", fn)
		}
		end := strings.Index(src[start+1:], "\nfunc ")
		if end < 0 {
			return src[start:]
		}
		return src[start : start+1+end]
	}
	for _, fn := range []string{"resumeManualCleanup", "finishManualCleanup", "RequestCleanup"} {
		b := body(fn)
		for _, forbidden := range []string{"drainCollectors", "markShutdownComplete", "cancelLoops"} {
			if strings.Contains(b, forbidden) {
				t.Errorf("%s calls %s; a manual cleanup is not a deletion", fn, forbidden)
			}
		}
	}

	// observeDrain must branch off a manual cleanup before anything a deletion does.
	observe := body("observeDrain")
	branch := strings.Index(observe, "if !endsConnector(drain.Reason) {")
	if branch < 0 {
		t.Fatal("observeDrain does not tell a manual cleanup from a deletion")
	}
	for _, deletionStep := range []string{"c.drainCollectors(ctx)", "c.markShutdownComplete(", "cancelLoops()"} {
		if i := strings.Index(observe, deletionStep); i >= 0 && i < branch {
			t.Errorf("observeDrain reaches %s before it checks the drain reason", deletionStep)
		}
	}
}

// draining is cleared when the request goes away, or the reconcile loops never run again after a cleanup;
// but not under a RequestCleanup that is still running, which owns the flag.
func TestEndDrainResumesTheLoopsUnlessACleanupIsRunning(t *testing.T) {
	c := &Coordinator{}
	c.draining.Store(true)
	c.automaticDrainDone.Store(true)
	c.cleanupInFlight.Store(true)
	c.endDrain()
	if !c.draining.Load() {
		t.Error("endDrain cleared draining under a running RequestCleanup")
	}

	c.cleanupInFlight.Store(false)
	c.endDrain()
	if c.draining.Load() || c.automaticDrainDone.Load() {
		t.Error("endDrain left the drain state set after the request was gone")
	}
}
