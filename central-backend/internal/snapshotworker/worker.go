package snapshotworker

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type SnapshotJob struct {
	ClusterID   string          `json:"cluster_id"`
	ClusterName string          `json:"cluster_name"`
	Timestamp   int64           `json:"timestamp"`
	Data        json.RawMessage `json:"data"` // Raw JSON snapshot data (namespaces, sources, etc.)
}

// WorkerPool manages a pool of workers that process cluster snapshots
type WorkerPool struct {
	workers     int
	queue       chan SnapshotJob
	redis       *redisclient.RedisClient
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	started     bool
	startedOnce sync.Once
}

func NewWorkerPool(redis *redisclient.RedisClient, workerCount int, queueSize int) *WorkerPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &WorkerPool{
		workers: workerCount,
		queue:   make(chan SnapshotJob, queueSize),
		redis:   redis,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (p *WorkerPool) Start() {
	p.startedOnce.Do(func() {
		p.started = true
		for i := 0; i < p.workers; i++ {
			p.wg.Add(1)
			go p.worker(i)
		}
		logger.Log.Info("Snapshot worker pool started", "workers", p.workers)
	})
}

func (p *WorkerPool) Stop() {
	if !p.started {
		return
	}
	p.cancel()
	close(p.queue)
	p.wg.Wait()
	logger.Log.Info("Snapshot worker pool stopped")
}

// Submit adds a snapshot job to the processing queue
// Returns false if the queue is full (non-blocking)
func (p *WorkerPool) Submit(job SnapshotJob) bool {
	select {
	case p.queue <- job:
		return true
	default:
		logger.Log.Warn("Snapshot queue full, dropping job",
			"clusterID", job.ClusterID,
			"clusterName", job.ClusterName)
		return false
	}
}

func (p *WorkerPool) worker(id int) {
	defer p.wg.Done()

	for {
		select {
		case <-p.ctx.Done():
			logger.Log.Info("Snapshot worker shutting down", "workerID", id)
			return
		case job, ok := <-p.queue:
			if !ok {
				logger.Log.Info("Snapshot worker queue closed", "workerID", id)
				return
			}
			p.processJob(id, job)
		}
	}
}

// processJob handles a single snapshot job
func (p *WorkerPool) processJob(workerID int, job SnapshotJob) {
	startTime := time.Now()

	timestamp := job.Timestamp
	if timestamp == 0 {
		timestamp = time.Now().Unix()
	}

	err := p.redis.SetClusterSnapshot(
		p.ctx,
		job.ClusterID,
		job.ClusterName,
		job.Data,
		timestamp,
	)

	if err != nil {
		logger.Log.Error("Failed to store cluster snapshot",
			"workerID", workerID,
			"clusterID", job.ClusterID,
			"clusterName", job.ClusterName,
			"error", err)
		return
	}

	duration := time.Since(startTime)
	logger.Log.Info("Cluster snapshot stored",
		"workerID", workerID,
		"clusterID", job.ClusterID,
		"clusterName", job.ClusterName,
		"dataSize", len(job.Data),
		"duration", duration)
}

func (p *WorkerPool) QueueLength() int {
	return len(p.queue)
}
