package ssehub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/metrics"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
)

type Event struct {
	Type        string          `json:"type"`
	Data        json.RawMessage `json:"data"`
	ClusterID   string          `json:"cluster_id,omitempty"`
	ClusterName string          `json:"cluster_name,omitempty"`
	Timestamp   int64           `json:"timestamp,omitempty"`
}

// ClientInfo holds information about a connected SSE client
type ClientInfo struct {
	ClusterID string
	Channel   chan []byte
}

type Hub struct {
	mu         sync.Mutex
	clients    map[chan []byte]*ClientInfo
	redis      *redisclient.RedisClient
	instanceID string
}

type redisEnvelope struct {
	Origin string `json:"origin"`
	Event  Event  `json:"event"`
}

func NewWithRedis(r *redisclient.RedisClient, instanceID string) *Hub {
	return &Hub{clients: make(map[chan []byte]*ClientInfo), redis: r, instanceID: instanceID}
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
		_ = redisclient.PublishJSON(context.Background(), h.redis, consts.GetSSEBroadcastChannel(), env)
	}
}

func (h *Hub) broadcastLocal(ev Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch, clientInfo := range h.clients {
		// Only send events to clients subscribed to this cluster
		if clientInfo.ClusterID != "" && ev.ClusterID != "" && clientInfo.ClusterID != ev.ClusterID {
			continue
		}
		select {
		case ch <- b:
		default:
		}
	}
}

func (h *Hub) SSEHandler(w http.ResponseWriter, r *http.Request) {
	// Parse clusterID from query parameters - required for filtering
	clusterID := r.URL.Query().Get("clusterID")
	if clusterID == "" {
		http.Error(w, "clusterID query parameter is required", http.StatusBadRequest)
		return
	}

	const unknown = "unknown"

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// Create channel and client info with cluster subscription
	ch := make(chan []byte, 256)
	clientInfo := &ClientInfo{
		ClusterID: clusterID,
		Channel:   ch,
	}

	h.mu.Lock()
	h.clients[ch] = clientInfo
	h.mu.Unlock()

	fmt.Fprintf(w, "data: %s\n\n", `{"type":"connected","data":"ok"}`)
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	notify := r.Context().Done()
	for {
		select {
		case b := <-ch:
			// Extract cluster info from the event for metrics
			var ev Event
			eventClusterID, eventClusterName := unknown, unknown
			if err := json.Unmarshal(b, &ev); err == nil {
				if ev.ClusterID != "" {
					eventClusterID = ev.ClusterID
				}
				if ev.ClusterName != "" {
					eventClusterName = ev.ClusterName
				}
			}
			metrics.SSEBroadcastRate.WithLabelValues(eventClusterID, eventClusterName).Inc()
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
