package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/metrics"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// warnInterval bounds how often a given engine warning is logged. During a DB
// outage every trace fails the same way; without throttling that's one warn
// line per trace. We log at most one line per message per interval and report
// how many were suppressed.
const warnInterval = 10 * time.Second

// Engine is the orchestration layer. Construct with New; the zero value is
// not usable.
type Engine struct {
	repo     repo.Repository
	logger   *commonlogger.OdigosLogger
	scorers  []Scorer
	clock    func() time.Time
	learning *LearningConfig
	alerts   *AlertConfig
	system   *SystemConfig

	// guardrails owns the service-guardrail snapshot plus the runtime state for
	// enforcing it (violation buffer, in-mesh service registry, evidence
	// throttle). See guardrail_runtime.go.
	guardrails *guardrailRuntime

	// anomalies owns the runtime state for recording anomalies (the firing
	// buffer). See anomaly_runtime.go.
	anomalies *anomalyRuntime

	// samples writes sample traces (examples, evidence) to the DB in batches
	// off the ingest path; sampleCounts enforces their per-transaction caps
	// from memory. See sample_writer.go.
	samples      *sampleWriter
	sampleCounts *sampleCounts

	// recommendations mines promoted transactions' relation samples for
	// guardrail rules worth suggesting, in its own goroutine. See
	// recommendation_miner.go.
	recommendations *recommendationMiner

	// store serves baseline reads from memory and accumulates merges as
	// deltas; Flush (and the background flusher) write them behind in batches.
	// This is what keeps DB load off the per-trace hot path.
	store *baselineStore

	// admission bounds how many distinct transactions each service may add to
	// the inventory (max_transactions_per_service). See transaction_admission.go.
	admission *transactionAdmission

	warnMu       sync.Mutex
	warnThrottle map[string]*warnState
}

// warnState tracks the last emit time and suppressed count for one warning key.
type warnState struct {
	last       time.Time
	suppressed int
}

// New returns an Engine wired with the default scorer set and the given
// learning- and alert-policy snapshots. Both are loaded once at startup (see
// LoadLearningConfig / LoadAlertConfig) and kept fresh by background reloaders,
// so the per-trace path resolves policies from memory with no DB round-trip.
//
// Nil snapshots are safe: a nil learning snapshot falls back to the count-only
// DefaultMinObservations guard, and a nil alert snapshot fires nothing (both
// Resolve on a nil receiver).
func New(r repo.Repository, logger *commonlogger.OdigosLogger, learning *LearningConfig, alerts *AlertConfig) *Engine {
	e := &Engine{
		repo:         r,
		logger:       logger,
		scorers:      defaultScorers(),
		clock:        time.Now,
		learning:     learning,
		alerts:       alerts,
		system:       newSystemConfig(repo.DefaultSystemSettings()),
		warnThrottle: map[string]*warnState{},
		admission:    newTransactionAdmission(),
	}
	e.store = newBaselineStore(r, logger)
	e.samples = newSampleWriter(r, e.warn)
	e.sampleCounts = newSampleCounts(r)
	// The runtimes read the clock through a closure (not e.clock's current value)
	// so a test that overrides e.clock after New still drives their timing.
	e.samples.afterInsert = e.sampleCounts.refresh
	e.guardrails = newGuardrailRuntime(r, func() time.Time { return e.clock() }, e.warn, e.keepSample)
	e.anomalies = newAnomalyRuntime(r, func() time.Time { return e.clock() }, e.warn, e.keepSample)
	e.recommendations = newRecommendationMiner(e)
	return e
}

// RunSampleWriter writes queued sample traces in batches until ctx is
// cancelled, then drains what is left. Run in its own goroutine, next to
// RunBaselineFlusher.
func (e *Engine) RunSampleWriter(ctx context.Context) {
	e.samples.run(ctx)
}

// RunRecommendationMiner mines promoted transactions for guardrail
// recommendations until ctx is cancelled. It is a singleton job: run only on
// the replica holding the leader Lease, so one miner serves the queued requests
// and the periodic sweeps. It never touches the ingest path.
func (e *Engine) RunRecommendationMiner(ctx context.Context) {
	e.recommendations.run(ctx)
}

