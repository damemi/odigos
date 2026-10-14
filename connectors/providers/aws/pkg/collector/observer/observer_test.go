package observer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/deployer"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/placement"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// TestManualModeNeverWritesToAWS is the "manual observe-only" criterion, asserted over every call that
// actually reached a client rather than over the interface that declares them.
//
// In manual (IaC) mode the whole stack is the customer's. Odigos owns its configuration and nothing else,
// and a single write here — a tag, a re-registration, a service update "to fix drift" — would be Odigos
// changing infrastructure it was explicitly not given.
func TestManualModeNeverWritesToAWS(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(func(svc *ecstypes.Service) {
		// The customer's own stack: their tags, not Odigos's.
		svc.Tags = ecsTags(map[string]string{
			baseapi.TagComponent: baseapi.ComponentCollector,
			baseapi.TagConnector: testConnector,
			baseapi.TagIaC:       "1",
		})
	})
	clients.elb.withHealthyIngress()
	clients.ecs.withStoppedTask(ecstypes.Task{StopCode: ecstypes.TaskStopCodeUserInitiated})

	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) { rec.Mode = spec.ModeManual })
	observer := testObserver(clients, store, spec.ModeManual)

	if err := observer.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}

	for _, call := range append(clients.ecs.calls, clients.elb.calls...) {
		verb, _, _ := strings.Cut(call, ":")
		if !strings.HasPrefix(verb, "Describe") && !strings.HasPrefix(verb, "List") {
			t.Errorf("manual mode called %q, which is not a read", call)
		}
	}
	// And it did look: an observer that wrote nothing because it read nothing would pass the assertion
	// above and be useless.
	if len(clients.ecs.calls) < 3 || len(clients.elb.calls) == 0 {
		t.Errorf("manual mode barely looked: ecs=%v elb=%v", clients.ecs.calls, clients.elb.calls)
	}
}

// TestManualModeIsWhatPublishesACustomersCollector. Nothing else looks at a Collector the customer
// deployed, so without this pass its row holds no endpoint — and no endpoint means no workload is ever
// pointed at it, which is the whole of.
func TestManualModeIsWhatPublishesACustomersCollector(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.elb.withHealthyIngress()

	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.Mode = spec.ModeManual
		rec.LicenseSecretRef = "arn:aws:secretsmanager:eu-west-1:123456789012:secret:theirs-AbCdEf"
		rec.ScalingMode = "manual"
		// Bounds an earlier pass recorded, which this one does not know and must not null.
		two, nine := 2, 9
		rec.MinCount, rec.MaxCount = &two, &nine
	})
	observer := testObserver(clients, store, spec.ModeManual)

	if err := observer.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}

	// The endpoint, resolved from the load balancer the SERVICE is attached to — not from a name lookup,
	// which would find nothing when the customer named their load balancer something else.
	if store.endpoint == nil {
		t.Fatalf("no endpoint recorded: %v", store.writes)
	}
	if !strings.Contains(store.endpoint[0], testLBDNS) {
		t.Errorf("endpoint = %q, want the discovered load balancer", store.endpoint[0])
	}
	if store.endpoint[2] != "lb" {
		t.Errorf("endpoint source = %q", store.endpoint[2])
	}
	// The ingress tier is 'none', which is the honest value: the rules in front of a customer's
	// Collector are the customer's, and Odigos resolved none.
	if store.endpoint[5] != "none" {
		t.Errorf("ingress tier = %q, want none", store.endpoint[5])
	}

	// The infrastructure the customer built, so a report can name it and #14's teardown can see it is
	// not Odigos's to remove.
	if store.infrastructure == nil {
		t.Fatalf("no infrastructure recorded: %v", store.writes)
	}
	if store.infrastructure.WorkloadRef != serviceARN() || store.infrastructure.FrontendRef != loadBalancerARN {
		t.Errorf("infrastructure = %+v", store.infrastructure)
	}
	if len(store.infrastructure.BackendRefs) != 2 {
		t.Errorf("target group refs = %v", store.infrastructure.BackendRefs)
	}

	// The image actually running, by digest — which in manual mode is the only way anybody learns the
	// customer applied a previous release's artifact.
	if store.image == nil || store.image[0] != testDigest {
		t.Errorf("image = %v, want the running digest", store.image)
	}
	if store.image != nil && store.image[2] != "amd64" {
		t.Errorf("stored image architecture = %q, want amd64 for ECS X86_64", store.image[2])
	}
	// The licence ref is carried through rather than nulled: this accessor writes it alongside the image
	// and the observer does not know it.
	if store.image[3] == "" {
		t.Error("the licence secret ref was nulled by an image observation")
	}

	// The counts observed; the BOUNDS carried through. That accessor writes all seven fields together,
	// so re-writing what it does not own is the only way not to null it.
	if !store.scaling.written || store.scaling.running == nil || *store.scaling.running != 2 {
		t.Errorf("scaling = %+v", store.scaling)
	}
	if store.scaling.mode != "manual" {
		t.Errorf("scaling mode = %q, want the row's own", store.scaling.mode)
	}
	if store.scaling.minC == nil || *store.scaling.minC != 2 || store.scaling.maxC == nil || *store.scaling.maxC != 9 {
		t.Errorf("the bounds were not carried through: min=%v max=%v", store.scaling.minC, store.scaling.maxC)
	}
}

