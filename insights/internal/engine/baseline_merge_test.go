package engine

import (
	"encoding/json"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestIsAdditiveClass(t *testing.T) {
	if !isAdditiveClass(repo.D3Latency) || !isAdditiveClass(repo.D7PayloadSize) {
		t.Fatal("D3 and D7 must flush as deltas so MergeData can add buckets")
	}
	for _, class := range []repo.DeviationClass{
		repo.D1CallEdges, repo.D2Egress, repo.D4ArgsReturns,
		repo.D5Libraries, repo.D6DBAccess,
	} {
		if isAdditiveClass(class) {
			t.Fatalf("%s is a set class; flushing a delta would drop members", class)
		}
	}
}

func TestMergeData_D1UnionsAndSorts(t *testing.T) {
	a := json.RawMessage(`{"edges":[{"from":{"svc":"frontend","op":"GET /"},"to":{"svc":"checkout","op":"pay"}},{"from":{"svc":"frontend","op":"GET /"},"to":{"svc":"catalog","op":"list"}}]}`)
	b := json.RawMessage(`{"edges":[{"from":{"svc":"frontend","op":"GET /"},"to":{"svc":"checkout","op":"pay"}},{"from":{"svc":"checkout","op":"pay"},"to":{"svc":"billing","op":"charge"}}]}`)

	got, err := (classMerger{}).MergeData(repo.D1CallEdges, a, b)
	if err != nil {
		t.Fatalf("MergeData: %v", err)
	}
	var d d1Data
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(d.Edges) != 3 {
		t.Fatalf("edges = %+v, want 3 distinct", d.Edges)
	}
	// lessD1Edge is (from.svc, from.op, to.svc, to.op)
	if d.Edges[0].From.Svc != "checkout" || d.Edges[0].To.Svc != "billing" {
		t.Fatalf("first edge = %+v, want checkout→billing", d.Edges[0])
	}
	if d.Edges[1].To.Svc != "catalog" || d.Edges[2].To.Svc != "checkout" {
		t.Fatalf("remaining edges = %+v, want catalog then checkout", d.Edges[1:])
	}

	again, err := (classMerger{}).MergeData(repo.D1CallEdges, got, a)
	if err != nil {
		t.Fatalf("re-merge: %v", err)
	}
	if string(again) != string(got) {
		t.Fatalf("re-merge of a subset must be idempotent:\n first %s\n again %s", got, again)
	}
}

func TestMergeData_D2CoalescesExternalToInMesh(t *testing.T) {
	// Two replicas saw the same {host,port}. One labeled it external, the
	// other in-mesh. External must AND so an in-mesh observation wins - otherwise
	// the flush path would keep growing / flipping the egress profile.
	a := json.RawMessage(`{"peers":[{"host":"stripe.com","port":443,"external":true,"owner":"unknown"}]}`)
	b := json.RawMessage(`{"peers":[{"host":"stripe.com","port":443,"external":false,"owner":"payments"}]}`)

	got, err := (classMerger{}).MergeData(repo.D2Egress, a, b)
	if err != nil {
		t.Fatalf("MergeData: %v", err)
	}
	var d d2Data
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(d.Peers) != 1 {
		t.Fatalf("peers = %+v, want a single {host,port}", d.Peers)
	}
	if d.Peers[0].External {
		t.Fatal("in-mesh observation must stick; External must not stay true after merge")
	}
	if d.Peers[0].Owner != "payments" {
		t.Fatalf("owner = %q, want the in-mesh owner", d.Peers[0].Owner)
	}
}

func TestMergeData_D3AddsHistogramBuckets(t *testing.T) {
	left := make([]uint64, histogram.BucketCount)
	right := make([]uint64, histogram.BucketCount)
	left[3] = 2
	right[3] = 5
	right[7] = 1
	a, _ := json.Marshal(d3Data{Latency: histogram.Histogram{Buckets: left}})
	b, _ := json.Marshal(d3Data{Latency: histogram.Histogram{Buckets: right}})

	got, err := (classMerger{}).MergeData(repo.D3Latency, a, b)
	if err != nil {
		t.Fatalf("MergeData: %v", err)
	}
	var d d3Data
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Latency.Buckets[3] != 7 {
		t.Fatalf("bucket 3 = %d, want 2+5", d.Latency.Buckets[3])
	}
	if d.Latency.Buckets[7] != 1 {
		t.Fatalf("bucket 7 = %d, want 1 from the right replica", d.Latency.Buckets[7])
	}
}

func TestMergeData_D4SeenSurvivesEmptySites(t *testing.T) {
	empty := json.RawMessage(`{}`)
	seen := json.RawMessage(`{"seen":true}`)

	got, err := (classMerger{}).MergeData(repo.D4ArgsReturns, empty, empty)
	if err != nil {
		t.Fatalf("empty merge: %v", err)
	}
	var none d4Data
	if err := json.Unmarshal(got, &none); err != nil {
		t.Fatalf("unmarshal empty: %v", err)
	}
	if none.Seen || len(none.Sites) != 0 {
		t.Fatalf("empty+empty = %+v, want unseen with no sites", none)
	}

	got, err = (classMerger{}).MergeData(repo.D4ArgsReturns, seen, empty)
	if err != nil {
		t.Fatalf("seen merge: %v", err)
	}
	var kept d4Data
	if err := json.Unmarshal(got, &kept); err != nil {
		t.Fatalf("unmarshal seen: %v", err)
	}
	if !kept.Seen {
		t.Fatal("Seen on either replica must survive flush so zero-value D4 can promote")
	}
}

func TestMergeData_UnknownClassIsEmptyPassthrough(t *testing.T) {
	got, err := (classMerger{}).MergeData(repo.DeviationClass("D9_retired"), json.RawMessage(`{"anything":1}`), json.RawMessage(`{"else":2}`))
	if err != nil {
		t.Fatalf("MergeData: %v", err)
	}
	if string(got) != `{}` {
		t.Fatalf("unknown class has no merge; must stay {}, got %s", got)
	}
}

func TestCapClassData_TrimsSortedSetDeterministically(t *testing.T) {
	edges := []d1Edge{
		{From: d1Node{Svc: "a", Op: "1"}, To: d1Node{Svc: "b", Op: "2"}},
		{From: d1Node{Svc: "c", Op: "3"}, To: d1Node{Svc: "d", Op: "4"}},
		{From: d1Node{Svc: "e", Op: "5"}, To: d1Node{Svc: "f", Op: "6"}},
	}
	raw, _ := json.Marshal(d1Data{Edges: edges})

	out, trimmed := capClassData(repo.D1CallEdges, raw, 2)
	if !trimmed {
		t.Fatal("expected trim when edges exceed max")
	}
	var d d1Data
	if err := json.Unmarshal(out, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(d.Edges) != 2 {
		t.Fatalf("capped edges = %+v, want first 2 of the already-sorted set", d.Edges)
	}
	if d.Edges[0] != edges[0] || d.Edges[1] != edges[1] {
		t.Fatalf("cap must keep the first max sorted edges, got %+v", d.Edges)
	}

	same, again := capClassData(repo.D1CallEdges, raw, 3)
	if again || string(same) != string(raw) {
		t.Fatal("at-capacity set must pass through unchanged")
	}
}

func TestCapClassData_AdditiveAndDisabledPassThrough(t *testing.T) {
	hist, _ := json.Marshal(d3Data{Latency: histogram.New()})
	out, trimmed := capClassData(repo.D3Latency, hist, 1)
	if trimmed || string(out) != string(hist) {
		t.Fatal("histogram classes are fixed-size and must not be capped")
	}

	raw := json.RawMessage(`{"edges":[{"from":{"svc":"a","op":"x"},"to":{"svc":"b","op":"y"}}]}`)
	out, trimmed = capClassData(repo.D1CallEdges, raw, 0)
	if trimmed || string(out) != string(raw) {
		t.Fatal("max<=0 disables the safety valve")
	}
}