func (e *Engine) SetSystemConfig(cfg *SystemConfig) {
	if cfg == nil {
		return
	}
	e.system = cfg
	e.applyCapacity(cfg.Get())
}

func (e *Engine) applyCapacity(s repo.SystemSettings) {
	if e.store == nil {
		return
	}
	e.store.setCaps(s.MaxBaselineSetMembers, s.MaxResidentTransactions)
}

// SetGuardrailConfig installs the service-guardrail snapshot. Injected after New
// (like the admin wiring in main.go) so the guardrail load stays optional and
// off the New signature; a nil snapshot polices nothing (Resolve is nil-safe).
func (e *Engine) SetGuardrailConfig(cfg *GuardrailConfig) {
	e.guardrails.setConfig(cfg)
}

// Process handles one trace end-to-end. Never returns an error to the
// caller; logs and moves on so that a single bad trace can't crash the
// gRPC handler. The caller is the OTLP receiver and a returned error
// would NACK the whole batch.
//
// A trace is decomposed into one sub-trace per service-entry span (see
// subtransaction.go); each is scored and learned as its own transaction.
// For a flat trace (root entry only) this is exactly one pass, identical to
// the pre-decomposition behaviour.
func (e *Engine) Process(ctx context.Context, t *Trace) {
	if t == nil || t.Root == nil {
		return
	}
	subs := subTraces(t)
	metrics.IncTracesProcessed()
	metrics.AddSubtransactionsProcessed(len(subs))
	// One deviation is visible to every entry enclosing it, so claims carries
	// each firing down the trace and keeps it to a single finding on the
	// outermost transaction that flagged it. See anomaly_claims.go.
	claims := newAnomalyClaims(t)
	for _, sub := range subs {
		e.processOne(ctx, sub, t, claims)
	}
}

