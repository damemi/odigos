package snapshot

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/odigos-io/central-proxy/internal/logger"
	websocketserver "github.com/odigos-io/central-proxy/internal/websocket"
	"github.com/odigos-io/central-proxy/kube"
)

const (
	// SnapshotInterval is the safety-net poll interval when no dirty events fire.
	SnapshotInterval = 5 * time.Second

	// SnapshotQuietPeriod waits for a short quiet stretch after informer
	// activity before sending (coalesce a create burst into one snapshot).
	SnapshotQuietPeriod = 200 * time.Millisecond

	// SnapshotMaxWait is the longest we delay a send while the cache stays
	// dirty. Pure debounce alone can starve under never-ending K8s
	// create/modify (timer keeps resetting); max-wait guarantees a flush.
	// We do NOT queue/batch individual events in memory — only a dirty bit.
	SnapshotMaxWait = 1 * time.Second
)

// MessageSender is the interface for sending messages over WebSocket
type MessageSender interface {
	SendSnapshot(msg *websocketserver.WebSocketMessageToCentralized) error
}

// Runner manages snapshot generation and sending — event-driven on cache dirty
// (quiet-period + max-wait), with a periodic ticker as a safety net.
type Runner struct {
	generator *Generator
	sender    MessageSender
	cache     *kube.Cache
	ctx       context.Context
}

// NewRunner creates a new snapshot runner with informer-based caching
func NewRunner(ctx context.Context, clusterID, clusterName string, client *kube.Client, sender MessageSender, ignoredNamespaces []string) *Runner {
	cache := kube.NewCache(ctx, client)
	generator := NewGenerator(clusterID, clusterName, cache, ignoredNamespaces)

	return &Runner{
		generator: generator,
		sender:    sender,
		cache:     cache,
		ctx:       ctx,
	}
}

// Start begins the cache sync and snapshot generation loop
func (r *Runner) Start() {
	logger.Log.Info("Starting cluster snapshot runner",
		"interval", SnapshotInterval,
		"quietPeriod", SnapshotQuietPeriod,
		"maxWait", SnapshotMaxWait)

	// Start the informer cache and wait for sync
	if err := r.cache.Start(r.ctx); err != nil {
		logger.Log.Error("Failed to start informer cache", "error", err)
		return
	}

	ticker := time.NewTicker(SnapshotInterval)
	defer ticker.Stop()

	// Timers start stopped; armed from DirtyNotify.
	quiet := time.NewTimer(time.Hour)
	maxWait := time.NewTimer(time.Hour)
	stopTimer := func(t *time.Timer) {
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
	}
	stopTimer(quiet)
	stopTimer(maxWait)
	defer stopTimer(quiet)
	defer stopTimer(maxWait)

	flushPending := false
	flush := func() {
		stopTimer(quiet)
		stopTimer(maxWait)
		flushPending = false
		r.sendSnapshotIfChanged()
	}

	// Send initial snapshot immediately after cache is synced
	r.sendSnapshotIfChanged()

	for {
		select {
		case <-r.ctx.Done():
			logger.Log.Info("Snapshot runner stopping due to context cancellation")
			return

		case <-r.cache.DirtyNotify():
			// Coalesce informer bursts. Quiet-period resets on each wake-up;
			// max-wait is armed only once per pending window so continuous
			// churn still flushes (unlike pure debounce, which can starve).
			if !flushPending {
				flushPending = true
				maxWait.Reset(SnapshotMaxWait)
			}
			stopTimer(quiet)
			quiet.Reset(SnapshotQuietPeriod)

		case <-quiet.C:
			flush()

		case <-maxWait.C:
			flush()

		case <-ticker.C:
			// Safety net for any missed wake-ups.
			if flushPending {
				flush()
			} else {
				r.sendSnapshotIfChanged()
			}
		}
	}
}

func (r *Runner) sendSnapshotIfChanged() {
	// Check if there have been any changes since the last snapshot
	if !r.cache.HasChanges() {
		return
	}

	// Reset the dirty flag before generating snapshot
	// This way any changes that occur during generation will be caught
	r.cache.ResetChanges()

	snapshot, err := r.generator.GenerateSnapshot()
	if err != nil {
		logger.Log.Error("Failed to generate snapshot", "error", err)
		return
	}

	msg := &websocketserver.WebSocketMessageToCentralized{
		Type:      websocketserver.MessageTypeClusterSnapshot,
		Data:      snapshot,
		RequestId: uuid.New().String(),
	}

	if err := r.sender.SendSnapshot(msg); err != nil {
		logger.Log.Error("Failed to send snapshot", "error", err)
		return
	}

	// Count total workloads for logging
	workloadCount := 0
	for _, ns := range snapshot.Namespaces {
		workloadCount += len(ns.Workloads)
	}

	logger.Log.Info("Sent cluster snapshot",
		"clusterId", snapshot.ClusterID,
		"namespaces", len(snapshot.Namespaces),
		"workloads", workloadCount)
}
