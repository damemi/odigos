package websocketserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/snapshotworker"
	"github.com/odigos-io/central-backend/internal/ssehub"
	"github.com/odigos-io/central-backend/internal/types"
)

const ProxyIDQueryParam = "proxyID"
const PlatformNameQueryParam = "platformName"
const PlatformTypeQueryParam = "platformType"
const OdigosVersionQueryParam = "version"
const GraphQLSchemaVersionQueryParam = "graphqlSchemaVersion"

type ProxyConnection struct {
	Conn          *websocket.Conn
	PlatformName  string
	PlatformType  consts.PlatformType
	OdigosVersion string
	// GraphQLSchemaVersion is the agents-api GraphQL schema version reported by VM
	// proxies. It selects which agents-api dialect the UI uses for this proxy.
	// Empty for k8s proxies, which don't report it.
	GraphQLSchemaVersion string
	ConnectedAt          int64
	cancelSubscription   context.CancelFunc // Used to cancel and restart Redis subscription on reconnect
}

type WebSocketServer struct {
	proxyConnections   sync.Map
	redisClient        *redisclient.RedisClient
	sseHub             *ssehub.Hub
	snapshotWorkerPool *snapshotworker.WorkerPool
}

func NewWebSocketServer(redisClient *redisclient.RedisClient, sseHub *ssehub.Hub, snapshotWorkerPool *snapshotworker.WorkerPool) *WebSocketServer {
	return &WebSocketServer{
		redisClient:        redisClient,
		sseHub:             sseHub,
		snapshotWorkerPool: snapshotWorkerPool,
	}
}

func (ws *WebSocketServer) HandleConnection(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	proxyID := r.URL.Query().Get(ProxyIDQueryParam)
	if proxyID == "" {
		http.Error(w, fmt.Sprintf("Missing parameter: %s", ProxyIDQueryParam), http.StatusBadRequest)
		return
	}

	currentVersion := r.URL.Query().Get(OdigosVersionQueryParam)
	if currentVersion == "" {
		http.Error(w, fmt.Sprintf("Missing parameter: %s", OdigosVersionQueryParam), http.StatusBadRequest)
		return
	}

	platformName := r.URL.Query().Get(PlatformNameQueryParam)
	if platformName == "" {
		logger.Log.Warn(fmt.Sprintf("Missing parameter: %s", PlatformNameQueryParam), "proxyID", proxyID)
	}

	platformTypeStr := r.URL.Query().Get(PlatformTypeQueryParam)
	var platformType consts.PlatformType
	switch platformTypeStr {
	case "vm":
		platformType = consts.PlatformTypeVM
	case "k8s":
		platformType = consts.PlatformTypeK8s
	case "connector":
		platformType = consts.PlatformTypeConnector
	default:
		http.Error(w, fmt.Sprintf("Unsupported platform type: %s. Supported types: %s, %s, %s", platformTypeStr, consts.PlatformTypeK8s, consts.PlatformTypeVM, consts.PlatformTypeConnector), http.StatusBadRequest)
		return
	}

	// Optional: VM proxies report the agents-api GraphQL schema version so the UI
	// can pick the right dialect. k8s proxies don't send it, so it's not required.
	graphqlSchemaVersion := r.URL.Query().Get(GraphQLSchemaVersionQueryParam)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode:      websocket.CompressionContextTakeover,
		CompressionThreshold: 512,
	})
	if err != nil {
		http.Error(w, "WebSocket upgrade failed", http.StatusInternalServerError)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "Closing connection")

	// Increase WebSocket Read Limit (default 32 KB is too low).
	// Uses package MaxMessageSize so MAX_MESSAGE_SIZE env override applies to Accept too.
	conn.SetReadLimit(int64(MaxMessageSize))

	// Create a separate cancellable context for the Redis subscription
	// This allows us to cancel and restart the subscription on Redis reconnect
	subCtx, cancelSub := context.WithCancel(ctx)

	now := time.Now().Unix()
	ws.proxyConnections.Store(proxyID, &ProxyConnection{
		Conn:                 conn,
		PlatformName:         platformName,
		PlatformType:         platformType,
		OdigosVersion:        currentVersion,
		GraphQLSchemaVersion: graphqlSchemaVersion,
		ConnectedAt:          now,
		cancelSubscription:   cancelSub,
	})
	defer ws.cleanupConnection(ctx, proxyID, conn)

	// Persist basic proxy metadata and initial timestamps
	if err := ws.redisClient.SetProxyMetadata(ctx, proxyID, platformName, string(platformType), currentVersion, graphqlSchemaVersion); err != nil {
		logger.Log.Warn("Failed to set proxy metadata", "proxyID", proxyID, "error", err)
	}
	if err := ws.redisClient.SetProxyMetadataStatus(ctx, proxyID, consts.StatusConnected); err != nil {
		logger.Log.Warn("Failed to set proxy metadata status", "proxyID", proxyID, "error", err)
	}
	if err := ws.redisClient.SetProxyConnectedAt(ctx, proxyID, now); err != nil {
		logger.Log.Warn("Failed to set proxy connectedAt", "proxyID", proxyID, "error", err)
	}
	if err := ws.redisClient.UpdateProxyLastSeen(ctx, proxyID, now); err != nil {
		logger.Log.Warn("Failed to update proxy lastSeenAt", "proxyID", proxyID, "error", err)
	}

	go ws.redisClient.SubscribeToRequests(subCtx, proxyID, ws.SendGraphQLRequest)
	ws.listenForMessages(ctx, proxyID, conn)
}

