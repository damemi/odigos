package redisclient

import (
	"context"
	"encoding/json"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/odigos-io/central-backend/internal/config"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/central-backend/internal/types"
)

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

func (r *RedisClient) GetProxyConnection(ctx context.Context, key string) (string, error) {
	result, err := r.client.Get(ctx, key).Result()
	if err != nil {
		return "", err
	}
	return result, nil
}

func (r *RedisClient) ListProxyConnectionsKeys(ctx context.Context) ([]string, error) {
	pattern := consts.GetProxyConnectionsPattern()
	keys, err := r.client.Keys(ctx, pattern).Result()
	if err != nil {
		return nil, err
	}
	return keys, nil
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

func (r *RedisClient) Publish(ctx context.Context, channel string, message interface{}) error {
	jsonMessage, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return r.client.Publish(ctx, channel, jsonMessage).Err()
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
	return r.client.Set(ctx, consts.SystemInitializedKey, value, time.Duration(consts.SystemInitializedTTL)*time.Second).Err()
}