func TestObservedECSArchitectureUsesStoredImageArchitecture(t *testing.T) {
	for _, tc := range []struct {
		name string
		ecs  ecstypes.CPUArchitecture
		want string
	}{
		{"x86", ecstypes.CPUArchitectureX8664, "amd64"},
		{"arm", ecstypes.CPUArchitectureArm64, "arm64"},
		{"unknown", ecstypes.CPUArchitecture("FUTURE_ARCH"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeECS().withRevision(revisionARN(1), func(td *ecstypes.TaskDefinition) {
				td.RuntimePlatform.CpuArchitecture = tc.ecs
			})
			revision, err := describeRevision(context.Background(), api, revisionARN(1))
			if err != nil {
				t.Fatal(err)
			}
			if revision.Architecture != tc.want {
				t.Errorf("architecture = %q, want %q", revision.Architecture, tc.want)
			}
		})
	}
}

// TestAutomaticModeLeavesTheConvergesRowsAlone. Two writers on a multi-column accessor is how half a row
// gets nulled: in automatic mode the converge writes the same objects AND the two things this pass cannot
// read — the ingress tier it resolved and the scalable target's bounds — so writing from here would
// replace those with nothing.
func TestAutomaticModeLeavesTheConvergesRowsAlone(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.elb.withHealthyIngress()

	store := newFakeStore().withRecord(nil)
	observer := testObserver(clients, store, spec.ModeAutomatic)

	if err := observer.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	for _, forbidden := range []string{"RecordInfrastructure", "RecordEndpoint", "RecordImage", "RecordScaling"} {
		if wrote(store.writes, forbidden) {
			t.Errorf("automatic mode wrote %s, which the converge owns: %v", forbidden, store.writes)
		}
	}
	// The row itself is still declared, because every other accessor updates an existing row and does
	// nothing to a missing one.
	if !wrote(store.writes, "Upsert:") {
		t.Errorf("the row was not declared: %v", store.writes)
	}
}

// TestTheOriginComesFromTheTagsOnWhatWasFound, which needs the tags to have been ASKED for: ECS returns
// them only when the describe includes them, and an observer that forgot would classify every Collector
// as the customer's.
func TestTheOriginComesFromTheTagsOnWhatWasFound(t *testing.T) {
	cases := map[string]struct {
		tags       map[string]string
		wantOrigin string
	}{
		"a Collector Odigos created":    {ourTags(), "odigos"},
		"a stack the customer applied":  {map[string]string{baseapi.TagComponent: baseapi.ComponentCollector, baseapi.TagIaC: "1"}, "customer"},
		"something with no Odigos tags": {nil, "customer"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clients := newFakeClients()
			clients.ecs.withService(func(svc *ecstypes.Service) { svc.Tags = ecsTags(tc.tags) })
			clients.elb.withHealthyIngress()
			store := newFakeStore().withRecord(nil)

			if err := testObserver(clients, store, spec.ModeAutomatic).
				ObserveCollector(context.Background(), testNetwork()); err != nil {
				t.Fatalf("ObserveCollector: %v", err)
			}
			if !wrote(store.writes, "Upsert:"+testVPC+"/aws.fargate-collector/automatic/"+tc.wantOrigin) {
				t.Fatalf("origin not recorded as %q: %v", tc.wantOrigin, store.writes)
			}
		})
	}
}

