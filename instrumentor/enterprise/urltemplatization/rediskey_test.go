package urltemplatization

import (
	"testing"

	"github.com/odigos-io/odigos/api/k8sconsts"
)

func TestParseUnmatchedRedisKey(t *testing.T) {
	meta, err := parseUnmatchedRedisKey("odigos:urltempl:unmatched:server:payments/Deployment/checkout/app")
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Server {
		t.Fatal("expected server key")
	}
	if meta.Workload != (k8sconsts.PodWorkload{
		Namespace: "payments",
		Kind:      k8sconsts.WorkloadKindDeployment,
		Name:      "checkout",
	}) {
		t.Fatalf("workload=%+v", meta.Workload)
	}
	if meta.ContainerName != "app" {
		t.Fatalf("container=%q", meta.ContainerName)
	}

	meta, err = parseUnmatchedRedisKey("odigos:urltempl:unmatched:client:default/Deployment/frontend/web")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Server {
		t.Fatal("expected client key")
	}
	if meta.ContainerName != "web" {
		t.Fatalf("container=%q", meta.ContainerName)
	}

	if _, err := parseUnmatchedRedisKey("odigos:urltempl:other:x"); err == nil {
		t.Fatal("expected error for unrecognized key")
	}
	if _, err := parseUnmatchedRedisKey("odigos:urltempl:unmatched:server:ns/Deployment"); err == nil {
		t.Fatal("expected error for incomplete key")
	}
}
