package engine

import (
	"container/list"
	"context"
	"encoding/json"
	"sync"
	"time"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos-enterprise/insights/internal/metrics"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// The in-memory baseline store keeps DB load tied to the number of learning
// transactions and the flush cadence, not the trace rate. Scoring reads from
// memory (after loading a transaction once); updates pile up as deltas that a
// background flusher writes to the DB in batches.
//
// Two limits keep memory bounded: an LRU cap on how many transactions stay in
// memory (flushing before evicting so no update is lost), and a per-set cap on
// how big a single baseline can grow (capClassData).
//
// It works across replicas because baselines only ever grow additively (set
// unions, histogram and count adds): each replica flushes its own deltas and
// reloads the merged result, so the DB converges to the union of all replicas
// without any cross-replica locking.
const (
	// defaultMaxResidentTxns caps how many transactions stay in memory at once.
	// At a few KB each that's tens of MB.
	defaultMaxResidentTxns = 50000

	// defaultPerSetCap caps a single class's set size. A healthy transaction has
	// a handful to a few hundred edges/peers/libs/tables; this only guards
	// against runaway growth (e.g. IDs in span names), not normal use.
	defaultPerSetCap = 5000
)

// baselineStore is the authoritative in-memory view of per-transaction
// baselines, with write-behind to the repository. Safe for concurrent use.
type baselineStore struct {
	repo   repo.Repository
	merger repo.BaselineMerger
	logger *commonlogger.OdigosLogger

	perSetCap int
	maxTxns   int

	// mu guards entries, lru, and dirty. It is NEVER held across a per-entry
	// lock or a DB call, to keep the lock ordering simple (store.mu first and
	// briefly, then entry.mu).
	mu      sync.Mutex
	entries map[int64]*transactionEntry
	lru     *list.List // front = most-recently-used; values are *transactionEntry
	dirty   map[int64]struct{}
}

// transactionEntry holds one transaction's baseline classes plus the unflushed
// deltas. entry.mu serializes scoring/merging for that transaction (the same
// lock the engine holds while it scores+merges a trace).
type transactionEntry struct {
	transactionID int64
	lruElem       *list.Element // managed under store.mu

	mu      sync.Mutex
	classes map[repo.DeviationClass]*classState
	// identity is the (service, namespace, operation, kind) resolved on the
	// hot path. Written behind with baselines/activity so the transactions
	// table is never upserted per trace.
	identity repo.Transaction
	// lastExampleAt is when this transaction last had a trace kept as an
	// "example" observation. The sampler uses it to throttle by time
	// (see sampler.go). Zero means none kept yet.
	lastExampleAt time.Time
	loaded        bool

	// activityVolume is the number of sub-transactions seen since the last
	// flush; lastSeen is the most recent time one arrived. Drained by flush
	// into a repo.ActivityDelta so the UI can rank transactions by frequency.
	activityVolume int64
	lastSeen       time.Time
}

// classState is the in-memory value plus the since-last-flush delta for one
// (transaction, class).
type classState struct {
	value repo.BaselineClass // full current value; the read path scores this

	// decoded is value.Data parsed into the form its scorer compares against,
	// kept because a promoted class is frozen and would otherwise be re-parsed
	// on every trace. Built on first use by decodedFor and dropped by setData,
	// so it cannot outlive the bytes it came from. Read-only once built: one
	// value serves every trace of this transaction.
	decoded decodedBaseline

	// Unflushed delta accumulators, drained by flush():
	countDelta   int64           // observations merged since last flush
	dataDelta    json.RawMessage // additive classes only (the histogram delta)
	promoteDelta bool            // promoted locally, not yet persisted
	dirty        bool

	additive bool // true => Data accumulates (flush sends dataDelta, not value)
}

// setData replaces this class's baseline bytes. Every in-place write to
// value.Data MUST go through here: it drops the decoded cache, which is what
// keeps a replica from scoring against a baseline it has already replaced -
// including one adopted from another replica after a flush. Callers that build
// a fresh classState instead need nothing, since its cache starts empty.
// Caller holds entry.mu.
func (cs *classState) setData(data json.RawMessage) {
	cs.value.Data = data
	cs.decoded = nil
}

// decodedFor returns d's view of this class's baseline, decoding on first use
// and reusing it until setData drops it. Caller holds entry.mu, the same lock
// that guards value.Data, so this fills the cache without further locking.
func (cs *classState) decodedFor(d baselineDecoder) decodedBaseline {
	if cs.decoded == nil {
		cs.decoded = d.decodeBaseline(cs.value.Data)
	}
	return cs.decoded
}

func newBaselineStore(r repo.Repository, logger *commonlogger.OdigosLogger) *baselineStore {
	return &baselineStore{
		repo:      r,
		merger:    classMerger{},
		logger:    logger,
		perSetCap: defaultPerSetCap,
		maxTxns:   defaultMaxResidentTxns,
		entries:   map[int64]*transactionEntry{},
		lru:       list.New(),
		dirty:     map[int64]struct{}{},
	}
}

func (s *baselineStore) setCaps(perSetCap, maxTxns int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if perSetCap > 0 {
		s.perSetCap = perSetCap
	}
	if maxTxns > 0 {
		s.maxTxns = maxTxns
	}
}

// get returns the (loaded) entry for transactionID, reading the baseline rows from the
// repository exactly once on a miss. The caller then locks entry.mu to
// score/merge. On a load error the half-created entry is removed so a later
// call retries cleanly.
func (s *baselineStore) get(ctx context.Context, transactionID int64) (*transactionEntry, error) {
	s.mu.Lock()
	if e, ok := s.entries[transactionID]; ok {
		s.lru.MoveToFront(e.lruElem)
		s.mu.Unlock()
		metrics.IncBaselineCache("hit")
		return s.ensureLoaded(ctx, e)
	}
	e := &transactionEntry{transactionID: transactionID, classes: map[repo.DeviationClass]*classState{}}
	e.lruElem = s.lru.PushFront(e)
	s.entries[transactionID] = e
	s.evictLocked(transactionID)
	s.setResidentLocked()
	s.mu.Unlock()
	metrics.IncBaselineCache("miss")
	return s.ensureLoaded(ctx, e)
}

// ensureLoaded reads a transaction's baseline rows from the DB the first time
// it's needed (a cache miss in get: a transaction seen for the first time, or
// again after eviction or a restart). It runs under the entry lock, so if
// several traces for that same transaction arrive at once, only the first does
// the DB read and the rest wait and reuse the result instead of all querying.
func (s *baselineStore) ensureLoaded(ctx context.Context, e *transactionEntry) (*transactionEntry, error) {
	e.mu.Lock()
	if e.loaded {
		e.mu.Unlock()
		return e, nil
	}
	rows, err := s.repo.GetBaselineForTransaction(ctx, e.transactionID)
	if err != nil {
		e.mu.Unlock()
		s.removeEntry(e.transactionID)
		return nil, err
	}
	for _, b := range rows {
		e.classes[b.Class] = &classState{value: b, additive: isAdditiveClass(b.Class)}
	}
	e.loaded = true
	e.mu.Unlock()
	return e, nil
}

// classState returns the per-class state, creating a seeded one if missing.
// Caller MUST hold e.mu.
func (e *transactionEntry) classState(class repo.DeviationClass, seed func() repo.BaselineClass) *classState {
	cs := e.classes[class]
	if cs == nil {
		cs = &classState{value: seed(), additive: isAdditiveClass(class)}
		e.classes[class] = cs
	}
	return cs
}

// markDirty records that transactionID has unflushed deltas for the flusher to drain.
func (s *baselineStore) markDirty(transactionID int64) {
	s.mu.Lock()
	s.dirty[transactionID] = struct{}{}
	metrics.SetBaselineDirty(len(s.dirty))
	s.mu.Unlock()
}

// evictLocked drops the least-recently-used CLEAN entries until we are back
// under the cap. Dirty entries are skipped (flush-before-evict); if everything
// resident is dirty we temporarily exceed the cap and let the flusher catch up
// rather than lose an unflushed delta. keepTransactionID is the entry just created by
// the caller - never a candidate, since it's about to be used. Caller MUST
// hold s.mu.
func (s *baselineStore) evictLocked(keepTransactionID int64) {
	for len(s.entries) > s.maxTxns {
		evicted := false
		for el := s.lru.Back(); el != nil; el = el.Prev() {
			e := el.Value.(*transactionEntry)
			if e.transactionID == keepTransactionID {
				continue
			}
			if _, isDirty := s.dirty[e.transactionID]; isDirty {
				continue
			}
			s.lru.Remove(el)
			delete(s.entries, e.transactionID)
			metrics.AddBaselineEvictions(1)
			evicted = true
			break
		}
		if !evicted {
			return
		}
	}
}

func (s *baselineStore) removeEntry(transactionID int64) {
	s.mu.Lock()
	if e, ok := s.entries[transactionID]; ok {
		s.lru.Remove(e.lruElem)
		delete(s.entries, transactionID)
	}
	delete(s.dirty, transactionID)
	s.setResidentLocked()
	metrics.SetBaselineDirty(len(s.dirty))
	s.mu.Unlock()
}

// clearAll drops every resident entry and pending dirty set. Used when the
// transaction inventory is wiped or identity settings change on reload.
func (s *baselineStore) clearAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = map[int64]*transactionEntry{}
	s.lru = list.New()
	s.dirty = map[int64]struct{}{}
	s.setResidentLocked()
	metrics.SetBaselineDirty(0)
}

