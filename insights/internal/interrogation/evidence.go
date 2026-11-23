package interrogation

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	// maxTransactions bounds the scope of a run whose request names no
	// transaction: the ones with the largest call graphs.
	maxTransactions = 5
	// maxEdgesPerTransaction bounds what one transaction adds to the model's
	// request.
	maxEdgesPerTransaction = 600
)

// How a transaction came into a run's scope.
const (
	selectedByUser = "user"
	selectedByAuto = "auto"
)

// txEvidence is one transaction's call graph as a node read it.
type txEvidence struct {
	// Source is the workload and the container the trie was recorded in.
	Source apstore.SourceKey
	Tx     apstore.Transaction
	Nodes  []apstore.CallTreeNode
	Graph  loop.AppEvidence
	// Truncated is whether the request gets only the first
	// maxEdgesPerTransaction edges of Graph.
	Truncated bool
}

// collectEvidence reads the call graph of each transaction in the run's scope
// (tick steps 5, 6, 8). The first reading chooses the scope: the
// transactions the request names, else the maxTransactions with the most
// edges. Later readings read the same transactions, so rounds compare. A
// transaction with no application edges is left out, as the tick skips it.
func collectEvidence(ctx context.Context, store Evidence, st *State) ([]txEvidence, error) {
	workload := workloadSource(st)
	if len(st.Transactions) > 0 {
		var out []txEvidence
		for _, s := range st.Transactions {
			source := workload
			source.ContainerName = s.Container
			e, ok, err := readTransaction(ctx, store, source, apstore.Transaction{ID: s.ID, RootSpanName: s.Name, RootSpanKind: s.Kind})
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, e)
			}
		}
		return out, nil
	}

	requested := map[string]bool{}
	for _, id := range st.Request.TransactionIDs {
		requested[strconv.FormatInt(id, 10)] = true
	}
	sources, err := store.ListSourcesWithTransactions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sources: %w", err)
	}
	var all []txEvidence
	for _, source := range sources {
		if source.Namespace != workload.Namespace || source.WorkloadKind != workload.WorkloadKind || source.WorkloadName != workload.WorkloadName {
			continue
		}
		txs, err := store.ListTransactions(ctx, source)
		if err != nil {
			return nil, fmt.Errorf("list transactions: %w", err)
		}
		for _, tx := range txs {
			if len(requested) > 0 && !requested[tx.ID] {
				continue
			}
			e, ok, err := readTransaction(ctx, store, source, tx)
			if err != nil {
				return nil, err
			}
			if ok {
				all = append(all, e)
			}
		}
	}
	slices.SortFunc(all, func(a, b txEvidence) int {
		if len(requested) == 0 {
			if c := cmp.Compare(len(b.Graph.CallGraph), len(a.Graph.CallGraph)); c != 0 {
				return c
			}
		}
		return cmp.Or(compareIDs(a.Tx.ID, b.Tx.ID), cmp.Compare(a.Source.ContainerName, b.Source.ContainerName))
	})
	if len(requested) == 0 && len(all) > maxTransactions {
		all = all[:maxTransactions]
	}
	return all, nil
}

func readTransaction(ctx context.Context, store Evidence, source apstore.SourceKey, tx apstore.Transaction) (txEvidence, bool, error) {
	nodes, err := store.GetCallTree(ctx, source, tx.ID)
	if err != nil {
		return txEvidence{}, false, fmt.Errorf("get call tree for %s: %w", tx.ID, err)
	}
	graph := loop.BuildAppEvidence(nodes)
	if len(graph.CallGraph) == 0 {
		return txEvidence{}, false, nil
	}
	return txEvidence{
		Source:    source,
		Tx:        tx,
		Nodes:     nodes,
		Graph:     graph,
		Truncated: len(graph.CallGraph) > maxEdgesPerTransaction,
	}, true, nil
}

