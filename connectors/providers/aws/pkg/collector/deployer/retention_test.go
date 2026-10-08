package deployer

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// ParkService is the reversible half of the retention policy: an idle network keeps its cluster, its
// load balancer, its target groups, its security group, its secrets and its task definition, and stops
// paying for tasks. Coming back is a scale-up rather than a redeployment, which is the whole reason it
// exists instead of tearing down at the first grace period — a redeployment moves the load balancer's
// DNS name, and every instrumented workload on the network is pointed at it.

func parkScenario(t *testing.T, tags map[string]string, desired int32) (*fakeECS, string) {
	t.Helper()
	api := newFakeECS()
	clusterARN := "arn:aws:ecs:" + testRegion + ":" + testAccount + ":cluster/" + ClusterName()
	api.withCluster(ClusterName(), ourTags(""), []string{"FARGATE"})
	api.withService(clusterARN, ServiceName(testRegion), tags, func(s *ecstypes.Service) {
		s.DesiredCount = desired
		s.RunningCount = desired
	})
	return api, clusterARN
}

func parkInput(cluster string) ParkInput {
	return ParkInput{
		Region:        testRegion,
		HostNetworkID: testNetwork,
		Identity:      testIdentity(),
		Mode:          spec.ModeAutomatic,
		ClusterARN:    cluster,
		Recorded:      true,
	}
}

// TestParkingScalesToZeroAndCreatesNothing.
func TestParkingScalesToZeroAndCreatesNothing(t *testing.T) {
	api, cluster := parkScenario(t, ourTags(testNetwork), 3)

	service, problems, err := ParkService(context.Background(), api, parkInput(cluster))
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems = %v", problems)
	}
	if service.DesiredCount != 0 || !service.Updated {
		t.Errorf("service = %+v, want a desired count of zero", service)
	}
	if !api.called("UpdateService") {
		t.Fatalf("no update was made: %v", api.calls)
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "CreateService") {
			t.Error("parking created a service")
		}
	}
}

// TestParkingAnAbsentServiceIsNotAFault, and creates nothing.
//
// An idle network has no placement decision behind it — no subnets, no ingress, no resolved image — so
// there is nothing to create a service FROM, and one created here would be a Collector nobody asked for
// running no tasks in subnets nobody chose. A missing service is also just "somebody removed it", which
// is not a condition worth putting on a row.
func TestParkingAnAbsentServiceIsNotAFault(t *testing.T) {
	api, cluster := parkScenario(t, ourTags(testNetwork), 2)
	delete(api.services, serviceKey(cluster, ServiceName(testRegion)))

	service, problems, err := ParkService(context.Background(), api, parkInput(cluster))
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if len(problems) != 0 || service.ARN != "" || service.Updated {
		t.Errorf("problems=%v service=%+v, want silence", problems, service)
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "CreateService") || strings.HasPrefix(call, "UpdateService") {
			t.Errorf("parking wrote to an absent service: %s", call)
		}
	}
}

// TestParkingIsIdempotent. An idle network is visited on every pass for as long as it stays idle, so a
// parked Collector must cost zero AWS writes per pass — the same steady-state property the converge path
// has, and for the same reason: a write per pass is a fleet that never stops changing.
func TestParkingIsIdempotent(t *testing.T) {
	api, cluster := parkScenario(t, ourTags(testNetwork), 0)

	if _, _, err := ParkService(context.Background(), api, parkInput(cluster)); err != nil {
		t.Fatalf("park: %v", err)
	}
	if api.called("UpdateService") {
		t.Errorf("an already-parked service was written to again: %v", api.calls)
	}
}

// TestParkingRefusesAServiceOdigosMayNotWriteTo. Scaling somebody else's workload down is a more
// surprising thing to do to a customer than declining to save them money.
func TestParkingRefusesAServiceOdigosMayNotWriteTo(t *testing.T) {
	for name, tags := range map[string]map[string]string{
		"another connector's": baseapi.CollectorManagedTags("other", "uid-other", testRegion, testNetwork),
		"untagged":            {},
	} {
		t.Run(name, func(t *testing.T) {
			api, cluster := parkScenario(t, tags, 4)

			service, problems, err := ParkService(context.Background(), api, parkInput(cluster))
			if err != nil {
				t.Fatalf("park: %v", err)
			}
			if service.Updated || api.called("UpdateService") {
				t.Fatalf("a service Odigos may not write to was scaled: %v", api.calls)
			}
			if !hasCode(problems, ProblemServiceNameConflict) {
				t.Errorf("problems = %v, want the refusal reported", codes(problems))
			}
		})
	}
}

