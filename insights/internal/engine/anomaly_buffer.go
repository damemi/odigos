package engine

import (
	"fmt"
	"sync"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// anomalyBuffer accumulates anomaly firings in memory, keyed by (transaction,
// signature), so a repeating deviation — which re-fires on every matching
// request — is flushed as one summed increment per flush interval instead of one
// row per trace. drain hands the accumulated issues to RecordAnomalyOccurrences
// and resets the map. Every firing is still counted (no suppression): the count
// is the security signal.
type anomalyBuffer struct {
	mu  sync.Mutex
	agg map[string]*anomalyAgg
}

// anomalyAgg is one issue's running tally since the last flush. The newest firing
// wins the display fields (evidence, score, trace); firstSeen keeps the earliest.
type anomalyAgg struct {
	transactionID int64
	signature     string
	classes       []repo.DeviationClass
	offending     string
	occurrence    repo.AnomalyOccurrence // newest firing's display fields
	count         int64
	// transaction-inventory generation this replica believed while counting
	inventoryEpoch int
}

func newAnomalyBuffer() *anomalyBuffer {
	return &anomalyBuffer{agg: map[string]*anomalyAgg{}}
}

// add records one anomaly firing. o carries this single firing's data (LastSeen
// is the firing time, MaxScore its score, Evidence/LastTraceID its trace). Safe
// for concurrent callers (the per-trace path).
//
// inventoryEpoch is the generation the caller believed while counting; it is
// part of the key so counts taken either side of a reset never merge into a
// group drain would have to keep or drop as a whole.
func (b *anomalyBuffer) add(o repo.AnomalyOccurrence, inventoryEpoch int) {
	key := fmt.Sprintf("%d\x00%s\x00%d", o.TransactionID, o.Signature, inventoryEpoch)
	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.agg[key]
	if a == nil {
		a = &anomalyAgg{
			transactionID:  o.TransactionID,
			signature:      o.Signature,
			classes:        o.TriggeredClasses,
			offending:      o.Offending,
			occurrence:     o,
			inventoryEpoch: inventoryEpoch,
		}
		a.occurrence.FirstSeen = o.LastSeen
		b.agg[key] = a
	}
	a.count++
	if o.MaxScore > a.occurrence.MaxScore {
		a.occurrence.MaxScore = o.MaxScore
	}
	if o.LastSeen.After(a.occurrence.LastSeen) {
		a.occurrence.LastSeen = o.LastSeen
		a.occurrence.LastTraceID = o.LastTraceID
		a.occurrence.Evidence = o.Evidence
	}
}

// drain returns the accumulated issues as flush increments and resets the
// buffer. Returns nil when nothing has accumulated (the common case).
//
// Every anomaly names a transaction, so a group left behind by an inventory
// reset is discarded rather than returned: its transaction is gone and writing
// it would put an issue back in the UI that the reset just cleared.
// currentInventoryEpoch is the generation live in storage — see
// liveInventoryEpoch.
func (b *anomalyBuffer) drain(currentInventoryEpoch int) []repo.AnomalyOccurrence {
	b.mu.Lock()
	agg := b.agg
	b.agg = map[string]*anomalyAgg{}
	b.mu.Unlock()
	if len(agg) == 0 {
		return nil
	}
	occs := make([]repo.AnomalyOccurrence, 0, len(agg))
	for _, a := range agg {
		if supersededByReset(a.inventoryEpoch, currentInventoryEpoch) {
			continue
		}
		o := a.occurrence
		o.Occurrences = a.count
		occs = append(occs, o)
	}
	return occs
}

// empty reports whether nothing has accumulated, so the flush can skip reading
// the live inventory generation on the common idle path.
func (b *anomalyBuffer) empty() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.agg) == 0
}
