package deployer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorplacement"
	basecollectorsettings "github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	awsingress "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/ingress"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// fakePlans serves a plan the way placement.Resolver does: resolved by the observer's pass, read by the
// deployer for the rest of it.
type fakePlans struct {
	plan       placement.Plan
	generation uint64
	resolved   bool
}

func (f *fakePlans) Plan() (placement.Plan, uint64, bool) {
	return f.plan, f.generation, f.resolved
}

// deployerStore is fakeTeardownStore plus the row writers the converge path uses. Composed rather than
// written again, because the properties being tested span both: what a converge RECORDS is exactly what
// a teardown is later authorized to remove.
type deployerStore struct {
	*fakeTeardownStore
	idleWrites []string
	statuses   []string
	idleErr    error
}

func newDeployerStore() *deployerStore {
	return &deployerStore{fakeTeardownStore: newFakeTeardownStore()}
}

func (d *deployerStore) RecordCollectorInfrastructure(_ context.Context, key basestore.CollectorKey, rec basestore.CollectorRecord) error {
	row, ok := d.rows[key]
	if !ok {
		return basestore.ErrCollectorNotFound
	}
	row.WorkloadRef, row.RevisionRef, row.RuntimeRef = rec.WorkloadRef, rec.RevisionRef, rec.RuntimeRef
	row.FrontendRef, row.BackendRefs = rec.FrontendRef, rec.BackendRefs
	row.IngressRef, row.PlacementRefs = rec.IngressRef, rec.PlacementRefs
	d.calls = append(d.calls, "RecordCollectorInfrastructure:"+key.Region)
	return nil
}

func (d *deployerStore) RecordCollectorEndpoint(_ context.Context, key basestore.CollectorKey, grpc, _, _, _, _, _ string) error {
	d.calls = append(d.calls, "RecordCollectorEndpoint:"+key.Region+"/"+grpc)
	return nil
}

func (d *deployerStore) RecordCollectorScaling(_ context.Context, key basestore.CollectorKey, mode string,
	desired, _, minCount, _ *int, _, _ string) error {
	count := "nil"
	if desired != nil {
		count = itoa(*desired)
	}
	bound := "nil"
	if minCount != nil {
		bound = itoa(*minCount)
	}
	d.calls = append(d.calls, "RecordCollectorScaling:"+key.Region+"/"+mode+"/desired="+count+"/min="+bound)
	return nil
}

func (d *deployerStore) RecordCollectorIdle(_ context.Context, key basestore.CollectorKey, idleSince *time.Time) error {
	if d.idleErr != nil {
		return d.idleErr
	}
	row, ok := d.rows[key]
	if !ok {
		return basestore.ErrCollectorNotFound
	}
	if idleSince == nil {
		d.idleWrites = append(d.idleWrites, "clear")
		row.IdleSince = nil
		return nil
	}
	d.idleWrites = append(d.idleWrites, "set")
	// COALESCE: only the first observation may set the clock, or every pass would push the deadline out
	// by one interval and nothing would ever be deleted.
	if row.IdleSince == nil {
		at := *idleSince
		row.IdleSince = &at
	}
	return nil
}

