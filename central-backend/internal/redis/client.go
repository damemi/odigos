package redisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/odigos-io/odigos/central-backend/graph/model"
	"github.com/odigos-io/odigos/central-backend/internal/config"
	"github.com/odigos-io/odigos/central-backend/internal/consts"
	"github.com/odigos-io/odigos/central-backend/internal/logger"
	"github.com/odigos-io/odigos/central-backend/internal/snapshotsummary"
	"github.com/odigos-io/odigos/central-backend/internal/types"
)

const (
	SLOW_THRESHOLD_DURATION = 200 * time.Millisecond
)

// RedisHashFieldValuesByKey represents results from Redis HGETALL across multiple hashes:
// map[redisHashKey]map[field]value
type RedisHashFieldValuesByKey = map[string]map[string]string

// PublishJSON publishes a typed value as JSON without requiring interface{} at call sites.
// This is a package-level helper because methods cannot declare type parameters.
func PublishJSON[T any](ctx context.Context, r *RedisClient, channel string, message T) error {
	b, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, channel, b).Err()
}

// OnReconnectFunc is called when Redis reconnects after being unavailable.
type OnReconnectFunc func(ctx context.Context)

type RedisClient struct {
	client         *redis.Client
	onReconnect    OnReconnectFunc
	lastResyncNano atomic.Int64
}

const resyncDebounceNanos = int64(5 * time.Second)

// scan count and batch size are used to scan the Redis database for keys and values in batches. 500 for faster scanning, 200 for batching.
const (
	scanCount = int64(500)
	batchSize = 200
)

func NewRedisClient(ctx context.Context) (*RedisClient, error) {
	rc := &RedisClient{}

	rdb := redis.NewClient(&redis.Options{
		Addr: config.Config.RedisAddr,
		// Increased pool size to handle many concurrent cluster connections
		// Default is 10 * runtime.GOMAXPROCS(0) which may not be enough for 60+ clusters
		PoolSize:     300,
		MinIdleConns: 20,
		// Retry on transient failures
		MaxRetries: 3,
		OnConnect: func(ctx context.Context, cn *redis.Conn) error {
			// Debounce: Run the first reconnect and ignore the rest for 5 seconds.
			now := time.Now().UnixNano()
			last := rc.lastResyncNano.Load()
			if now-last < resyncDebounceNanos {
				return nil
			}

			if !rc.lastResyncNano.CompareAndSwap(last, now) {
				return nil
			}

			logger.Log.Info("Redis reconnected, resyncing proxy keys")
			if rc.onReconnect != nil {
				rc.onReconnect(ctx)
			}
			return nil
		},
	})

	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		return nil, err
	}

	rc.client = rdb

	return rc, nil
}

// Sets a callback to be invoked when Redis reconnects after being unavailable.
// Currently used to resync all proxy connections to Redis in case redis is lost.
func (r *RedisClient) SetOnReconnect(fn OnReconnectFunc) {
	r.onReconnect = fn
}

func (r *RedisClient) DeleteProxyConnection(ctx context.Context, proxyID string) error {
	metaKey := consts.GetProxyMetadataKey(proxyID) // proxy-meta:{backendID}:{proxyID}
	// The snapshot (and its summary) goes with the connection, so a deleted
	// cluster doesn't leave stale workloads behind for the snapshot queries.
	snapshotKey := consts.GetClusterSnapshotKey(proxyID)

	result := r.client.Del(ctx, metaKey, snapshotKey)
	if result.Err() != nil {
		return result.Err()
	}
	return nil
}

func (r *RedisClient) hgetAllBatch(ctx context.Context, keys []string) (RedisHashFieldValuesByKey, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	pipe := r.client.Pipeline()
	cmds := make(map[string]*redis.StringStringMapCmd, len(keys))
	for _, key := range keys {
		cmds[key] = pipe.HGetAll(ctx, key)
	}

	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}

	out := make(RedisHashFieldValuesByKey, len(keys))
	for key, cmd := range cmds {
		m, err := cmd.Result()
		if err != nil {
			return nil, err
		}
		out[key] = m
	}
	return out, nil
}