// TestAMissingServiceIsReportedWithoutNullingTheRefs — the recreation criterion's dangerous half.
// Writing empty refs over a Collector whose service is gone would drop teardown's authority over the
// objects that ARE still there, the load balancer and the security group, and leak them silently.
func TestAMissingServiceIsReportedWithoutNullingTheRefs(t *testing.T) {
	clients := newFakeClients()
	clients.elb.withHealthyIngress()
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.Mode = spec.ModeManual
		rec.WorkloadRef = serviceARN()
		rec.FrontendRef = loadBalancerARN
	})

	if err := testObserver(clients, store, spec.ModeManual).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if wrote(store.writes, "RecordInfrastructure") {
		t.Fatalf("the refs were rewritten with a missing service: %v", store.writes)
	}
	if store.status == collectorstatus.StatusReady {
		t.Errorf("a Collector with no service was recorded as %q", store.status)
	}
	if !strings.Contains(store.message, "apply the published deployment artifacts") {
		t.Errorf("the recorded message does not name the customer's next action: %q", store.message)
	}
}

// TestAutomaticRecreationIsReportedAsTheTransitItIs. "customer deleted the workload manually — Auto:
// recreate": the row has to say that a control has already been triggered, so nobody goes looking for one.
func TestAutomaticRecreationIsReportedAsTheTransitItIs(t *testing.T) {
	clients := newFakeClients()
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.WorkloadRef = serviceARN()
		rec.OpAMPLastSeenAt = ago(2 * time.Minute) // stale, not never: it ran once
		rec.Status = collectorstatus.StatusReady
	})

	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if store.status != collectorstatus.StatusDegraded {
		t.Fatalf("status = %q, want Degraded: telemetry is being dropped", store.status)
	}
	if !strings.Contains(store.message, "recreates it on the next converge pass") {
		t.Errorf("the message does not name the recreation: %q", store.message)
	}
}

// TestTheObserverEscalatesAndNeverClearsALiveVerdict.
//
// Two writers share the status column and see different planes. The OpAMP session records the
// Collector's own rejection of a configuration with the agent's validation error verbatim, and nothing
// this package reads can reconstruct that — so overwriting it with "the infrastructure looks fine" would
// delete the only copy of the most specific sentence anybody wrote about that Collector.
func TestTheObserverEscalatesAndNeverClearsALiveVerdict(t *testing.T) {
	rejection := "the Collector rejected this configuration and is still running the last one that applied " +
		"cleanly. It reported: unknown type: \"batchh\""

	// Live session, recorded Degraded from the rejection, and nothing wrong with the infrastructure.
	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.elb.withHealthyIngress()
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.OpAMPLastSeenAt = ago(10 * time.Second)
		rec.Status = collectorstatus.StatusDegraded
		rec.Message = rejection
	})
	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if wrote(store.writes, "SetStatus") {
		t.Fatalf("the session's verdict was overwritten: %v -> %q", store.writes, store.message)
	}

	// The same row, and now the infrastructure IS worse. Escalating is safe and necessary: a Collector
	// whose tasks cannot start is not adequately described by a configuration complaint.
	worse := newFakeClients()
	worse.ecs.withService(func(svc *ecstypes.Service) { svc.RunningCount = 0 })
	worse.ecs.withStoppedTask(ecstypes.Task{
		StopCode: ecstypes.TaskStopCode("TaskFailedToStart"),
		Containers: []ecstypes.Container{{
			Name:   aws.String(deployer.CollectorContainerName),
			Reason: aws.String("CannotPullContainerError: i/o timeout"),
		}},
	})
	worse.elb.withHealthyIngress()
	escalating := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.OpAMPLastSeenAt = ago(10 * time.Second)
		rec.Status = collectorstatus.StatusDegraded
		rec.Message = rejection
	})
	if err := testObserver(worse, escalating, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if escalating.status != collectorstatus.StatusError {
		t.Fatalf("status = %q, want Error: the image cannot be pulled", escalating.status)
	}
}

