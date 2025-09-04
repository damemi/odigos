package ssehub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/odigos-io/central-backend/internal/consts"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type Event struct {
	Type        string          `json:"type"`
	Data        json.RawMessage `json:"data"`
	ClusterID   string          `json:"cluster_id,omitempty"`
	ClusterName string          `json:"cluster_name,omitempty"`
	Timestamp   int64           `json:"timestamp,omitempty"`
}

type Hub struct {
	mu         sync.Mutex
	clients    map[chan []byte]struct{}
	redis      *redisclient.RedisClient
	instanceID string
}

type redisEnvelope struct {
	Origin string `json:"origin"`
	Event  Event  `json:"event"`
}

func NewWithRedis(r *redisclient.RedisClient, instanceID string) *Hub {
	return &Hub{clients: make(map[chan []byte]struct{}), redis: r, instanceID: instanceID}
}

func (h *Hub) StartRedisSSEBroadcast(ctx context.Context) {
	if h.redis == nil {
		return
	}
	channel := consts.GetSSEBroadcastChannel()
	go h.redis.SubscribeToSSEBroadcast(ctx, channel, func(payload string) {
		var env redisEnvelope
		if err := json.Unmarshal([]byte(payload), &env); err != nil {
			return
		}
		if env.Origin == h.instanceID {
			return
		}
		h.broadcastLocal(env.Event)
	})
}

func (h *Hub) Broadcast(ev Event) {
	h.broadcastLocal(ev)

	if h.redis != nil {
		env := redisEnvelope{Origin: h.instanceID, Event: ev}
		_ = h.redis.Publish(context.Background(), consts.GetSSEBroadcastChannel(), env)
	}
}

func (h *Hub) broadcastLocal(ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- b:
		default:
		}
	}
}

func (h *Hub) SSEHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := make(chan []byte, 32)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()

	fmt.Fprintf(w, "data: %s\n\n", `{"type":"connected","data":"ok"}`)
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	notify := r.Context().Done()
	for {
		select {
		case b := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", string(b))
			flusher.Flush()
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-notify:
			h.mu.Lock()
			delete(h.clients, ch)
			h.mu.Unlock()
			close(ch)
			return
		}
	}
}
