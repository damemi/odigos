package engine

import (
	"encoding/json"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func TestCoalesceD2Peers_CollapsesAnnotations(t *testing.T) {
	got := coalesceD2Peers([]peer{
		{Host: "pricing", Port: 8080, Owner: "frontend", External: true},
		{Host: "pricing", Port: 8080, Owner: "frontend", External: false},
		{Host: "pricing", Port: 8080, Owner: "membership", External: false},
		{Host: "stripe.com", Port: 443, Owner: "frontend", External: true},
	})
	if len(got) != 2 {
		t.Fatalf("got %d peers, want 2: %+v", len(got), got)
	}
	if got[0].Host != "pricing" || got[0].Port != 8080 {
		t.Fatalf("first peer = %+v, want pricing:8080", got[0])
	}
	if got[0].External {
		t.Fatalf("pricing should stick to internal once seen in-mesh, got %+v", got[0])
	}
	// Prefer owner from an in-mesh observation; both candidates were internal.
	if got[0].Owner != "frontend" {
		t.Fatalf("owner = %q, want frontend (lexicographic among in-mesh)", got[0].Owner)
	}
	if !got[1].External || got[1].Host != "stripe.com" {
		t.Fatalf("stripe peer = %+v", got[1])
	}
}

func TestD2Merge_AnnotationFlipDoesNotGrow(t *testing.T) {
	sc := d2Egress{}
	// CLIENT leaf → peersFromTrace marks external=true (no nested SERVER).
	tr := mkTrace(
		withRoot("/buy", "frontend", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "call pricing", tracepb.Span_SPAN_KIND_CLIENT, map[string]string{
			"server.address": "pricing",
			"server.port":    "8080",
		}, "lib"),
	)
	// Same host:port already learned as internal (external omitted).
	prior, _ := json.Marshal(d2Data{Peers: []peer{
		{Host: "pricing", Port: 8080, Owner: "frontend"},
	}})
	after, err := sc.Merge(tr, prior)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if baselineGrew(sc, prior, after) {
		t.Fatalf("annotation-only flip grew baseline:\n prior %s\n after %s", prior, after)
	}
}