// processOne scores and learns a single (sub-)transaction. The Trace passed
// in is already scoped to one service-entry's subtree: its Root is the entry
// span and its Spans/RawOTLP cover the entry plus its descendants. full is the
// original, undecomposed trace - passed through only so a guardrail violation
// can capture the whole trace as evidence (the offending caller lives above
// this sub-trace's root and isn't in t's subtree). claims is shared across the
// trace's entries and suppresses deviations an enclosing entry already fired.
func (e *Engine) processOne(ctx context.Context, t *Trace, full *Trace, claims *anomalyClaims) {
	// 1. Resolve transaction identity locally (deterministic hash — no DB).
	// Persistence of the transactions row rides the write-behind flush with
	// activity/baselines, so delete+recreate works across replicas without a
	// per-trace upsert or a cross-pod identity cache.
	ident, ok := identifyTransaction(t)
	if !ok {
		// Broken-context trace (root is an outbound client span). Drop
		// silently - see discardableRoot for the reasoning.
		return
	}
	// Fold configured low-cardinality dimensions from the entry span only into
	// Operation before hashing. Changing settings creates new transaction_ids
	// for new traffic; existing baselines are not migrated. One snapshot for the
	// whole trace, so the dimensions that shaped this identity, the fingerprint
	// that names their scheme, and the inventory generation it belongs to all
	// come from the same settings generation.
	system := e.system.snapshot()
	ident.Operation = applyIdentityDimensions(ident.Operation, t.Root, system.settings.TransactionIdentityDimensions)
	tx := repo.NewTransaction(ident.Service, ident.Namespace, ident.Operation, ident.Kind)
	// 1a. Per-service cap on distinct transactions. An identity the inventory
	// does not know yet is dropped once its service is full, before anything
	// (guardrail state, baselines, activity, samples) is recorded for it. This
	// is what stops a span name carrying a request id from growing the
	// inventory tables without bound. Known identities always pass.
	serviceKey := repo.ServiceScopeKey(ident.Namespace, ident.Service)
	if !e.admission.admit(serviceKey, tx.ID, system.settings.MaxTransactionsPerService) {
		metrics.IncTransactionsDroppedOverCap()
		e.warn("drop unseen transaction, service at max_transactions_per_service",
			fmt.Errorf("%s: %q holds %d transactions", serviceKey, ident.Operation, system.settings.MaxTransactionsPerService))
		return
	}
	// Every service that anchors a transaction is in-mesh; remember it so
	// guardrail egress enforcement won't police a call to it just because its
	// SERVER span didn't nest under the caller in this particular trace.
	e.guardrails.remember(ident.Namespace, ident.Service)

	// 1b. Service guardrails: police the caller→callee edge against the operator's
	// declared rules, independently of the learned baselines. A violation fires
	// its own finding but does NOT stop learning - the guardrail is about inbound
	// identity, orthogonal to what this transaction learns about its own subtree.
	e.guardrails.evaluate(ctx, t, tx, full, system)

	// 2. Get the in-memory entry (load-through on first touch) and seed any
	// missing class rows. Everything below runs under the per-transaction
	// lock, so scoring/merging for one transaction is serialized while the
	// flusher drains a snapshot of the deltas concurrently.
	entry, err := e.store.get(ctx, tx.ID)
	if err != nil {
		e.warn("load baselines", err)
		return
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	entry.identity = tx
	e.seedEntryClasses(entry)

	// Count this sub-transaction's activity (volume + last_seen), even after
	// promotion, so the UI can rank by frequency. markDirty here (not only in
	// the learning step) so the anomaly path's early return still flushes it.
	entry.activityVolume++
	entry.lastSeen = e.clock()
	e.store.markDirty(tx.ID)

	// 3. Score every PROMOTED class against the in-memory value.
	deviated, evidence := e.scoreEntry(t, entry, serviceKey)
	// Drop what an enclosing entry already fired, so this stays one finding on
	// the broadest transaction that saw it rather than one per level. Only
	// reporting is suppressed — learning below continues, so each transaction
	// still knows its own subtree and can flag on its own once it is no longer
	// enclosed (its caller restarts, or traffic arrives at it directly).
	deviated, evidence = claims.filter(t.Root, deviated, evidence)

	// 4. Decide whether to fire. The resolved policy gives the risk-score
	// threshold and any per-class weight overrides. Score the in-memory
	// deviations first as a cheap gate; only if they clear the threshold do we
	// re-validate against the freshest persisted baseline (confirm-on-anomaly),
	// so a stale in-memory view on one replica can't raise a false alert for
	// something another replica already learned.
	//
	// A fired anomaly does NOT skip the learn loop below. Poisoning protection
	// of ESTABLISHED baselines is already enforced there - promoted classes are
	// frozen (never merged) - so the only thing an early return would add is to
	// starve classes that are still LEARNING. That breaks operator "re-learn":
	// a reset class could never converge while any promoted sibling keeps
	// firing. We instead let learning classes keep folding in, and only skip
	// keeping the anomalous trace as a normal "example" (step 6).
	anomalyFired := false
	if len(deviated) > 0 {
		if policy, ok := e.resolveAlertPolicy(tx); ok {
			// FireAtScore is taken literally: 0 is a valid "fire on any signal"
			// (info) threshold, not "unset". A blank threshold is defaulted when
			// the policy is created, and the DB column defaults to DefaultFireAtScore.
			threshold := policy.FireAtScore
			if threshold < 0 {
				threshold = 0
			}
			if e.assessRisk(deviated, evidence, policy).Score >= threshold {
				confirmed, confEv := e.confirmAnomaly(ctx, t, entry, deviated, evidence)
				if len(confirmed) > 0 {
					if assessment := e.assessRisk(confirmed, confEv, policy); assessment.Score >= threshold {
						e.anomalies.record(ctx, t, tx, &policy, confirmed, confEv, assessment, system.settings.TransactionInventoryEpoch)
						claims.take(t.Root, confirmed, confEv)
						anomalyFired = true
					}
				}
				// If it didn't confirm, it's a false alarm: the fresh baseline
				// already covers it (or the re-score fell below threshold). The
				// adopt inside confirmAnomaly already refreshed the in-memory
				// value; learning proceeds normally below either way.
			}
		}
	}

	// 5. Learn from this trace. For each class still learning: fold the trace
	// in, count the observation, and promote it if it now meets its threshold.
	// Promoted classes are skipped ("frozen") - anything genuinely new would
	// have been caught as a deviation in step 3, and this freeze is what keeps a
	// fired anomaly from poisoning an established baseline - so once every class
	// promotes, the transaction stops writing baselines and only the activity
	// counter (above) keeps flushing. Resolve scope once here; the promotion
	// gate looks up the most specific policy per class below.
	settledThisTrace := false
	for _, sc := range e.scorers {
		cs := entry.classes[sc.Class()]
		if cs.value.Settled() {
			continue // frozen: promoted, or saturated and never going to promote
		}
		lb := cs.learningFor(sc)
		var updated json.RawMessage
		var changed, learnedGrew bool
		if lb != nil {
			changed, learnedGrew = lb.merge(t)
			updated = cs.value.Data
			if changed {
				var err error
				if updated, err = lb.encode(); err != nil {
					// lb already holds this trace; re-parse value.Data next time.
					cs.learning = nil
					e.warn(fmt.Sprintf("merge %s", sc.Class()), err)
					continue
				}
			}
		} else {
			var err error
			if updated, err = sc.Merge(t, cs.value.Data); err != nil {
				e.warn(fmt.Sprintf("merge %s", sc.Class()), err)
				continue
			}
		}
		// A set that outgrows the cap is not a baseline, it is an identity leak
		// (ids in span names, per-request hosts). Freeze it as saturated: keep
		// the capped subset so the store never receives the overflow, and stop
		// counting it as learning so it can neither promote nor block the
		// transaction's other classes from finishing.
		if lb == nil || lb.members() > e.store.perSetCap {
			if capped, didCap := capClassData(sc.Class(), updated, e.store.perSetCap); didCap {
				cs.saturate(capped)
				settledThisTrace = true
				metrics.IncBaselineClassesSaturated(string(sc.Class()))
				e.warn(fmt.Sprintf("baseline %s saturated at per-set cap %d", sc.Class(), e.store.perSetCap), errPerSetCap)
				continue
			}
		}
		// Track convergence for the stability conditions: did this trace add
		// something new (a new edge/peer/bucket) or just reinforce what we
		// already knew? Compared before we overwrite the value.
		var grew bool
		switch {
		case lb == nil:
			grew = baselineGrew(sc, cs.value.Data, updated)
			cs.setData(updated)
		case isStabilityComparator(sc):
			grew = learnedGrew
			cs.setLearned(updated)
		default:
			grew = changed && !bytes.Equal(cs.value.Data, updated)
			cs.setLearned(updated)
		}
		cs.value.ObservationCount++
		cs.countDelta++
		if grew {
			cs.value.LastChangedAt = e.clock()
			cs.value.ObservationCountAtLastChange = cs.value.ObservationCount
		}
		if cs.additive {
			e.mergeDelta(cs, sc, t)
		}
		if promoteClassIfReady(cs, e.learning.Resolve(sc.Class(), tx.ID, serviceKey), e.clock()) {
			settledThisTrace = true
			// A promoted class is frozen, so its parsed learning copies are done.
			cs.learning = nil
			cs.learningDelta = nil
		}
		cs.dirty = true
	}

	// Only when this observation newly finished learning for the transaction -
	// not on every subsequent trace of an already-promoted service.
	if settledThisTrace && e.allClassesPromoted(entry) {
		e.maybeAutoEnableTransactionGuardrailAfterPromote(ctx, ident.Namespace, ident.Service, tx.ID)
		// The relation samples kept below are complete now; mine them for
		// guardrail recommendations in the background.
		e.recommendations.requestMine(tx.ID)
	}

	// 6. Keep representative traces for the UI, but never a fired anomaly (that
	// trace is already stored by the anomaly path and isn't "normal"). Collect
	// throttled "example" samples only while learning and under the configured
	// cap; once every class has promoted (or the cap is full), stop so the
	// compare view's base column stays fixed instead of drifting with live traffic.
	// Relation samples follow the same rule and feed the recommendation miner.
	if !anomalyFired && !e.allClassesPromoted(entry) {
		e.recordExampleSample(ctx, t, tx, entry)
		e.recordRelationSample(ctx, full, tx, entry)
	}
}

// allClassesPromoted reports whether every scored class for this transaction has
// settled (promoted and frozen, or saturated and never going to promote), i.e.
// the transaction has finished learning. Caller holds entry.mu.
func (e *Engine) allClassesPromoted(entry *transactionEntry) bool {
	for _, sc := range e.scorers {
		cs := entry.classes[sc.Class()]
		if cs == nil || !cs.value.Settled() {
			return false
		}
	}
	return true
}

// scoreEntry runs every promoted class's scorer against the in-memory value,
// returning the deviated classes and their evidence. Caller holds entry.mu.
func (e *Engine) scoreEntry(t *Trace, entry *transactionEntry, serviceKey string) ([]repo.DeviationClass, map[string]json.RawMessage) {
	var deviated []repo.DeviationClass
	evidence := map[string]json.RawMessage{}
	for _, sc := range e.scorers {
		cs := entry.classes[sc.Class()]
		// A saturated class is never enforced, even if an operator force-
		// promoted it: its set is truncated, so every unseen member would
		// read as a deviation.
		if cs == nil || !cs.value.Promoted || cs.value.Saturated {
			continue
		}
		// Score the decoded baseline when the scorer can take one: this class is
		// promoted, so its data is frozen and the same decode serves every trace
		// until something replaces it.
		var (
			dev bool
			ev  json.RawMessage
			err error
		)
		if bd, ok := sc.(baselineDecoder); ok {
			dev, ev, err = cs.decodedFor(bd).score(t)
		} else {
			dev, ev, err = sc.Score(t, cs.value.Data)
		}
		if err != nil {
			e.warn(fmt.Sprintf("score %s", sc.Class()), err)
			continue
		}
		if dev {
			deviated = append(deviated, sc.Class())
			if ev != nil {
				evidence[string(sc.Class())] = ev
			}
		}
	}
	return deviated, evidence
}

// confirmAnomaly re-validates a candidate anomaly against the freshest
// persisted baseline and adopts that fresh data into memory. It returns the
// subset of classes that STILL deviate (plus their evidence). A class is
// re-confirmed only if the fresh DB data is present and non-empty and the
// scorer still flags it; if the DB has nothing fresher (not yet flushed) we
// trust the in-memory decision rather than suppress a real alert - except when
// memory holds an established (promoted, non-empty) baseline, which cannot
// legitimately be missing from the DB and instead means the inventory was
// wiped. Caller holds entry.mu.
func (e *Engine) confirmAnomaly(
	ctx context.Context,
	t *Trace,
	entry *transactionEntry,
	deviated []repo.DeviationClass,
	evidence map[string]json.RawMessage,
) ([]repo.DeviationClass, map[string]json.RawMessage) {
	fresh, err := e.repo.GetBaselineForTransaction(ctx, entry.transactionID)
	if err != nil {
		// Can't reach the DB to disprove: keep the in-memory decision.
		e.warn("confirm anomaly: load baselines", err)
		return deviated, evidence
	}
	freshByClass := make(map[repo.DeviationClass]repo.BaselineClass, len(fresh))
	for _, b := range fresh {
		freshByClass[b.Class] = b
	}

	confirmed := make([]repo.DeviationClass, 0, len(deviated))
	confEv := make(map[string]json.RawMessage, len(deviated))
	for _, class := range deviated {
		fb, ok := freshByClass[class]
		if !ok || isEmptyData(fb.Data) {
			// A promoted class with content in memory was necessarily persisted
			// when it promoted, so an absent or empty row is not a lagging
			// flush - the inventory was wiped underneath this replica (reset, or
			// a per-transaction delete) and this deviation is measured against a
			// baseline that no longer exists. Suppress it and let the class
			// re-learn instead of reporting every structural fact as new.
			if cs, held := entry.classes[class]; held && cs.value.Promoted && !isEmptyData(cs.value.Data) {
				continue
			}
			// Nothing fresher persisted yet: can't disprove, trust memory.
			confirmed = append(confirmed, class)
			if ev := evidence[string(class)]; ev != nil {
				confEv[string(class)] = ev
			}
			continue
		}
		sc := e.scorerForClass(class)
		if sc == nil {
			confirmed = append(confirmed, class)
			continue
		}
		dev, ev, scErr := sc.Score(t, fb.Data)
		if scErr != nil {
			e.warn(fmt.Sprintf("confirm score %s", class), scErr)
			confirmed = append(confirmed, class)
			continue
		}
		if dev {
			confirmed = append(confirmed, class)
			if ev != nil {
				confEv[string(class)] = ev
			}
		}
	}

	// Adopt the fresh values so future traces score against the converged
	// baseline (and stop re-confirming the same known edges).
	e.mergePersistedIntoEntry(entry, fresh)
	return confirmed, confEv
}

// mergePersistedIntoEntry brings the in-memory entry up to date with what's
// stored in the DB (which may include learning from other replicas). For each
// persisted class it: adds the class if memory didn't have it, keeps the larger
// observation count, keeps promoted=true if either side is promoted, and unions
// set data so no edges are lost. The additive histogram is left alone because
// memory holds unflushed observations the DB hasn't seen yet. Caller holds entry.mu.
func (e *Engine) mergePersistedIntoEntry(entry *transactionEntry, fresh []repo.BaselineClass) {
	for _, fb := range fresh {
		cs := entry.classes[fb.Class]
		if cs == nil {
			cs = &classState{value: fb, additive: isAdditiveClass(fb.Class)}
			entry.classes[fb.Class] = cs
			continue
		}
		if fb.ObservationCount > cs.value.ObservationCount {
			cs.value.ObservationCount = fb.ObservationCount
		}
		if fb.Promoted {
			cs.value.Promoted = true
		}
		if fb.Saturated {
			cs.value.Saturated = true
		}
		if cs.additive {
			// Leave the in-memory histogram as-is; its unflushed observations
			// are richer than (a superset of) the persisted view.
			continue
		}
		if e.store.absorbSetData(cs, fb.Class, fb.Data, true) {
			e.store.markDirty(entry.transactionID)
		}
	}
}

// scorerForClass returns the scorer that handles the given deviation class, or
// nil if none is registered for it.
func (e *Engine) scorerForClass(class repo.DeviationClass) Scorer {
	for _, sc := range e.scorers {
		if sc.Class() == class {
			return sc
		}
	}
	return nil
}

// Flush drains all pending baseline deltas to the repository. Called by the
// background flusher on a timer, on graceful shutdown, and directly in tests.
// It also drains the guardrail-violation buffer first so counts persist on the
// same cadence; a buffer failure is logged but never blocks the baseline flush.
func (e *Engine) Flush(ctx context.Context) error {
	e.guardrails.flushViolations(ctx)
	e.guardrails.flushShadow(ctx)
	e.anomalies.flush(ctx)
	e.samples.drain(ctx)
	start := time.Now()
	err := e.store.flush(ctx)
	// Record every flusher cycle (including activity-only / idle) so Storage
	// health Insights↔DB is not stuck on "never flushed" after baselines settle.
	if err != nil {
		metrics.ObserveBaselineFlush("error", time.Since(start).Seconds())
	} else {
		metrics.ObserveBaselineFlush("ok", time.Since(start).Seconds())
	}
	return err
}

// resolveAlertPolicy returns the single alert policy governing this transaction
// (transaction override → service override → global default) from the in-memory
// scope snapshot, if one exists and is enabled. A disabled policy for the
// resolved scope suppresses alerting there - it does NOT fall back to a broader
// policy (replace semantics), so ok is false. The caller applies the policy's
// threshold and weight overrides to the risk score to decide firing.
func (e *Engine) resolveAlertPolicy(tx repo.Transaction) (repo.Policy, bool) {
	serviceKey := repo.ServiceScopeKey(tx.Namespace, tx.Service)
	p, ok := e.alerts.Resolve(tx.ID, serviceKey)
	if !ok || !p.Enabled {
		return repo.Policy{}, false
	}
	return p, true
}

// evidenceWriteTimeout bounds the DB work behind keeping a sample trace (the
// one-time count load, and the writer's final drain on shutdown). Keeping a
// sample is never worth stalling for: past this point, dropping the trace is
// the better trade.
const evidenceWriteTimeout = 5 * time.Second

// detachedEvidenceContext derives the context used to retain a sample trace.
// Evidence is what makes a finding actionable - it is the trace a user opens to
// investigate, and the one Accept folds into the baseline. Once the engine has
// judged a trace worth keeping, whether it survives must not depend on the
// instrumented application still being connected. Request values are kept; only
// cancellation is dropped.
func detachedEvidenceContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), evidenceWriteTimeout)
}

