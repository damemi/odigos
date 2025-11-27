package redisclient

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/go-redis/redis/v8"
	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/types"
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

type RedisClient struct {
	client *redis.Client
}

func NewRedisClient(ctx context.Context) (*RedisClient, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr: config.Config.RedisAddr,
	})

	_, err := rdb.Ping(ctx).Result()
	if err != nil {
		return nil, err
	}

	return &RedisClient{
		client: rdb,
	}, nil
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

func (r *RedisClient) MarkAllConnectionsAsDisconnected(ctx context.Context) error {
	pattern := consts.GetProxyConnectionsPattern()
	keys, err := r.client.Keys(ctx, pattern).Result()
	if err != nil {
		return err
	}

	for _, key := range keys {
		err := r.client.Set(ctx, key, consts.StatusDisconnected, 0).Err()
		if err != nil {
			logger.Log.Error("Failed to remove connection from Redis", "key", key, "error", err)
		}
	}

	return nil
}

func (r *RedisClient) Close() error {
	err := r.client.Close()
	if err != nil {
		return err
	}
	return nil
}

// GetSystemInitialized checks if the system has been initialized (has users)
func (r *RedisClient) GetSystemInitialized(ctx context.Context) (bool, error) {
	result, err := r.client.Get(ctx, consts.SystemInitializedKey).Result()

	if err != nil {
		if err.Error() == "redis: nil" {
			return false, nil
		}
		return false, err
	}
	return result == "true", nil
}

// SetSystemInitialized sets the system initialization status
func (r *RedisClient) SetSystemInitialized(ctx context.Context, initialized bool) error {
	value := "false"
	if initialized {
		value = "true"
	}
	return r.client.Set(ctx, consts.SystemInitializedKey, value, 0).Err()
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