func (ws *WebSocketServer) listenForMessages(ctx context.Context, proxyID string, conn *websocket.Conn) {
	for {
		_, msg, err := conn.Read(ctx)
		if err != nil {
			logger.Log.Info("Client disconnected", "proxyID", proxyID)
			break
		}

		var parsedMessage WebsocketMessageFromProxy
		if err := json.Unmarshal(msg, &parsedMessage); err != nil {
			logger.Log.Error("Failed to parse WebSocket message", "proxyID", proxyID, "error", err)
			continue
		}

		// Update last-seen timestamp on any message
		if err := ws.redisClient.UpdateProxyLastSeen(ctx, proxyID, time.Now().Unix()); err != nil {
			logger.Log.Warn("Failed to update proxy lastSeenAt", "proxyID", proxyID, "error", err)
		}

		switch parsedMessage.Type {
		case MessageTypeSSEEvent:
			if ws.sseHub != nil {
				ev := ssehub.Event{
					Type:      "k8s_cluster_event",
					Data:      parsedMessage.Data,
					ClusterID: proxyID,
					Timestamp: time.Now().Unix(),
				}
				if platformName, ok := ws.GetPlatformName(proxyID); ok {
					ev.ClusterName = platformName
				}
				ws.sseHub.Broadcast(ev)
			}
		case MessageTypeClusterSnapshot:
			ws.handleClusterSnapshot(proxyID, parsedMessage.Data)
		default:
			// fall through to existing publishing behavior
		}

		responseChannel := consts.GetResponsesChannel(proxyID, parsedMessage.RequestId)
		if err := redisclient.PublishJSON(ctx, ws.redisClient, responseChannel, parsedMessage); err != nil {
			logger.Log.Error("Failed to publish response to Redis", "proxyID", proxyID, "error", err)
		}

	}
}