// requestGraph is the part of e's graph the model is sent.
func (e txEvidence) requestGraph() loop.AppEvidence {
	if e.Truncated {
		return loop.AppEvidence{CallGraph: e.Graph.CallGraph[:maxEdgesPerTransaction]}
	}
	return e.Graph
}

// compareIDs orders transaction ids as the numbers they are.
func compareIDs(a, b string) int {
	x, errA := strconv.ParseInt(a, 10, 64)
	y, errB := strconv.ParseInt(b, 10, 64)
	if errA != nil || errB != nil {
		return cmp.Compare(a, b)
	}
	return cmp.Compare(x, y)
}

// observed records gather's reading: the scope on the run's first, each
// transaction's summary and the round's counts. It returns the request's
// evidence, each transaction marked changed when its graph differs from the
// previous round's, and the evidence event.
func (st *State) observed(read []txEvidence, probes []loop.CustomProbe, timings measure.Timings, window time.Duration, t time.Time) ([]transactionEvidence, Event) {
	counts := evidenceCounts(read, st.edgeCount())
	prev := map[string]string{}
	for _, s := range st.Previous {
		prev[s.Container+"/"+s.ID] = s.Hash
	}
	firstReading := len(st.Scope) == 0
	txs := make([]transactionEvidence, 0, len(read))
	st.Transactions = make([]TransactionSummary, 0, len(read))
	for _, e := range read {
		hash := loop.HashEvidence(e.Graph)
		before, seen := prev[e.Source.ContainerName+"/"+e.Tx.ID]
		txs = append(txs, transactionEvidence{
			Request: loop.BuildFullAnalysisRequest(e.Source, probes, loop.TransactionCallTree{Transaction: e.Tx, Nodes: e.Nodes}, e.requestGraph()),
			Changed: seen && before != hash,
			Timings: requestTimings(timings[e.Tx.ID], window),
		})
		st.Transactions = append(st.Transactions, TransactionSummary{
			ID: e.Tx.ID, Container: e.Source.ContainerName, Name: e.Tx.RootSpanName, Kind: e.Tx.RootSpanKind,
			Edges: len(e.Graph.CallGraph), Functions: evidenceCounts([]txEvidence{e}, 0).Functions, Hash: hash, Truncated: e.Truncated,
		})
		if firstReading {
			id, _ := strconv.ParseInt(e.Tx.ID, 10, 64)
			by := selectedByAuto
			if len(st.Request.TransactionIDs) > 0 {
				by = selectedByUser
			}
			st.Scope = append(st.Scope, ScopeTransaction{TransactionID: repo.TransactionID(id), Operation: e.Tx.RootSpanName, Kind: e.Tx.RootSpanKind, SelectedBy: by})
		}
	}
	st.currentRound().Observed = &counts
	st.currentRound().Timings = st.entryTimings(timings, window)
	return txs, evidenceCollected(st, counts, sampleEdges(read), truncated(read), t)
}

// observedAfter records observe's reading of the round's scope against
// gather's, and returns its event. The summaries stay gather's, so the next
// round compares with what the model was last sent.
func (st *State) observedAfter(read []txEvidence, t time.Time) Event {
	r := st.currentRound()
	before := 0
	if r.Observed != nil {
		before = r.Observed.Edges
	}
	counts := evidenceCounts(read, before)
	r.ObservedAfter = &counts
	return evidenceCollected(st, counts, sampleEdges(read), truncated(read), t)
}

// entryTimings is the entry timings of the transactions in scope that had
// requests in the window.
func (st *State) entryTimings(timings measure.Timings, window time.Duration) []TxTiming {
	var out []TxTiming
	for _, s := range st.Transactions {
		id, _ := strconv.ParseInt(s.ID, 10, 64)
		if e, ok := entryTimings(timings[s.ID], window); ok {
			out = append(out, TxTiming{TransactionID: repo.TransactionID(id), EntryTimings: e})
		}
	}
	return out
}

