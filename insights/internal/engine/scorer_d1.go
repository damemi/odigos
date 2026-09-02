package engine

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// d1CallEdges scores against the set of directed (parent → child) call edges in
// the trace's span tree, where each node is a (service, operation) tuple; new
// edges deviate. Keying on service+operation (not operation alone) means a call
// into a new service is a new edge even if that operation name exists elsewhere,
// catching lateral movement a name-only key would miss.
//
// JSONB shape:
//
//	{"edges": [{"from": {"svc": "...", "op": "..."}, "to": {"svc": "...", "op": "..."}}, ...]}
type d1CallEdges struct{}

type d1Node struct {
	Svc string `json:"svc"`
	Op  string `json:"op"`
}

type d1Edge struct {
	From d1Node `json:"from"`
	To   d1Node `json:"to"`
}

type d1Data struct {
	Edges []d1Edge `json:"edges"`
}

func (d1CallEdges) Class() repo.DeviationClass { return repo.D1CallEdges }

func (s d1CallEdges) Score(t *Trace, current json.RawMessage) (bool, json.RawMessage, error) {
	return s.decodeBaseline(current).score(t)
}

// decodeBaseline implements baselineDecoder.
func (d1CallEdges) decodeBaseline(raw json.RawMessage) decodedBaseline {
	d, _ := decodeD1(raw)
	return d
}

// score implements decodedBaseline; it only reads baseline.
func (baseline d1Data) score(t *Trace) (bool, json.RawMessage, error) {
	// Nothing learned yet (cold start / re-learn): nothing can deviate.
	if len(baseline.Edges) == 0 {
		return false, nil, nil
	}

	known := map[d1Edge]struct{}{}
	for _, e := range baseline.Edges {
		known[e] = struct{}{}
	}

	var newEdges []d1Edge
	for _, e := range edgesFromTrace(t) {
		if _, ok := known[e]; !ok {
			newEdges = append(newEdges, e)
			known[e] = struct{}{} // de-dup within the same trace
		}
	}
	if len(newEdges) == 0 {
		return false, nil, nil
	}
	ev, err := json.Marshal(map[string]any{"new_edges": newEdges})
	if err != nil {
		return true, nil, err
	}
	return true, ev, nil
}

// d1FanoutMinServices is the number of DISTINCT new target services one request
// must reach to be flagged as fan-out — a breadth that looks like lateral
// spread or enumeration rather than normal, stable dependencies.
const d1FanoutMinServices = 5

// EnrichRisk implements RiskEnricher: it flags a D1 deviation where one request
// introduces many new target services at once. Counts distinct new callees so
// multiple new operations on the same service don't inflate the count.
func (d1CallEdges) EnrichRisk(evidence json.RawMessage, _ enrichConfig) []risk.Signal {
	if len(evidence) == 0 {
		return nil
	}
	var ev struct {
		NewEdges []d1Edge `json:"new_edges"`
	}
	if err := json.Unmarshal(evidence, &ev); err != nil {
		return nil
	}
	services := map[string]struct{}{}
	for _, e := range ev.NewEdges {
		if e.To.Svc != "" {
			services[e.To.Svc] = struct{}{}
		}
	}
	if len(services) < d1FanoutMinServices {
		return nil
	}
	return []risk.Signal{enricherSignal("d1_edge_fanout", risk.CategoryLateralMovement,
		fmt.Sprintf("reached %d new services in one request — possible lateral movement / enumeration", len(services)))}
}

func (d1CallEdges) Merge(t *Trace, current json.RawMessage) (json.RawMessage, error) {
	baseline, _ := decodeD1(current)
	set := map[d1Edge]struct{}{}
	for _, e := range baseline.Edges {
		set[e] = struct{}{}
	}
	for _, e := range edgesFromTrace(t) {
		set[e] = struct{}{}
	}
	merged := make([]d1Edge, 0, len(set))
	for e := range set {
		merged = append(merged, e)
	}
	sort.Slice(merged, func(i, j int) bool { return lessD1Edge(merged[i], merged[j]) })
	return json.Marshal(d1Data{Edges: merged})
}

