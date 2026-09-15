package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// recommendationMiner turns a promoted transaction's stored relation samples
// into guardrail recommendations. It runs in its own goroutine: the ingest
// path only drops a transaction id into its queue when learning finishes, and
// a periodic sweep picks up whatever the queue missed (a replica restart, a
// transaction promoted by another replica, samples that landed late). Every
// replica runs a miner; the mining-run table acts as a soft claim so they do
// not all mine the same transaction, and mining is deterministic so if two do,
// they write the same rows.
type recommendationMiner struct {
	e     *Engine
	queue chan int64
	wake  chan struct{}
}

const (
	minerQueueSize = 256
	// minerSweepInterval is how often the miner looks for unmined promoted
	// transactions it was not told about.
	minerSweepInterval = time.Minute
	// minerSweepBatch bounds how many transactions one sweep mines.
	minerSweepBatch = 20
	// minerClaimStale is how long another replica's in-progress claim is
	// respected before the transaction is considered up for grabs again.
	minerClaimStale = 10 * time.Minute
	// minerSettleDelay lets the sample writer land the last queued relation
	// samples before a just-promoted transaction is mined.
	minerSettleDelay = 3 * sampleFlushInterval
	// minerRunTimeout bounds one transaction's mining (loads + mining + writes).
	minerRunTimeout = 2 * time.Minute
)

func newRecommendationMiner(e *Engine) *recommendationMiner {
	return &recommendationMiner{e: e, queue: make(chan int64, minerQueueSize), wake: make(chan struct{}, 1)}
}

// requestMine asks for one transaction to be mined soon. Never blocks: a full
// queue drops the request and the periodic sweep picks the transaction up.
func (m *recommendationMiner) requestMine(transactionID int64) {
	select {
	case m.queue <- transactionID:
	default:
	}
}

// requestSweep asks for an immediate sweep (the operator's "recompute all").
func (m *recommendationMiner) requestSweep() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

// run services the queue and the sweep ticker until ctx is cancelled.
func (m *recommendationMiner) run(ctx context.Context) {
	ticker := time.NewTicker(minerSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-m.queue:
			// Samples queued on the ingest path may still be in the writer.
			select {
			case <-ctx.Done():
				return
			case <-time.After(minerSettleDelay):
			}
			m.mine(ctx, id)
		case <-m.wake:
			m.sweep(ctx)
		case <-ticker.C:
			m.sweep(ctx)
		}
	}
}

// sweep mines every promoted transaction that still has unmined samples.
func (m *recommendationMiner) sweep(ctx context.Context) {
	candidates, err := m.e.repo.ListRecommendationMiningCandidates(ctx, len(m.e.scorers), minerClaimStale, minerSweepBatch)
	if err != nil {
		m.e.warn("list recommendation mining candidates", err)
		return
	}
	for _, c := range candidates {
		if ctx.Err() != nil {
			return
		}
		m.mine(ctx, c.TransactionID)
	}
}

// mine runs one transaction end to end: claim, load samples, mine, merge with
// the previous run's rows so operator decisions survive, write, release the
// claim, and refresh the guardrail snapshot so shadow evaluation of the new
// open recommendations starts on this replica right away.
func (m *recommendationMiner) mine(ctx context.Context, transactionID int64) {
	ctx, cancel := context.WithTimeout(ctx, minerRunTimeout)
	defer cancel()
	start := time.Now()
	now := m.e.clock()
	if err := m.e.repo.RecordRecommendationMiningRun(ctx, repo.RecommendationMiningRun{TransactionID: transactionID, ClaimedAt: now}); err != nil {
		m.e.warn("claim recommendation mining run", err)
		metrics.ObserveRecommendationMining("error", time.Since(start).Seconds(), 0)
		return
	}
	recs, sampleCount, err := m.e.mineTransaction(ctx, transactionID)
	if err != nil {
		m.e.warn(fmt.Sprintf("mine recommendations for transaction %d", transactionID), err)
		metrics.ObserveRecommendationMining("error", time.Since(start).Seconds(), 0)
		// Release the claim so the sweep retries instead of waiting it out.
		_ = m.e.repo.RecordRecommendationMiningRun(ctx, repo.RecommendationMiningRun{TransactionID: transactionID, ClaimedAt: now, MinedAt: now})
		return
	}
	if err := m.e.storeMinedRecommendations(ctx, transactionID, recs); err != nil {
		m.e.warn(fmt.Sprintf("store recommendations for transaction %d", transactionID), err)
		metrics.ObserveRecommendationMining("error", time.Since(start).Seconds(), 0)
		return
	}
	if err := m.e.repo.RecordRecommendationMiningRun(ctx, repo.RecommendationMiningRun{
		TransactionID: transactionID, ClaimedAt: now, MinedAt: m.e.clock(), SampleCount: sampleCount, Candidates: len(recs),
	}); err != nil {
		m.e.warn("record recommendation mining run", err)
	}
	result := "ok"
	if len(recs) == 0 {
		result = "empty"
	}
	metrics.ObserveRecommendationMining(result, time.Since(start).Seconds(), len(recs))
	if err := m.e.ReloadGuardrailConfig(ctx); err != nil {
		m.e.warn("reload guardrail config after mining", err)
	}
}

