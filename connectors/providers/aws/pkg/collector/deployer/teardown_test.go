package deployer

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/smithy-go"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const testCollectorID = "col-1"

// teardownScenario is a fully provisioned Collector: the customer-owned cluster plus every object
// Odigos creates, with every ref recorded. Tests take it apart one property at a time.
type teardownScenario struct {
	clients *fakeTeardownClients
	store   *fakeTeardownStore
	in      TeardownInput
}

func newTeardownScenario() *teardownScenario {
	clients := newFakeTeardownClients()
	tags := ourTags(testNetwork)

	clusterARN := "arn:aws:ecs:" + testRegion + ":" + testAccount + ":cluster/" + ClusterName()
	clients.ecs.withCluster(ClusterName(), tags, []string{"FARGATE", "FARGATE_SPOT"})
	clients.ecs.withService(clusterARN, ServiceName(testRegion), tags, func(s *ecstypes.Service) {
		s.DesiredCount = 2
		s.RunningCount = 2
	})
	clients.elb.withLoadBalancer(LoadBalancerName(testNetwork), tags, []string{"sg-1"}, []string{"subnet-a"})
	clients.elb.withTargetGroup(TargetGroupName(testNetwork, 4317), tags)
	clients.elb.withTargetGroup(TargetGroupName(testNetwork, 4318), tags)
	clients.ec2.withGroupIn(testNetwork, SecurityGroupName(testNetwork), tags)
	clients.secrets.
		withSecret(CredentialSecretName(testConnector, testRegion), tags, true).
		withSecret(BootstrapSecretName(testConnector, testRegion), tags, true).
		withSecret(LicenseSecretName(testConnector, testRegion), ourTags(""), true)
	clients.logs.withGroup(logGroupARN("aws", testRegion, testAccount, LogGroupName(testConnector)), ourTags(""))
	clients.scaling.withTarget(ScalableTargetResourceID(ClusterName(), testRegion), 2, 10, tags)

	sg := clients.ec2.groups[testNetwork+"/"+SecurityGroupName(testNetwork)]
	store := newFakeTeardownStore().
		withRecord(basestore.CollectorRecord{
			Key:              testKey(),
			NetworkID:        testNetwork,
			Kind:             testKind,
			Mode:             spec.ModeAutomatic,
			Origin:           "odigos",
			CollectorID:      testCollectorID,
			CredentialRef:    clients.secrets.secrets[CredentialSecretName(testConnector, testRegion)].arn,
			LicenseSecretRef: clients.secrets.secrets[LicenseSecretName(testConnector, testRegion)].arn,
			WorkloadRef:      clients.ecs.serviceARN(ServiceName(testRegion)),
			RuntimeRef:       clusterARN,
			FrontendRef:      clients.elb.arnOf(LoadBalancerName(testNetwork)),
			BackendRefs: []string{
				clients.elb.arnOf(TargetGroupName(testNetwork, 4317)),
				clients.elb.arnOf(TargetGroupName(testNetwork, 4318)),
			},
			IngressRef: aws.ToString(sg.GroupId),
		}).
		withCredentials(testCollectorID, collectorcredential.StateActive)

	return &teardownScenario{
		clients: clients,
		store:   store,
		in: TeardownInput{
			Key:       testKey(),
			NetworkID: testNetwork,
			Mode:      spec.ModeAutomatic,
			Reason:    collectorcredential.ReasonTeardown,
			Identity:  testIdentity(),
			Partition: "aws",
			AccountID: testAccount,
		},
	}
}

func TestTeardownOfFirstPassRowRemovesOwnedLogGroup(t *testing.T) {
	clients := newFakeTeardownClients()
	arn := logGroupARN("aws", testRegion, testAccount, LogGroupName(testConnector))
	clients.logs.withGroup(arn, ourTags(""))
	records := newFakeTeardownStore().withRecord(basestore.CollectorRecord{
		Key: testKey(), NetworkID: testNetwork, Kind: testKind,
		Mode: spec.ModeAutomatic, Origin: "odigos",
	})
	result, err := Teardown(context.Background(), clients, records, TeardownInput{
		Key: testKey(), NetworkID: testNetwork, Mode: spec.ModeAutomatic,
		Identity: testIdentity(), Partition: "aws", AccountID: testAccount,
	})
	if err != nil || !result.RecordRemoved || countPrefix(clients.logs.calls, "DeleteLogGroup:"+LogGroupName(testConnector)) != 1 {
		t.Fatalf("first-pass log group teardown = %+v, %v, calls=%v", result, err, clients.logs.calls)
	}
}