func (s *baselineStore) setResidentLocked() { metrics.SetBaselineResident(len(s.entries)) }

// forEachEntry calls fn for every resident transaction entry. The store lock is
// not held during fn; each entry's mu must be acquired by the caller when needed.
func (s *baselineStore) forEachEntry(fn func(*transactionEntry)) {
	s.mu.Lock()
	entries := make([]*transactionEntry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, e)
	}
	s.mu.Unlock()
	for _, e := range entries {
		fn(e)
	}
}

// residentIDs returns the set of transaction IDs currently held in memory.
func (s *baselineStore) residentIDs() map[int64]struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int64]struct{}, len(s.entries))
	for id := range s.entries {
		out[id] = struct{}{}
	}
	return out
}

// flushSnap captures one class's drained delta so a failed apply can be rolled
// back into the accumulators (so additive count/histogram deltas aren't lost).
type flushSnap struct {
	entry        *transactionEntry
	class        repo.DeviationClass
	contribution json.RawMessage
	countDelta   int64
	promote      bool
	additive     bool
}

// flush drains every dirty transaction's deltas and applies them to the
// repository in a single batch, then adopts the merged authoritative rows back
// into memory. On apply error the drained deltas are restored so nothing is
// lost. Safe to call concurrently with scoring/merging.
func (s *baselineStore) flush(ctx context.Context) error {
	s.mu.Lock()
	if len(s.dirty) == 0 {
		s.mu.Unlock()
		return nil
	}
	transactionIDs := make([]int64, 0, len(s.dirty))
	for id := range s.dirty {
		transactionIDs = append(transactionIDs, id)
	}
	s.mu.Unlock()

	var (
		identities []repo.Transaction
		deltas     []repo.BaselineDelta
		snaps      []flushSnap
		activity   []repo.ActivityDelta
		actSnaps   []activitySnap
	)
	for _, id := range transactionIDs {
		s.mu.Lock()
		e := s.entries[id]
		delete(s.dirty, id) // optimistic; re-added on rollback or new merges
		s.mu.Unlock()
		if e == nil {
			continue
		}

		e.mu.Lock()
		if e.identity.ID != 0 {
			identities = append(identities, e.identity)
		}
		if e.activityVolume > 0 {
			activity = append(activity, repo.ActivityDelta{
				TransactionID: id, Volume: e.activityVolume, LastSeen: e.lastSeen,
			})
			actSnaps = append(actSnaps, activitySnap{entry: e, volume: e.activityVolume})
			e.activityVolume = 0
		}
		for class, cs := range e.classes {
			if !cs.dirty {
				continue
			}
			var contribution json.RawMessage
			if cs.additive {
				contribution = cs.dataDelta
				if len(contribution) == 0 {
					contribution = json.RawMessage(`{}`)
				}
			} else {
				contribution = cs.value.Data
			}
			deltas = append(deltas, repo.BaselineDelta{
				TransactionID:                id,
				Class:                        class,
				Contribution:                 contribution,
				CountDelta:                   cs.countDelta,
				Promote:                      cs.promoteDelta,
				DataSchemaVersion:            cs.value.DataSchemaVersion,
				LearningStartedAt:            cs.value.LearningStartedAt,
				LastChangedAt:                cs.value.LastChangedAt,
				ObservationCountAtLastChange: cs.value.ObservationCountAtLastChange,
			})
			snaps = append(snaps, flushSnap{
				entry: e, class: class, contribution: contribution,
				countDelta: cs.countDelta, promote: cs.promoteDelta, additive: cs.additive,
			})
			cs.countDelta = 0
			cs.dataDelta = nil
			cs.promoteDelta = false
			cs.dirty = false
		}
		e.mu.Unlock()
	}

	s.mu.Lock()
	metrics.SetBaselineDirty(len(s.dirty))
	s.mu.Unlock()

	if len(deltas) == 0 && len(activity) == 0 && len(identities) == 0 {
		return nil
	}

	// Identity first so listings that join from transactions see the row even
	// when only activity/baselines follow in the same cycle.
	if len(identities) > 0 {
		if err := s.repo.UpsertTransactions(ctx, identities); err != nil {
			s.rollbackActivity(actSnaps)
			s.rollback(snaps)
			return err
		}
	}

	if len(deltas) > 0 {
		merged, err := s.repo.ApplyBaselineDeltas(ctx, deltas, s.merger)
		if err != nil {
			s.rollback(snaps)
			s.rollbackActivity(actSnaps)
			return err
		}
		s.adopt(merged)
	}

	// Activity is additive and only feeds UI ordering, so a failed record just
	// restores the drained volume to retry next cycle (baseline, if any,
	// already applied above).
	if len(activity) > 0 {
		if err := s.repo.RecordActivity(ctx, activity); err != nil {
			s.rollbackActivity(actSnaps)
			return err
		}
	}
	return nil
}