// GetProxyMetaKeyValues scans all proxy metadata hashes (proxy-meta:*)
func (r *RedisClient) GetProxyMetaKeyValues(ctx context.Context) (RedisHashFieldValuesByKey, error) {
	pattern := consts.GetProxyMetaConnectionsPattern()

	result := make(RedisHashFieldValuesByKey)
	var cursor uint64

	batch := make([]string, 0, batchSize)
	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, pattern, scanCount).Result()
		if err != nil {
			return nil, err
		}

		for _, key := range keys {
			batch = append(batch, key)
			if len(batch) >= batchSize {
				m, err := r.hgetAllBatch(ctx, batch)
				if err != nil {
					return nil, err
				}
				for k, v := range m {
					result[k] = v
				}
				batch = batch[:0]
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	if len(batch) > 0 {
		m, err := r.hgetAllBatch(ctx, batch)
		if err != nil {
			return nil, err
		}
		for k, v := range m {
			result[k] = v
		}
	}
	return result, nil
}

func (r *RedisClient) SubscribeToRequests(ctx context.Context, proxyID string, handler func(ctx context.Context, proxyID string, request types.GqlRequestPayload) error) {
	requestsChannel := consts.GetRequestsChannel(proxyID)
	retryDelay := time.Second
	maxRetryDelay := 30 * time.Second

	// Outer loop: handles reconnection with exponential backoff
	for {

		if ctx.Err() != nil {
			logger.Log.Info("Stopping Redis Requests subscription", "proxyID", proxyID)
			return
		}

		sub := r.client.Subscribe(ctx, requestsChannel)
		retryDelay = time.Second
		ch := sub.Channel()

		// Inner loop: handles message processing
	innerLoop:
		for {
			select {
			case <-ctx.Done():
				sub.Close()
				logger.Log.Info("Stopping Redis Requests subscription", "proxyID", proxyID)
				return
			case msg, ok := <-ch:
				if !ok {
					sub.Close()
					logger.Log.Warn("Redis subscription lost, will reconnect", "proxyID", proxyID, "retryIn", retryDelay)
					break innerLoop
				}

				retryDelay = time.Second

				var request types.GqlRequestPayload
				if err := json.Unmarshal([]byte(msg.Payload), &request); err != nil {
					logger.Log.Warn("Failed to unmarshal request", "error", err)
					continue
				}

				if err := handler(ctx, proxyID, request); err != nil {
					logger.Log.Error("Failed to handle request", "error", err)
				}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(retryDelay):
			retryDelay = min(retryDelay*2, maxRetryDelay)
		}
	}
}

// SubscribeToResponses subscribes to a response channel and waits for Redis
// to confirm the subscription before returning. This prevents a race condition where a response
// could be published before the subscription is established.
func (r *RedisClient) SubscribeToResponses(ctx context.Context, proxyID string, requestId string) (*redis.PubSub, <-chan *redis.Message, error) {
	channel := consts.GetResponsesChannel(proxyID, requestId)
	sub := r.client.Subscribe(ctx, channel)

	// Wait for subscription confirmation from Redis
	_, err := sub.Receive(ctx)
	if err != nil {
		sub.Close()
		return nil, nil, fmt.Errorf("failed to confirm subscription: %w", err)
	}

	return sub, sub.Channel(), nil
}

func (r *RedisClient) SubscribeToSSEBroadcast(ctx context.Context, channel string, handler func(payload string)) {
	sub := r.client.Subscribe(ctx, channel)
	ch := sub.Channel()
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("Stopping Redis SSE broadcast subscription", "channel", channel)
			return
		case msg, ok := <-ch:
			if !ok {
				logger.Log.Warn("Redis SSE broadcast subscription channel closed unexpectedly", "channel", channel)
				return
			}
			handler(msg.Payload)
		}
	}
}

