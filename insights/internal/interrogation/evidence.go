package interrogation

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
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
func (st *State) observed(read []txEvidence, probes []loop.CustomProbe, t time.Time) ([]transactionEvidence, Event) {
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