// activitySnap captures a drained activity volume so a failed RecordActivity
// can restore it (mirrors flushSnap for baseline deltas).
type activitySnap struct {
	entry  *transactionEntry
	volume int64
}

// rollbackActivity re-adds drained volume after a failed record and re-marks
// the transaction dirty so the next flush retries it.
func (s *baselineStore) rollbackActivity(snaps []activitySnap) {
	if len(snaps) == 0 {
		return
	}
	for _, sn := range snaps {
		sn.entry.mu.Lock()
		sn.entry.activityVolume += sn.volume
		sn.entry.mu.Unlock()
	}
	s.mu.Lock()
	for _, sn := range snaps {
		s.dirty[sn.entry.transactionID] = struct{}{}
	}
	metrics.SetBaselineDirty(len(s.dirty))
	s.mu.Unlock()
}

// rollback re-adds drained deltas after a failed apply. Additive deltas are
// merged back into the accumulator; idempotent (set) classes just re-mark dirty
// since their full value still lives in classState.value and will be re-sent.
func (s *baselineStore) rollback(snaps []flushSnap) {
	touched := map[int64]struct{}{}
	for _, sn := range snaps {
		e := sn.entry
		e.mu.Lock()
		if cs := e.classes[sn.class]; cs != nil {
			cs.countDelta += sn.countDelta
			if sn.promote {
				cs.promoteDelta = true
			}
			if sn.additive {
				base := cs.dataDelta
				if len(base) == 0 {
					base = json.RawMessage(`{}`)
				}
				if m, mErr := s.merger.MergeData(sn.class, base, sn.contribution); mErr == nil {
					cs.dataDelta = m
				}
			}
			cs.dirty = true
		}
		e.mu.Unlock()
		touched[e.transactionID] = struct{}{}
	}
	s.mu.Lock()
	for id := range touched {
		s.dirty[id] = struct{}{}
	}
	metrics.SetBaselineDirty(len(s.dirty))
	s.mu.Unlock()
}

