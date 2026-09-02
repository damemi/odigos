package engine

import (
	"context"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

const promotionSweepBatchSize = 500

// promoteClassIfReady marks a class promoted when its policy is met.
func promoteClassIfReady(cs *classState, policy repo.LearningPolicy, now time.Time) bool {
	if cs.value.Promoted {
		return false
	}
	if !shouldPromote(policy, cs.value, now) {
		return false
	}
	cs.value.Promoted = true
	cs.promoteDelta = true
	cs.dirty = true
	return true
}

// sweepPromotions evaluates time-based promotion for resident and cold baselines.
// Called after each write-behind flush so classes can graduate without a new trace.
func (e *Engine) sweepPromotions(ctx context.Context) {
	mem := e.sweepResidentPromotions(ctx)
	if mem > 0 {
		metrics.AddPromotionSweeperClasses("memory", mem)
	}
	filter, ok := e.learning.timeSweepCutoffs(e.clock())
	if !ok {
		return
	}
	db := e.sweepColdPromotions(ctx, filter)
	if db > 0 {
		metrics.AddPromotionSweeperClasses("db", db)
	}
}

func (e *Engine) sweepResidentPromotions(ctx context.Context) int {
	promoted := 0
	e.store.forEachEntry(func(entry *transactionEntry) {
		entry.mu.Lock()
		tx, ok, err := e.repo.GetTransactionByID(ctx, entry.transactionID)
		if err != nil {
			entry.mu.Unlock()
			e.warn("sweep: get transaction", err)
			return
		}
		if !ok {
			entry.mu.Unlock()
			return
		}
		serviceKey := repo.ServiceScopeKey(tx.Namespace, tx.Service)
		anyPromoted := false
		for _, sc := range e.scorers {
			cs := entry.classes[sc.Class()]
			if cs == nil {
				continue
			}
			if promoteClassIfReady(cs, e.learning.Resolve(sc.Class(), tx.ID, serviceKey), e.clock()) {
				anyPromoted = true
				promoted++
			}
		}
		finishedLearning := anyPromoted && e.allClassesPromoted(entry)
		entry.mu.Unlock()
		if anyPromoted {
			e.store.markDirty(entry.transactionID)
		}
		if finishedLearning {
			e.maybeAutoEnableTransactionGuardrailAfterPromote(ctx, tx.Namespace, tx.Service, tx.ID)
		}
	})
	return promoted
}

func (e *Engine) sweepColdPromotions(ctx context.Context, filter repo.UnpromotedBaselineFilter) int {
	resident := e.store.residentIDs()
	promoted := 0
	filter.Limit = promotionSweepBatchSize
	for {
		rows, err := e.repo.ListUnpromotedBaselines(ctx, filter)
		if err != nil {
			e.warn("sweep: list unpromoted baselines", err)
			return promoted
		}
		if len(rows) == 0 {
			return promoted
		}
		for _, row := range rows {
			if _, inMem := resident[row.TransactionID]; inMem {
				continue
			}
			tx, ok, err := e.repo.GetTransactionByID(ctx, row.TransactionID)
			if err != nil {
				e.warn("sweep: get transaction", err)
				continue
			}
			if !ok {
				continue
			}
			serviceKey := repo.ServiceScopeKey(tx.Namespace, tx.Service)
			bc := repo.BaselineClass{
				TransactionID:                row.TransactionID,
				Class:                        row.Class,
				ObservationCount:             row.ObservationCount,
				LearningStartedAt:            row.LearningStartedAt,
				LastChangedAt:                row.LastChangedAt,
				ObservationCountAtLastChange: row.ObservationCountAtLastChange,
			}
			if !shouldPromote(e.learning.Resolve(row.Class, tx.ID, serviceKey), bc, e.clock()) {
				continue
			}
			if err := e.repo.PromoteBaselineClass(ctx, row.TransactionID, row.Class); err != nil {
				e.warn("sweep: promote baseline class", err)
				continue
			}
			e.store.removeEntry(row.TransactionID)
			promoted++
			if e.transactionFullyPromoted(ctx, row.TransactionID) {
				e.maybeAutoEnableTransactionGuardrailAfterPromote(ctx, tx.Namespace, tx.Service, tx.ID)
			}
		}
		if len(rows) < promotionSweepBatchSize {
			return promoted
		}
	}
}

func (e *Engine) transactionFullyPromoted(ctx context.Context, transactionID int64) bool {
	baselines, err := e.repo.GetBaselineForTransaction(ctx, transactionID)
	if err != nil {
		e.warn("sweep: get baselines", err)
		return false
	}
	byClass := make(map[repo.DeviationClass]bool, len(baselines))
	for _, b := range baselines {
		byClass[b.Class] = b.Promoted
	}
	for _, sc := range e.scorers {
		if !byClass[sc.Class()] {
			return false
		}
	}
	return true
}