// TestASilentCollectorsStatusIsTheObserversToMove. With no live session there is no other writer, so
// this pass owns the column outright and may move it in either direction — which is what lets a repaired
// Collector stop being reported as broken.
func TestASilentCollectorsStatusIsTheObserversToMove(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.elb.withHealthyIngress()
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		// Never connected, and a stale Error from an earlier pass.
		rec.OpAMPLastSeenAt = nil
		rec.Status = collectorstatus.StatusError
		rec.Message = "an earlier pass could not pull the image"
	})

	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if store.status != collectorstatus.StatusDegraded {
		t.Fatalf("status = %q, want Degraded: it is running and has never connected", store.status)
	}
	if !strings.Contains(store.message, "never connected") {
		t.Errorf("the message does not diagnose the silence: %q", store.message)
	}
}

// TestAnIdenticalVerdictIsNotRewritten. Steady state is zero writes, the same property every converge
// half in the deployer holds — and here it also stops the row's updated_at churning on every tick.
func TestAnIdenticalVerdictIsNotRewritten(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(func(svc *ecstypes.Service) { svc.RunningCount = 0 })
	clients.elb.withHealthyIngress()
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) { rec.OpAMPLastSeenAt = nil })

	observer := testObserver(clients, store, spec.ModeAutomatic)
	if err := observer.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if !wrote(store.writes, "SetStatus") {
		t.Fatalf("the first pass recorded nothing: %v", store.writes)
	}

	// Feed the verdict back as the row, which is what the next pass reads.
	store.record.Status, store.record.Message = store.status, store.message
	store.writes = nil
	if err := observer.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if wrote(store.writes, "SetStatus") {
		t.Fatalf("an unchanged verdict was rewritten: %v", store.writes)
	}
}

// TestPlacementRefusalsReachTheRowThroughThePass. A network with no usable subnet has no Collector to
// look at, so the refusal reaches its row through the plan the listing resolved — and a network dropped
// from the listing would have no row at all.
func TestPlacementRefusalsReachTheRowThroughThePass(t *testing.T) {
	refusal := collectorstatus.Problem{
		Code:     "CollectorNoUsableSubnet",
		Severity: collectorstatus.SeverityError,
		Message:  "no subnet in " + testVPC + " has a route a Collector could export through",
	}
	planner := &fakePlanner{plan: placement.Plan{
		Kind: "aws.fargate-collector",
		Placements: map[string]placement.Placement{
			testRegion + "/" + testVPC: {Region: testRegion, NetworkID: testVPC, Problems: []collectorstatus.Problem{refusal}},
		},
	}}

	clients := newFakeClients()
	store := newFakeStore().withRecord(nil)
	observer := testObserver(clients, store, spec.ModeAutomatic)
	observer.Planner = planner

	// The listing is what resolves the plan, so the order of the two calls is the coordinator's contract
	// and this test is the only place it is exercised.
	if _, err := observer.CollectorNetworks(context.Background()); err != nil {
		t.Fatalf("CollectorNetworks: %v", err)
	}
	if err := observer.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if store.status != collectorstatus.StatusError {
		t.Fatalf("status = %q, want Error", store.status)
	}
	if !strings.Contains(store.message, "no subnet in "+testVPC) {
		t.Errorf("the refusal is not in the recorded message: %q", store.message)
	}
}