func (r *RedisClient) Close() error {
	err := r.client.Close()
	if err != nil {
		return err
	}
	return nil
}

// HealthCheck performs a health check on the Redis connection
func (r *RedisClient) HealthCheck(ctx context.Context) model.ServicesStatus {
	if r.client == nil {
		msg := "Redis client not initialized"
		return model.ServicesStatus{
			Status:  model.HealthStatusDown,
			Message: &msg,
		}
	}
	// Measure response time
	start := time.Now()
	_, err := r.client.Ping(ctx).Result()
	duration := time.Since(start)

	if err != nil {
		msg := fmt.Sprintf("Redis ping failed: %v", err)
		return model.ServicesStatus{
			Status:  model.HealthStatusDown,
			Message: &msg,
		}
	}

	if duration > SLOW_THRESHOLD_DURATION {
		msg := fmt.Sprintf("Redis is slow (response time: %dms)", duration.Milliseconds())
		return model.ServicesStatus{
			Status:  model.HealthStatusWarning,
			Message: &msg,
		}
	}
	return model.ServicesStatus{
		Status:  model.HealthStatusHealthy,
		Message: nil,
	}
}

// HasSubscribers checks if a Redis pub/sub channel has any active subscribers.
// Uses the PUBSUB NUMSUB command to query subscriber count.
func (r *RedisClient) HasSubscribers(ctx context.Context, channel string) (bool, error) {
	result, err := r.client.PubSubNumSub(ctx, channel).Result()
	if err != nil {
		return false, err
	}
	count, exists := result[channel]
	return exists && count > 0, nil
}

// ProxyMetadata holds durable proxy details and timestamps.
type ProxyMetadata struct {
	PlatformName         string
	PlatformType         consts.PlatformType
	OdigosVersion        string
	GraphQLSchemaVersion string
	ConnectedAt          int64
	LastSeenAt           int64
	Status               consts.ProxyConnectionStatus
}

// SetProxyMetadata sets basic proxy metadata fields (name/type/version) without touching timestamps.
// graphqlSchemaVersion is the agents-api schema version reported by VM proxies (empty for k8s proxies).
func (r *RedisClient) SetProxyMetadata(ctx context.Context, proxyID, name, platformType, version, graphqlSchemaVersion string) error {
	key := consts.GetProxyMetadataKey(proxyID)
	return r.client.HSet(
		ctx,
		key,
		consts.ProxyMetaFieldPlatformName, name,
		consts.ProxyMetaFieldPlatformType, platformType,
		consts.ProxyMetaFieldOdigosVersion, version,
		consts.ProxyMetaFieldGraphQLSchemaVersion, graphqlSchemaVersion,
	).Err()
}

// SetProxyConnectedAt sets the time when the proxy connected (unix seconds).
func (r *RedisClient) SetProxyConnectedAt(ctx context.Context, proxyID string, connectedAt int64) error {
	key := consts.GetProxyMetadataKey(proxyID)
	return r.client.HSet(ctx, key, consts.ProxyMetaFieldConnectedAt, strconv.FormatInt(connectedAt, 10)).Err()
}

// UpdateProxyLastSeen updates the last seen timestamp (unix seconds).
func (r *RedisClient) UpdateProxyLastSeen(ctx context.Context, proxyID string, lastSeenAt int64) error {
	key := consts.GetProxyMetadataKey(proxyID)
	return r.client.HSet(ctx, key, consts.ProxyMetaFieldLastSeenAt, strconv.FormatInt(lastSeenAt, 10)).Err()
}

// SetProxyMetadataStatus sets the connection status field inside proxy metadata hash.
func (r *RedisClient) SetProxyMetadataStatus(ctx context.Context, proxyID string, status consts.ProxyConnectionStatus) error {
	key := consts.GetProxyMetadataKey(proxyID)
	return r.client.HSet(ctx, key, consts.ProxyMetaFieldStatus, string(status)).Err()
}

