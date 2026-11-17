package engine

import (
	"context"
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/common/collector/sourceidentity"

	"github.com/odigos-io/odigos/insights/internal/autopilot"
	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestAutopilotTransaction_JavaEbpfOnly(t *testing.T) {
	entryID := hex.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	childID := hex.EncodeToString([]byte{8, 7, 6, 5, 4, 3, 2, 1})
	otherSvcID := hex.EncodeToString([]byte{9, 9, 9, 9, 9, 9, 9, 9})

	javaRes := map[string]string{
		semconv.ServiceName:         "checkout",
		semconv.K8SNamespaceName:    "default",
		"k8s.deployment.name":       "checkout",
		semconv.K8SContainerName:    "app",
		semconv.TelemetryDistroName: javaEbpfDistroName,
	}
	otherRes := map[string]string{
		semconv.ServiceName:         "pricing",
		semconv.K8SNamespaceName:    "default",
		"k8s.deployment.name":       "pricing",
		semconv.K8SContainerName:    "app",
		semconv.TelemetryDistroName: "java-instrumentation",
	}

	entry := &Span{
		SpanID: entryID, Name: "GET /buy", Service: "checkout",
		Kind: tracepb.Span_SPAN_KIND_SERVER, ScopeName: "nethttp", ScopeVersion: "1.0",
		StartUnix: uint64(time.Unix(1, 0).UnixNano()), ResourceAttrs: javaRes,
	}
	child := &Span{
		SpanID: childID, ParentSpanID: entryID, Name: "SELECT", Service: "checkout",
		Kind: tracepb.Span_SPAN_KIND_CLIENT, ResourceAttrs: javaRes,
	}
	other := &Span{
		SpanID: otherSvcID, ParentSpanID: entryID, Name: "POST /calc", Service: "pricing",
		Kind: tracepb.Span_SPAN_KIND_SERVER, ResourceAttrs: otherRes,
	}
	entry.Children = []*Span{child, other}

	// The trie is filed under the transaction processOne resolved for the entry.
	tx := repo.NewTransaction("checkout", "default", "GET /buy", repo.KindHTTP, sourceidentity.FromK8sResourceAttributesMap(javaRes))
	itx, ok := autopilotTransaction(entry, tx, sourceidentity.FromK8sResourceAttributesMap)
	if !ok {
		t.Fatal("expected java-ebpf entry to be recorded")
	}
	if itx.TransactionID != strconv.FormatInt(tx.ID, 10) || itx.WorkloadName != "checkout" || itx.WorkloadKind != "Deployment" {
		t.Fatalf("unexpected tx: %+v want id %d", itx, tx.ID)
	}
	if itx.RootSpanName != "GET /buy" || itx.RootSpanKind == "" {
		t.Fatalf("missing root span fields: %+v", itx)
	}
	// Same-workload only: entry + child, not pricing.
	if len(itx.SpanIDs) != 2 {
		t.Fatalf("span ids = %d, want 2", len(itx.SpanIDs))
	}
	if itx.SpanIDs[0] != string([]byte{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("entry span id not raw bytes")
	}

	// A non-java-ebpf entry (pricing's own sub-transaction) yields nothing.
	pricingTx := repo.NewTransaction("pricing", "default", "POST /calc", repo.KindHTTP, sourceidentity.FromK8sResourceAttributesMap(otherRes))
	if _, ok := autopilotTransaction(other, pricingTx, sourceidentity.FromK8sResourceAttributesMap); ok {
		t.Fatal("expected non-java-ebpf entry to be skipped")
	}
}

// TestProcessOne_ReportsAdmission guards what keeps dropped identities out of
// autopilot: processOne reports ok=false for a sub-transaction the
// per-service cap dropped, so Process never enqueues a trie for it.
func TestProcessOne_ReportsAdmission(t *testing.T) {
	ctx := context.Background()
	eng := New(newFakeRepo(), nil, nil, nil)
	eng.SetSystemConfig(newSystemConfig(repo.SystemSettings{MaxTransactionsPerService: 1}))

	process := func(op string) (repo.Transaction, bool) {
		tr := serverTrace("api", op)
		return eng.processOne(ctx, tr, tr, newAnomalyClaims(tr))
	}
	admitted, ok := process("GET /a")
	if !ok || admitted.Operation != "GET /a" {
		t.Fatalf("first identity: ok=%v tx=%+v, want admitted", ok, admitted)
	}
	if _, ok := process("GET /b"); ok {
		t.Fatal("identity over the per-service cap must not be reported as admitted")
	}
	if _, ok := process("GET /a"); !ok {
		t.Fatal("known identity must stay admitted")
	}
}

func TestSameWorkloadSpan(t *testing.T) {
	k8s := func(workload string) map[string]string {
		return map[string]string{
			semconv.K8SNamespaceName: "default",
			"k8s.deployment.name":    workload,
			semconv.K8SContainerName: "app",
		}
	}
	// Two workloads sharing service.name are different workloads.
	a := &Span{Service: "checkout", ResourceAttrs: k8s("checkout")}
	b := &Span{Service: "checkout", ResourceAttrs: k8s("checkout-canary")}
	if sameWorkloadSpan(a, b, sourceidentity.FromK8sResourceAttributesMap) {
		t.Fatal("different workloads with the same service.name must not match")
	}
	if !sameWorkloadSpan(a, &Span{Service: "other", ResourceAttrs: k8s("checkout")}, sourceidentity.FromK8sResourceAttributesMap) {
		t.Fatal("same workload must match regardless of service.name")
	}
	// service.name is the fallback only when a side lacks a source identity.
	if !sameWorkloadSpan(a, &Span{Service: "checkout"}, sourceidentity.FromK8sResourceAttributesMap) {
		t.Fatal("expected service.name fallback when one side has no identity")
	}
	if sameWorkloadSpan(a, &Span{Service: "pricing"}, sourceidentity.FromK8sResourceAttributesMap) {
		t.Fatal("fallback must still compare service.name")
	}
}

func TestAutopilotClientRecordNilSafe(t *testing.T) {
	var c *autopilot.Client
	c.Record(autopilot.TraceBatch{})
}