// TestAnUnlistedPassStillObserves. ObserveCollector must not depend on a plan having been cached: the
// coordinator lists first, but a Collector observed without one is still worth reporting on.
func TestAnUnlistedPassStillObserves(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(func(svc *ecstypes.Service) { svc.RunningCount = 0 })
	clients.elb.withHealthyIngress()
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) { rec.OpAMPLastSeenAt = nil })

	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if store.status == "" {
		t.Fatal("nothing was recorded without a cached plan")
	}
}

// TestTheListingIsPlacementsAnswerUnfiltered, refusals included: a network dropped from this list has no
// row to report its refusal on, which turns "this VPC has no subnet a Collector could export from" into
// silence.
func TestTheListingIsPlacementsAnswerUnfiltered(t *testing.T) {
	planner := &fakePlanner{plan: placement.Plan{Kind: "aws.fargate-collector"}}
	observer := testObserver(newFakeClients(), newFakeStore(), spec.ModeAutomatic)
	observer.Planner = planner

	networks, err := observer.CollectorNetworks(context.Background())
	if err != nil {
		t.Fatalf("CollectorNetworks: %v", err)
	}
	if len(networks) != 0 {
		t.Errorf("networks = %v over an empty plan", networks)
	}

	// A planner that cannot answer is an error rather than an empty fleet: an empty list means "this
	// connector has no Collectors", which is a different and reassuring answer.
	observer.Planner = &fakePlanner{err: errThrottled}
	if _, err := observer.CollectorNetworks(context.Background()); err == nil {
		t.Fatal("a failed placement resolution returned no error")
	}
}

func TestManualModeDiscoversCustomerCollectorWithoutConfiguredRegion(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(func(service *ecstypes.Service) {
		tags := baseapi.CollectorIdentityTags(testConnector, testUID, testRegion, testVPC)
		tags[baseapi.TagIaC] = "customer"
		service.Tags = ecsTags(tags)
	})
	clients.elb.withHealthyIngress()
	store := newFakeStore()
	store.missing = true
	observer := testObserver(clients, store, spec.ModeManual)
	observer.Regions = func(context.Context) ([]string, error) { return []string{testRegion}, nil }
	observer.Settings = func() collectorsettings.Settings {
		settings := testSettings()
		settings.Defaults.ClusterRef = ""
		return settings
	}

	networks, err := observer.CollectorNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 1 || networks[0].NetworkID != testVPC || networks[0].ClusterRef == "" {
		t.Fatalf("discovered networks = %+v", networks)
	}
	if err := observer.ObserveCollector(context.Background(), networks[0]); err != nil {
		t.Fatal(err)
	}
	if store.infrastructure == nil || store.infrastructure.WorkloadRef != serviceARN() {
		t.Fatalf("customer service was not recorded: %+v", store.infrastructure)
	}
	if len(store.writes) == 0 || !strings.Contains(store.writes[0], "/customer") {
		t.Fatalf("customer ownership was not retained: %v", store.writes)
	}
	if len(store.endpoint) == 0 || store.endpoint[0] == "" {
		t.Fatalf("customer endpoint was not recorded: %+v", store.endpoint)
	}
}

func TestManualDiscoveryIgnoresForeignCollector(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(func(service *ecstypes.Service) {
		service.Tags = ecsTags(baseapi.CollectorManagedTags("someone-else", "other-id", testRegion, testVPC))
	})
	observer := testObserver(clients, newFakeStore(), spec.ModeManual)
	observer.Regions = func(context.Context) ([]string, error) { return []string{testRegion}, nil }
	networks, err := observer.CollectorNetworks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(networks) != 0 {
		t.Fatalf("foreign Collector was discovered: %+v", networks)
	}
}

func TestManualDiscoveryKeepsUsingRecordedCluster(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	cluster := "arn:aws:ecs:" + testRegion + ":" + testAccount + ":cluster/customer-cluster"
	service := clients.ecs.services[serviceKey(deployer.ClusterName(), deployer.ServiceName(testRegion))]
	service.ClusterArn = aws.String(cluster)
	clients.ecs.services = map[string]*ecstypes.Service{serviceKey(cluster, deployer.ServiceName(testRegion)): service}
	clients.elb.withHealthyIngress()
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.RuntimeRef = cluster
		rec.Mode = spec.ModeManual
	})
	observer := testObserver(clients, store, spec.ModeManual)
	if err := observer.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatal(err)
	}
	if store.infrastructure == nil || store.infrastructure.RuntimeRef != cluster {
		t.Fatalf("recorded cluster was not reused: %+v", store.infrastructure)
	}
}