type ClusterSnapshotData struct {
	ClusterID   string `json:"cluster_id"`
	ClusterName string `json:"cluster_name"`
	Data        []byte `json:"data"`
	Timestamp   int64  `json:"timestamp"`
}

// SetClusterSnapshot stores a cluster snapshot in Redis.
// The snapshot is stored as a hash with data, timestamp, cluster name and summary fields.
// A nil summary clears the stored one, so it never describes an older snapshot.
func (r *RedisClient) SetClusterSnapshot(ctx context.Context, clusterID string, clusterName string, data []byte, timestamp int64, summary *snapshotsummary.Summary) error {
	key := consts.GetClusterSnapshotKey(clusterID)
	values := []interface{}{
		consts.SnapshotFieldData, string(data),
		consts.SnapshotFieldTimestamp, strconv.FormatInt(timestamp, 10),
		consts.SnapshotFieldClusterName, clusterName,
	}
	if summary != nil {
		summaryJSON, err := json.Marshal(summary)
		if err != nil {
			return fmt.Errorf("failed to marshal snapshot summary: %w", err)
		}
		values = append(values, consts.SnapshotFieldSummary, string(summaryJSON))
	}

	// One transaction, so a reader never pairs new data with the previous summary.
	_, err := r.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, key, values...)
		if summary == nil {
			pipe.HDel(ctx, key, consts.SnapshotFieldSummary)
		}
		return nil
	})
	return err
}

// SetWorkloadSummary stores compact agent statistics without inventing a
// Kubernetes snapshot. The connection list uses the same batched summary read.
func (r *RedisClient) SetWorkloadSummary(ctx context.Context, proxyID string, summary *snapshotsummary.Summary) error {
	if err := ValidateWorkloadSummary(summary); err != nil {
		return err
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return fmt.Errorf("failed to marshal workload summary: %w", err)
	}
	_, err = r.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.HSet(ctx, consts.GetClusterSnapshotKey(proxyID), consts.SnapshotFieldSummary, summaryJSON)
		pipe.HSet(ctx, consts.GetProxyMetadataKey(proxyID), consts.ProxyMetaFieldLastSeenAt, time.Now().Unix())
		return nil
	})
	return err
}

func ValidateWorkloadSummary(summary *snapshotsummary.Summary) error {
	if summary == nil || summary.Workloads < 0 || summary.InstrumentedWorkloads < 0 || summary.InstrumentedWorkloads > summary.Workloads || summary.SnapshotTimestamp <= 0 {
		return fmt.Errorf("invalid workload summary")
	}
	healthTotal := 0
	for _, count := range summary.WorkloadsByHealth {
		if count < 0 || count > summary.InstrumentedWorkloads-healthTotal {
			return fmt.Errorf("invalid workload health count")
		}
		healthTotal += count
	}
	return nil
}

// setSummaryIfMissingScript writes the summary only while the snapshot still
// exists and has none. A snapshot stored in the meantime carries its own
// summary, and a deleted one must not be recreated as a summary-only hash.
var setSummaryIfMissingScript = redis.NewScript(`
if redis.call('HEXISTS', KEYS[1], ARGV[1]) == 0 then
	return 0
end
return redis.call('HSETNX', KEYS[1], ARGV[2], ARGV[3])
`)

// SetClusterSnapshotSummaryIfMissing stores a summary built from an already
// stored snapshot. It reports whether the summary was written.
func (r *RedisClient) SetClusterSnapshotSummaryIfMissing(ctx context.Context, clusterID string, summary *snapshotsummary.Summary) (bool, error) {
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		return false, fmt.Errorf("failed to marshal snapshot summary: %w", err)
	}
	key := consts.GetClusterSnapshotKey(clusterID)
	written, err := setSummaryIfMissingScript.Run(ctx, r.client, []string{key},
		consts.SnapshotFieldData, consts.SnapshotFieldSummary, string(summaryJSON)).Int()
	if err != nil {
		return false, err
	}
	return written == 1, nil
}

