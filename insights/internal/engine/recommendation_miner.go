package engine

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// recommendationMiner turns learned traffic into guardrail recommendations: a
// promoted transaction's stored relation samples into attribute correlations,
// and a fully-learned service's profile into a guardrail card. It runs in its
// own goroutine: the ingest path only drops a transaction id (and its service)
// into the queues when learning finishes, and periodic sweeps pick up whatever
// the queues missed (a transaction promoted on another replica, samples that
// landed late, a guardrail the operator edited, a restart). The miner is a
// singleton on the leader, so a request handled on a follower is not mined
// until the leader's next sweep (up to minerSweepInterval for a transaction,
// minerServiceSweepInterval for a service card). The mining-run table records
// each completed run so the sweep knows what still needs work; mining is
// deterministic so re-mining a transaction writes the same rows.
type recommendationMiner struct {
	e        *Engine
	queue    chan int64
	services chan serviceRef
	wake     chan struct{}
}

// serviceRef names one service to mine a guardrail card for.
type serviceRef struct {
	namespace, service string
}

const (
	minerQueueSize = 256
	// minerSweepInterval is how often the miner looks for unmined promoted
	// transactions it was not told about.
	minerSweepInterval = time.Minute
	// minerServiceSweepInterval is how often open service cards are refreshed
	// against the current guardrails and profiles. Slower than the transaction
	// sweep: a card costs a few profile queries, and it only changes when an
	// operator edits a guardrail or a service finishes learning.
	minerServiceSweepInterval = 5 * time.Minute
	// minerSweepBatch bounds how many transactions one sweep mines.
	minerSweepBatch = 20
	// minerSettleDelay lets the sample writer land the last queued relation
	// samples before a just-promoted transaction is mined.
	minerSettleDelay = 3 * sampleFlushInterval
	// minerRunTimeout bounds one transaction's mining (loads + mining + writes).
	minerRunTimeout = 2 * time.Minute
)

func newRecommendationMiner(e *Engine) *recommendationMiner {
	return &recommendationMiner{
		e:        e,
		queue:    make(chan int64, minerQueueSize),
		services: make(chan serviceRef, minerQueueSize),
		wake:     make(chan struct{}, 1),
	}
}

// requestMine asks for one transaction to be mined soon. Never blocks: a full
// queue drops the request and the periodic sweep picks the transaction up.
func (m *recommendationMiner) requestMine(transactionID int64) {
	select {
	case m.queue <- transactionID:
	default:
	}
}

// requestMineService asks for one service's guardrail card to be refreshed
// soon. Never blocks; the service sweep is the fallback.
func (m *recommendationMiner) requestMineService(namespace, service string) {
	select {
	case m.services <- serviceRef{namespace: namespace, service: service}:
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

// run services the queues and the sweep tickers until ctx is cancelled. A
// single goroutine on the leader, so requests and sweeps are serialized and
// never mine the same transaction concurrently - no claim needed.
func (m *recommendationMiner) run(ctx context.Context) {
	ticker := time.NewTicker(minerSweepInterval)
	defer ticker.Stop()
	serviceTicker := time.NewTicker(minerServiceSweepInterval)
	defer serviceTicker.Stop()
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
		case ref := <-m.services:
			// The promote flag and the automatic transaction guardrail that
			// follow a promotion may still be in flight.
			select {
			case <-ctx.Done():
				return
			case <-time.After(minerSettleDelay):
			}
			m.mineService(ctx, ref)
		case <-m.wake:
			m.sweep(ctx)
			m.sweepServices(ctx)
		case <-ticker.C:
			m.sweep(ctx)
		case <-serviceTicker.C:
			m.sweepServices(ctx)
		}
	}
}

// sweepServices refreshes the guardrail card of every service that could have
// one: services whose card is open (a guardrail edit may have changed what is
// left to enforce) and services without a card yet. Applied and dismissed
// cards are the operator's decision and are left alone.
func (m *recommendationMiner) sweepServices(ctx context.Context) {
	services, err := m.e.repo.ListServiceStats(ctx, 10000)
	if err != nil {
		m.e.warn("list services for guardrail recommendations", err)
		return
	}
	decided, err := m.e.repo.ListRecommendations(ctx, repo.RecommendationFilter{Kind: repo.RecommendationKindServiceGuardrail, IncludeStale: true})
	if err != nil {
		m.e.warn("list service guardrail recommendations", err)
		return
	}
	settled := map[string]struct{}{}
	for _, rec := range decided {
		if rec.State != repo.RecommendationOpen {
			settled[rec.ID] = struct{}{}
		}
	}
	for _, s := range services {
		if ctx.Err() != nil {
			return
		}
		if _, ok := settled[repo.ServiceGuardrailRecommendationID(s.Namespace, s.Service)]; ok {
			continue
		}
		m.mineService(ctx, serviceRef{namespace: s.Namespace, service: s.Service})
	}
}

// mineService rebuilds one service's guardrail card and refreshes the shadow
// set so its lists are checked against live traffic right away.
func (m *recommendationMiner) mineService(ctx context.Context, ref serviceRef) {
	ctx, cancel := context.WithTimeout(ctx, minerRunTimeout)
	defer cancel()
	start := time.Now()
	changed, err := m.e.refreshServiceGuardrailRecommendation(ctx, ref.namespace, ref.service)
	if err != nil {
		m.e.warn(fmt.Sprintf("mine guardrail recommendation for %s/%s", ref.namespace, ref.service), err)
		metrics.ObserveRecommendationMining("error", time.Since(start).Seconds(), 0)
		return
	}
	if !changed {
		return
	}
	metrics.ObserveRecommendationMining("ok", time.Since(start).Seconds(), 1)
	if err := m.e.ReloadGuardrailConfig(ctx); err != nil {
		m.e.warn("reload guardrail config after mining", err)
	}
}

// sweep mines every promoted transaction that still has unmined samples.
func (m *recommendationMiner) sweep(ctx context.Context) {
	candidates, err := m.e.repo.ListRecommendationMiningCandidates(ctx, len(m.e.scorers), minerSweepBatch)
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

// mine runs one transaction end to end: load samples, mine, merge with the
// previous run's rows so operator decisions survive, write, record the run, and
// refresh the guardrail snapshot so shadow evaluation of the new open
// recommendations starts right away.
func (m *recommendationMiner) mine(ctx context.Context, transactionID int64) {
	ctx, cancel := context.WithTimeout(ctx, minerRunTimeout)
	defer cancel()
	start := time.Now()
	now := m.e.clock()
	recs, sampleCount, err := m.e.mineTransaction(ctx, transactionID)
	if err != nil {
		m.e.warn(fmt.Sprintf("mine recommendations for transaction %d", transactionID), err)
		metrics.ObserveRecommendationMining("error", time.Since(start).Seconds(), 0)
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