func TestUncheckpointedOwnedSecretRetainsFirstPassRow(t *testing.T) {
	clients := newFakeTeardownClients()
	name := LicenseSecretName(testConnector, testRegion)
	clients.secrets.withSecret(name, ourTags(""), true)
	records := newFakeTeardownStore().withRecord(basestore.CollectorRecord{
		Key: testKey(), NetworkID: testNetwork, Kind: testKind,
		Mode: spec.ModeAutomatic, Origin: "odigos",
	})
	result, err := Teardown(context.Background(), clients, records, TeardownInput{
		Key: testKey(), NetworkID: testNetwork, Mode: spec.ModeAutomatic,
		Identity: testIdentity(), Partition: "aws", AccountID: testAccount,
	})
	if err != nil || result.RecordRemoved || len(leakedObjects(result.Kept)) != 1 {
		t.Fatalf("uncheckpointed secret was discarded: %+v, %v", result, err)
	}
	if _, err := records.GetManagedCollector(context.Background(), testKey()); err != nil {
		t.Fatalf("recovery row was lost: %v", err)
	}
	if _, ok := clients.secrets.secrets[name]; !ok {
		t.Fatal("secret without a recorded ref was deleted")
	}
}

func (s *teardownScenario) run(t *testing.T) TeardownResult {
	t.Helper()
	result, err := Teardown(context.Background(), s.clients, s.store, s.in)
	if err != nil {
		t.Fatalf("teardown: %v", err)
	}
	return result
}

// TestEveryLiveCredentialIsRevokedBeforeAnythingIsDeleted is the first acceptance criterion, and the
// ordering is the whole of it.
//
// A task that outlives the delete call — draining, stuck, or held by a deployment that will not settle —
// keeps an authenticated OpAMP session open and keeps being handed the desired configuration, which
// carries the destination's own credential. Deleting first and revoking afterwards would make that
// window as long as the AWS calls take.
//
// EVERY live credential, not the active one: a teardown that lands during a rotation finds an active and
// a superseded credential, and the superseded one is accepted for the length of the old tasks' drain.
func TestEveryLiveCredentialIsRevokedBeforeAnythingIsDeleted(t *testing.T) {
	s := newTeardownScenario()
	s.store.withCredentials(testCollectorID,
		collectorcredential.StateActive, collectorcredential.StateSuperseded, collectorcredential.StateRevoked)

	result := s.run(t)

	if result.RevokedCredentials != 2 {
		t.Fatalf("revoked %d credentials, want the two LIVE ones (the third is already revoked)", result.RevokedCredentials)
	}
	if len(s.clients.deletes()) == 0 {
		t.Fatal("nothing was deleted, so the ordering assertion below would pass vacuously")
	}
	// The revocation is on the STORE's call log and the deletions are on the clients', so the assertion
	// is that the store saw it before any client did — which is the ordering, expressed the only way it
	// can be observed from outside.
	if !s.store.called("RevokeCollectorCredentials") {
		t.Fatal("no revocation was recorded")
	}
	if got := s.store.calls[0]; !strings.HasPrefix(got, "RevokeCollectorCredentials") {
		t.Errorf("the first store call was %q; the revocation must come before every delete", got)
	}
	if !s.store.called("RevokeCollectorCredentials:" + testCollectorID + "/teardown/connector") {
		t.Errorf("the revocation did not carry the teardown reason and the connector actor: %v", s.store.calls)
	}
}

// TestNothingIsDeletedWhenTheRevocationFails is the ordering's real content.
//
// "Revoke first" is only a safety property if a FAILED revocation stops the teardown: deleting the
// compute after a revocation that did not happen leaves the window open indefinitely, and the connector
// that would have retried it is being deleted.
func TestNothingIsDeletedWhenTheRevocationFails(t *testing.T) {
	s := newTeardownScenario()
	s.store.revokeErr = errors.New("the credential store is unreachable")

	result, err := Teardown(context.Background(), s.clients, s.store, s.in)
	if err == nil {
		t.Fatal("a failed revocation returned no error, so a drain would have reported the teardown complete")
	}
	if deletes := s.clients.deletes(); len(deletes) != 0 {
		t.Fatalf("deleted %v after the revocation failed", deletes)
	}
	if len(s.store.deleted) != 0 {
		t.Error("the Collector record was deleted, which would strip the authority to remove what is still standing")
	}
	problem := problemWith(t, result.Problems, ProblemTeardownRevocationFailed)
	if problem.Severity != collectorstatus.SeverityError {
		t.Errorf("severity = %q, want Error", problem.Severity)
	}
}