func (ws *WebSocketServer) cleanupConnection(ctx context.Context, proxyID string, oldConn *websocket.Conn) {
	// Only cleanup if the stored connection is the SAME as the one being disconnected.
	// This prevents a race condition where an old connection's cleanup runs after
	// a new connection has already been established with the same proxyID.
	connVal, ok := ws.proxyConnections.Load(proxyID)
	if !ok {
		// Already cleaned up or replaced by a newer connection
		logger.Log.Info("Connection already cleaned up or replaced", "proxyID", proxyID)
		return
	}

	proxyConn := connVal.(*ProxyConnection)

	// Check if this is the same connection (not a newer one that reconnected)
	if proxyConn.Conn != oldConn {
		logger.Log.Info("Skipping cleanup - connection was replaced by a newer one",
			"proxyID", proxyID,
			"platformName", proxyConn.PlatformName)
		return
	}

	// Cancel the Redis subscription goroutine
	if proxyConn.cancelSubscription != nil {
		proxyConn.cancelSubscription()
	}

	logger.Log.Info("Cleaning up connection", "proxyID", proxyID, "platformName", proxyConn.PlatformName, "platformType", proxyConn.PlatformType)
	ws.proxyConnections.Delete(proxyID)

	if err := ws.redisClient.SetProxyMetadataStatus(ctx, proxyID, consts.StatusDisconnected); err != nil {
		logger.Log.Error("Failed to update proxy metadata status in Redis", "proxyID", proxyID, "error", err)
	}
	// Record last seen on disconnect as well
	if err := ws.redisClient.UpdateProxyLastSeen(ctx, proxyID, time.Now().Unix()); err != nil {
		logger.Log.Warn("Failed to update proxy lastSeenAt on disconnect", "proxyID", proxyID, "error", err)
	}
}

func (ws *WebSocketServer) SendGraphQLRequest(ctx context.Context, proxyID string, request types.GqlRequestPayload) error {
	connValue, ok := ws.proxyConnections.Load(proxyID)
	if !ok {
		return errors.New("client not connected")
	}

	proxyConn, ok := connValue.(*ProxyConnection)
	if !ok {
		return errors.New("invalid proxy connection type")
	}
	conn := proxyConn.Conn

	msg := WebSocketMessageToProxy{
		Type:      MessageTypeGraphQLQuery,
		Query:     request.Query,
		Variables: request.Variables,
		RequestId: request.RequestId,
		IsAdmin:   request.IsAdmin,
	}

	jsonMsg, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	// If message is small enough, send it directly
	if len(jsonMsg) <= MaxMessageSize {
		return conn.Write(ctx, websocket.MessageText, jsonMsg)
	}

	// Message is too large - try to batch if it's a supported mutation
	if strings.Contains(request.Query, "persistK8sSources") {
		if err := ws.sendSourcesInBatches(ctx, conn, request, proxyID); err != nil {
			logger.Log.Error("Failed to send sources in batches, trying as single message", "proxyID", proxyID, "error", err)
			return conn.Write(ctx, websocket.MessageText, jsonMsg)
		}
		return nil
	}

	// For other large messages that can't be batched, log warning and try to send anyway
	logger.Log.Warn("Sending large message that cannot be batched",
		"proxyID", proxyID,
		"requestID", request.RequestId,
		"size", len(jsonMsg))
	return conn.Write(ctx, websocket.MessageText, jsonMsg)
}

