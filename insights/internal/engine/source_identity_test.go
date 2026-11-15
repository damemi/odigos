package engine

import (
	"context"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestHashTransactionID_SourceIdentity(t *testing.T) {
	base := repo.HashTransactionID("svc", "ns", "GET /", repo.KindHTTP, nil)
	if got := repo.HashTransactionID("svc", "ns", "GET /", repo.KindHTTP, map[string]string{}); got != base {
		t.Errorf("empty source identity changed the id: %d != %d", got, base)
	}
	a := repo.HashTransactionID("svc", "ns", "GET /", repo.KindHTTP, map[string]string{"k8s.container.name": "app", "odigos.workload.name": "a"})
	b := repo.HashTransactionID("svc", "ns", "GET /", repo.KindHTTP, map[string]string{"k8s.container.name": "app", "odigos.workload.name": "b"})
	if a == base || a == b {
		t.Errorf("source identity not hashed: base=%d a=%d b=%d", base, a, b)
	}
}

func TestEngine_SplitsTransactionsBySourceIdentity(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(DefaultMinObservations), nil)

	for _, workload := range []string{"checkout-a", "checkout-b", "checkout-a"} {
		tr := mkTrace(withRoot("GET /items", "checkout", tracepb.Span_SPAN_KIND_SERVER, nil))
		tr.Root.ResourceAttrs = map[string]string{
			"k8s.namespace.name":  "shop",
			"k8s.deployment.name": workload,
		}
		eng.Process(context.Background(), tr)
	}
	if err := eng.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if len(r.transactions) != 2 {
		t.Fatalf("expected 2 transactions (one per workload), got %d", len(r.transactions))
	}
	workloads := map[string]bool{}
	for _, tx := range r.transactions {
		workloads[tx.SourceIdentity["k8s.deployment.name"]] = true
	}
	if !workloads["checkout-a"] || !workloads["checkout-b"] {
		t.Errorf("stored source identities = %v, want checkout-a and checkout-b", workloads)
	}
}
