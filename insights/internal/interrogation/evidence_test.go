package interrogation

import (
	"context"
	"fmt"
	"slices"
	"testing"

	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
)

// chain is a call graph of n edges, each method calling the next.
func chain(prefix string, n int) [][2]string {
	edges := make([][2]string, n)
	for i := range edges {
		edges[i] = [2]string{fmt.Sprintf("com.claims.%s.m%d", prefix, i), fmt.Sprintf("com.claims.%s.m%d", prefix, i+1)}
	}
	return edges
}

// scopeStore has transactions of the test workload in two containers, with
// call graphs of known sizes, and a busier transaction of another workload.
func scopeStore() *fakeStore {
	s := newFakeStore()
	add := func(container, id string, edges int) {
		s.add(testNamespace, testKind, testWorkload, container, apstore.Transaction{ID: id, RootSpanName: "op " + id, RootSpanKind: "server"}, chain("T"+id, edges)...)
	}
	add("app", "10", 3)
	add("app", "11", 5)
	add("app", "12", 5)
	add("app", "13", 1)
	add("app", "14", 2)
	add("app", "15", 4)
	add("sidecar", "9", 5)
	// Runtime frames only: no application edge. The exporter drops runtime
	// frames, so this stands for a call tree that has no app frame left.
	s.add(testNamespace, testKind, testWorkload, "app", apstore.Transaction{ID: "16"}, [2]string{"java.util.concurrent.ThreadPoolExecutor.runWorker", "java.lang.Thread.run"})
	s.add(testNamespace, testKind, "other", "app", apstore.Transaction{ID: "1"}, chain("Other", 50)...)
	return s
}

func scopeIDs(read []txEvidence) []string {
	var out []string
	for _, e := range read {
		out = append(out, e.Source.ContainerName+"/"+e.Tx.ID)
	}
	return out
}

func TestCollectEvidence_Scope(t *testing.T) {
	tests := []struct {
		name   string
		ids    []int64
		want   []string
		wantBy string
	}{
		{
			name:   "largest call graphs, ties by id",
			want:   []string{"sidecar/9", "app/11", "app/12", "app/15", "app/10"},
			wantBy: selectedByAuto,
		},
		{
			name:   "the request's transactions",
			ids:    []int64{13, 9, 99, 16},
			want:   []string{"sidecar/9", "app/13"},
			wantBy: selectedByUser,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := scopeStore()
			st := runningState()
			st.Request.TransactionIDs = tt.ids
			read, err := collectEvidence(context.Background(), store, st)
			if err != nil {
				t.Fatal(err)
			}
			if got := scopeIDs(read); !slices.Equal(got, tt.want) {
				t.Fatalf("scope %v, want %v", got, tt.want)
			}
			st.observed(read, nil, testStart)
			for i, s := range st.Scope {
				if fmt.Sprint(s.TransactionID) != read[i].Tx.ID || s.SelectedBy != tt.wantBy || s.Operation != read[i].Tx.RootSpanName || s.Kind != "server" {
					t.Errorf("scope[%d] = %+v", i, s)
				}
			}

			// Later readings keep the scope, whatever grows meanwhile.
			store.add(testNamespace, testKind, testWorkload, "app", apstore.Transaction{ID: "20"}, chain("Busy", 40)...)
			again, err := collectEvidence(context.Background(), store, st)
			if err != nil {
				t.Fatal(err)
			}
			if got := scopeIDs(again); !slices.Equal(got, tt.want) {
				t.Fatalf("second reading %v, want the scope %v", got, tt.want)
			}
		})
	}
}

func TestCollectEvidence_Truncates(t *testing.T) {
	store := newFakeStore()
	store.add(testNamespace, testKind, testWorkload, "app", apstore.Transaction{ID: "8841"}, chain("Big", maxEdgesPerTransaction+1)...)
	st := runningState()
	read, err := collectEvidence(context.Background(), store, st)
	if err != nil {
		t.Fatal(err)
	}
	txs, ev := st.observed(read, nil, testStart)
	if n := len(txs[0].Request.CallGraph); n != maxEdgesPerTransaction {
		t.Fatalf("request has %d edges, want %d", n, maxEdgesPerTransaction)
	}
	if s := st.Transactions[0]; s.Edges != maxEdgesPerTransaction+1 || !s.Truncated || s.Functions != maxEdgesPerTransaction+2 {
		t.Fatalf("summary %+v, want the whole graph's counts and truncated", s)
	}
	if ev.Payload["truncated"] != true || ev.Payload["edges"] != maxEdgesPerTransaction+1 {
		t.Fatalf("evidence event %v, want the truncation recorded", ev.Payload)
	}
	if samples := ev.Payload["sample_edges"].([]map[string]any); len(samples) != maxSamples {
		t.Fatalf("%d sample edges, want %d", len(samples), maxSamples)
	}
}

// Rounds compare: a transaction's evidence changed when its graph's hash
// differs from the previous round's reading, and new edges are the growth
// since then. A second reading in a round, a retry's, compares the same way.
func TestObserved_Changes(t *testing.T) {
	store := newFakeStore()
	store.add(testNamespace, testKind, testWorkload, "app", apstore.Transaction{ID: "8841"}, testEdges...)
	st := runningState()
	read := func() []transactionEvidence {
		t.Helper()
		r, err := collectEvidence(context.Background(), store, st)
		if err != nil {
			t.Fatal(err)
		}
		txs, _ := st.observed(r, nil, testStart)
		return txs
	}
	next := func() {
		st.closeRound(RoundObserved, testStart)
		st.openRound(false, testStart)
	}
	if txs := read(); txs[0].Changed || st.currentRound().Observed.NewEdges != 2 {
		t.Fatalf("first reading: changed %v, %+v", txs[0].Changed, st.currentRound().Observed)
	}
	next()
	if txs := read(); txs[0].Changed || st.currentRound().Observed.NewEdges != 0 {
		t.Fatalf("same graph: changed %v, %+v", txs[0].Changed, st.currentRound().Observed)
	}
	store.grow(testTrieKey, [2]string{"com.claims.IntakeService.validate", "com.claims.RulesEngine.check"})
	r, err := collectEvidence(context.Background(), store, st)
	if err != nil {
		t.Fatal(err)
	}
	ev := st.observedAfter(r, testStart)
	if after := st.currentRound().ObservedAfter; after.NewEdges != 1 || after.Edges != 3 || ev.Payload["new_edges"] != 1 {
		t.Fatalf("observed after %+v, want the new edge", after)
	}
	next()
	for attempt := 1; attempt <= 2; attempt++ {
		if txs := read(); !txs[0].Changed || st.currentRound().Observed.NewEdges != 1 {
			t.Fatalf("grown graph, reading %d: changed %v, %+v", attempt, txs[0].Changed, st.currentRound().Observed)
		}
	}
}