func (d *deployerStore) SetCollectorStatus(_ context.Context, key basestore.CollectorKey,
	status collectorstatus.Status, message string) error {
	d.statuses = append(d.statuses, string(status)+":"+message)
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// idleDeployer is a Deployer over a network the plan no longer places a Collector on, with everything it
// built still recorded.
func idleDeployer(t *testing.T, retention collectorsettings.Retention, clock time.Time) (*Deployer, *deployerStore, *fakeClients, *fakeTeardownClients) {
	d, store, clients, teardown, _ := idleDeployerOrdered(t, retention, clock)
	return d, store, clients, teardown
}

// idleDeployerOrdered additionally returns the shared call log, for the one test that asserts an order
// spanning two AWS services.
func idleDeployerOrdered(t *testing.T, retention collectorsettings.Retention, clock time.Time) (
	*Deployer, *deployerStore, *fakeClients, *fakeTeardownClients, *sink) {
	t.Helper()

	order := &sink{}
	clients := newFakeClients()
	clients.ecs.order, clients.scaling.order = order, order
	teardown := newFakeTeardownClients()
	clusterARN := "arn:aws:ecs:" + testRegion + ":" + testAccount + ":cluster/" + ClusterName()

	for _, ecs := range []*fakeECS{clients.ecs, teardown.ecs} {
		ecs.withCluster(ClusterName(), ourTags(""), []string{"FARGATE"})
		ecs.withService(clusterARN, ServiceName(testRegion), ourTags(testNetwork), func(s *ecstypes.Service) {
			s.DesiredCount = 2
			s.RunningCount = 2
		})
	}
	clients.scaling.withTarget(ScalableTargetResourceID(ClusterName(), testRegion), 2, 10, ourTags(testNetwork))

	store := newDeployerStore()
	store.withRecord(basestore.CollectorRecord{
		Key: testKey(), NetworkID: testNetwork, Kind: testKind, Mode: spec.ModeAutomatic, Origin: "odigos",
		CollectorID: testCollectorID,
		WorkloadRef: clients.ecs.serviceARN(ServiceName(testRegion)),
		RuntimeRef:  clusterARN,
	})
	store.withCredentials(testCollectorID, "active")

	plan := placement.Plan{Kind: testKind}
	plan.Idle = append(plan.Idle, idleRef())

	return &Deployer{
		Provisioner: testProvisioner(clients, store.fakeStore, testLicense),
		Teardown:    teardown,
		Store:       store,
		Plans:       &fakePlans{plan: plan, generation: 1, resolved: true},
		Kind:        testKind,
		Mode:        spec.ModeAutomatic,
		Settings: func() collectorsettings.Settings {
			return collectorsettings.Settings{
				Enabled: true,
				Defaults: collectorsettings.Network{
					Network: basecollectorsettings.Network{
						Enabled: true, CPU: "1024", Memory: "2048", Retention: retention,
					},
					ClusterRef:       ClusterName(),
					ExecutionRoleARN: "arn:aws:iam::" + testAccount + ":role/OdigosCollectorExecutionRole",
				},
			}
		},
		Now: func() time.Time { return clock },
	}, store, clients, teardown, order
}

func idleNetwork() baseapi.CollectorNetwork {
	return baseapi.CollectorNetwork{Region: testRegion, NetworkID: testNetwork, Kind: testKind}
}

// TestTheFirstIdlePassOnlyStartsTheClock, and acts on nothing.
//
// This is what stops a connector restart, or one incomplete discovery cycle, from deleting a Collector:
// the pass that DISCOVERS the network is idle has no idea how long it has been, so the only safe thing
// it can do is write down that it is now.
func TestTheFirstIdlePassOnlyStartsTheClock(t *testing.T) {
	d, store, clients, teardown := idleDeployer(t,
		collectorsettings.Retention{IdleDeleteAfterHours: 1}, time.Now().UTC())

	if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if len(store.idleWrites) != 1 || store.idleWrites[0] != "set" {
		t.Fatalf("idle writes = %v, want the clock started once", store.idleWrites)
	}
	if store.rows[testKey()].IdleSince == nil {
		t.Fatal("the clock was not recorded")
	}
	if deletes := teardown.deletes(); len(deletes) != 0 {
		t.Fatalf("the first idle pass deleted %v", deletes)
	}
	if clients.ecs.called("UpdateService") {
		t.Error("the first idle pass scaled the Collector")
	}
}

// TestASecondIdlePassDoesNotRestartTheClock. Only the first observation may set it; a pass that pushed
// the deadline out by one interval would mean nothing was ever deleted, and the policy would silently
// never apply.
func TestASecondIdlePassDoesNotRestartTheClock(t *testing.T) {
	started := time.Now().UTC().Add(-90 * time.Minute)
	d, store, _, teardown := idleDeployer(t,
		collectorsettings.Retention{IdleDeleteAfterHours: 48}, time.Now().UTC())
	store.rows[testKey()].IdleSince = &started

	for pass := 0; pass < 3; pass++ {
		if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
			t.Fatalf("converge: %v", err)
		}
	}
	if got := store.rows[testKey()].IdleSince; got == nil || !got.Equal(started) {
		t.Fatalf("the clock moved to %v, want it held at %v", got, started)
	}
	if len(teardown.deletes()) != 0 {
		t.Errorf("a Collector inside its grace was torn down: %v", teardown.deletes())
	}
}