// sendSourcesInBatches splits a persistK8sSources mutation into smaller batches
func (ws *WebSocketServer) sendSourcesInBatches(ctx context.Context, conn *websocket.Conn, request types.GqlRequestPayload, proxyID string) error {
	// Extract sources array from variables
	sourcesRaw, ok := request.Variables["sources"]
	if !ok {
		return errors.New("persistK8sSources mutation missing 'sources' variable")
	}

	sources, ok := sourcesRaw.([]interface{})
	if !ok {
		// Try to handle if it's already a typed slice
		return errors.New("'sources' variable is not an array")
	}

	if len(sources) == 0 {
		// Empty sources, send as-is
		return ws.sendSingleMessage(ctx, conn, request)
	}

	// Calculate batch size by estimating per-item size
	// Start with a reasonable batch size and adjust if needed
	batchSize := ws.calculateBatchSize(request, sources)

	logger.Log.Info("Batching large persistK8sSources request",
		"proxyID", proxyID,
		"originalRequestID", request.RequestId,
		"totalSources", len(sources),
		"batchSize", batchSize)

	// Send sources in batches
	for i := 0; i < len(sources); i += batchSize {
		end := i + batchSize
		if end > len(sources) {
			end = len(sources)
		}

		batch := sources[i:end]

		// Create a new request for this batch
		batchVariables := make(map[string]interface{})
		for k, v := range request.Variables {
			if k == "sources" {
				batchVariables[k] = batch
			} else {
				batchVariables[k] = v
			}
		}

		// Generate a unique request ID for each batch
		batchRequestID := fmt.Sprintf("%s-batch-%d", request.RequestId, i/batchSize)

		batchRequest := types.GqlRequestPayload{
			Query:     request.Query,
			Variables: batchVariables,
			RequestId: batchRequestID,
			IsAdmin:   request.IsAdmin,
		}

		if err := ws.sendSingleMessage(ctx, conn, batchRequest); err != nil {
			logger.Log.Error("Failed to send batch",
				"proxyID", proxyID,
				"batchRequestID", batchRequestID,
				"batchIndex", i/batchSize,
				"error", err)
			return err
		}

		logger.Log.Debug("Sent batch",
			"proxyID", proxyID,
			"batchRequestID", batchRequestID,
			"batchIndex", i/batchSize,
			"batchSize", len(batch),
			"totalSources", len(sources))
	}

	logger.Log.Info("Successfully sent all batches",
		"proxyID", proxyID,
		"originalRequestID", request.RequestId,
		"totalBatches", (len(sources)+batchSize-1)/batchSize)

	return nil
}

// calculateBatchSize determines how many items to include in each batch
func (ws *WebSocketServer) calculateBatchSize(request types.GqlRequestPayload, sources []interface{}) int {
	// Estimate the overhead (query, metadata, etc.) by creating a message with empty sources
	testVariables := make(map[string]interface{})
	for k, v := range request.Variables {
		if k == "sources" {
			testVariables[k] = []interface{}{}
		} else {
			testVariables[k] = v
		}
	}

	testMsg := WebSocketMessageToProxy{
		Type:      MessageTypeGraphQLQuery,
		Query:     request.Query,
		Variables: testVariables,
		RequestId: uuid.New().String(), // Use a UUID to estimate typical request ID size
		IsAdmin:   request.IsAdmin,
	}

	overheadJSON, _ := json.Marshal(testMsg)
	overhead := len(overheadJSON)

	// Estimate per-source size by sampling
	sampleSize := min(10, len(sources))
	totalSampleSize := 0
	for i := 0; i < sampleSize; i++ {
		itemJSON, _ := json.Marshal(sources[i])
		totalSampleSize += len(itemJSON)
	}
	avgItemSize := totalSampleSize / sampleSize

	// Add some buffer for JSON array syntax (commas, brackets)
	avgItemSize += 2

	// Calculate how many items fit in the max message size (with 20% safety margin)
	availableSpace := int(float64(MaxMessageSize)*0.8) - overhead
	if availableSpace <= 0 {
		return 1 // Minimum batch size
	}

	batchSize := availableSpace / avgItemSize
	if batchSize < 1 {
		batchSize = 1
	}

	return batchSize
}

// sendSingleMessage sends a single GraphQL request message
func (ws *WebSocketServer) sendSingleMessage(ctx context.Context, conn *websocket.Conn, request types.GqlRequestPayload) error {
	msg := WebSocketMessageToProxy{
		Type:      MessageTypeGraphQLQuery,
		Query:     request.Query,
		Variables: request.Variables,
		RequestId: request.RequestId,
		IsAdmin:   request.IsAdmin,
	}

	jsonMsg, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return conn.Write(ctx, websocket.MessageText, jsonMsg)
}