// TestManualModeDeletesNothing is the third acceptance criterion, and plainest sentence: in
// manual mode teardown deletes nothing, whatever the tags say.
//
// It is also what makes the automatic → manual hand-over safe without rewriting a single tag. The
// objects still carry `odigos-managed` from when Odigos created them, and every ref is still recorded —
// both halves of the deletion gate are satisfied — and the mode alone refuses.
func TestManualModeDeletesNothing(t *testing.T) {
	s := newTeardownScenario()
	s.in.Mode = spec.ModeManual

	result := s.run(t)

	if deletes := s.clients.deletes(); len(deletes) != 0 {
		t.Fatalf("manual mode deleted %v", deletes)
	}
	if len(s.store.deleted) != 0 {
		t.Error("manual mode deleted the Collector record")
	}
	// The credentials still go: Odigos no longer configures this Collector, and nothing should keep
	// pulling a configuration that carries the destination's credential.
	if result.RevokedCredentials != 1 {
		t.Errorf("revoked %d credentials in manual mode, want the live one", result.RevokedCredentials)
	}
	problem := problemWith(t, result.Problems, ProblemTeardownManualMode)
	if problem.Severity != collectorstatus.SeverityInfo {
		t.Errorf("severity = %q, want Info: deleting nothing is the contract, not a fault", problem.Severity)
	}
}

// TestThePreprovisionedClusterKeepsTheClusterAndLosesTheService is the ECS ownership boundary:
// automatic deployment owns its service, never the cluster that hosts it.
func TestThePreprovisionedClusterKeepsTheClusterAndLosesTheService(t *testing.T) {
	s := newTeardownScenario()

	result := s.run(t)

	if !removed(result, "ECS service") {
		t.Fatalf("the service was not removed: %v", result.Removed)
	}
	kept := keptOf(t, result, "ECS cluster")
	if !strings.Contains(kept.Reason, "collector.regions") {
		t.Errorf("the cluster was kept for the wrong reason: %q", kept.Reason)
	}
	if _, ok := s.clients.ecs.clusters[ClusterName()]; !ok {
		t.Fatal("the pre-provisioned cluster was removed")
	}
	// Info rather than Degraded: Odigos never owned it, nothing is wrong, and reporting it as a fault
	// would put a standing finding on every teardown of every customer-named cluster.
	problems := problemsFor(result, ProblemTeardownRetained)
	for _, p := range problems {
		if strings.Contains(p.Message, "ECS cluster") && p.Severity != collectorstatus.SeverityInfo {
			t.Errorf("the customer-named cluster was reported as %q, want Info", p.Severity)
		}
	}
}

// TestConnectorWideObjectsSurviveWhileAnotherRegionNeedsThem is the second acceptance criterion.
//
// The connector roles are shared, so they go with the last Collector that uses them. The cluster is
// customer-owned and remains regardless of how many Collectors use it.
func TestConnectorWideObjectsSurviveWhileAnotherRegionNeedsThem(t *testing.T) {
	s := newTeardownScenario()
	other := basestore.CollectorKey{Region: "us-west-2"}
	s.store.withRecord(basestore.CollectorRecord{
		Key: other, NetworkID: "vpc-other", Kind: testKind, Mode: spec.ModeAutomatic,
		RuntimeRef: "arn:aws:ecs:us-west-2:" + testAccount + ":cluster/" + ClusterName(),
	})

	result := s.run(t)

	// This region's own objects still go.
	for _, kind := range []string{"ECS service", "load balancer", "target group", "security group", "log group"} {
		if !removed(result, kind) {
			t.Errorf("%s was not removed even though only the SHARED objects are still in use: %v", kind, result.Removed)
		}
	}
	// The licence secret is regional and therefore goes with this region's Collector.
	if _, ok := s.clients.secrets.secrets[LicenseSecretName(testConnector, testRegion)]; ok {
		t.Error("the region's licence secret survived its regional Collector")
	}
	// The per-Collector secrets are NOT shared and go with this network.
	if _, ok := s.clients.secrets.secrets[CredentialSecretName(testConnector, testRegion)]; ok {
		t.Error("this Collector's own credential secret survived its teardown")
	}
}

// TestTheLastNetworkTakesTheOwnedSharedObjectsWithIt removes Odigos-owned shared objects, while the
// pre-provisioned ECS cluster remains customer-owned.
func TestTheLastNetworkTakesTheOwnedSharedObjectsWithIt(t *testing.T) {
	s := newTeardownScenario()

	result := s.run(t)

	for _, kind := range []string{"log group", "Secrets Manager secret"} {
		if !removed(result, kind) {
			t.Errorf("%s survived the last Collector that used it: %v", kind, result.Removed)
		}
	}
	if kept := keptOf(t, result, "ECS cluster"); !strings.Contains(kept.Reason, "customer-provisioned") {
		t.Errorf("cluster was preserved for the wrong reason: %q", kept.Reason)
	}
	if !result.RecordRemoved {
		t.Error("the record was kept even though everything was removed")
	}
}

