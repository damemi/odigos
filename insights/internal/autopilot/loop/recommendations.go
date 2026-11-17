package loop

import (
	"strings"

	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

// storedRecommendations maps model recommendations onto tx_recommendations
// rows. Every recommendation is kept, including probes another rule already
// covers: whether to apply one is decided when rules are reconciled, so it can
// change when that other rule does.
func storedRecommendations(source clickhouse.SourceKey, transactionID, revision string, recs []schema.Recommendation) []clickhouse.Recommendation {
	out := make([]clickhouse.Recommendation, 0, len(recs))
	for _, rec := range recs {
		out = append(out, clickhouse.Recommendation{
			Namespace:          source.Namespace,
			WorkloadKind:       source.WorkloadKind,
			WorkloadName:       source.WorkloadName,
			ContainerName:      source.ContainerName,
			TransactionID:      transactionID,
			Language:           rec.Language,
			Target:             probeTarget(rec),
			Category:           rec.Category,
			Function:           rec.Function,
			Why:                strings.TrimSpace(rec.Why),
			ClassName:          rec.Probe.ClassName,
			MethodName:         rec.Probe.MethodName,
			PackageName:        rec.Probe.PackageName,
			FunctionName:       rec.Probe.FunctionName,
			ReceiverName:       rec.Probe.ReceiverName,
			ReceiverMethodName: rec.Probe.ReceiverMethodName,
			Signature:          rec.Probe.Signature,
			AnalysisRevision:   revision,
		})
	}
	return out
}

// recommendationFromStored is the inverse of storedRecommendations, used to
// rebuild rule probes from the store.
func recommendationFromStored(r clickhouse.Recommendation) schema.Recommendation {
	return schema.Recommendation{
		Category: r.Category,
		Function: r.Function,
		Language: r.Language,
		Why:      r.Why,
		Probe: schema.Probe{
			ClassName:          r.ClassName,
			MethodName:         r.MethodName,
			PackageName:        r.PackageName,
			FunctionName:       r.FunctionName,
			ReceiverName:       r.ReceiverName,
			ReceiverMethodName: r.ReceiverMethodName,
			Signature:          r.Signature,
		},
	}
}

// unstoredRecommendations drops recommendations whose (Language, Target) is
// already in existing or earlier in recs, so the store keeps one row per
// probe instead of one per analysis that repeats it.
func unstoredRecommendations(recs, existing []clickhouse.Recommendation) []clickhouse.Recommendation {
	have := make(map[string]bool, len(existing)+len(recs))
	for _, r := range existing {
		have[r.Language+"|"+r.Target] = true
	}
	var out []clickhouse.Recommendation
	for _, r := range recs {
		k := r.Language + "|" + r.Target
		if have[k] {
			continue
		}
		have[k] = true
		out = append(out, r)
	}
	return out
}