// lessD1Edge orders edges by (from.svc, from.op, to.svc, to.op) so the
// persisted JSON is deterministic.
func lessD1Edge(a, b d1Edge) bool {
	if a.From.Svc != b.From.Svc {
		return a.From.Svc < b.From.Svc
	}
	if a.From.Op != b.From.Op {
		return a.From.Op < b.From.Op
	}
	if a.To.Svc != b.To.Svc {
		return a.To.Svc < b.To.Svc
	}
	return a.To.Op < b.To.Op
}

func decodeD1(raw json.RawMessage) (d1Data, error) {
	if len(raw) == 0 {
		return d1Data{}, nil
	}
	var d d1Data
	err := json.Unmarshal(raw, &d)
	if err != nil {
		// Unparseable for the current shape: treat as empty so the class
		// re-learns rather than erroring.
		return d1Data{}, err
	}
	return d, nil
}

// Explain implements Explainer: it attributes each new call edge to the child
// span that received it, so the compare view can tint exactly where the
// unfamiliar edge landed. Rebuilds each span's parent edge from the trace tree
// (same (service, operation) key Score/Merge use) and matches it against the
// evidence's new set.
func (d1CallEdges) Explain(evidence json.RawMessage, spans []SpanView) Finding {
	f := Finding{
		Class:       repo.D1CallEdges,
		Kind:        FindingStructural,
		Title:       titleFor(repo.D1CallEdges),
		RawEvidence: evidence,
	}

	var ev struct {
		NewEdges []d1Edge `json:"new_edges"`
	}
	_ = json.Unmarshal(evidence, &ev)

	newEdges := make(map[d1Edge]struct{}, len(ev.NewEdges))
	for _, e := range ev.NewEdges {
		newEdges[e] = struct{}{}
	}

	// Index spans by ID so each can look up its parent and rebuild its edge.
	byID := make(map[string]SpanView, len(spans))
	for _, v := range spans {
		byID[v.SpanID] = v
	}
	for _, v := range spans {
		parent, ok := byID[v.ParentSpanID]
		if !ok {
			continue // root (or parent not retained): no edge to attribute
		}
		edge := d1Edge{
			From: d1Node{Svc: parent.Service, Op: parent.Name},
			To:   d1Node{Svc: v.Service, Op: v.Name},
		}
		if _, ok := newEdges[edge]; !ok {
			continue
		}
		f.SpanHighlights = append(f.SpanHighlights, SpanHighlight{
			SpanID:   v.SpanID,
			Severity: "high",
			Reason:   fmt.Sprintf("%s → %s", edgeNodeLabel(edge.From), edgeNodeLabel(edge.To)),
		})
	}

	f.Summary = summarizeD1(ev.NewEdges)
	return f
}

// summarizeD1 returns a short headline; per-edge detail already renders via the
// highlight list and tinted spans, so repeating it here would be noise.
func summarizeD1(edges []d1Edge) string {
	if len(edges) == 1 {
		return "The anomalous trace introduced a new call edge."
	}
	return "The anomalous trace introduced new call edges."
}

// edgeNodeLabel renders a call-edge endpoint as "service/operation", degrading
// when either half is missing.
func edgeNodeLabel(n d1Node) string {
	switch {
	case n.Svc != "" && n.Op != "":
		return n.Svc + "/" + n.Op
	case n.Svc != "":
		return n.Svc
	default:
		return n.Op
	}
}

// edgesFromTrace walks the span tree and emits a (parent → child) edge for
// every parent/child pair, each node tagged with its service. The root itself
// produces no edge — the transaction IS the root, captured by the Transaction
// table, not D1. Used by Score and Merge.
func edgesFromTrace(t *Trace) []d1Edge {
	out := []d1Edge{}
	var walk func(*Span)
	walk = func(s *Span) {
		for _, c := range s.Children {
			out = append(out, d1Edge{
				From: d1Node{Svc: s.Service, Op: s.Name},
				To:   d1Node{Svc: c.Service, Op: c.Name},
			})
			walk(c)
		}
	}
	walk(t.Root)
	return out
}