// TestAnAdoptedStackIsNeverDeleted. A stack applied from a published Odigos artifact carries the
// identity tags and `odigos-iac`, and adoption never writes the ownership marker — so Odigos converges
// it for its whole life and then leaves it exactly where it is.
func TestAnAdoptedStackIsNeverDeleted(t *testing.T) {
	artifact := baseapi.CollectorIdentityTags(testConnector, testUID, testRegion, testNetwork)
	artifact[baseapi.TagIaC] = "true"
	// The dangerous shape: a customer who ALSO pasted the ownership tag into their own document. It
	// still reads as customer-created, because `odigos-iac` disqualifies.
	artifact[baseapi.TagManaged] = baseapi.TagManagedTrue

	s := newTeardownScenario()
	clusterARN := s.store.rows[testKey()].RuntimeRef
	s.clients.ecs.withService(clusterARN, ServiceName(testRegion), artifact, nil)

	result := s.run(t)

	if removed(result, "ECS service") {
		t.Fatal("a service applied from an artifact was deleted")
	}
	kept := keptOf(t, result, "ECS service")
	if !strings.Contains(kept.Reason, "you created it") {
		t.Errorf("reason = %q, want the artifact-origin explanation", kept.Reason)
	}
	for _, p := range problemsFor(result, ProblemTeardownRetained) {
		if strings.Contains(p.Message, "ECS service") && p.Severity != collectorstatus.SeverityInfo {
			t.Errorf("an adopted stack was reported as %q, want Info", p.Severity)
		}
	}
}

// TestAnUnrecordedObjectIsKeptAndReportedLoudly. Two independent records must agree: a tag with
// no record refuses, because a tag is the one thing a customer could write by hand.
//
// Reported as DEGRADED rather than Info, and the distinction is the point: an adopted object left
// standing is by design, whereas this one looks like something Odigos created and cannot remove — which
// is the state that actually leaks.
func TestAnUnrecordedObjectIsKeptAndReportedLoudly(t *testing.T) {
	s := newTeardownScenario()
	s.store.rows[testKey()].FrontendRef = ""

	result := s.run(t)

	if removed(result, "load balancer") {
		t.Fatal("a load balancer with no recorded ref was deleted on the strength of its tag alone")
	}
	if len(s.clients.elb.calls) > 0 {
		for _, call := range s.clients.elb.calls {
			if strings.HasPrefix(call, "DeleteLoadBalancer") {
				t.Fatal("the load balancer was deleted")
			}
		}
	}
	kept := keptOf(t, result, "load balancer")
	if !kept.Leaked {
		t.Error("a load balancer Odigos created and cannot remove was not reported as leaked")
	}
	if !strings.Contains(kept.Reason, "does not record its ref") {
		t.Errorf("reason = %q, want it to name which of the two records is missing", kept.Reason)
	}
	for _, p := range problemsFor(result, ProblemTeardownRetained) {
		if strings.Contains(p.Message, "load balancer") && p.Severity != collectorstatus.SeverityDegraded {
			t.Errorf("an object Odigos created and cannot remove was reported as %q, want Degraded", p.Severity)
		}
	}

	// And the record SURVIVES. Deleting it would take the second half of the deletion gate with it, so
	// the load balancer would become infrastructure nothing records and nobody owns — and the row is also
	// the surface the retention is reported on, so the report would go quiet at the same moment.
	if result.RecordRemoved || len(s.store.deleted) != 0 {
		t.Error("the record was deleted while an object Odigos created was left standing")
	}
	if problem := problemWith(t, result.Problems, ProblemTeardownIncomplete); problem.Severity != collectorstatus.SeverityError {
		t.Errorf("severity = %q, want Error", problem.Severity)
	}
}