// seedEntryClasses fills in any missing per-class state in memory for the
// transaction. Unlike the old path this does NOT touch the DB: a freshly
// seeded class is persisted lazily by the flusher once it accumulates a delta.
// Caller holds entry.mu.
func (e *Engine) seedEntryClasses(entry *transactionEntry) {
	now := e.clock()
	for _, sc := range e.scorers {
		entry.classState(sc.Class(), func() repo.BaselineClass {
			return repo.BaselineClass{
				TransactionID:     entry.transactionID,
				Class:             sc.Class(),
				Data:              json.RawMessage(`{}`),
				DataSchemaVersion: 1,
				LearningStartedAt: now,
			}
		})
	}
}

// errPerSetCap is a sentinel used only for the throttled per-set-cap warning.
var errPerSetCap = errors.New("per-set cap reached; class frozen as saturated and will not be enforced")

// emptyOr returns d, or an empty JSON object when d is empty, so a scorer's
// Merge always receives valid bytes when seeding the additive delta.
// mergeDelta folds t into an additive class's unflushed delta, the same
// Merge(t, emptyOr(dataDelta)) the class itself gets. Caller holds entry.mu.
func (e *Engine) mergeDelta(cs *classState, sc Scorer, t *Trace) {
	dl := cs.learningDeltaFor(sc)
	if dl == nil {
		if delta, err := sc.Merge(t, emptyOr(cs.dataDelta)); err == nil {
			cs.dataDelta = delta
		}
		return
	}
	if changed, _ := dl.merge(t); !changed {
		// Merge returns its input when the trace carries nothing for the class.
		cs.dataDelta = emptyOr(cs.dataDelta)
		return
	}
	delta, err := dl.encode()
	if err != nil {
		cs.learningDelta = nil
		return
	}
	cs.dataDelta = delta
}

