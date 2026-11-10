package loop

import (
	"github.com/odigos-io/odigos/insights/internal/interrogation/db/clickhouse"
	"github.com/odigos-io/odigos/insights/internal/interrogation/schema"
)

type transactionTrie struct {
	Transaction clickhouse.Transaction
	Nodes       []clickhouse.CallTrieNode
}

func buildFullAnalysisRequest(source clickhouse.SourceKey, probes []customProbe, tx transactionTrie, ev AppEvidence) schema.AnalysisRequest {
	return schema.NewFullEvidenceRequest(
		schema.Service{
			Namespace: source.Namespace,
			Kind:      source.WorkloadKind,
			Name:      source.WorkloadName,
			Container: source.ContainerName,
		},
		existingProbes(probes),
		schema.RequestTxn{
			ID:           tx.Transaction.ID,
			RootSpanName: tx.Transaction.RootSpanName,
			RootSpanKind: tx.Transaction.RootSpanKind,
		},
		toRequestGraph(ev.CallGraph),
	)
}

func buildDeltaAnalysisRequest(source clickhouse.SourceKey, probes []customProbe, tx transactionTrie, addedGraph []CallGraphEdge) schema.AnalysisRequest {
	return schema.NewDeltaEvidenceRequest(
		schema.Service{
			Namespace: source.Namespace,
			Kind:      source.WorkloadKind,
			Name:      source.WorkloadName,
			Container: source.ContainerName,
		},
		existingProbes(probes),
		schema.RequestTxn{
			ID:           tx.Transaction.ID,
			RootSpanName: tx.Transaction.RootSpanName,
			RootSpanKind: tx.Transaction.RootSpanKind,
		},
		toRequestGraph(addedGraph),
	)
}

func existingProbes(probes []customProbe) []schema.ExistingProbe {
	// Dedupe by language|target so occupancy matches the apply-path unique count.
	type key struct{ lang, target string }
	seen := make(map[key]bool, len(probes))
	existing := make([]schema.ExistingProbe, 0, len(probes))
	for _, p := range probes {
		k := key{p.Language, p.Target}
		if seen[k] {
			continue
		}
		seen[k] = true
		existing = append(existing, schema.ExistingProbe{
			Language: p.Language,
			Target:   p.Target,
			RuleName: p.RuleName,
		})
	}
	return existing
}

func toRequestGraph(in []CallGraphEdge) []schema.RequestCallEdge {
	out := make([]schema.RequestCallEdge, 0, len(in))
	for _, e := range in {
		out = append(out, schema.RequestCallEdge{Caller: e.Caller, Callee: e.Callee})
	}
	return out
}