// TestAnAdoptedObjectDoesNotHoldTheRecordOpen is the other side of the rule above, and the reason it is
// not simply "keep the row whenever anything was kept".
//
// A stack applied from an artifact is left standing on EVERY teardown, for ever, by design. Holding the
// Collector's record — and with it the connector's delete — open over an object Odigos was never
// entitled to remove would turn the correct outcome into a stuck finalizer on every adopted network.
func TestAnAdoptedObjectDoesNotHoldTheRecordOpen(t *testing.T) {
	artifact := baseapi.CollectorIdentityTags(testConnector, testUID, testRegion, testNetwork)
	artifact[baseapi.TagIaC] = "true"

	s := newTeardownScenario()
	clusterARN := s.store.rows[testKey()].RuntimeRef
	s.clients.ecs.withService(clusterARN, ServiceName(testRegion), artifact, nil)

	result := s.run(t)

	if kept := keptOf(t, result, "ECS service"); kept.Leaked {
		t.Error("an artifact-applied service was reported as leaked; Odigos never created it")
	}
	if !result.RecordRemoved {
		t.Error("an adopted object held the Collector's record open, which would stick the connector delete")
	}
}

// TestATagWithNoOwnershipMarkerIsRefusedAndSaidSo covers the other direction: the ref IS recorded and
// the object carries no ownership tag, which is what an object Odigos adopted by name looks like.
func TestATagWithNoOwnershipMarkerIsRefusedAndSaidSo(t *testing.T) {
	s := newTeardownScenario()
	clusterARN := s.store.rows[testKey()].RuntimeRef
	s.clients.ecs.withService(clusterARN, ServiceName(testRegion),
		baseapi.CollectorIdentityTags(testConnector, testUID, testRegion, testNetwork), nil)

	result := s.run(t)

	if removed(result, "ECS service") {
		t.Fatal("a service with no ownership tag was deleted")
	}
	kept := keptOf(t, result, "ECS service")
	if !strings.Contains(kept.Reason, "you created it") {
		t.Errorf("reason = %q", kept.Reason)
	}
}

// TestAForeignObjectIsRefusedLoudly. Another connector's Collector in the same account is never touched,
// and it is a Degraded rather than an Info: two connectors sharing an account with colliding names is a
// configuration somebody has to fix.
func TestAForeignObjectIsRefusedLoudly(t *testing.T) {
	s := newTeardownScenario()
	foreign := baseapi.CollectorManagedTags("other-connector", "uid-other", testRegion, testNetwork)
	clusterARN := s.store.rows[testKey()].RuntimeRef
	s.clients.ecs.withService(clusterARN, ServiceName(testRegion), foreign, nil)
	s.clients.ecs.serviceTasks[serviceKey(clusterARN, ServiceName(testRegion))] = []string{"arn:aws:ecs:task/foreign"}

	result := s.run(t)

	if removed(result, "ECS service") {
		t.Fatal("another connector's service was deleted")
	}
	if countPrefix(s.clients.ecs.calls, "StopTask:") != 0 {
		t.Fatal("another connector's task was stopped")
	}
	kept := keptOf(t, result, "ECS service")
	if !strings.Contains(kept.Reason, "different connector") {
		t.Errorf("reason = %q, want it to name the other connector", kept.Reason)
	}
	for _, p := range problemsFor(result, ProblemTeardownRetained) {
		if strings.Contains(p.Message, "ECS service") && p.Severity != collectorstatus.SeverityDegraded {
			t.Errorf("a foreign object was reported as %q, want Degraded", p.Severity)
		}
	}
}

// TestTheDeletionOrderFollowsTheDependencies. Every step of it is forced by AWS rather than chosen, and
// getting one wrong produces an error that names the wrong object.
func TestTheDeletionOrderFollowsTheDependencies(t *testing.T) {
	s := newTeardownScenario()
	s.run(t)

	order := s.clients.deletes()
	indexOf := func(prefix string) int {
		for i, call := range order {
			if strings.HasPrefix(call, prefix) {
				return i
			}
		}
		t.Fatalf("%s never happened: %v", prefix, order)
		return -1
	}

	// The scalable target before the service it addresses: a target left registered against a deleted
	// service is one Odigos can no longer address, because the resource id names a service that is gone.
	if indexOf("DeregisterScalableTarget") > indexOf("DeleteService") {
		t.Errorf("the scalable target was deregistered after its service: %v", order)
	}
	// The load balancer before its target groups: a group a listener still forwards to cannot be
	// deleted, and deleting the load balancer is what removes the listeners.
	if indexOf("DeleteLoadBalancer") > indexOf("DeleteTargetGroup") {
		t.Errorf("a target group was deleted before its load balancer: %v", order)
	}
	// The security group last of the network objects: it is attached to the load balancer AND the tasks.
	if indexOf("DeleteSecurityGroup") < indexOf("DeleteLoadBalancer") {
		t.Errorf("the security group was deleted before the load balancer holding it: %v", order)
	}
}