// TestAFailedReadIsReportedRatherThanReturned. Returning the first error would mean a throttled
// DescribeTargetHealth cost the service state, the task reasons and the endpoint too — a Collector
// reported as unobservable because one of six calls was unlucky.
func TestAFailedReadIsReportedRatherThanReturned(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.elb.withHealthyIngress()
	clients.elb.describeHealthErr = errThrottled

	observation := Observe(context.Background(), clients.ecs, clients.elb,
		ObserveInput{Region: testRegion, NetworkID: testVPC, ClusterRef: deployer.ClusterName()})

	if !observation.ServiceFound || observation.RunningCount != 2 {
		t.Fatalf("the primary facts were lost: %+v", observation)
	}
	if !observation.Partial {
		t.Error("the observation is not marked partial")
	}
	problem := problemWith(t, observation.Problems, ProblemObservationFailed)
	// Info, because the primary facts stand: a Collector must not be reported as faulty for a read that
	// was throttled.
	if problem.Severity != collectorstatus.SeverityInfo {
		t.Errorf("severity = %q, want Info", problem.Severity)
	}
}

// TestAFailedServiceDescribeConcludesNothing. The one read whose failure means the rest cannot be
// judged, so it is Degraded and the pass draws no conclusion from the absence.
func TestAFailedServiceDescribeConcludesNothing(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.describeServiceErr = errThrottled

	observation := Observe(context.Background(), clients.ecs, clients.elb,
		ObserveInput{Region: testRegion, NetworkID: testVPC, ClusterRef: deployer.ClusterName()})

	if observation.ServiceFound || !observation.Partial {
		t.Fatalf("observation = %+v", observation)
	}
	problem := problemWith(t, observation.Problems, ProblemObservationFailed)
	if problem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", problem.Severity)
	}
	if !strings.Contains(problem.Message, "may be perfectly healthy") {
		t.Errorf("the message does not say the Collector was not judged: %q", problem.Message)
	}
	// And nothing else was attempted: there is nothing to read about a service that could not be read.
	if len(clients.elb.calls) > 0 {
		t.Errorf("the load balancer was described anyway: %v", clients.elb.calls)
	}
}

// TestTheEndpointIsNotUnpublishedByAnUnreadablePass. Recording an empty endpoint would re-point every
// workload on the network away from a Collector that is merely unreadable this pass.
func TestTheEndpointIsNotUnpublishedByAnUnreadablePass(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	// No load balancer registered with the fake, so nothing resolves.
	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.Mode = spec.ModeManual
		rec.EndpointGRPC = testLBDNS + ":4317"
	})

	if err := testObserver(clients, store, spec.ModeManual).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if wrote(store.writes, "RecordEndpoint") {
		t.Fatalf("the endpoint was rewritten from an unresolved pass: %v", store.endpoint)
	}
}

// TestAnUnbuildableClientIsAnError, because a pass that could not reach AWS at all has established
// nothing — and the coordinator logs an error from here and moves to the next network, which is exactly
// the right outcome.
func TestAnUnbuildableClientIsAnError(t *testing.T) {
	clients := newFakeClients()
	clients.err = errThrottled
	store := newFakeStore().withRecord(nil)

	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err == nil {
		t.Fatal("no error from an unbuildable client")
	}
	if len(store.writes) > 0 {
		t.Errorf("wrote to the row without reading AWS: %v", store.writes)
	}
}

