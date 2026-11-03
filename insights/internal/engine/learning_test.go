package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

func egressTrace(host, port string) *Trace {
	return mkTrace(
		withRoot("/buy", "frontend", tracepb.Span_SPAN_KIND_SERVER, nil),
		withChild("root", "call "+host, tracepb.Span_SPAN_KIND_CLIENT, map[string]string{
			"server.address": host,
			"server.port":    port,
		}, "lib"),
	)
}

func libsTrace(scopes ...string) *Trace {
	t := mkTrace(withRoot("/libs", "frontend", tracepb.Span_SPAN_KIND_SERVER, nil))
	for i := 0; i+1 < len(scopes); i += 2 {
		t.Spans = append(t.Spans, &Span{ScopeName: scopes[i], ScopeVersion: scopes[i+1]})
	}
	return t
}

// learningTraces mixes traces that grow, reinforce, and carry nothing for each
// learner class, so every branch of merge is exercised in one sequence.
func learningTraces() []*Trace {
	var out []*Trace
	for i := 0; i < 3; i++ {
		out = append(out,
			mkCallTrace("frontend", "pricing"),
			mkCallTrace("frontend", fmt.Sprintf("svc-%d", i)),
			egressTrace("pricing", "8080"),
			egressTrace(fmt.Sprintf("host-%d.example.com", i), "443"),
			libsTrace("okhttp3", "4.11.0", "postgresql", fmt.Sprintf("42.%d", i)),
			latencyTrace(int64(100*(i+1))),
			latencyTrace(1_000_000),
			httpTrace(map[string]string{"http.request.body.size": "100"}),
			httpTrace(map[string]string{"http.request.body.size": fmt.Sprint(1 << (10 + i)), "http.response.body.size": "4096"}),
			// processOne only sees rooted traces; this one carries nothing for any class.
			mkTrace(withRoot("/idle", "idle", tracepb.Span_SPAN_KIND_INTERNAL, nil)),
			d4Trace(probe("com.acme.UserService", "isAdmin", "return.value", "false")),
			d4Trace(probe("com.acme.UserService", fmt.Sprintf("op%d", i%2), "arg.0", "42")),
			dbTrace(map[string]string{"db.system": "postgresql", "db.query.text": "SELECT * FROM users"}),
			dbTrace(map[string]string{"db.system": "postgresql", "db.query.text": fmt.Sprintf("UPDATE t%d SET x = 1", i)}),
		)
	}
	// More distinct values than d4EnumCap, so the enum opens mid-sequence.
	for v := 0; v < 16; v++ {
		out = append(out, d4Trace(probe("com.acme.UserService", "getUser", "arg.0", fmt.Sprint(v))))
	}
	return out
}

func TestLearningMatchesMerge(t *testing.T) {
	scorers := []Scorer{d1CallEdges{}, d2Egress{}, d3Latency{}, d4ArgsReturns{}, d5Libraries{}, d6DBAccess{}, d7PayloadSize{}}
	starts := map[string]json.RawMessage{
		"empty":         nil,
		"unparseable":   json.RawMessage(`{"edges":"nope","peers":3,"libs":{},"latency":7,"request":"x","sites":7,"accesses":"x"}`),
		"non-canonical": json.RawMessage(`{"edges":[{"from":{"svc":"z","op":"z"},"to":{"svc":"a","op":"a"}},{"from":{"svc":"a","op":"a"},"to":{"svc":"b","op":"b"}}],"peers":[{"host":"z","port":1},{"host":"a","port":2}],"libs":[{"ns":"z","ver":"1"},{"ns":"a","ver":"1"}],"accesses":[{"system":"z","ns":"","table":"z","op":"SELECT"},{"system":"a","ns":"","table":"a","op":"SELECT"}],"query_shapes":[{"op":"Z","tables":"z"},{"op":"A","tables":"a"}],"sites":{"com.acme.UserService.getUser":{"args":{"0":{"formats":["int"],"values":["7"]}}}}}`),
	}
	for _, sc := range scorers {
		for name, start := range starts {
			t.Run(fmt.Sprintf("%s/%s", sc.Class(), name), func(t *testing.T) {
				want, got := start, start
				lb := sc.(learner).decodeLearning(got)
				for i, tr := range learningTraces() {
					updated, err := sc.Merge(tr, want)
					if err != nil {
						t.Fatalf("trace %d: merge: %v", i, err)
					}
					wantGrew := baselineGrew(sc, want, updated)
					want = updated

					changed, learnedGrew := lb.merge(tr)
					next := got
					if changed {
						if next, err = lb.encode(); err != nil {
							t.Fatalf("trace %d: encode: %v", i, err)
						}
					}
					gotGrew := learnedGrew
					if !isStabilityComparator(sc) {
						gotGrew = changed && !bytes.Equal(got, next)
					}
					got = next

					if !bytes.Equal(want, got) {
						t.Fatalf("trace %d: stored bytes differ\nmerge:    %s\nlearning: %s", i, want, got)
					}
					if wantGrew != gotGrew {
						t.Fatalf("trace %d: grew = %v, merge path says %v", i, gotGrew, wantGrew)
					}
					// capClassData treats max <= 0 as no cap, so n-1 needs n > 1.
					if n := lb.members(); n > 1 {
						if _, capped := capClassData(sc.Class(), want, n-1); !capped {
							t.Fatalf("trace %d: members() = %d but the stored set is smaller", i, n)
						}
						if _, capped := capClassData(sc.Class(), want, n); capped {
							t.Fatalf("trace %d: members() = %d but the stored set is larger", i, n)
						}
					}
				}
			})
		}
	}
}

func TestLearningDeltaMatchesMerge(t *testing.T) {
	e := &Engine{}
	for _, sc := range []Scorer{d3Latency{}, d7PayloadSize{}} {
		t.Run(string(sc.Class()), func(t *testing.T) {
			var want json.RawMessage
			cs := &classState{additive: true}
			for i, tr := range learningTraces() {
				// Mirror a flush between traces, which must drop the parsed delta.
				if i == 10 {
					want = nil
					cs.setDelta(nil)
				}
				d, err := sc.Merge(tr, emptyOr(want))
				if err != nil {
					t.Fatalf("trace %d: merge: %v", i, err)
				}
				want = d
				e.mergeDelta(cs, sc, tr)
				if !bytes.Equal(want, cs.dataDelta) {
					t.Fatalf("trace %d: delta differs\nmerge:    %s\nlearning: %s", i, want, cs.dataDelta)
				}
			}
		})
	}
}

func TestSetDataDropsLearning(t *testing.T) {
	cs := &classState{}
	sc := d1CallEdges{}
	lb := cs.learningFor(sc)
	lb.merge(mkCallTrace("a", "b"))
	other, _ := json.Marshal(d1Data{Edges: []d1Edge{}})
	cs.setData(other)
	if cs.learning != nil {
		t.Fatal("setData kept a parsed baseline that no longer matches value.Data")
	}
	if got := cs.learningFor(sc).members(); got != 0 {
		t.Fatalf("re-parsed baseline has %d members, want the 0 just stored", got)
	}
}