// TestTheRecordIsDeletedLastAndOnlyWhenNothingIsLeft. The refs are half the deletion gate, so a row
// deleted while an object still stands strips the authority to remove that object for ever.
func TestTheRecordIsDeletedLastAndOnlyWhenNothingIsLeft(t *testing.T) {
	s := newTeardownScenario()
	// A load balancer AWS will not release yet — the expected outcome of the pass that deleted it, since
	// the interfaces take minutes to go.
	s.clients.ec2.deleteErr = &smithyAPIError{code: "DependencyViolation"}

	result, err := Teardown(context.Background(), s.clients, s.store, s.in)
	if err == nil {
		t.Fatal("an incomplete teardown reported success, so the drain would have dropped the finalizer")
	}
	if result.RecordRemoved || len(s.store.deleted) != 0 {
		t.Fatal("the record was deleted while the security group was still standing")
	}
	problem := problemWith(t, result.Problems, ProblemTeardownIncomplete)
	if problem.Severity != collectorstatus.SeverityPending {
		t.Errorf("severity = %q, want Pending", problem.Severity)
	}
	// Everything else still went. A dependency AWS has not released must not cost the rest of the
	// teardown, or a fleet would be removed one object per pass.
	if !removed(result, "ECS service") || !removed(result, "load balancer") {
		t.Errorf("one blocked object stopped the rest: %v", result.Removed)
	}

	// The next pass finishes it, which is what "resumable" means.
	s.clients.ec2.deleteErr = nil
	second := s.run(t)
	if !second.RecordRemoved {
		t.Errorf("the second pass did not finish the teardown: kept %v", second.Kept)
	}
}

func TestTeardownStopsTasksAndWaitsForDrainingService(t *testing.T) {
	s := newTeardownScenario()
	cluster := "arn:aws:ecs:" + testRegion + ":" + testAccount + ":cluster/" + ClusterName()
	name := ServiceName(testRegion)
	key := serviceKey(cluster, name)
	s.clients.ecs.serviceTasks[key] = []string{"arn:aws:ecs:task/collector-1", "arn:aws:ecs:task/collector-2"}
	s.clients.ec2.deleteErr = &smithyAPIError{code: "DependencyViolation"}

	first, err := Teardown(context.Background(), s.clients, s.store, s.in)
	if !errors.Is(err, baseapi.ErrCollectorTeardownRetry) || first.RecordRemoved {
		t.Fatalf("service with running tasks was reported complete: result=%+v err=%v", first, err)
	}
	if countPrefix(s.clients.ecs.calls, "StopTask:") != 2 {
		t.Fatalf("Collector tasks were not stopped: %v", s.clients.ecs.calls)
	}
	if got := aws.ToString(s.clients.ecs.services[key].Status); got != "DRAINING" {
		t.Fatalf("service status = %q, want DRAINING", got)
	}

	// ECS may leave the service DRAINING after its tasks have stopped. That service still holds
	// teardown authority, even if all the other resources disappeared in the first pass.
	s.clients.ec2.deleteErr = nil
	second, err := Teardown(context.Background(), s.clients, s.store, s.in)
	if !errors.Is(err, baseapi.ErrCollectorTeardownRetry) || second.RecordRemoved {
		t.Fatalf("DRAINING service was reported complete: result=%+v err=%v", second, err)
	}
	if countPrefix(s.clients.ecs.calls, "DeleteService:") != 1 {
		t.Fatalf("DRAINING service should not be deleted again: %v", s.clients.ecs.calls)
	}

	delete(s.clients.ecs.services, key) // ECS finishes draining and reports the service absent.
	third := s.run(t)
	if !third.RecordRemoved {
		t.Fatal("teardown did not finish after the service became inactive")
	}
}

func TestTeardownDependencyDelayDoesNotHideAnotherFailure(t *testing.T) {
	s := newTeardownScenario()
	s.clients.ec2.deleteErr = &smithyAPIError{code: "DependencyViolation"}
	s.clients.secrets.deleteErr = errors.New("secret deletion denied")

	result, err := Teardown(context.Background(), s.clients, s.store, s.in)
	if err == nil || errors.Is(err, baseapi.ErrCollectorTeardownRetry) {
		t.Fatalf("mixed teardown failure was classified as retry-only: %v", err)
	}
	if problem := problemWith(t, result.Problems, ProblemTeardownIncomplete); problem.Severity != collectorstatus.SeverityError {
		t.Fatalf("mixed teardown severity = %q, want Error", problem.Severity)
	}
}

