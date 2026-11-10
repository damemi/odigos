package coordinator

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/config"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// fakeCollectorRows is what this connector recorded. The drain reads THIS rather than the placement
// plan, and the difference is the point: placement answers "which networks should have a Collector",
// which during a drain is nobody, and the rows answer "which Collectors did this connector build".
type fakeCollectorRows struct {
	records []store.CollectorRecord
	err     error
}

func (f fakeCollectorRows) ListManagedCollectors(context.Context) ([]store.CollectorRecord, error) {
	return f.records, f.err
}

func recordedNetworks(ids ...string) fakeCollectorRows {
	var out []store.CollectorRecord
	for _, id := range ids {
		out = append(out, store.CollectorRecord{
			Key: store.CollectorKey{Region: "us-east-1"}, NetworkID: id, Kind: collectorKind,
		})
	}
	return fakeCollectorRows{records: out}
}

func drainCoordinator(caps spec.Capabilities, rows collectorRecords, rec *recorder) *Coordinator {
	provider := collectorProvider{
		observers: map[string]api.CollectorObserver{collectorKind: rec},
		deployers: map[string]api.CollectorDeployer{collectorKind: rec},
	}
	return &Coordinator{
		cfg:           config.Config{Capabilities: caps},
		provider:      provider,
		collectorRows: rows,
	}
}

