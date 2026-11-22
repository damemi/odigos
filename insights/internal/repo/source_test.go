package repo

import "testing"

func TestNewSource_K8s(t *testing.T) {
	identity := map[string]string{
		"k8s.namespace.name":   "shop",
		"k8s.container.name":   "app",
		"odigos.workload.kind": "Deployment",
		"odigos.workload.name": "checkout",
	}
	s := NewSource(identity)
	if s.Platform != SourcePlatformK8s || s.K8s == nil {
		t.Fatalf("platform = %q, k8s = %v", s.Platform, s.K8s)
	}
	want := K8sSource{Namespace: "shop", WorkloadKind: "Deployment", WorkloadName: "checkout", ContainerName: "app"}
	if *s.K8s != want {
		t.Errorf("k8s = %+v, want %+v", *s.K8s, want)
	}
	if s.ID != HashSourceID(identity) {
		t.Errorf("id = %d, want %d", s.ID, HashSourceID(identity))
	}
}

func TestNewSource_SemconvWorkloadAttribute(t *testing.T) {
	s := NewSource(map[string]string{
		"k8s.namespace.name":   "shop",
		"k8s.container.name":   "db",
		"k8s.statefulset.name": "orders",
	})
	if s.K8s == nil || s.K8s.WorkloadKind != "StatefulSet" || s.K8s.WorkloadName != "orders" {
		t.Fatalf("k8s = %+v", s.K8s)
	}
}

func TestNewSource_IncompleteIdentityIsUnknown(t *testing.T) {
	for _, identity := range []map[string]string{
		nil,
		{"k8s.namespace.name": "shop", "k8s.deployment.name": "checkout"},
		{"host.name": "vm-1", "service.name": "nginx"},
	} {
		s := NewSource(identity)
		if s.Platform != SourcePlatformUnknown || s.K8s != nil {
			t.Errorf("%v: platform = %q, k8s = %+v", identity, s.Platform, s.K8s)
		}
	}
}

func TestHashSourceID(t *testing.T) {
	a := HashSourceID(map[string]string{"k8s.container.name": "app", "odigos.workload.name": "a"})
	b := HashSourceID(map[string]string{"k8s.container.name": "app", "odigos.workload.name": "b"})
	if a == b || a < 0 || b < 0 {
		t.Errorf("a=%d b=%d", a, b)
	}
	if HashSourceID(nil) != HashSourceID(map[string]string{}) {
		t.Error("nil and empty identity hash differently")
	}
}