// TestAMissingRowWithNoServiceDoesNotInventAnOrigin: no AWS object means no ownership tags. The
// provisioning path creates a row as Odigos-owned before it creates resources; treating this absence as
// a customer discovery would make a connector restart permanently remove its own deletion authority.
func TestAMissingRowWithNoServiceDoesNotInventAnOrigin(t *testing.T) {
	clients := newFakeClients()
	clients.elb.withHealthyIngress()
	store := newFakeStore() // no record

	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if len(store.writes) != 0 {
		t.Errorf("a missing service invented Collector state: %v", store.writes)
	}
}

// TestAFoundServiceStillSeedsTheRowFromItsTags is the actual discovery case: an observer that starts
// after a Collector exists must reconstruct its ownership from the AWS tags, not from connector mode.
func TestAFoundServiceStillSeedsTheRowFromItsTags(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(func(svc *ecstypes.Service) { svc.Tags = ecsTags(ourTags()) })
	clients.elb.withHealthyIngress()
	store := newFakeStore() // no recorded row

	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if !wrote(store.writes, "Upsert:"+testVPC+"/aws.fargate-collector/automatic/odigos") {
		t.Errorf("found service was not seeded from its ownership tags: %v", store.writes)
	}
}

// TestTheObserverIsWiredAsTheReadOnlyHalf. The compile-time assertion in the package proves the shape;
// this proves the two calls behave as the coordinator's contract requires — the listing resolves the
// plan, and observation is per network so one network's failure is one network's failure.
func TestTheObserverIsWiredAsTheReadOnlyHalf(t *testing.T) {
	var iface baseapi.CollectorObserver = testObserver(newFakeClients(), newFakeStore(), spec.ModeManual)
	if _, err := iface.CollectorNetworks(context.Background()); err != nil {
		t.Fatalf("CollectorNetworks through the interface: %v", err)
	}
	if err := iface.ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector through the interface: %v", err)
	}
}

// TestTheObserverNeverDowngradesALiveVerdict is the other half of the escalate-only rule, and the half
// that only a weaker verdict reaches: the OpAMP session records an Error it alone can see — a credential
// bound to a different connector, a refused agent description — and an infrastructure finding of
// Degraded must not replace it, because nothing this pass reads could put it back.
func TestTheObserverNeverDowngradesALiveVerdict(t *testing.T) {
	refusal := "this Collector reports a different odigos.cloud.connector.id than its credential is " +
		"bound to, so it is not the Collector Odigos would be configuring"

	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.elb.withHealthyIngress()
	// One unhealthy target: a genuine Degraded finding, and strictly weaker than the recorded Error.
	clients.elb.withTargets(4317,
		target("10.0.0.1", targetHealthy, "", ""),
		target("10.0.1.1", targetUnhealthy, reasonTimeout, "Health checks failed"))

	store := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.OpAMPLastSeenAt = ago(10 * time.Second)
		rec.Status = collectorstatus.StatusError
		rec.Message = refusal
	})
	if err := testObserver(clients, store, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if wrote(store.writes, "SetStatus") {
		t.Fatalf("a Degraded finding replaced the session's Error: %v -> %q", store.writes, store.message)
	}

	// The same finding over a SILENT Collector is written, because then there is no other writer and
	// this pass owns the column — which is what lets a repaired Collector stop reading as broken.
	silent := newFakeStore().withRecord(func(rec *basestore.CollectorRecord) {
		rec.OpAMPLastSeenAt = nil
		rec.Status = collectorstatus.StatusError
		rec.Message = refusal
	})
	if err := testObserver(clients, silent, spec.ModeAutomatic).
		ObserveCollector(context.Background(), testNetwork()); err != nil {
		t.Fatalf("ObserveCollector: %v", err)
	}
	if silent.status != collectorstatus.StatusDegraded {
		t.Errorf("status = %q, want Degraded: nothing else is writing this row", silent.status)
	}
}