// measuredProbe is what the spans of target in the scope's transactions
// showed.
func (st *State) measuredProbe(timings measure.Timings, target string, window time.Duration, t time.Time) Measured {
	var sum measure.Measure
	for _, s := range st.Transactions {
		if tx := timings[s.ID]; tx != nil && tx.Sites[target] != nil {
			sum.Merge(tx.Sites[target])
		}
	}
	sum.SortErrors()
	errs := make([]ErrorCount, len(sum.Errors))
	for i, e := range sum.Errors {
		errs[i] = ErrorCount{Message: e.Message, Count: e.Count}
	}
	return Measured{
		WindowSeconds: int(window.Seconds()), MeasuredAt: t, Spans: sum.N, Errors: sum.Err,
		AvgMs: roundMs(sum.AvgMs()), P95Ms: roundMs(sum.P95Ms()), MaxMs: roundMs(maxMs(&sum)), SelfAvgMs: roundMs(sum.SelfAvgMs()),
		TopErrors: errs,
	}
}

func entryTimings(tx *measure.Transaction, window time.Duration) (EntryTimings, bool) {
	if tx == nil || tx.Entry.N == 0 {
		return EntryTimings{}, false
	}
	e := &tx.Entry
	return EntryTimings{
		WindowSeconds: int(window.Seconds()), Requests: e.N, Errors: e.Err,
		AvgMs: roundMs(e.AvgMs()), P95Ms: roundMs(e.P95Ms()), MaxMs: roundMs(maxMs(e)),
		AvgSpans: roundMs(float64(e.Spans) / float64(e.N)), UncoveredAvgMs: roundMs(e.SelfAvgMs()),
	}, true
}

// requestTimings is the model's view of a transaction's timings, nil when it
// had no requests in the window.
func requestTimings(tx *measure.Transaction, window time.Duration) *RequestTimings {
	e, ok := entryTimings(tx, window)
	if !ok {
		return nil
	}
	out := &RequestTimings{requestEntry: requestEntry(e), Probes: []ProbeTiming{}}
	for _, site := range slices.Sorted(maps.Keys(tx.Sites)) {
		m := tx.Sites[site]
		out.Probes = append(out.Probes, ProbeTiming{
			Target: site, Spans: m.N, Errors: m.Err,
			AvgMs: roundMs(m.AvgMs()), P95Ms: roundMs(m.P95Ms()), MaxMs: roundMs(maxMs(m)), SelfAvgMs: roundMs(m.SelfAvgMs()),
		})
	}
	return out
}

func maxMs(m *measure.Measure) float64 {
	return float64(m.MaxUs) / float64(time.Millisecond/time.Microsecond)
}

func roundMs(v float64) float64 {
	return math.Round(v*100) / 100
}

// edgeCount is how many edges the previous round's reading had.
func (st *State) edgeCount() int {
	n := 0
	for _, s := range st.Previous {
		n += s.Edges
	}
	return n
}

// evidenceCounts sums up a reading. Trie rows are only ever added, so the
// growth over the reading before is the number of new edges.
func evidenceCounts(read []txEvidence, before int) EvidenceCounts {
	c := EvidenceCounts{Transactions: len(read)}
	functions := map[string]bool{}
	for _, e := range read {
		c.Edges += len(e.Graph.CallGraph)
		for _, edge := range e.Graph.CallGraph {
			functions[edge.Caller], functions[edge.Callee] = true, true
		}
	}
	c.Functions = len(functions)
	c.NewEdges = max(c.Edges-before, 0)
	return c
}

func sampleEdges(read []txEvidence) []apschema.RequestCallEdge {
	var out []apschema.RequestCallEdge
	for _, e := range read {
		for _, edge := range e.Graph.CallGraph {
			if len(out) == maxSamples {
				return out
			}
			out = append(out, apschema.RequestCallEdge{Caller: edge.Caller, Callee: edge.Callee})
		}
	}
	return out
}

func truncated(read []txEvidence) bool {
	return slices.ContainsFunc(read, func(e txEvidence) bool { return e.Truncated })
}
