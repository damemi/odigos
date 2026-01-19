package redisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/odigos-io/central-backend/graph/model"
	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/types"
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

	result := r.client.Del(ctx, metaKey)
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

func (r *RedisClient) SubscribeToResponses(ctx context.Context, proxyID string, requestId string, handler func(ctx context.Context, proxyID string, response string) error) {
	redisResponsesChannelKey := consts.GetResponsesChannel(proxyID, requestId)
	sub := r.client.Subscribe(ctx, redisResponsesChannelKey)
	ch := sub.Channel()

	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("Stopping Redis response subscription", "proxyID", proxyID)
			return
		case msg, ok := <-ch:
			if !ok {
				logger.Log.Warn("Redis response subscription channel closed unexpectedly", "proxyID", proxyID)
				return
			}
			response := msg.Payload
			if err := handler(ctx, proxyID, response); err != nil {
				logger.Log.Error("Failed to handle response", "error", err)
			}
			return
		}
	}
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

// DeleteProxyKeysForBackend deletes all proxy-related keys for a specific backend ID
func (r *RedisClient) DeleteProxyKeysForBackend(ctx context.Context, backendID string) error {
	if backendID == "" {
		return fmt.Errorf("backend ID cannot be empty")
	}
	// Delete proxy metadata keys (proxy-meta:{backendID}:*)
	metadataPattern := fmt.Sprintf("proxy-meta:%s:*", backendID)
	metadataKeys, err := r.client.Keys(ctx, metadataPattern).Result()
	if err != nil {
		return err
	}

	// Combine all keys to delete

	if len(metadataKeys) == 0 {
		logger.Log.Info("No proxy keys found for backend", "backendID", backendID)
		return nil
	}

	// Delete all found keys for this specific backend
	err = r.client.Del(ctx, metadataKeys...).Err()
	if err != nil {
		logger.Log.Error("Failed to delete proxy keys for backend", "backendID", backendID, "metadataKeys", len(metadataKeys), "error", err)
		return err
	}

	logger.Log.Info("Deleted proxy keys for backend", "backendID", backendID, "metadataKeys", len(metadataKeys), "total", len(metadataKeys))
	return nil
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
	PlatformName  string
	PlatformType  string
	OdigosVersion string
	ConnectedAt   int64
	LastSeenAt    int64
	Status        consts.ProxyConnectionStatus
}

// SetProxyMetadata sets basic proxy metadata fields (name/type/version) without touching timestamps.
func (r *RedisClient) SetProxyMetadata(ctx context.Context, proxyID, name, platformType, version string) error {
	key := consts.GetProxyMetadataKey(proxyID)
	return r.client.HSet(
		ctx,
		key,
		consts.ProxyMetaFieldPlatformName, name,
		consts.ProxyMetaFieldPlatformType, platformType,
		consts.ProxyMetaFieldOdigosVersion, version,
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