func isStabilityComparator(sc Scorer) bool {
	_, ok := sc.(stabilityComparator)
	return ok
}

func emptyOr(d json.RawMessage) json.RawMessage {
	if len(d) == 0 {
		return json.RawMessage(`{}`)
	}
	return d
}

// isEmptyData reports whether baseline bytes carry no learned content, i.e. are
// absent or the empty object - used by confirm-on-anomaly to decide a fresh
// row can't disprove an in-memory deviation.
func isEmptyData(d json.RawMessage) bool {
	if len(d) == 0 {
		return true
	}
	return string(bytes.TrimSpace(d)) == "{}"
}

// warn logs a best-effort failure, throttled per message. The first occurrence
// logs immediately; further occurrences of the same message within warnInterval
// are counted and folded into the next emitted line as "suppressed". Throttling
// is keyed by msg so a distinct, rarer warning is never hidden behind a flood of
// a different one.
func (e *Engine) warn(msg string, err error) {
	if e.logger == nil {
		return
	}
	now := e.clock()

	e.warnMu.Lock()
	st := e.warnThrottle[msg]
	if st == nil {
		st = &warnState{}
		e.warnThrottle[msg] = st
	}
	if !st.last.IsZero() && now.Sub(st.last) < warnInterval {
		st.suppressed++
		e.warnMu.Unlock()
		return
	}
	suppressed := st.suppressed
	st.suppressed = 0
	st.last = now
	e.warnMu.Unlock()

	if suppressed > 0 {
		e.logger.Warn("engine: "+msg, "error", err, "suppressed", suppressed)
	} else {
		e.logger.Warn("engine: "+msg, "error", err)
	}
}
