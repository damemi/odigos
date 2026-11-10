package engine

import (
	"encoding/hex"
	"strconv"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestInterrogationBatchFromTrace_JavaEbpfOnly(t *testing.T) {
	traceID := hex.EncodeToString([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
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

	batch, ok := interrogationBatchFromTrace(&Trace{
		TraceID: traceID,
		Root:    entry,
		Spans:   []*Span{entry, child, other},
	}, nil, nil)
	if !ok {
		t.Fatal("expected java-ebpf batch")
	}
	if len(batch.Transactions) != 1 {
		t.Fatalf("transactions = %d, want 1 (pricing is not java-ebpf)", len(batch.Transactions))
	}
	tx := batch.Transactions[0]
	wantID := repo.NewTransaction("checkout", "default", "GET /buy", repo.KindHTTP)
	if tx.TransactionID != strconv.FormatInt(wantID.ID, 10) || tx.WorkloadName != "checkout" {
		t.Fatalf("unexpected tx: %+v want id %d", tx, wantID.ID)
	}
	if tx.RootSpanName != "GET /buy" || tx.RootSpanKind == "" {
		t.Fatalf("missing root span fields: %+v", tx)
	}
	// Same-workload only: entry + child, not pricing.
	if len(tx.SpanIDs) != 2 {
		t.Fatalf("span ids = %d, want 2", len(tx.SpanIDs))
	}
	wantRaw := string([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	if tx.SpanIDs[0] != wantRaw {
		t.Fatalf("entry span id not raw bytes")
	}

	// An identity the per-service cap dropped gets no trie: the inventory
	// never holds it, so the loop could never analyze it.
	adm := newTransactionAdmission()
	full := &Trace{TraceID: traceID, Root: entry, Spans: []*Span{entry, child, other}}
	if _, ok := interrogationBatchFromTrace(full, nil, adm.contains); ok {
		t.Fatal("expected unadmitted identity to be skipped")
	}
	adm.admit(repo.ServiceScopeKey("default", "checkout"), wantID.ID, 0)
	if _, ok := interrogationBatchFromTrace(full, nil, adm.contains); !ok {
		t.Fatal("expected admitted identity to be recorded")
	}

	// Non-java root yields nothing.
	entry.ResourceAttrs = otherRes
	if _, ok := interrogationBatchFromTrace(&Trace{TraceID: traceID, Root: entry, Spans: []*Span{entry}}, nil, nil); ok {
		t.Fatal("expected non-java-ebpf trace to be skipped")
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
	if sameWorkloadSpan(a, b) {
		t.Fatal("different workloads with the same service.name must not match")
	}
	if !sameWorkloadSpan(a, &Span{Service: "other", ResourceAttrs: k8s("checkout")}) {
		t.Fatal("same workload must match regardless of service.name")
	}
	// service.name is the fallback only when a side lacks a source identity.
	if !sameWorkloadSpan(a, &Span{Service: "checkout"}) {
		t.Fatal("expected service.name fallback when one side has no identity")
	}
	if sameWorkloadSpan(a, &Span{Service: "pricing"}) {
		t.Fatal("fallback must still compare service.name")
	}
}

func TestInterrogationClientRecordNilSafe(t *testing.T) {
	var c *interrogation.Client
	c.Record(interrogation.TraceBatch{})
}