// TestAnIdleCollectorIsKeptByDefault. The overwhelmingly common outcome: a report and no action.
func TestAnIdleCollectorIsKeptByDefault(t *testing.T) {
	started := time.Now().UTC().Add(-365 * 24 * time.Hour)
	d, store, clients, teardown := idleDeployer(t, collectorsettings.Retention{}, time.Now().UTC())
	store.rows[testKey()].IdleSince = &started

	if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if deletes := teardown.deletes(); len(deletes) != 0 {
		t.Fatalf("the default policy deleted %v after a year idle", deletes)
	}
	if clients.ecs.called("UpdateService") {
		t.Error("the default policy scaled the Collector")
	}
	// Not reported as a fault either: a Collector kept on a network with no sources is doing exactly
	// what it was configured to do, and a standing Degraded on every network a customer turned off is
	// how Degraded stops meaning anything.
	if len(store.statuses) != 0 {
		t.Errorf("statuses = %v, want silence", store.statuses)
	}
}

// TestParkingRemovesTheAutoscalerBeforeTheCount. Application Auto Scaling holds desiredCount in `auto`
// mode, so setting the count to zero while a target was still registered would be undone within a
// minute by a policy reacting to a CPU metric that no longer exists — the Collector would flap between
// zero and the minimum for as long as it stayed idle.
func TestParkingRemovesTheAutoscalerBeforeTheCount(t *testing.T) {
	started := time.Now().UTC().Add(-10 * time.Hour)
	d, store, _, _, order := idleDeployerOrdered(t,
		collectorsettings.Retention{IdleScaleToZeroAfterHours: 6, IdleDeleteAfterHours: 48}, time.Now().UTC())
	store.rows[testKey()].IdleSince = &started

	if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
		t.Fatalf("converge: %v", err)
	}

	// One shared log, because the two calls are on different AWS APIs and indices into two separate
	// slices would compare two unrelated numbers.
	deregistered, scaled := order.indexOf("DeregisterScalableTarget"), order.indexOf("UpdateService")
	if deregistered < 0 {
		t.Fatalf("the scalable target was not deregistered: %v", order.calls)
	}
	if scaled < 0 {
		t.Fatalf("the service was not scaled to zero: %v", order.calls)
	}
	if scaled < deregistered {
		t.Errorf("the count was written before the autoscaler was removed, so a policy reacting to a CPU "+
			"metric that no longer exists would put it straight back: %v", order.calls)
	}
	if countPrefix(order.calls, "UpdateService") != 1 {
		t.Errorf("the count was written %d times, want once: %v",
			countPrefix(order.calls, "UpdateService"), order.calls)
	}
	// The row records the parked policy so a Collector at zero tasks has an explanation.
	if !store.called("RecordCollectorScaling:" + testRegion + "/manual/desired=0") {
		t.Errorf("the parked state was not recorded: %v", store.calls)
	}
	// And it is said out loud, on every pass for as long as it applies.
	if !anyContains(store.statuses, "scaled to zero") {
		t.Errorf("statuses = %v, want the parking explained", store.statuses)
	}
}

// TestParkingKeepsEverythingElse. The whole reason it exists instead of tearing down at the first grace
// period: coming back is a scale-up rather than a redeployment.
func TestParkingKeepsEverythingElse(t *testing.T) {
	started := time.Now().UTC().Add(-10 * time.Hour)
	d, store, _, teardown := idleDeployer(t,
		collectorsettings.Retention{IdleScaleToZeroAfterHours: 6}, time.Now().UTC())
	store.rows[testKey()].IdleSince = &started

	if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if deletes := teardown.deletes(); len(deletes) != 0 {
		t.Fatalf("parking deleted %v", deletes)
	}
	if _, ok := store.rows[testKey()]; !ok {
		t.Error("parking removed the Collector's record")
	}
}

// TestTheDeleteGraceRunsTheSameTeardownAsAConnectorDelete. Idleness decides WHEN teardown runs and never
// what it is allowed to remove — one destroyer, two triggers.
func TestTheDeleteGraceRunsTheSameTeardownAsAConnectorDelete(t *testing.T) {
	started := time.Now().UTC().Add(-50 * time.Hour)
	d, store, _, teardown := idleDeployer(t,
		collectorsettings.Retention{IdleScaleToZeroAfterHours: 6, IdleDeleteAfterHours: 48}, time.Now().UTC())
	store.rows[testKey()].IdleSince = &started

	if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
		t.Fatalf("converge: %v", err)
	}
	// Credentials first, exactly as in a connector delete.
	if !store.called("RevokeCollectorCredentials:" + testCollectorID + "/teardown/connector") {
		t.Fatalf("the credentials were not revoked: %v", store.calls)
	}
	if !anyPrefix(teardown.deletes(), "DeleteService") {
		t.Fatalf("the service was not deleted: %v", teardown.calls())
	}
	if len(store.deleted) != 1 {
		t.Errorf("the record was not removed: %v", store.deleted)
	}
}