// GetClusterSnapshotSummaries returns the stored summary of each given cluster
// in one round trip. Clusters without a snapshot, or whose snapshot has no
// summary yet, are left out of the result.
func (r *RedisClient) GetClusterSnapshotSummaries(ctx context.Context, clusterIDs []string) (map[string]*snapshotsummary.Summary, error) {
	result := make(map[string]*snapshotsummary.Summary, len(clusterIDs))
	if len(clusterIDs) == 0 {
		return result, nil
	}

	pipe := r.client.Pipeline()
	cmds := make([]*redis.StringCmd, len(clusterIDs))
	for i, clusterID := range clusterIDs {
		cmds[i] = pipe.HGet(ctx, consts.GetClusterSnapshotKey(clusterID), consts.SnapshotFieldSummary)
	}
	// Exec reports redis.Nil when any field is missing; each command is checked below.
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	for i, cmd := range cmds {
		raw, err := cmd.Result()
		if err == redis.Nil {
			continue
		}
		if err != nil {
			return nil, err
		}
		var summary snapshotsummary.Summary
		if err := json.Unmarshal([]byte(raw), &summary); err != nil {
			logger.Log.Warn("Failed to parse cluster snapshot summary", "clusterID", clusterIDs[i], "error", err)
			continue
		}
		result[clusterIDs[i]] = &summary
	}
	return result, nil
}

// GetClusterSnapshotIDs lists the ids of all clusters that have a stored snapshot.
func (r *RedisClient) GetClusterSnapshotIDs(ctx context.Context) ([]string, error) {
	var clusterIDs []string
	iter := r.client.Scan(ctx, 0, consts.GetClusterSnapshotsPattern(), scanCount).Iterator()
	for iter.Next(ctx) {
		if clusterID := consts.ExtractClusterIDFromSnapshotKey(iter.Val()); clusterID != "" {
			clusterIDs = append(clusterIDs, clusterID)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return clusterIDs, nil
}

// GetClusterSnapshot retrieves a cluster snapshot from Redis.
// Returns nil if the snapshot doesn't exist.
func (r *RedisClient) GetClusterSnapshot(ctx context.Context, clusterID string) (*ClusterSnapshotData, error) {
	key := consts.GetClusterSnapshotKey(clusterID)
	m, err := r.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if _, hasData := m[consts.SnapshotFieldData]; !hasData {
		return nil, nil // No snapshot exists
	}

	snapshot := &ClusterSnapshotData{
		ClusterID:   clusterID,
		ClusterName: m[consts.SnapshotFieldClusterName],
		Data:        []byte(m[consts.SnapshotFieldData]),
	}

	if v := m[consts.SnapshotFieldTimestamp]; v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			snapshot.Timestamp = n
		}
	}

	return snapshot, nil
}

// GetAllClusterSnapshots retrieves all cluster snapshots from Redis.
// Returns a map of clusterID -> ClusterSnapshotData.
func (r *RedisClient) GetAllClusterSnapshots(ctx context.Context) (map[string]*ClusterSnapshotData, error) {
	pattern := consts.GetClusterSnapshotsPattern()
	keys, err := r.client.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, err
	}

	result := make(map[string]*ClusterSnapshotData)
	for _, key := range keys {
		clusterID := consts.ExtractClusterIDFromSnapshotKey(key)
		if clusterID == "" {
			continue
		}

		snapshot, err := r.GetClusterSnapshot(ctx, clusterID)
		if err != nil {
			logger.Log.Warn("Failed to get cluster snapshot", "clusterID", clusterID, "error", err)
			continue
		}
		if snapshot != nil {
			result[clusterID] = snapshot
		}
	}

	return result, nil
}

// DeleteClusterSnapshot removes a cluster snapshot from Redis.
func (r *RedisClient) DeleteClusterSnapshot(ctx context.Context, clusterID string) error {
	key := consts.GetClusterSnapshotKey(clusterID)
	return r.client.Del(ctx, key).Err()
}
