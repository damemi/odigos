package engine

import (
	"context"
	"encoding/hex"
	"maps"
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

	javaIdentity := sourceidentity.FromK8sResourceAttributesMap(javaRes)
	otherIdentity := sourceidentity.FromK8sResourceAttributesMap(otherRes)

	entry := &Span{
		SpanID: entryID, Name: "GET /buy", Service: "checkout",
		Kind: tracepb.Span_SPAN_KIND_SERVER, ScopeName: "nethttp", ScopeVersion: "1.0",
		StartUnix: uint64(time.Unix(1, 0).UnixNano()), ResourceAttrs: javaRes, SourceIdentity: javaIdentity,
	}
	child := &Span{
		SpanID: childID, ParentSpanID: entryID, Name: "SELECT", Service: "checkout",
		Kind: tracepb.Span_SPAN_KIND_CLIENT, ResourceAttrs: javaRes, SourceIdentity: javaIdentity,
	}
	other := &Span{
		SpanID: otherSvcID, ParentSpanID: entryID, Name: "POST /calc", Service: "pricing",
		Kind: tracepb.Span_SPAN_KIND_SERVER, ResourceAttrs: otherRes, SourceIdentity: otherIdentity,
	}
	entry.Children = []*Span{child, other}

	// The call tree is filed under the transaction processOne resolved for the entry.
	tx := repo.NewTransaction("checkout", "default", "GET /buy", repo.KindHTTP, javaIdentity)
	sub := &Trace{Root: entry, Spans: []*Span{entry, child, other}}
	sub.ServiceInvocationSpans = serviceEntrySpans(sub)[0].ServiceInvocationSpans
	itx, ok := autopilotServiceInvocation(sub, tx)
	if !ok {
		t.Fatal("expected java-ebpf entry to be recorded")
	}
	if itx.TransactionID != strconv.FormatInt(tx.ID, 10) || !maps.Equal(itx.SourceIdentity, tx.SourceIdentity) {
		t.Fatalf("unexpected tx: %+v want id %d", itx, tx.ID)
	}
	// Same-workload only: entry + child, not pricing.
	if len(itx.SpanIDs) != 2 {
		t.Fatalf("span ids = %d, want 2", len(itx.SpanIDs))
	}
	if itx.SpanIDs[0] != entryID {
		t.Fatalf("entry span id = %q, want %q", itx.SpanIDs[0], entryID)
	}

	// A non-java-ebpf entry (pricing's own sub-transaction) yields nothing.
	pricingTx := repo.NewTransaction("pricing", "default", "POST /calc", repo.KindHTTP, otherIdentity)
	if _, ok := autopilotServiceInvocation(&Trace{Root: other, Spans: []*Span{other}, ServiceInvocationSpans: []*Span{other}}, pricingTx); ok {
		t.Fatal("expected non-java-ebpf entry to be skipped")
	}
}

// TestProcessOne_ReportsAdmission guards what keeps dropped identities out of
// autopilot: processOne reports ok=false for a sub-transaction the
// per-service cap dropped, so Process never enqueues a call tree for it.
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

func TestAutopilotClientRecordNilSafe(t *testing.T) {
	var c *autopilot.Client
	c.Record(autopilot.TraceBatch{})
}
