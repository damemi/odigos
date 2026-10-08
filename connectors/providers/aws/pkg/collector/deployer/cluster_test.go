package deployer

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

func clusterInput() ClusterInput {
	return ClusterInput{Region: testRegion, Mode: spec.ModeAutomatic, Ref: "their-cluster"}
}

func TestClusterClientIsReadOnly(t *testing.T) {
	typeOf := reflect.TypeOf((*ClusterClient)(nil)).Elem()
	if typeOf.NumMethod() != 1 || typeOf.Method(0).Name != "DescribeClusters" {
		methods := make([]string, 0, typeOf.NumMethod())
		for i := 0; i < typeOf.NumMethod(); i++ {
			methods = append(methods, typeOf.Method(i).Name)
		}
		t.Fatalf("cluster client methods = %v, want only DescribeClusters", methods)
	}
}

func TestClusterRefIsRequiredBeforeAnyAWSCall(t *testing.T) {
	api := newFakeECS()
	in := clusterInput()
	in.Ref = ""

	got, problems, err := EnsureCluster(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ARN != "" || len(api.calls) != 0 {
		t.Fatalf("cluster = %+v, calls = %v", got, api.calls)
	}
	if !collectorstatus.HasError(problems) || problems[0].Code != ProblemClusterRequired {
		t.Fatalf("problems = %v", codes(problems))
	}
	if !strings.Contains(problems[0].Message, "Provision an ECS cluster first") {
		t.Errorf("message does not explain the prerequisite: %s", problems[0].Message)
	}
}

func TestConfiguredClusterIsValidatedAndNeverMutated(t *testing.T) {
	api := newFakeECS().withCluster("their-cluster", map[string]string{"team": "platform"}, []string{"FARGATE", "FARGATE_SPOT"})

	got, problems, err := EnsureCluster(context.Background(), api, clusterInput())
	if err != nil || len(problems) != 0 {
		t.Fatalf("EnsureCluster: %v / %v", err, problems)
	}
	if got.Name != "their-cluster" || got.ARN == "" || !got.CustomerNamed || !got.SupportsSpot() {
		t.Fatalf("cluster = %+v", got)
	}
	if len(api.calls) != 1 || !strings.HasPrefix(api.calls[0], "DescribeClusters") {
		t.Fatalf("calls = %v, want one read", api.calls)
	}
}

func TestMissingOrInactiveConfiguredClusterIsRefused(t *testing.T) {
	for name, api := range map[string]*fakeECS{
		"missing":  newFakeECS(),
		"inactive": newFakeECS().withCluster("their-cluster", nil, []string{"FARGATE"}),
	} {
		t.Run(name, func(t *testing.T) {
			if cluster := api.clusters["their-cluster"]; name == "inactive" && cluster != nil {
				cluster.Status = strPtr("INACTIVE")
			}
			got, problems, err := EnsureCluster(context.Background(), api, clusterInput())
			if err != nil {
				t.Fatal(err)
			}
			if got.ARN != "" || !collectorstatus.HasError(problems) || problems[0].Code != ProblemClusterMissing {
				t.Fatalf("cluster = %+v, problems = %v", got, codes(problems))
			}
			if len(api.calls) != 1 || !strings.HasPrefix(api.calls[0], "DescribeClusters") {
				t.Fatalf("calls = %v, want one read", api.calls)
			}
		})
	}
}

func TestSpotAssociationIsReportedButNeverRepaired(t *testing.T) {
	api := newFakeECS().withCluster("their-cluster", nil, []string{"FARGATE"})
	in := clusterInput()
	in.SpotWanted = true

	got, problems, err := EnsureCluster(context.Background(), api, in)
	if err != nil || got.ARN == "" {
		t.Fatalf("EnsureCluster: %+v / %v", got, err)
	}
	if len(problems) != 1 || problems[0].Code != ProblemSpotUnavailable || problems[0].Severity != collectorstatus.SeverityDegraded {
		t.Fatalf("problems = %v", problems)
	}
	if len(api.calls) != 1 || !strings.HasPrefix(api.calls[0], "DescribeClusters") {
		t.Fatalf("calls = %v, want no capacity-provider mutation", api.calls)
	}
}

func TestManualModeMakesNoClusterCall(t *testing.T) {
	api := newFakeECS()
	in := clusterInput()
	in.Mode = spec.ModeManual

	got, problems, err := EnsureCluster(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if got.ARN != "" || len(api.calls) != 0 {
		t.Fatalf("touched ECS in manual mode: %+v / %v", got, api.calls)
	}
	if len(problems) != 1 || problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", codes(problems))
	}
}

func strPtr(s string) *string { return &s }
