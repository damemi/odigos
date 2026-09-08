package engine

import (
	"encoding/json"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// anomalyClaims keeps one suspicious behaviour to one finding.
//
// Every inbound span anchors its own transaction and their subtrees nest, so
// when pricing calls an unknown host that call sits inside pricing's subtree,
// inside coupon's, and inside frontend's. Each of those baselines flags it
// independently. Without this, a single exfiltration attempt lands in the feed
// three times and whoever is triaging has to work out that the three findings
// are the same event.
//
// subTraces yields entries outermost-first, so the finding lands on the
// broadest transaction that flagged it. That is also where it is most useful:
// frontend's finding names the customer-facing endpoint the call happened
// under, which is where an investigation starts, while pricing's would only say
// the call happened.
//
// Suppression is per offending item and only for what actually fired above, so
// a transaction still reports anything the levels above it did not flag. That
// covers the case where an outer baseline already treats a behaviour as normal
// and an inner one does not: nothing is claimed above, and the inner
// transaction reports it as its own finding.
//
// Scoped to a single Process call on a single goroutine: no locking, and no
// coordination between replicas, since a trace is decomposed and scored
// entirely within one pod.
type anomalyClaims struct {
	claimed map[string]struct{}
}

func newAnomalyClaims() *anomalyClaims {
	return &anomalyClaims{claimed: map[string]struct{}{}}
}

// claimKeys identifies what a finding is about within one trace: one key per
// offending item — the specific new peer, edge or library. Keying on the item
// rather than the class is what lets a transaction stay quiet about the peer
// its caller already reported while still reporting a different one of its own.
// Classes that name no discrete items claim the whole class instead: D3 latency
// and D8 payload size measure the entry itself rather than reporting new facts.
func claimKeys(class repo.DeviationClass, ev json.RawMessage) []string {
	items := offendingItems(ev)
	if len(items) == 0 {
		return []string{string(class)}
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, string(class)+"\x00"+item)
	}
	return keys
}

// filter drops the classes whose every offending item already fired higher up
// in this trace, returning what is left to fire on. A class holding even one
// unclaimed item is kept whole rather than trimmed: evidence blobs mix the
// dedup keys with per-class detail (D4 pairs new_deviations with violations),
// and rewriting one without the other would leave the finding inconsistent.
func (c *anomalyClaims) filter(
	deviated []repo.DeviationClass,
	evidence map[string]json.RawMessage,
) ([]repo.DeviationClass, map[string]json.RawMessage) {
	kept := make([]repo.DeviationClass, 0, len(deviated))
	for _, class := range deviated {
		if c.fullyClaimed(class, evidence[string(class)]) {
			metrics.IncAnomalyDuplicateSuppressed(string(class))
			continue
		}
		kept = append(kept, class)
	}
	if len(kept) == len(deviated) {
		return deviated, evidence
	}
	keptEvidence := make(map[string]json.RawMessage, len(kept))
	for _, class := range kept {
		if ev := evidence[string(class)]; ev != nil {
			keptEvidence[string(class)] = ev
		}
	}
	return kept, keptEvidence
}

func (c *anomalyClaims) fullyClaimed(class repo.DeviationClass, ev json.RawMessage) bool {
	for _, key := range claimKeys(class, ev) {
		if _, ok := c.claimed[key]; !ok {
			return false
		}
	}
	return true
}

// take records what a transaction actually fired. Claiming on fire rather than
// on deviation keeps a lenient policy on an outer transaction from silencing a
// stricter one deeper in the trace: a deviation scored below the outer's
// threshold, or dropped by confirm-on-anomaly, stays available to fire below.
func (c *anomalyClaims) take(deviated []repo.DeviationClass, evidence map[string]json.RawMessage) {
	for _, class := range deviated {
		for _, key := range claimKeys(class, evidence[string(class)]) {
			c.claimed[key] = struct{}{}
		}
	}
}
