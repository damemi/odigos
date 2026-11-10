package awsprovider

import (
	"testing"

	workloadstore "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

func TestCollectorRoutingProgressCountsBothAWSWorkloadKinds(t *testing.T) {
	services := []workloadstore.ECSService{
		{Region: "eu-west-1", ClusterARN: "cluster", ServiceName: "ready", Instrumented: true, RoutingVia: "collector"},
		{Region: "eu-west-1", ClusterARN: "cluster", ServiceName: "failed", Instrumented: true,
			CutoverFailure: workloadstore.CutoverFailure{Reason: "AccessDenied"}},
		{Region: "eu-west-1", ClusterARN: "cluster", ServiceName: "unowned"},
	}
	functions := []workloadstore.LambdaFunction{
		{Region: "eu-west-1", FunctionARN: "function/pending", Instrumented: true},
		{Region: "us-east-1", FunctionARN: "function/other", Instrumented: true},
	}
	progress := collectorRoutingProgress(services, functions)
	if got := progress["eu-west-1"]; got.Instrumented != 3 || got.Routed != 1 || got.Failed != 1 || len(got.FailureRefs) != 1 || got.FailureRefs[0] != "cluster/failed" {
		t.Fatalf("regional progress = %+v", got)
	}
	if got := progress["us-east-1"]; got.Instrumented != 1 || got.Routed != 0 || got.Failed != 0 {
		t.Fatalf("other region progress = %+v", got)
	}
}