// TestParkingRefusesInManualMode. Belt over braces: the coordinator wires no deployer for a manual-mode
// type, so nothing can reach this — and this package moves replica counts, so "the caller could not have
// been reached" is a worse guarantee than "the function refuses".
func TestParkingRefusesInManualMode(t *testing.T) {
	api, cluster := parkScenario(t, ourTags(testNetwork), 2)
	in := parkInput(cluster)
	in.Mode = spec.ModeManual

	_, problems, err := ParkService(context.Background(), api, in)
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if !hasCode(problems, ProblemManualModeProvisioning) {
		t.Errorf("problems = %v, want the mode guard", codes(problems))
	}
	if api.called("UpdateService") {
		t.Error("manual mode scaled a service")
	}
}

// TestParkingNeedsTheRecord too: scaling is a mutation, and the mutation gate is the same one the
// converge uses rather than a second reading of the tags.
func TestParkingReadsTheSameOwnershipGateAsTheConverge(t *testing.T) {
	api, cluster := parkScenario(t, ourTags(testNetwork), 2)
	in := parkInput(cluster)
	in.Identity = collectorownership.Identity{ConnectorName: "someone-else", ConnectorID: "uid-else"}

	_, problems, err := ParkService(context.Background(), api, in)
	if err != nil {
		t.Fatalf("park: %v", err)
	}
	if api.called("UpdateService") {
		t.Fatal("another connector's Collector was scaled to zero")
	}
	if !hasCode(problems, ProblemServiceNameConflict) {
		t.Errorf("problems = %v", codes(problems))
	}
}

// TestClusterNameOfTakesTheLastSegment. Application Auto Scaling addresses a service as
// `service/<cluster NAME>/<service name>` and refuses the ARN every other call in this package passes
// around, so a parked Collector's target could not be deregistered without this — and the error it
// returns quotes the resource id back without saying which half is wrong.
func TestClusterNameOfTakesTheLastSegment(t *testing.T) {
	for in, want := range map[string]string{
		"arn:aws:ecs:eu-west-1:123456789012:cluster/odigos-collector": "odigos-collector",
		"odigos-collector": "odigos-collector",
		"":                 "",
	} {
		if got := clusterNameOf(in); got != want {
			t.Errorf("clusterNameOf(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTheLogGroupOwnershipMarkerIsWrittenOnlyOnCreation.
//
// EnsureLogGroup is the one place in this package that writes into an object without checking ownership
// first — a log group is append-only and holds nothing but the Collector's own stdout, so sharing one
// with a customer costs less than refusing a Collector over a plausible name. That permissiveness is
// exactly why the ownership marker may not be written on the already-exists path: this branch is reached
// both by a group Odigos created and by a customer's own, so stamping `odigos-managed` there would hand
// teardown the right to delete a log group Odigos did not create — and take the customer's logs with it.
func TestTheLogGroupOwnershipMarkerIsWrittenOnlyOnCreation(t *testing.T) {
	in := LogGroupInput{
		Connector: testConnector, ConnectorID: testUID, Partition: "aws",
		AccountID: testAccount, Region: testRegion, RetentionDays: 7, Mode: spec.ModeAutomatic,
	}

	created := newFakeLogs()
	if _, _, err := EnsureLogGroup(context.Background(), created, in); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := created.groups[LogGroupName(testConnector)]; got[baseapi.TagManaged] != baseapi.TagManagedTrue {
		t.Errorf("a log group Odigos CREATED does not carry the ownership marker: %v", got)
	}

	// Second pass: the group exists, so the tag refresh runs. It must not stamp ownership.
	existing := newFakeLogs()
	existing.groups[LogGroupName(testConnector)] = map[string]string{}
	if _, _, err := EnsureLogGroup(context.Background(), existing, in); err != nil {
		t.Fatalf("converge: %v", err)
	}
	refreshed := existing.tagged[logGroupARN("aws", testRegion, testAccount, LogGroupName(testConnector))]
	if _, stamped := refreshed[baseapi.TagManaged]; stamped {
		t.Errorf("converging an EXISTING log group stamped the ownership marker (%v), which would let "+
			"teardown delete a customer's log group and their logs with it", refreshed)
	}
	// The identity tags are still written: attribution is the stated reason this branch tags at all.
	if refreshed[baseapi.TagConnector] != testConnector || refreshed[baseapi.TagComponent] != baseapi.ComponentCollector {
		t.Errorf("the identity tags were not refreshed: %v", refreshed)
	}
}

var _ = aws.String
var _ = time.Second
