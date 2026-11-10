package redisclient

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/odigos-io/odigos/central-backend/internal/consts"
)

func setupTestRedis(t *testing.T) (*miniredis.Miniredis, *RedisClient) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("failed to start miniredis: %v", err)
	}

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	rc := &RedisClient{client: client}
	return mr, rc
}

func TestSubscribeToResponses_ReturnsValidChannels(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sub, msgChan, err := rc.SubscribeToResponses(ctx, "test-proxy", "test-request-123")
	if err != nil {
		t.Fatalf("SubscribeToResponses failed: %v", err)
	}
	defer sub.Close()

	if sub == nil {
		t.Fatal("expected non-nil subscription")
	}
	if msgChan == nil {
		t.Fatal("expected non-nil message channel")
	}
}

func TestSubscribeToResponses_ReceivesPublishedMessage(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proxyID := "test-proxy"
	requestID := "test-request-456"
	expectedResponse := `{"data": {"getOverviewMetrics": {"sources": []}}}`

	sub, msgChan, err := rc.SubscribeToResponses(ctx, proxyID, requestID)
	if err != nil {
		t.Fatalf("SubscribeToResponses failed: %v", err)
	}
	defer sub.Close()

	responseChannel := consts.GetResponsesChannel(proxyID, requestID)
	if err := rc.client.Publish(ctx, responseChannel, expectedResponse).Err(); err != nil {
		t.Fatalf("failed to publish response: %v", err)
	}

	select {
	case msg := <-msgChan:
		if msg.Payload != expectedResponse {
			t.Errorf("expected payload %q, got %q", expectedResponse, msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestSubscribeToResponses_FastResponseNotLost(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	proxyID := "test-proxy"
	requestID := "test-request-789"
	expectedResponse := `{"data": "fast response"}`

	sub, msgChan, err := rc.SubscribeToResponses(ctx, proxyID, requestID)
	if err != nil {
		t.Fatalf("SubscribeToResponses failed: %v", err)
	}
	defer sub.Close()

	responseChannel := consts.GetResponsesChannel(proxyID, requestID)
	if err := rc.client.Publish(ctx, responseChannel, expectedResponse).Err(); err != nil {
		t.Fatalf("failed to publish response: %v", err)
	}

	select {
	case msg := <-msgChan:
		if msg.Payload != expectedResponse {
			t.Errorf("expected payload %q, got %q", expectedResponse, msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout - fast response was lost")
	}
}

func TestSubscribeToResponses_ContextCancellation(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err := rc.SubscribeToResponses(ctx, "test-proxy", "test-request-cancel")
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
}

func TestHasSubscribers_ReturnsTrueWhenSubscribed(t *testing.T) {
	mr, rc := setupTestRedis(t)
	defer mr.Close()

	ctx := context.Background()
	channel := "test-channel"

	has, err := rc.HasSubscribers(ctx, channel)
	if err != nil {
		t.Fatalf("HasSubscribers failed: %v", err)
	}
	if has {
		t.Error("expected no subscribers initially")
	}

	sub := rc.client.Subscribe(ctx, channel)
	defer sub.Close()

	_, err = sub.Receive(ctx)
	if err != nil {
		t.Fatalf("failed to confirm subscription: %v", err)
	}

	has, err = rc.HasSubscribers(ctx, channel)
	if err != nil {
		t.Fatalf("HasSubscribers failed: %v", err)
	}
	if !has {
		t.Error("expected to have subscribers after subscribing")
	}
}
