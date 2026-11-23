package autopilot

import (
	"context"
	"sync"
	"time"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	// callTreeSyncDelay is how long a source waits after its first change
	// before the sync starts, so nodes merged in the meantime go in the same sync.
	callTreeSyncDelay = 2 * time.Second
	// callTreeSyncRetryDelay is how long a failed sync waits before retrying.
	callTreeSyncRetryDelay = 30 * time.Second
	// callTreeSyncQueueSize is how many batches of changed keys wait for the
	// loop before new ones are dropped.
	callTreeSyncQueueSize = 1024
)

// CallTreeStore stores call tree nodes; *postgres.Repo implements it.
type CallTreeStore interface {
	InsertCallTreeNodes(ctx context.Context, nodes []repo.CallTreeNode) error
}

// callTreeSyncer copies the Redis call trees that gained nodes into the
// call tree store. A source's first change schedules its sync callTreeSyncDelay
// later, and keys of that source that change before the sync starts join it.
// A failed sync keeps its keys pending and retries. It runs on every replica
// that correlates traces, which is safe because stored nodes are skipped.
//
// changed only queues keys on a channel. One loop goroutine owns the pending
// keys and the timers, and runs one sync at a time in its own goroutine, so a
// slow sync never stops the loop from reading the queue.
type callTreeSyncer struct {
	redis      *tracesRedis
	store      CallTreeStore
	logger     *commonlogger.OdigosLogger
	delay      time.Duration
	retryDelay time.Duration

	changes chan []string
	due     chan int64
	results chan callTreeSyncResult

	// Drop log throttling; owned by the caller of changed (the batcher goroutine).
	droppedKeys int
	lastDropLog time.Time

	// Sync-failure log throttling; owned by the loop goroutine.
	failedSyncs    int
	lastFailureLog time.Time

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type callTreeSyncResult struct {
	source int64
	keys   map[string]struct{}
	nodes  int
	err    error
}

// pendingSync is a source's changed keys waiting for its sync.
type pendingSync struct {
	keys map[string]struct{}
	// ready is set once the source's timer fired and it waits for the worker.
	ready bool
}

func newCallTreeSyncer(rdb *tracesRedis, st CallTreeStore, logger *commonlogger.OdigosLogger, delay, retryDelay time.Duration) *callTreeSyncer {
	return &callTreeSyncer{
		redis:      rdb,
		store:      st,
		logger:     logger,
		delay:      delay,
		retryDelay: retryDelay,
		changes:    make(chan []string, callTreeSyncQueueSize),
		due:        make(chan int64),
		results:    make(chan callTreeSyncResult),
	}
}

func (s *callTreeSyncer) start() {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wg.Add(1)
	go s.loop(ctx)
}

// stop drops pending syncs. A dropped call tree is synced whole on its next
// change.
func (s *callTreeSyncer) stop() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
		s.wg.Wait()
	}
}

// changed queues call tree keys that gained nodes. It never blocks: when the
// queue is full the keys are dropped, and the call tree is synced whole on its
// next change.
func (s *callTreeSyncer) changed(keys []string) {
	if len(keys) == 0 {
		return
	}
	select {
	case s.changes <- keys:
		return
	default:
	}
	s.droppedKeys += len(keys)
	now := time.Now()
	if s.lastDropLog.IsZero() || now.Sub(s.lastDropLog) >= failureLogEvery {
		s.logger.Warn("autopilot call tree sync queue full; dropped changed call trees",
			"dropped_keys", s.droppedKeys, "queue_size", cap(s.changes))
		s.droppedKeys = 0
		s.lastDropLog = now
	}
}

func (s *callTreeSyncer) loop(ctx context.Context) {
	defer s.wg.Done()
	pending := make(map[int64]*pendingSync)
	var ready []int64
	syncing := false

	add := func(source int64, key string, delay time.Duration) {
		p := pending[source]
		if p == nil {
			p = &pendingSync{keys: make(map[string]struct{})}
			pending[source] = p
			time.AfterFunc(delay, func() {
				select {
				case s.due <- source:
				case <-ctx.Done():
				}
			})
		}
		p.keys[key] = struct{}{}
	}

	for {
		if !syncing && len(ready) > 0 {
			source := ready[0]
			ready = ready[1:]
			keys := pending[source].keys
			delete(pending, source)
			syncing = true
			s.wg.Add(1)
			go s.sync(ctx, source, keys)
		}

		select {
		case <-ctx.Done():
			return
		case keys := <-s.changes:
			for _, key := range keys {
				if meta, ok := parseSyncableCallTreeKey(key); ok {
					add(meta.SourceID, key, s.delay)
				}
			}
		case source := <-s.due:
			if p := pending[source]; p != nil && !p.ready {
				p.ready = true
				ready = append(ready, source)
			}
		case res := <-s.results:
			syncing = false
			if res.err != nil {
				for key := range res.keys {
					add(res.source, key, s.retryDelay)
				}
				s.reportFailure(res.err, time.Now())
				continue
			}
			s.reportFailure(nil, time.Now())
			s.logger.Debug("synced redis call trees to postgres",
				"source_id", res.source,
				"keys", len(res.keys),
				"nodes", res.nodes,
			)
		}
	}
}

// sync copies one source's changed call trees and reports back to the loop.
func (s *callTreeSyncer) sync(ctx context.Context, source int64, keys map[string]struct{}) {
	defer s.wg.Done()
	nodes, err := s.syncKeys(ctx, keys)
	select {
	case s.results <- callTreeSyncResult{source: source, keys: keys, nodes: nodes, err: err}:
	case <-ctx.Done():
	}
}

// syncKeys reads every node of the given call trees and inserts them in one
// call. Whole call trees are re-sent, and nodes already stored are skipped.
func (s *callTreeSyncer) syncKeys(ctx context.Context, keys map[string]struct{}) (int, error) {
	var all []repo.CallTreeNode
	for key := range keys {
		meta, ok := parseSyncableCallTreeKey(key)
		if !ok {
			continue
		}
		part, err := s.redis.loadCallTreeStructureNodes(ctx, key, meta)
		if err != nil {
			return 0, err
		}
		all = append(all, part...)
	}
	if len(all) == 0 {
		return 0, nil
	}
	if err := s.store.InsertCallTreeNodes(ctx, all); err != nil {
		return 0, err
	}
	return len(all), nil
}

// reportFailure logs sync failures at most once per failureLogEvery, so a
// Postgres outage (or a schema the migrate Job has not created yet) does not
// log every retry, and logs once when syncs recover.
func (s *callTreeSyncer) reportFailure(err error, now time.Time) {
	if err == nil {
		if s.failedSyncs > 0 {
			s.logger.Info("autopilot call tree sync recovered", "failed_syncs", s.failedSyncs)
			s.failedSyncs = 0
			s.lastFailureLog = time.Time{}
		}
		return
	}
	s.failedSyncs++
	if s.lastFailureLog.IsZero() || now.Sub(s.lastFailureLog) >= failureLogEvery {
		s.logger.Warn("autopilot call tree sync failed; will retry",
			"error", err, "failed_syncs", s.failedSyncs, "retry_in", s.retryDelay.String())
		s.lastFailureLog = now
	}
}
