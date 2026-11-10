package detector

import (
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// Only Fargate workloads may enter the inventory: EC2-launch-type ECS belongs to the cluster agent.
// Capacity-provider services/tasks report an EMPTY launch type, so the filters must read the capacity
// provider (and platform family) too — these matrices pin that.

func TestIsFargateService(t *testing.T) {
	strategy := func(names ...string) []ecstypes.CapacityProviderStrategyItem {
		var out []ecstypes.CapacityProviderStrategyItem
		for _, n := range names {
			out = append(out, ecstypes.CapacityProviderStrategyItem{CapacityProvider: aws.String(n)})
		}
		return out
	}
	cases := []struct {
		name string
		svc  ecstypes.Service
		want bool
	}{
		{"launch type FARGATE", ecstypes.Service{LaunchType: ecstypes.LaunchTypeFargate}, true},
		{"launch type EC2", ecstypes.Service{LaunchType: ecstypes.LaunchTypeEc2}, false},
		{"launch type EXTERNAL", ecstypes.Service{LaunchType: ecstypes.LaunchTypeExternal}, false},
		{"FARGATE capacity provider", ecstypes.Service{CapacityProviderStrategy: strategy("FARGATE")}, true},
		{"FARGATE_SPOT capacity provider", ecstypes.Service{CapacityProviderStrategy: strategy("FARGATE_SPOT")}, true},
		{"mixed spot-first strategy", ecstypes.Service{CapacityProviderStrategy: strategy("FARGATE_SPOT", "FARGATE")}, true},
		{"EC2 ASG capacity provider", ecstypes.Service{CapacityProviderStrategy: strategy("my-asg-provider")}, false},
		{"cluster-default strategy, Fargate platform family", ecstypes.Service{PlatformFamily: aws.String("Linux")}, true},
		{"nothing set", ecstypes.Service{}, false},
		// EC2 launch type wins even if a platform family were reported.
		{"EC2 with platform family", ecstypes.Service{LaunchType: ecstypes.LaunchTypeEc2, PlatformFamily: aws.String("Linux")}, false},
	}
	for _, tc := range cases {
		if got := isFargateService(tc.svc); got != tc.want {
			t.Errorf("%s: isFargateService = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsFargateTask(t *testing.T) {
	cases := []struct {
		name string
		task ecstypes.Task
		want bool
	}{
		{"launch type FARGATE", ecstypes.Task{LaunchType: ecstypes.LaunchTypeFargate}, true},
		{"launch type EC2", ecstypes.Task{LaunchType: ecstypes.LaunchTypeEc2}, false},
		{"FARGATE capacity provider", ecstypes.Task{CapacityProviderName: aws.String("FARGATE")}, true},
		{"FARGATE_SPOT capacity provider", ecstypes.Task{CapacityProviderName: aws.String("FARGATE_SPOT")}, true},
		{"EC2 ASG capacity provider", ecstypes.Task{CapacityProviderName: aws.String("my-asg-provider")}, false},
		{"platform family only", ecstypes.Task{PlatformFamily: aws.String("Linux")}, true},
		{"nothing set", ecstypes.Task{}, false},
	}
	for _, tc := range cases {
		if got := isFargateTask(tc.task); got != tc.want {
			t.Errorf("%s: isFargateTask = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLaunchTypeLabel(t *testing.T) {
	if got := launchTypeLabel("FARGATE", ""); got != "FARGATE" {
		t.Errorf("explicit launch type should pass through, got %q", got)
	}
	if got := launchTypeLabel("", "FARGATE_SPOT"); got != "FARGATE" {
		t.Errorf("capacity-provider tasks should be labeled FARGATE, got %q", got)
	}
	if got := launchTypeLabel("", "my-asg"); got != "" {
		t.Errorf("non-Fargate capacity provider keeps its empty launch type, got %q", got)
	}
}

func TestDeploymentControllerType(t *testing.T) {
	if got := deploymentControllerType(nil); got != string(ecstypes.DeploymentControllerTypeEcs) {
		t.Errorf("nil controller = %q, want ECS default", got)
	}
	dc := &ecstypes.DeploymentController{Type: ecstypes.DeploymentControllerTypeExternal}
	if got := deploymentControllerType(dc); got != "EXTERNAL" {
		t.Errorf("got %q", got)
	}
}

func TestServiceNameFromGroup(t *testing.T) {
	if got := serviceNameFromGroup("service:checkout"); got != "checkout" {
		t.Errorf("got %q", got)
	}
	if got := serviceNameFromGroup("family:one-off"); got != "" {
		t.Errorf("standalone task group should yield empty service, got %q", got)
	}
}

func TestChunk(t *testing.T) {
	got := chunk([]string{"a", "b", "c", "d", "e"}, 2)
	if len(got) != 3 || len(got[0]) != 2 || len(got[2]) != 1 {
		t.Errorf("chunk = %v", got)
	}
	if chunk([]string(nil), 10) != nil {
		t.Error("empty input should chunk to nil")
	}
}

// TestOdigosOwnCollectorIsNotAWorkload. The managed Collector runs as a Fargate service, so without
// this it lands in the discovered-workloads list as something to instrument — and instrumenting it puts
// the two halves of Odigos into a fight: the instrumentor adds its sidecar to the Collector's task
// definition, the Collector deployer re-registers that definition without it, and the pair roll the
// service on every reconcile pass for ever.
//
// Matched on the component tag alone, so a Collector belonging to ANOTHER connector — or one whose
// connector tags a customer stripped — is excluded too. Attribution decides what Odigos may write to;
// this decides what Odigos treats as a customer's application, and no managed Collector is one.
func TestOdigosOwnCollectorIsNotAWorkload(t *testing.T) {
	collectorTags := func(extra map[string]string) []ecstypes.Tag {
		tags := map[string]string{baseapi.TagComponent: baseapi.ComponentCollector}
		for k, v := range extra {
			tags[k] = v
		}
		out := make([]ecstypes.Tag, 0, len(tags))
		for k, v := range tags {
			out = append(out, ecstypes.Tag{Key: aws.String(k), Value: aws.String(v)})
		}
		return out
	}

	for _, tc := range []struct {
		name string
		tags []ecstypes.Tag
		want bool
	}{
		{"this connector's own", collectorTags(map[string]string{
			baseapi.TagConnector: "prod", baseapi.TagConnectorID: "uid-1", baseapi.TagManaged: baseapi.TagManagedTrue,
		}), true},
		{"another connector's", collectorTags(map[string]string{
			baseapi.TagConnector: "staging", baseapi.TagConnectorID: "uid-other",
		}), true},
		{"a Collector whose connector tags were stripped", collectorTags(nil), true},
		// A customer's own application, however it is tagged, stays a workload.
		{"a customer's service", []ecstypes.Tag{{Key: aws.String("team"), Value: aws.String("payments")}}, false},
		{"no tags at all", nil, false},
		// A tag that merely mentions Odigos is not the component marker.
		{"a lookalike", []ecstypes.Tag{{Key: aws.String("odigos-connector"), Value: aws.String("prod")}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isManagedCollector(tc.tags); got != tc.want {
				t.Errorf("isManagedCollector = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestServiceTagsAreAlwaysRequested. The exclusion above works only if the describe asks for tags, and
// it used to ask only when a discovery filter was configured — which is most installations not asking.
func TestServiceTagsAreAlwaysRequested(t *testing.T) {
	source, err := os.ReadFile("fargate-task.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(source)
	i := strings.Index(body, "func clusterFargateServices")
	if i < 0 {
		t.Fatal("clusterFargateServices not found; this test has stopped checking anything")
	}
	fn := body[i:]
	if j := strings.Index(fn, "\nfunc "); j >= 0 {
		fn = fn[:j]
	}
	if !strings.Contains(fn, "Include:  []ecstypes.ServiceField{ecstypes.ServiceFieldTags}") {
		t.Error("DescribeServices does not request TAGS unconditionally, so a managed Collector would not be recognised and would be discovered as an instrumentable workload")
	}
	if strings.Contains(fn, "if len(tags) > 0 {\n\t\t\t\tin.Include") {
		t.Error("TAGS are requested only when a discovery filter is set")
	}
	// And the filter is actually CONSULTED. The matrix above tests the predicate; this tests that the
	// scan calls it — without which a managed Collector is stored as an instrumentable workload and the
	// predicate is dead code that passes its own tests.
	if !strings.Contains(fn, "if isManagedCollector(svc.Tags) {") {
		t.Error("the service scan does not skip managed Collectors, so Odigos's own Collector enters the workload inventory")
	}
}
