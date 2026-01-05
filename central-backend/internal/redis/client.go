package redisclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
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
	client      *redis.Client
	onReconnect OnReconnectFunc
}

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
	})

	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		return nil, err
	}

	rc.client = rdb

	return rc, nil
}

// StartHealthMonitor runs a background goroutine that monitors Redis health.
// It calls onReconnect only when Redis transitions from unhealthy to healthy,
// which indicates a Redis restart requiring data resync.
func (r *RedisClient) StartHealthMonitor(ctx context.Context, interval time.Duration) {
	wasHealthy := true
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("Stopping Redis health monitor")
			return
		case <-ticker.C:
			_, err := r.client.Ping(ctx).Result()
			isHealthy := err == nil

			if !wasHealthy && isHealthy {
				logger.Log.Info("Redis reconnected after failure, resyncing proxy keys")
				if r.onReconnect != nil {
					r.onReconnect(ctx)
				}
			} else if wasHealthy && !isHealthy {
				logger.Log.Warn("Redis connection lost", "error", err)
			}

			wasHealthy = isHealthy
		}
	}
}

// Sets a callback to be invoked when Redis reconnects after being unavailable.
// Currently used to resync all proxy connections to Redis in case redis is lost.
func (r *RedisClient) SetOnReconnect(fn OnReconnectFunc) {
	r.onReconnect = fn
}

func (r *RedisClient) AddProxyConnection(ctx context.Context, proxyID, status string) error {
	key := consts.GetProxyConnectionChannelKey(proxyID)
	return r.client.Set(ctx, key, status, 0).Err()
}

func (r *RedisClient) SetProxyConnectionStatus(ctx context.Context, proxyID string, status consts.ProxyConnectionStatus) error {
	key := consts.GetProxyConnectionChannelKey(proxyID)
	return r.client.Set(ctx, key, string(status), 0).Err()
}

func (r *RedisClient) GetProxyConnection(ctx context.Context, key string) (consts.ProxyConnectionStatus, error) {
	result, err := r.client.Get(ctx, key).Result()
	if err != nil {
		return "", err
	}
	return consts.ProxyConnectionStatus(result), nil
}

func (r *RedisClient) ListProxyConnectionsKeys(ctx context.Context) ([]string, error) {
	pattern := consts.GetProxyConnectionsPattern()
	keys, err := r.client.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func (r *RedisClient) DeleteProxyConnection(ctx context.Context, proxyID string) error {
	key := consts.GetProxyConnectionChannelKey(proxyID)
	result := r.client.Del(ctx, key)
	if result.Err() != nil {
		return result.Err()
	}
	return nil
}

func (r *RedisClient) SubscribeToRequests(ctx context.Context, proxyID string, handler func(ctx context.Context, proxyID string, request types.GqlRequestPayload) error) {
	requestsChannel := consts.GetRequestsChannel(proxyID)
	sub := r.client.Subscribe(ctx, requestsChannel)
	ch := sub.Channel()

	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("Stopping Redis Requests subscription", "proxyID", proxyID)
			return
		case msg, ok := <-ch:
			if !ok {
				logger.Log.Warn("Redis request subscription channel closed unexpectedly", "proxyID", proxyID)
				return
			}

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

	// Delete proxy connection keys (proxy:{backendID}:*)
	connectionPattern := fmt.Sprintf("proxy:%s:*", backendID)
	connectionKeys, err := r.client.Keys(ctx, connectionPattern).Result()
	if err != nil {
		return err
	}

	// Delete proxy metadata keys (proxy-meta:{backendID}:*)
	metadataPattern := fmt.Sprintf("proxy-meta:%s:*", backendID)
	metadataKeys, err := r.client.Keys(ctx, metadataPattern).Result()
	if err != nil {
		return err
	}

	// Combine all keys to delete
	allKeys := append(connectionKeys, metadataKeys...)

	if len(allKeys) == 0 {
		logger.Log.Info("No proxy keys found for backend", "backendID", backendID)
		return nil
	}

	// Delete all found keys for this specific backend
	err = r.client.Del(ctx, allKeys...).Err()
	if err != nil {
		logger.Log.Error("Failed to delete proxy keys for backend", "backendID", backendID, "connectionKeys", len(connectionKeys), "metadataKeys", len(metadataKeys), "error", err)
		return err
	}

	logger.Log.Info("Deleted proxy keys for backend", "backendID", backendID, "connectionKeys", len(connectionKeys), "metadataKeys", len(metadataKeys), "total", len(allKeys))
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

// GetProxyMetadata returns proxy metadata if present. Missing fields will be empty/zero.
func (r *RedisClient) GetProxyMetadata(ctx context.Context, proxyID string) (*ProxyMetadata, error) {
	key := consts.GetProxyMetadataKey(proxyID)
	m, err := r.client.HGetAll(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	md := &ProxyMetadata{
		PlatformName:  m[consts.ProxyMetaFieldPlatformName],
		PlatformType:  m[consts.ProxyMetaFieldPlatformType],
		OdigosVersion: m[consts.ProxyMetaFieldOdigosVersion],
	}
	if v := m[consts.ProxyMetaFieldConnectedAt]; v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			md.ConnectedAt = n
		}
	}
	if v := m[consts.ProxyMetaFieldLastSeenAt]; v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			md.LastSeenAt = n
		}
	}
	return md, nil
}