// TestOnlyTheCollectorsOwnContainerIsRead. A definition with a second container is a customer's own
// artifact or a sidecar somebody added; taking the image from whichever came first would report a
// sidecar's digest as the Collector's build and turn every such stack into permanent image drift.
func TestOnlyTheCollectorsOwnContainerIsRead(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.ecs.withRevision(revisionARN(1), func(td *ecstypes.TaskDefinition) {
		// The Collector FIRST and the sidecar second, deliberately: a filter that stopped working would
		// let the last container win, and with the Collector last the test would pass either way.
		td.ContainerDefinitions = []ecstypes.ContainerDefinition{
			{
				Name:        aws.String(deployer.CollectorContainerName),
				Image:       aws.String("ghcr.io/odigos-io/odigos-enterprise-collector@" + testDigest),
				Environment: []ecstypes.KeyValuePair{{Name: aws.String("ODIGOS_OPAMP_ENDPOINT"), Value: aws.String(testEndpoint)}},
			},
			{
				Name:        aws.String("their-log-shipper"),
				Image:       aws.String("docker.io/library/fluent-bit@sha256:9999999999999999999999999999999999999999999999999999999999999999"),
				Environment: []ecstypes.KeyValuePair{{Name: aws.String("ODIGOS_OPAMP_ENDPOINT"), Value: aws.String("wss://their-own-thing/opamp")}},
			},
		}
	})
	clients.elb.withHealthyIngress()

	observation := Observe(context.Background(), clients.ecs, clients.elb,
		ObserveInput{Region: testRegion, NetworkID: testVPC, ClusterRef: deployer.ClusterName()})

	if observation.Revision.Digest != testDigest {
		t.Fatalf("digest = %q, want the Collector's own", observation.Revision.Digest)
	}
	if observation.Revision.Environment["ODIGOS_OPAMP_ENDPOINT"] != testEndpoint {
		t.Errorf("the environment came from the wrong container: %v", observation.Revision.Environment)
	}
}

// TestStoppedTasksComeBackNewestFirst. ListTasks does not promise an order, and the diagnosis reports
// the most recent unexpected stop — over an unordered list that would be an arbitrary one, so a
// Collector that recovered would keep reporting the failure it recovered from.
func TestStoppedTasksComeBackNewestFirst(t *testing.T) {
	clients := newFakeClients()
	clients.ecs.withService(nil)
	clients.elb.withHealthyIngress()
	// Installed OLDEST first, which is what makes this a test of the sort rather than of the fake.
	clients.ecs.withStoppedTask(ecstypes.Task{
		StopCode:   ecstypes.TaskStopCode("EssentialContainerExited"),
		StoppedAt:  ago(30 * time.Minute),
		Containers: []ecstypes.Container{{Name: aws.String(deployer.CollectorContainerName), Reason: aws.String("OutOfMemoryError: memory usage")}},
	})
	clients.ecs.withStoppedTask(ecstypes.Task{
		StopCode:   ecstypes.TaskStopCode("TaskFailedToStart"),
		StoppedAt:  ago(1 * time.Minute),
		Containers: []ecstypes.Container{{Name: aws.String(deployer.CollectorContainerName), Reason: aws.String("CannotPullContainerError: i/o timeout")}},
	})

	observation := Observe(context.Background(), clients.ecs, clients.elb,
		ObserveInput{Region: testRegion, NetworkID: testVPC, ClusterRef: deployer.ClusterName()})

	if len(observation.StoppedTasks) != 2 {
		t.Fatalf("stopped tasks = %d", len(observation.StoppedTasks))
	}
	if observation.StoppedTasks[0].Cause() != StopCauseImagePull {
		t.Fatalf("the newest stop is %s, want the image pull that happened a minute ago",
			observation.StoppedTasks[0].Cause())
	}
	// And a task with no timestamp sorts last rather than in front of everything, which is what a naive
	// comparison over a nil pointer would do.
	clients.ecs.withStoppedTask(ecstypes.Task{StopCode: ecstypes.TaskStopCode("TaskFailedToStart")})
	again := Observe(context.Background(), clients.ecs, clients.elb,
		ObserveInput{Region: testRegion, NetworkID: testVPC, ClusterRef: deployer.ClusterName()})
	if again.StoppedTasks[0].Cause() != StopCauseImagePull {
		t.Errorf("a task with no stop time outranked a timestamped one: %+v", again.StoppedTasks[0])
	}
}