// TestAnAlreadyDeletedObjectIsSuccess. Teardown is resumed after an interrupted pass far more often than
// it is run cleanly, so "already gone" has to be success everywhere — otherwise a network whose service
// went in pass one could never finish.
func TestAnAlreadyDeletedObjectIsSuccess(t *testing.T) {
	s := newTeardownScenario()
	delete(s.clients.ecs.services, serviceKey(s.store.rows[testKey()].RuntimeRef, ServiceName(testRegion)))
	delete(s.clients.elb.lbs, LoadBalancerName(testNetwork))
	delete(s.clients.secrets.secrets, CredentialSecretName(testConnector, testRegion))

	result := s.run(t)

	if !result.RecordRemoved {
		t.Fatalf("a resumed teardown did not complete: %v / %v", result.Kept, result.Problems)
	}
}

// TestTheSecretsAreForceDeleted. A scheduled deletion reserves the NAME for up to 30 days, so a
// teardown-then-enable cycle inside that window finds EnsureSecret restoring a secret whose credential
// was revoked at teardown. Forcing is the answer chosen; what matters is that one is chosen.
func TestTheSecretsAreForceDeleted(t *testing.T) {
	s := newTeardownScenario()
	s.run(t)

	var deletes int
	for _, call := range s.clients.secrets.calls {
		if !strings.HasPrefix(call, "DeleteSecret:") {
			continue
		}
		deletes++
		if !strings.HasSuffix(call, "force=true") {
			t.Errorf("%s left the name reserved for the recovery window", call)
		}
	}
	if deletes != 3 {
		t.Errorf("deleted %d secrets, want the credential, the bootstrap document and the licence", deletes)
	}
}

// TestTheServiceIsForceDeleted. A service with running tasks cannot be deleted otherwise, and draining
// it first would give a teardown two places to be interrupted instead of one — for no benefit, because
// the credentials are already revoked and a draining task holds no session worth preserving.
func TestTheServiceIsForceDeleted(t *testing.T) {
	s := newTeardownScenario()
	s.run(t)

	for _, call := range s.clients.ecs.calls {
		if strings.HasPrefix(call, "DeleteService:") && !strings.HasSuffix(call, "force=true") {
			t.Errorf("%s: a service with running tasks cannot be deleted without force", call)
		}
	}
}

// TestCapabilityRemovedRevokesAndDeletesNothing. an older connector image that cannot
// parse the `collectors` key is indistinguishable from a capability a customer removed, so deleting on
// absence would turn an image rollback into a customer's Collector disappearing.
func TestCapabilityRemovedRevokesAndDeletesNothing(t *testing.T) {
	s := newTeardownScenario()
	s.in.Reason = collectorcredential.ReasonCapabilityRemoved

	result := s.run(t)

	if result.RevokedCredentials != 1 {
		t.Errorf("revoked %d credentials, want the live one", result.RevokedCredentials)
	}
	if deletes := s.clients.deletes(); len(deletes) != 0 {
		t.Fatalf("capability removal deleted %v", deletes)
	}
	if result.RecordRemoved {
		t.Error("capability removal deleted the Collector record")
	}
	problem := problemWith(t, result.Problems, ProblemTeardownCapabilityRemoved)
	if problem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded: it is still exporting under a configuration nothing updates", problem.Severity)
	}
}

// TestAnUnrecordedNetworkDeletesNothingAndSaysSo. Losing the connector's database loses delete authority
// — the correct direction to fail in — and the customer has to be told, because only a tag scan or a
// hand deletion resolves it.
func TestAnUnrecordedNetworkDeletesNothingAndSaysSo(t *testing.T) {
	s := newTeardownScenario()
	delete(s.store.rows, testKey())

	result := s.run(t)

	if deletes := s.clients.deletes(); len(deletes) != 0 {
		t.Fatalf("deleted %v for a network nothing records", deletes)
	}
	problem := problemWith(t, result.Problems, ProblemTeardownUnrecorded)
	if problem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", problem.Severity)
	}
}

// TestACustomerSuppliedSecurityGroupIsNeverDeleted. `securityGroupIds` REPLACES Odigos's group on the
// tasks, so deleting one a customer chose would take out whatever else uses it.
func TestACustomerSuppliedSecurityGroupIsNeverDeleted(t *testing.T) {
	s := newTeardownScenario()
	s.in.CustomerSecurityGroupIDs = []string{s.store.rows[testKey()].IngressRef}

	result := s.run(t)

	if removed(result, "security group") {
		t.Fatal("a security group the customer supplied was deleted")
	}
	kept := keptOf(t, result, "security group")
	if !strings.Contains(kept.Reason, "collector.regions") {
		t.Errorf("reason = %q", kept.Reason)
	}
}

