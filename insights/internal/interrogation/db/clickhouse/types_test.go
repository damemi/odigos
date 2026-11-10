package clickhouse

import (
	"testing"

	"github.com/odigos-io/odigos/common/consts"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
)

func TestSourceKeyFromAttributes(t *testing.T) {
	src := SourceKeyFromAttributes(map[string]string{
		semconv.K8SNamespaceName:           "shop",
		consts.OdigosWorkloadKindAttribute: "Deployment",
		consts.OdigosWorkloadNameAttribute: "checkout",
		semconv.K8SContainerName:           "app",
	})
	if !src.K8SComplete() {
		t.Fatal("expected complete k8s source")
	}
	if src.Namespace != "shop" || src.WorkloadName != "checkout" {
		t.Fatalf("got %+v", src)
	}
	attrs := src.lookupAttributes()
	if attrs[consts.OdigosWorkloadKindAttribute] != "Deployment" {
		t.Fatalf("lookup %#v", attrs)
	}
}

func TestSourceKeyLookupFromK8SFields(t *testing.T) {
	src := SourceKey{
		Namespace:     "shop",
		WorkloadKind:  "Deployment",
		WorkloadName:  "checkout",
		ContainerName: "app",
	}
	attrs := src.lookupAttributes()
	if attrs[semconv.K8SNamespaceName] != "shop" || attrs[semconv.K8SContainerName] != "app" {
		t.Fatalf("lookup %#v", attrs)
	}
}

func TestSourceKeyHostOnly(t *testing.T) {
	src := SourceKeyFromAttributes(map[string]string{
		semconv.HostName: "vm-1",
	})
	if src.K8SComplete() {
		t.Fatal("host source should not look like k8s")
	}
	if src.lookupAttributes()[semconv.HostName] != "vm-1" {
		t.Fatalf("lookup %#v", src.lookupAttributes())
	}
}
