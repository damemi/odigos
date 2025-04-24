package websocketserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/coder/websocket"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/types"
)

const ProxyIDQueryParam = "proxyID"
const ClusterNameQueryParam = "clusterName"

type ProxyConnection struct {
	Conn        *websocket.Conn
	ClusterName string
}

type WebSocketServer struct {
	proxyConnections sync.Map
	redisClient      *redisclient.RedisClient
}

func NewWebSocketServer(redisClient *redisclient.RedisClient) *WebSocketServer {
	return &WebSocketServer{redisClient: redisClient}
}

func (ws *WebSocketServer) HandleConnection(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	proxyID := r.URL.Query().Get(ProxyIDQueryParam)
	if proxyID == "" {
		http.Error(w, "Missing proxyID parameter", http.StatusBadRequest)
		return
	}

	clusterName := r.URL.Query().Get(ClusterNameQueryParam)
	if clusterName == "" {
		logger.Log.Warn("clusterName not provided for proxyID", "proxyID", proxyID)
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		CompressionMode: websocket.CompressionDisabled,
	})
	if err != nil {
		http.Error(w, "WebSocket upgrade failed", http.StatusInternalServerError)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "Closing connection")

	// Increase WebSocket Read Limit (default 32 KB is too low)
	const maxMessageSize = 10 * 1024 * 1024 // 10 MB
	conn.SetReadLimit(maxMessageSize)

	ws.proxyConnections.Store(proxyID, &ProxyConnection{
		Conn:        conn,
		ClusterName: clusterName,
	})
	defer ws.cleanupConnection(ctx, proxyID)

	if err := ws.redisClient.AddProxyConnection(ctx, proxyID, string(consts.StatusConnected)); err != nil {
		logger.Log.Error("Failed to store connection in Redis", "error", err)
		return
	}

	go ws.redisClient.SubscribeToRequests(ctx, proxyID, ws.SendGraphQLRequest)
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

		responseChannel := consts.GetResponsesChannel(proxyID, parsedMessage.RequestId)
		if err := ws.redisClient.Publish(ctx, responseChannel, parsedMessage); err != nil {
			logger.Log.Error("Failed to publish response to Redis", "proxyID", proxyID, "error", err)
		}

	}
}

func (ws *WebSocketServer) cleanupConnection(ctx context.Context, proxyID string) {
	connVal, ok := ws.proxyConnections.Load(proxyID)
	if ok {
		proxyConn := connVal.(*ProxyConnection)
		logger.Log.Info("Cleaning up connection", "proxyID", proxyID, "clusterName", proxyConn.ClusterName)
	}
	ws.proxyConnections.Delete(proxyID)

	if err := ws.redisClient.SetProxyConnectionStatus(ctx, proxyID, consts.StatusDisconnected); err != nil {
		logger.Log.Error("Failed to update proxy status in Redis", "proxyID", proxyID, "error", err)
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
		Type:      string(MessageTypeGraphQLQuery),
		Query:     request.Query,
		Variables: request.Variables,
		RequestId: request.RequestId,
	}

	jsonMsg, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return conn.Write(ctx, websocket.MessageText, jsonMsg)
}

func (ws *WebSocketServer) GetClusterName(proxyID string) (string, bool) {
	connValue, ok := ws.proxyConnections.Load(proxyID)
	if !ok {
		return "", false
	}

	proxyConn, ok := connValue.(*ProxyConnection)
	if !ok {
		return "", false
	}

	return proxyConn.ClusterName, true
}