// TestTheDrainTearsDownWhatWasRecorded, not what placement currently wants.
//
// A network the plan has forgotten — a VPC deleted, a region whose topology scan failed, a source
// removed last week — still has a row, and it is exactly the one that would otherwise be leaked. It is
// also why this does not go through CollectorNetworks: during a drain the answer to "which networks
// should have a Collector" is nobody.
func TestTheDrainTearsDownWhatWasRecorded(t *testing.T) {
	rec := newRecorder()
	c := drainCoordinator(automaticCaps(), recordedNetworks("vpc-1", "vpc-2"), rec)

	if err := c.drainCollectors(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	_, converged, tornDown := rec.calls()
	if len(tornDown) != 2 || tornDown[0] != "vpc-1" || tornDown[1] != "vpc-2" {
		t.Fatalf("tore down %v, want both recorded networks", tornDown)
	}
	if len(converged) != 0 {
		t.Errorf("a drain converged %v", converged)
	}
}

// TestAManualModeDrainTearsDownNothing is the rule, enforced by there being no deployer to call
// rather than by a check inside one. The rows are still there and still carry their refs; the type
// simply has no entry in ActiveCollectorDeploymentTypes.
func TestAManualModeDrainTearsDownNothing(t *testing.T) {
	rec := newRecorder()
	c := drainCoordinator(manualCaps(), recordedNetworks("vpc-1"), rec)

	if err := c.drainCollectors(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, _, tornDown := rec.calls(); len(tornDown) != 0 {
		t.Fatalf("a manual-mode drain tore down %v", tornDown)
	}
}

// TestARowOfAnUndeployableTypeIsLeftAlone covers the other half: a connector with two Collector types,
// one automatic and one manual, tears down only the one it deployed.
func TestARowOfAnUndeployableTypeIsLeftAlone(t *testing.T) {
	rec := newRecorder()
	rows := recordedNetworks("vpc-1")
	rows.records = append(rows.records, store.CollectorRecord{
		Key:       store.CollectorKey{Region: "us-east-2"},
		NetworkID: "vpc-manual",
		Kind:      "cloud.somebody-elses-collector",
	})
	c := drainCoordinator(automaticCaps(), rows, rec)

	if err := c.drainCollectors(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	_, _, tornDown := rec.calls()
	if len(tornDown) != 1 || tornDown[0] != "vpc-1" {
		t.Fatalf("tore down %v, want only the type this connector deploys", tornDown)
	}
}

// TestTheTeardownGateDoesNotMirrorTheConvergeGate. This is the asymmetry worth the whole test file.
//
// activeCollectorHandlers refuses to hand out deployers when the provider renders no Collector
// configuration, because building a Collector nothing will ever configure spends a customer's money on
// something that boots inert and stays there. That refusal must NOT extend to teardown, because the two
// fail in opposite directions: refusing to create costs a feature and is recoverable, while refusing to
// destroy leaks a running Collector holding a destination credential into a customer's account after the
// connector that owned it is gone.
//
// A build whose config plane was removed — a downgrade to an image that renders nothing — would
// otherwise delete its CR and leave the entire fleet standing, silently, with the rows that authorized
// removing it deleted alongside.
func TestTheTeardownGateDoesNotMirrorTheConvergeGate(t *testing.T) {
	rec := newRecorder()
	// deployOnlyProvider implements no DesiredConfig, which is what a connector without a config plane
	// looks like.
	p := deployOnlyProvider{
		observers: map[string]api.CollectorObserver{collectorKind: rec},
		deployers: map[string]api.CollectorDeployer{collectorKind: rec},
	}
	c := &Coordinator{
		cfg:           config.Config{Capabilities: automaticCaps()},
		provider:      p,
		collectorRows: recordedNetworks("vpc-1"),
	}

	// The converge gate refuses, as it should.
	if _, deployers := c.activeCollectorHandlers(p); len(deployers) != 0 {
		t.Fatalf("the converge gate wired %d deployers for a provider that renders nothing", len(deployers))
	}
	// The teardown gate does not.
	if handlers := c.collectorTeardownHandlers(p); len(handlers) != 1 {
		t.Fatalf("the teardown gate wired %d deployers, want the one that built the fleet", len(handlers))
	}
	if err := c.drainCollectors(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, _, tornDown := rec.calls(); len(tornDown) != 1 {
		t.Fatalf("tore down %v; a connector that lost its config plane must still remove what it built", tornDown)
	}
}

// TestAFailedRowListingFailsTheDrain. Answering "nothing recorded" from a failed read would drop the
// finalizer with the whole fleet running, and the refs in those rows are half the authority to remove
// any of it.
func TestAFailedRowListingFailsTheDrain(t *testing.T) {
	rec := newRecorder()
	c := drainCoordinator(automaticCaps(), fakeCollectorRows{err: errors.New("database unreachable")}, rec)

	if err := c.drainCollectors(context.Background()); err == nil {
		t.Fatal("a failed listing reported the drain complete")
	}
	if _, _, tornDown := rec.calls(); len(tornDown) != 0 {
		t.Errorf("tore down %v on the strength of a failed listing", tornDown)
	}
}

// TestOneNetworksFailureDoesNotAbandonTheRest, including a panic. A drain that stopped at the first
// error would leave the remaining Collectors both running and unreported, and the error it returned
// would name only the first of them.
func TestOneNetworksFailureDoesNotAbandonTheRest(t *testing.T) {
	rec := newRecorder()
	rec.failOn["vpc-2"] = errors.New("the security group is still attached")
	rec.panicOn["vpc-3"] = true
	c := drainCoordinator(automaticCaps(), recordedNetworks("vpc-1", "vpc-2", "vpc-3", "vpc-4"), rec)

	err := c.drainCollectors(context.Background())
	if err == nil {
		t.Fatal("a drain with a failed teardown reported success")
	}
	_, _, tornDown := rec.calls()
	if len(tornDown) != 4 {
		t.Fatalf("attempted %v, want every recorded network", tornDown)
	}
	// Both failures are in the error, not just the first: an operator retrying the delete needs to know
	// how much is left.
	for _, want := range []string{"vpc-2", "vpc-3"} {
		if !contains(err.Error(), want) {
			t.Errorf("the error does not name %s: %v", want, err)
		}
	}
}

// TestNoRecordsIsNotAnError: a connector that never deployed a Collector drains cleanly.
func TestNoRecordsIsNotAnError(t *testing.T) {
	rec := newRecorder()
	c := drainCoordinator(automaticCaps(), fakeCollectorRows{}, rec)

	if err := c.drainCollectors(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	if _, _, tornDown := rec.calls(); len(tornDown) != 0 {
		t.Errorf("tore down %v with nothing recorded", tornDown)
	}
}

// TestAProviderWithNoCollectorSurfaceDrainsCleanly. The interfaces are optional.
func TestAProviderWithNoCollectorSurfaceDrainsCleanly(t *testing.T) {
	c := &Coordinator{
		cfg:           config.Config{Capabilities: automaticCaps()},
		provider:      fakeProvider{},
		collectorRows: recordedNetworks("vpc-1"),
	}
	if err := c.drainCollectors(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}

// TestBothDrainPathsPropagateAFailedTeardown.
//
// A source guard, and it exists because the two call sites are the whole point of the feature and cannot
// be exercised end to end here: Shutdown and observeDrain both reach the database through a concrete
// *store.Store, so driving either of them needs a live pool.
//
// What it pins is the consequence rather than the shape. Both paths withhold shutdownComplete on a
// failed Collector teardown, because MarkShutdownComplete is what lets connector-runtime remove the CR
// finalizer — and dropping the finalizer while a Collector is still running in the customer's account,
// holding their destination credential, with the row that authorized removing it deleted alongside, is
// precisely the silently-leaked infrastructure exists to prevent.
func TestBothDrainPathsPropagateAFailedTeardown(t *testing.T) {
	raw, err := os.ReadFile("shutdown.go")
	if err != nil {
		t.Fatalf("read shutdown.go: %v", err)
	}
	src := string(raw)

	calls := strings.Count(src, "c.drainCollectors(ctx)")
	if calls != 2 {
		t.Fatalf("shutdown.go calls drainCollectors %d times, want both drain paths (Shutdown and "+
			"observeDrain). RequestCleanup must NOT call it: disabling every source is not a decision to "+
			"destroy the egress tier, and idleness plus the retention policy is what decides that", calls)
	}
	// Every call is checked, and every check leaves the drain incomplete.
	guarded := strings.Count(src, "if err := c.drainCollectors(ctx); err != nil {")
	if guarded != calls {
		t.Errorf("%d of %d drainCollectors calls are guarded by an error check; an unchecked one reports a "+
			"drain complete over a Collector that is still running", guarded, calls)
	}
	for _, want := range []string{
		// Shutdown returns, so the process exits non-zero with the drain request intact.
		`return fmt.Errorf("collector teardown failed: %w", err)`,
		// observeDrain returns false, so the next heartbeat tick retries.
		`coordLog.Error("managed Collector teardown incomplete; will retry on next tick", "error", err)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("shutdown.go does not withhold completion on a failed teardown; missing:\n\t%s", want)
		}
	}
	// And it must run only after the instrumentation rollback: nothing should still be exporting into a
	// Collector while it is being removed.
	if strings.Index(src, "c.drainInstrumentation(ctx)") > strings.Index(src, "c.drainCollectors(ctx)") {
		t.Error("Collectors are torn down before the workloads stop exporting into them")
	}
}

// TestRequestCleanupDoesNotTearDownCollectors, stated where somebody adding a third drain path will
// read it.
//
// RequestCleanup rolls instrumentation back while the pod keeps running, and a customer who re-enables a
// source a minute later would otherwise have paid for a full redeployment of every workload on the
// network — the load balancer's DNS name moves when a Collector is recreated. Sources coming off leaves
// each network idle, and the retention policy decides how long its Collector is kept, which by
// default is for ever.
func TestRequestCleanupDoesNotTearDownCollectors(t *testing.T) {
	raw, err := os.ReadFile("shutdown.go")
	if err != nil {
		t.Fatalf("read shutdown.go: %v", err)
	}
	src := string(raw)

	start := strings.Index(src, "func (c *Coordinator) RequestCleanup(")
	if start < 0 {
		t.Fatal("could not find RequestCleanup")
	}
	if strings.Contains(src[start:], "drainCollectors") {
		t.Error("RequestCleanup tears down Collectors. It is not a deletion — the pod keeps running and " +
			"sources can be re-enabled — so retention owns that decision, not this")
	}
}