// TestAManualModeDeployerDeletesNothingWhenIdle. Unreachable in practice — the coordinator wires no
// deployer for a manual-mode type — and asserted because this object holds destructive clients.
func TestAManualModeDeployerDeletesNothingWhenIdle(t *testing.T) {
	started := time.Now().UTC().Add(-500 * time.Hour)
	d, store, clients, teardown := idleDeployer(t,
		collectorsettings.Retention{IdleScaleToZeroAfterHours: 1, IdleDeleteAfterHours: 2}, time.Now().UTC())
	store.rows[testKey()].IdleSince = &started
	d.Mode = spec.ModeManual

	if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if deletes := teardown.deletes(); len(deletes) != 0 {
		t.Fatalf("a manual-mode deployer deleted %v", deletes)
	}
	if clients.ecs.called("UpdateService") {
		t.Error("a manual-mode deployer scaled a Collector the customer owns")
	}
}

// TestANetworkWithSourcesAgainHasItsClockCleared, and cleared rather than paused: the graces are about
// how long a Collector has been unused CONTINUOUSLY, so resuming a half-elapsed clock would let a
// network used every day accumulate its way to a deletion.
func TestANetworkWithSourcesAgainHasItsClockCleared(t *testing.T) {
	started := time.Now().UTC().Add(-40 * time.Hour)
	d, store, _, _ := idleDeployer(t,
		collectorsettings.Retention{IdleDeleteAfterHours: 48}, time.Now().UTC())
	store.rows[testKey()].IdleSince = &started

	// The plan places it again, with a refused placement so the converge stops before touching AWS —
	// the clock is cleared before any of that, which is the ordering being asserted.
	plans := d.Plans.(*fakePlans)
	plans.plan.Idle = nil
	plans.plan.Placements = map[string]placement.Placement{
		testRegion + "/" + testNetwork: {Region: testRegion, NetworkID: testNetwork},
	}

	if err := d.ConvergeCollector(context.Background(), idleNetwork()); err != nil {
		t.Fatalf("converge: %v", err)
	}
	if row := store.rows[testKey()]; row.IdleSince != nil {
		t.Errorf("the clock survived a network getting its sources back: %v", row.IdleSince)
	}
}

// TestNoPlanConvergesNothing. Reached outside a pass, the subnets are unknown — and a Collector placed
// in subnets nobody chose may have no egress route to the destination at all, which looks exactly like a
// healthy Collector.
func TestNoPlanConvergesNothing(t *testing.T) {
	d, store, clients, teardown := idleDeployer(t, collectorsettings.Retention{}, time.Now().UTC())
	d.Plans = &fakePlans{}

	err := d.ConvergeCollector(context.Background(), idleNetwork())
	if err == nil {
		t.Fatal("converging without a plan reported success")
	}
	if clients.ecs.called("CreateService") || len(teardown.deletes()) != 0 {
		t.Error("converging without a plan touched AWS")
	}
	if len(store.statuses) != 1 || !strings.HasPrefix(store.statuses[0], "Error:Collector convergence failed:") {
		t.Errorf("statuses = %v, want the planning error preserved on the Collector", store.statuses)
	}
}

// TestAProviderErrorIsRecorded. A failed AWS call used to travel only as ConvergeCollector's return
// value, which the coordinator logged, while the API kept reporting Pending with no reason. Raw provider
// failures must be converted into the same persisted error channel as structured ensure problems.
func TestAProviderErrorIsRecorded(t *testing.T) {
	d, store, clients, _ := idleDeployer(t, collectorsettings.Retention{}, time.Now().UTC())
	plans := d.Plans.(*fakePlans)
	plans.plan.Idle = nil
	plans.plan.Placements = map[string]placement.Placement{
		testRegion + "/" + testNetwork: {
			Region: testRegion, NetworkID: testNetwork,
			Selection: placement.Selection{SubnetIDs: []string{"subnet-a"}},
		},
	}
	plans.plan.Networks = collectorplacement.Plan{Networks: []collectorplacement.Network{
		{NetworkRef: collectorplacement.NetworkRef{Region: testRegion, NetworkID: testNetwork}, Role: collectorplacement.RoleCollector},
	}, Regions: []collectorplacement.Region{{Region: testRegion,
		Host: collectorplacement.NetworkRef{Region: testRegion, NetworkID: testNetwork}}}}

	clients.secrets.createErr = errors.New("AccessDenied: secretsmanager:CreateSecret is not authorized")
	err := d.ConvergeCollector(context.Background(), idleNetwork())
	if err == nil || !strings.Contains(err.Error(), "secretsmanager:CreateSecret") {
		t.Fatalf("converge error = %v, want the AWS failure", err)
	}
	if len(store.statuses) != 1 || !strings.Contains(store.statuses[0], "Error:Collector convergence failed:") ||
		!strings.Contains(store.statuses[0], "secretsmanager:CreateSecret") {
		t.Fatalf("statuses = %v, want the AWS failure persisted as Error", store.statuses)
	}
}