// adopt folds the post-merge authoritative rows back into memory so a replica
// converges on what every replica has learned. Count and promoted are SET
// (additive reconciliation happens via deltas, never re-added here); set data
// is UNIONed so edges learned concurrently with the flush survive; additive
// data is SET (its concurrent observations live in the fresh dataDelta).
func (s *baselineStore) adopt(merged []repo.BaselineClass) {
	for _, m := range merged {
		s.mu.Lock()
		e := s.entries[m.TransactionID]
		s.mu.Unlock()
		if e == nil {
			continue // evicted mid-flush; already persisted, reloads fresh later
		}
		e.mu.Lock()
		cs := e.classes[m.Class]
		if cs == nil {
			cs = &classState{additive: isAdditiveClass(m.Class)}
			e.classes[m.Class] = cs
		}
		cs.value.TransactionID = m.TransactionID
		cs.value.Class = m.Class
		cs.value.DataSchemaVersion = m.DataSchemaVersion
		cs.value.ObservationCount = m.ObservationCount
		cs.value.Promoted = m.Promoted
		if cs.value.LearningStartedAt.IsZero() {
			cs.value.LearningStartedAt = m.LearningStartedAt
		}
		// Adopt the freshest convergence marker (max across replicas), so a
		// change learned elsewhere resets this replica's stability clock too.
		if m.LastChangedAt.After(cs.value.LastChangedAt) {
			cs.value.LastChangedAt = m.LastChangedAt
		}
		if m.ObservationCountAtLastChange > cs.value.ObservationCountAtLastChange {
			cs.value.ObservationCountAtLastChange = m.ObservationCountAtLastChange
		}
		if cs.additive {
			cs.setData(m.Data)
		} else if u, uErr := s.merger.MergeData(m.Class, cs.value.Data, m.Data); uErr == nil {
			cs.setData(u)
		} else {
			cs.setData(m.Data)
		}
		e.mu.Unlock()
	}
}
