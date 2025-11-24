package websocketserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/logger"
	redisclient "github.com/odigos-io/central-backend/internal/redis"
	"github.com/odigos-io/central-backend/internal/ssehub"
	"github.com/odigos-io/central-backend/internal/types"
	"github.com/odigos-io/central-backend/internal/version"
)

const ProxyIDQueryParam = "proxyID"
const PlatformNameQueryParam = "platformName"
const PlatformTypeQueryParam = "platformType"
const OdigosVersionQueryParam = "version"

type ProxyConnection struct {
	Conn          *websocket.Conn
	PlatformName  string
	PlatformType  consts.PlatformType
	OdigosVersion string
}

type WebSocketServer struct {
	proxyConnections sync.Map
	redisClient      *redisclient.RedisClient
	sseHub           *ssehub.Hub
}

func NewWebSocketServer(redisClient *redisclient.RedisClient, sseHub *ssehub.Hub) *WebSocketServer {
	return &WebSocketServer{redisClient: redisClient, sseHub: sseHub}
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
	if !version.IsVersionSupported(currentVersion) {
		http.Error(w, fmt.Sprintf("Unsupported version: %s. Supported versions: %s - %s", currentVersion, version.MinSupportedVersion, version.MaxSupportedVersion), http.StatusUpgradeRequired)
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
	default:
		http.Error(w, fmt.Sprintf("Unsupported platform type: %s. Supported types: %s, %s", platformTypeStr, consts.PlatformTypeK8s, consts.PlatformTypeVM), http.StatusBadRequest)
		return
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
		Conn:          conn,
		PlatformName:  platformName,
		PlatformType:  platformType,
		OdigosVersion: currentVersion,
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
		default:
			// fall through to existing publishing behavior
		}

		responseChannel := consts.GetResponsesChannel(proxyID, parsedMessage.RequestId)
		if err := redisclient.PublishJSON(ctx, ws.redisClient, responseChannel, parsedMessage); err != nil {
			logger.Log.Error("Failed to publish response to Redis", "proxyID", proxyID, "error", err)
		}

	}
}

func (ws *WebSocketServer) cleanupConnection(ctx context.Context, proxyID string) {
	connVal, ok := ws.proxyConnections.Load(proxyID)
	if ok {
		proxyConn := connVal.(*ProxyConnection)
		logger.Log.Info("Cleaning up connection", "proxyID", proxyID, "platformName", proxyConn.PlatformName, "platformType", proxyConn.PlatformType)
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
