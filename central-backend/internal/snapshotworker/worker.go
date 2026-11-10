package snapshotworker

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/snapshotsummary"
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
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.backfillSummaries()
		}()
		logger.Log.Info("Snapshot worker pool started", "workers", p.workers)
	})
}

// backfillSummaries builds the summary of every stored snapshot that lacks one:
// snapshots stored before summaries existed, or under an older summary field.
// A proxy only re-sends its snapshot after something in its cluster changes,
// so without this a quiet cluster would have no statistics.
func (p *WorkerPool) backfillSummaries() {
	clusterIDs, err := p.redis.GetClusterSnapshotIDs(p.ctx)
	if err != nil {
		logger.Log.Error("Failed to list cluster snapshots for summary backfill", "error", err)
		return
	}
	summaries, err := p.redis.GetClusterSnapshotSummaries(p.ctx, clusterIDs)
	if err != nil {
		logger.Log.Error("Failed to read cluster snapshot summaries for backfill", "error", err)
		return
	}

	backfilled := 0
	for _, clusterID := range clusterIDs {
		if _, ok := summaries[clusterID]; ok {
			continue
		}
		if p.ctx.Err() != nil {
			return
		}

		snapshot, err := p.redis.GetClusterSnapshot(p.ctx, clusterID)
		if err != nil {
			logger.Log.Warn("Failed to read stored cluster snapshot for summary backfill", "clusterID", clusterID, "error", err)
			continue
		}
		if snapshot == nil {
			continue
		}
		summary, err := snapshotsummary.Compute(snapshot.Data, snapshot.Timestamp)
		if err != nil {
			logger.Log.Warn("Failed to summarize stored cluster snapshot", "clusterID", clusterID, "error", err)
			continue
		}
		written, err := p.redis.SetClusterSnapshotSummaryIfMissing(p.ctx, clusterID, summary)
		if err != nil {
			logger.Log.Warn("Failed to store backfilled snapshot summary", "clusterID", clusterID, "error", err)
			continue
		}
		if written {
			backfilled++
		}
	}

	if backfilled > 0 {
		logger.Log.Info("Backfilled cluster snapshot summaries", "count", backfilled)
	}
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

	// A snapshot that can't be summarized is still stored; its summary is
	// cleared rather than left describing the previous snapshot.
	summary, err := snapshotsummary.Compute(job.Data, timestamp)
	if err != nil {
		logger.Log.Warn("Failed to summarize cluster snapshot",
			"workerID", workerID,
			"clusterID", job.ClusterID,
			"error", err)
	}

	err = p.redis.SetClusterSnapshot(
		p.ctx,
		job.ClusterID,
		job.ClusterName,
		job.Data,
		timestamp,
		summary,
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