// TestTheFallbackGateCountsConsecutivePasses. Every routed workload HAS a security group, so zero
// resolved means the inventory is incomplete rather than that the senders have none — and widening a
// Collector's ingress to a whole VPC on an incomplete read cannot be walked back, because the telemetry
// that arrived through the widened rule was accepted.
func TestTheFallbackGateCountsConsecutivePasses(t *testing.T) {
	d := &Deployer{}
	none := []awsingress.Source{{Name: "svc-a"}}
	resolved := []awsingress.Source{{Name: "svc-a", SecurityGroupIDs: []string{"sg-1"}}}

	for want := 1; want <= 3; want++ {
		if got := d.countUnresolved(testNetwork, none); got != want {
			t.Fatalf("pass %d counted %d", want, got)
		}
	}
	// Reset rather than decremented: the gate asks "has discovery failed EVERY time for three passes",
	// and a decaying count would eventually open the fallback for a network whose discovery mostly works.
	if got := d.countUnresolved(testNetwork, resolved); got != 0 {
		t.Fatalf("one resolved group left the count at %d", got)
	}
	if got := d.countUnresolved(testNetwork, none); got != 1 {
		t.Errorf("the count resumed at %d rather than starting again", got)
	}
	// Per network: one VPC's incomplete discovery must not open another's fallback.
	if got := d.countUnresolved("vpc-other", none); got != 1 {
		t.Errorf("the counter is shared across networks: %d", got)
	}
}

// TestOnlyAFailureReachesTheStatusColumn. Three writers divide that column by what each can see, and
// this one only ever makes it worse: claiming Ready from here would clear a finding the observer had
// just made, and "the writes went through" is not evidence a Collector is running.
func TestOnlyAFailureReachesTheStatusColumn(t *testing.T) {
	d, store, _, _ := idleDeployer(t, collectorsettings.Retention{}, time.Now().UTC())

	d.escalate(context.Background(), testKey(), nil)
	d.escalate(context.Background(), testKey(), []collectorstatus.Problem{
		{Code: "X", Severity: collectorstatus.SeverityInfo, Message: "nothing to do"},
		{Code: "Y", Severity: collectorstatus.SeverityPending, Message: "waiting"},
	})
	if len(store.statuses) != 0 {
		t.Fatalf("statuses = %v, want nothing written for Info and Pending", store.statuses)
	}

	d.escalate(context.Background(), testKey(), []collectorstatus.Problem{
		{Code: "A", Severity: collectorstatus.SeverityDegraded, Message: "degraded thing"},
		{Code: "B", Severity: collectorstatus.SeverityError, Message: "the specific cause"},
		{Code: "C", Severity: collectorstatus.SeverityInfo, Message: "trailing note"},
	})
	if len(store.statuses) != 1 {
		t.Fatalf("statuses = %v, want one", store.statuses)
	}
	// The worst problem leads, wherever it was authored, and the count says there is more to read.
	if !strings.HasPrefix(store.statuses[0], "Error:the specific cause") {
		t.Errorf("status = %q, want the Error first", store.statuses[0])
	}
	if !strings.Contains(store.statuses[0], "1 further condition") {
		t.Errorf("status = %q, want the remaining conditions counted", store.statuses[0])
	}
}

func idleRef() collectorplacement.NetworkRef {
	return collectorplacement.NetworkRef{Region: testRegion, NetworkID: testNetwork}
}

func anyContains(values []string, want string) bool {
	for _, v := range values {
		if strings.Contains(v, want) {
			return true
		}
	}
	return false
}

func countPrefix(calls []string, prefix string) int {
	var n int
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

var _ = aws.String
var _ = errors.New