// loadRelationSamples reads a transaction's stored relation samples and
// decodes each into the full trace plus this transaction's sub-trace.
func (e *Engine) loadRelationSamples(ctx context.Context, transactionID int64) ([]relationSample, error) {
	limit := e.system.Get().RelationSamplesPerTransaction
	if limit < repo.MaxRelationSamplesPerTransaction {
		// Read a little past the cap: replicas can overshoot it, and more
		// evidence only makes the result stronger.
		limit = limit * 2
	}
	stored, err := e.repo.ListObservationsWithRaw(ctx, transactionID, repo.RelationSampleReason, limit)
	if err != nil {
		return nil, err
	}
	dims := e.system.Get().TransactionIdentityDimensions
	samples := make([]relationSample, 0, len(stored))
	for _, obs := range stored {
		if len(obs.RawOTLP) == 0 {
			continue
		}
		full, err := traceFromOTLP(obs.RawOTLP, obs.TraceID)
		if err != nil {
			continue
		}
		sample := relationSample{full: full, observedAt: obs.ObservedAt}
		for _, sub := range subTraces(full) {
			if id, ok := subTransactionID(sub, dims); ok && id == transactionID {
				sample.sub = sub
				break
			}
		}
		if sample.sub == nil {
			// The identity scheme changed since this trace was kept; it no
			// longer belongs to this transaction.
			continue
		}
		samples = append(samples, sample)
	}
	return samples, nil
}

// mineTransaction loads a transaction's samples, mines them, and shapes the
// result as recommendation rows (state and timestamps are filled in by
// storeMinedRecommendations). Returns how many samples were used.
func (e *Engine) mineTransaction(ctx context.Context, transactionID int64) ([]repo.Recommendation, int, error) {
	samples, err := e.loadRelationSamples(ctx, transactionID)
	if err != nil {
		return nil, 0, err
	}
	candidates := mineRelations(samples, relationMiningConfigFrom(e.system.Get()))
	now := e.clock()
	recs := make([]repo.Recommendation, 0, len(candidates))
	for rank, c := range candidates {
		recs = append(recs, repo.Recommendation{
			ID:            repo.RecommendationID(transactionID, repo.RecommendationKindAttributeCorrelation, c.spec),
			Kind:          repo.RecommendationKindAttributeCorrelation,
			Scope:         repo.ScopeTransaction,
			ScopeKey:      repo.TransactionScopeKey(transactionID),
			TransactionID: transactionID,
			State:         repo.RecommendationOpen,
			Rank:          rank,
			Spec:          c.spec,
			Transform:     c.transform,
			Transport:     c.transport,
			Observed:      c.observed,
			Held:          c.held,
			Distinct:      c.distinct,
			SampleCount:   len(samples),
			Examples:      c.examples,
			MinedAt:       now,
		})
	}
	return recs, len(samples), nil
}

// storeMinedRecommendations writes a mining run's rows, carrying over each
// existing row's state and creation time so an applied or dismissed
// recommendation stays that way, and marking rows the run no longer produced
// as stale rather than deleting them (an applied rule keeps its provenance).
func (e *Engine) storeMinedRecommendations(ctx context.Context, transactionID int64, mined []repo.Recommendation) error {
	existing, err := e.repo.ListRecommendations(ctx, repo.RecommendationFilter{TransactionID: transactionID, IncludeStale: true})
	if err != nil {
		return err
	}
	byID := make(map[string]repo.Recommendation, len(existing))
	for _, rec := range existing {
		byID[rec.ID] = rec
	}
	now := e.clock()
	rows := make([]repo.Recommendation, 0, len(mined)+len(existing))
	for _, rec := range mined {
		if prev, ok := byID[rec.ID]; ok {
			rec.State = prev.State
			rec.CreatedAt = prev.CreatedAt
			delete(byID, rec.ID)
		} else {
			rec.CreatedAt = now
		}
		rows = append(rows, rec)
	}
	for _, prev := range byID {
		if prev.Stale {
			continue
		}
		prev.Stale = true
		rows = append(rows, prev)
	}
	return e.repo.UpsertRecommendations(ctx, rows)
}