// TestAFailedListingKeepsEveryySharedObject. Answering "no network needs the cluster" from a failed read
// is how a region's whole Collector fleet loses the cluster it runs in.
func TestAFailedListingKeepsEverySharedObject(t *testing.T) {
	s := newTeardownScenario()
	s.store.listErr = errors.New("the database is unreachable")

	result := s.run(t)

	for _, kind := range []string{"ECS cluster", "log group"} {
		if removed(result, kind) {
			t.Errorf("%s was removed on the strength of a failed listing", kind)
		}
	}
	// The network's own objects still go: they are not shared, so nothing about them depended on the read.
	if !removed(result, "ECS service") {
		t.Errorf("the service was not removed: %v", result.Removed)
	}
}

// TestOnlyTeardownHoldsADestructiveVerb is the source guard, and it is what keeps the split honest.
//
// The converge path's client interfaces contain no delete, so `EnsureService` cannot delete a service
// however it is called and a describe that came back empty cannot be turned into a deletion by a bug
// three functions away. That property is one edit away from being lost, and it would be lost silently.
//
// `DeregisterScalableTarget` in scaling.go is the one written-down exception: switching a network from
// `auto` to `manual` has to remove the target Odigos registered, and that call is gated on the
// same two records this file gates everything on.
func TestOnlyTeardownHoldsADestructiveVerb(t *testing.T) {
	destructive := regexp.MustCompile(`\b(Delete|Deregister)[A-Z]\w*\(ctx`)
	allowed := map[string]map[string]bool{
		"scaling.go": {"DeregisterScalableTarget": true},
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			name == "teardown.go" || name == "clients.go" {
			continue
		}
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, match := range destructive.FindAllString(string(raw), -1) {
			verb := strings.TrimSuffix(match, "(ctx")
			if allowed[name][verb] {
				continue
			}
			t.Errorf("%s calls %s. Destructive verbs belong in teardown.go, whose client interfaces are "+
				"the only ones that declare them — see the exception list in this test", name, verb)
		}
	}
}

// --- helpers ---

func removed(result TeardownResult, kind string) bool {
	for _, r := range result.Removed {
		if strings.HasPrefix(r, kind+" ") {
			return true
		}
	}
	return false
}

func keptOf(t *testing.T, result TeardownResult, kind string) KeptObject {
	t.Helper()
	for _, k := range result.Kept {
		if k.Kind == kind {
			return k
		}
	}
	t.Fatalf("%s is neither removed nor reported as kept; teardown said nothing about it: %v", kind, result.Kept)
	return KeptObject{}
}

func problemsFor(result TeardownResult, code string) []collectorstatus.Problem {
	var out []collectorstatus.Problem
	for _, p := range result.Problems {
		if p.Code == code {
			out = append(out, p)
		}
	}
	return out
}

// smithyAPIError is an EC2-shaped error: a code on a generic APIError rather than a distinct type.
type smithyAPIError struct{ code string }

func (e *smithyAPIError) Error() string                 { return e.code }
func (e *smithyAPIError) ErrorCode() string             { return e.code }
func (e *smithyAPIError) ErrorMessage() string          { return e.code }
func (e *smithyAPIError) ErrorFault() smithy.ErrorFault { return smithy.FaultServer }

var _ smithy.APIError = (*smithyAPIError)(nil)

// TestASecretAlreadyScheduledForDeletionIsLeftAlone.
//
// Deleting a secret only SCHEDULES it, with a recovery window somebody chose. Teardown force-deletes,
// which is unrecoverable — so re-deleting one that is already on its way out would turn a customer's
// recoverable deletion into an unrecoverable one behind their back, and it is the only call here that
// could destroy something a person had deliberately left recoverable.
func TestASecretAlreadyScheduledForDeletionIsLeftAlone(t *testing.T) {
	s := newTeardownScenario()
	scheduled := time.Now().UTC()
	s.clients.secrets.secrets[CredentialSecretName(testConnector, testRegion)].deletedDate = &scheduled

	result := s.run(t)

	// An exact match: the bootstrap secret's name is the credential secret's plus "/bootstrap", so a
	// prefix comparison would report the wrong one.
	scheduledCall := "DeleteSecret:" + CredentialSecretName(testConnector, testRegion) + "/force=true"
	for _, call := range s.clients.secrets.calls {
		if call == scheduledCall {
			t.Fatalf("a secret already scheduled for deletion was deleted again: %s", call)
		}
	}
	// The rest of the teardown is unaffected: one secret somebody is already removing is not a reason to
	// leave a Collector running.
	if !removed(result, "ECS service") {
		t.Errorf("the teardown stopped over a secret that is already going: %v", result.Removed)
	}
	if !result.RecordRemoved {
		t.Errorf("the record was kept: %v / %v", result.Kept, result.Problems)
	}
}
