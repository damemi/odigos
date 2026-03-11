package snapshot

import (
	"context"
	"time"

	"github.com/google/uuid"
	"k8s.io/client-go/kubernetes"

	"github.com/odigos-io/central-proxy/internal/logger"
	websocketserver "github.com/odigos-io/central-proxy/internal/websocket"
	"github.com/odigos-io/central-proxy/kube"
	odigosclientset "github.com/odigos-io/odigos/api/generated/odigos/clientset/versioned"
)

const (
	// SnapshotInterval is the time between checking for changes
	SnapshotInterval = 5 * time.Second
)

// MessageSender is the interface for sending messages over WebSocket
type MessageSender interface {
	SendSnapshot(msg *websocketserver.WebSocketMessageToCentralized) error
}

// Runner manages periodic snapshot generation and sending
type Runner struct {
	generator *Generator
	sender    MessageSender
	cache     *kube.Cache
	ctx       context.Context
}

// NewRunner creates a new snapshot runner with informer-based caching
func NewRunner(ctx context.Context, clusterID, clusterName string, clientset kubernetes.Interface, odigosClient odigosclientset.Interface, sender MessageSender, ignoredNamespaces []string) *Runner {
	cache := kube.NewCache(clientset, odigosClient)
	generator := NewGenerator(clusterID, clusterName, cache, ignoredNamespaces)

	return &Runner{
		generator: generator,
		sender:    sender,
		cache:     cache,
		ctx:       ctx,
	}
}

// Start begins the cache sync and periodic snapshot generation loop
func (r *Runner) Start() {
	logger.Log.Info("Starting cluster snapshot runner",
		"interval", SnapshotInterval)

	// Start the informer cache and wait for sync
	if err := r.cache.Start(r.ctx); err != nil {
		logger.Log.Error("Failed to start informer cache", "error", err)
		return
	}

	ticker := time.NewTicker(SnapshotInterval)
	defer ticker.Stop()

	// Send initial snapshot immediately after cache is synced
	r.sendSnapshotIfChanged()

	for {
		select {
		case <-r.ctx.Done():
			logger.Log.Info("Snapshot runner stopping due to context cancellation")
			return
		case <-ticker.C:
			r.sendSnapshotIfChanged()
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
