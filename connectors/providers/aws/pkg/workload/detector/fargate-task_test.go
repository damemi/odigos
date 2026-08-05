package detector

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
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