func (ws *WebSocketServer) GetPlatformName(proxyID string) (string, bool) {
	connValue, ok := ws.proxyConnections.Load(proxyID)
	if !ok {
		return "", false
	}

	proxyConn, ok := connValue.(*ProxyConnection)
	if !ok {
		return "", false
	}

	return proxyConn.PlatformName, true
}

func (ws *WebSocketServer) GetPlatformType(proxyID string) (consts.PlatformType, bool) {
	connValue, ok := ws.proxyConnections.Load(proxyID)
	if !ok {
		return "", false
	}

	proxyConn, ok := connValue.(*ProxyConnection)
	if !ok {
		return "", false
	}

	return proxyConn.PlatformType, true
}

func (ws *WebSocketServer) GetOdigosVersion(proxyID string) (string, bool) {
	connValue, ok := ws.proxyConnections.Load(proxyID)
	if !ok {
		return "", false
	}

	proxyConn, ok := connValue.(*ProxyConnection)
	if !ok {
		return "", false
	}
	return proxyConn.OdigosVersion, true
}

func (ws *WebSocketServer) GetGraphQLSchemaVersion(proxyID string) (string, bool) {
	connValue, ok := ws.proxyConnections.Load(proxyID)
	if !ok {
		return "", false
	}

	proxyConn, ok := connValue.(*ProxyConnection)
	if !ok {
		return "", false
	}
	return proxyConn.GraphQLSchemaVersion, true
}

func (ws *WebSocketServer) handleClusterSnapshot(proxyID string, data json.RawMessage) {
	if ws.snapshotWorkerPool == nil {
		logger.Log.Warn("Received cluster snapshot but worker pool is not initialized", "proxyID", proxyID)
		return
	}

	// Get platform name for the cluster
	clusterName := proxyID
	if name, ok := ws.GetPlatformName(proxyID); ok && name != "" {
		clusterName = name
	}

	job := snapshotworker.SnapshotJob{
		ClusterID:   proxyID,
		ClusterName: clusterName,
		Timestamp:   time.Now().Unix(),
		Data:        data,
	}

	if !ws.snapshotWorkerPool.Submit(job) {
		logger.Log.Warn("Failed to submit snapshot job - queue full", "proxyID", proxyID)
	}
}

// In case redis is lost, we need to resync all proxy connections to Redis.
func (ws *WebSocketServer) ResyncAllProxiesToRedis(ctx context.Context) {
	var count int
	ws.proxyConnections.Range(func(key, value interface{}) bool {
		proxyID, ok := key.(string)
		if !ok {
			return true
		}

		proxyConn, ok := value.(*ProxyConnection)
		if !ok {
			return true
		}

		// Resync metadata only - subscriptions are self-healing and will reconnect automatically
		if err := ws.redisClient.SetProxyMetadata(ctx, proxyID, proxyConn.PlatformName, string(proxyConn.PlatformType), proxyConn.OdigosVersion, proxyConn.GraphQLSchemaVersion); err != nil {
			logger.Log.Warn("Failed to resync proxy metadata", "proxyID", proxyID, "error", err)
		}
		if err := ws.redisClient.SetProxyMetadataStatus(ctx, proxyID, consts.StatusConnected); err != nil {
			logger.Log.Warn("Failed to resync proxy metadata status", "proxyID", proxyID, "error", err)
		}
		if err := ws.redisClient.SetProxyConnectedAt(ctx, proxyID, proxyConn.ConnectedAt); err != nil {
			logger.Log.Warn("Failed to resync proxy connectedAt", "proxyID", proxyID, "error", err)
		}
		if err := ws.redisClient.UpdateProxyLastSeen(ctx, proxyID, time.Now().Unix()); err != nil {
			logger.Log.Warn("Failed to update proxy lastSeenAt during resync", "proxyID", proxyID, "error", err)
		}

		count++
		return true
	})

	logger.Log.Info("Resynced all proxy connections to Redis", "count", count)
}
