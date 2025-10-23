package websocketserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"time"

	"github.com/coder/websocket"

	"github.com/odigos-io/central-proxy/internal/config"
	"github.com/odigos-io/central-proxy/internal/consts"
	"github.com/odigos-io/central-proxy/internal/graphql"
	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/central-proxy/kube"
)

// NewWebSocketClient creates a WebSocket client
func NewWebSocketClient(ctx context.Context, client *kube.Client, wsURL string, clusterName string, odigosVersion string) (*WebSocketClient, error) {
	proxyID, err := config.GetOdigosDeploymentId(ctx, client)
	if err != nil {
		return nil, err
	}

	u := url.URL{
		Scheme:   "ws",
		Host:     wsURL,
		Path:     "/ws/proxy",
		RawQuery: fmt.Sprintf("proxyID=%s&platformName=%s&version=%s&platformType=%s", proxyID, clusterName, odigosVersion, consts.PlatformTypeK8sString),
	}
	conn, _, err := websocket.Dial(ctx, u.String(), &websocket.DialOptions{})
	if err != nil {
		return nil, err
	}

	// Increase WebSocket Read Limit (default 32 KB is too low)
	const maxMessageSize = 10 * 1024 * 1024 // 10 MB
	conn.SetReadLimit(maxMessageSize)

	gqlClient := graphql.NewGraphQLClient()

	return &WebSocketClient{conn: conn, url: wsURL, gqlClient: gqlClient, ctx: ctx}, nil
}

func (w *WebSocketClient) Send(data WebSocketMessageToCentralized) error {
	if w.conn == nil {
		return nil
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	return w.conn.Write(w.ctx, websocket.MessageText, jsonData)
}

func (w *WebSocketClient) Run(kubeClient *kube.Client) {
	for {
		select {
		case <-w.ctx.Done():
			logger.Log.Info("WebSocket client context canceled")
			if w.conn != nil {
				w.conn.Close(websocket.StatusNormalClosure, "context canceled")
			}
			return

		default:

			_, message, err := w.conn.Read(w.ctx)
			if err != nil {
				logger.Log.Error("WebSocket read failed", "error", err)
				w.conn.Close(websocket.StatusInternalError, "read error")
				newConn := w.handleReconnect(kubeClient)
				if newConn == nil {
					logger.Log.Error("WebSocket reconnection failed, stopping client")
					return
				}
				w.conn = newConn
				continue
			}

			w.handleMessage(message)
		}
	}
}

func (w *WebSocketClient) handleMessage(message []byte) {
	var parsedMessage WebSocketMessageFromCentralized
	if err := json.Unmarshal(message, &parsedMessage); err != nil {
		logger.Log.Error("Failed to parse WebSocket message", "error", err)
		return
	}

	switch parsedMessage.Type {
	case MessageTypeGraphQLQuery:
		resp, err := w.gqlClient.ForwardGraphQLRequest(w.ctx, parsedMessage.RequestId, parsedMessage.Query, parsedMessage.Variables)
		if err != nil {
			logger.Log.Error("Failed GraphQL request", "error", err, "request_id", parsedMessage.RequestId)
			return
		}

		response := WebSocketMessageToCentralized{
			Type:      MessageTypeGraphQLResponse,
			Data:      resp,
			RequestId: parsedMessage.RequestId,
		}

		if err := w.Send(response); err != nil {
			logger.Log.Error("Failed to send GraphQL response", "error", err, "request_id", parsedMessage.RequestId)
		}

	default:
		//TODO: return error to centralized ui?
		logger.Log.Warn("Unknown WebSocket message type received", "type", parsedMessage.Type)
	}
}

func (w *WebSocketClient) handleReconnect(kubeClient *kube.Client) *websocket.Conn {
	maxRetries := 10
	baseDelay := 2 * time.Second

	proxyID, err := config.GetOdigosDeploymentId(w.ctx, kubeClient)
	if err != nil {
		logger.Log.Error("Failed to get proxyID during reconnect", "error", err)
		return nil
	}

	odigosVersion, err := config.GetOdigosVersion(w.ctx, kubeClient)
	if err != nil {
		logger.Log.Error("Failed to get version during reconnect", "error", err)
		return nil
	}

	_, clusterName, err := config.GetCentralBackendURLAndClusterName(w.ctx, kubeClient)
	if err != nil {
		logger.Log.Error("Failed to get clusterName during reconnect", "error", err)
		return nil
	}

	u := url.URL{
		Scheme:   "ws",
		Host:     w.url,
		Path:     "/ws/proxy",
		RawQuery: fmt.Sprintf("proxyID=%s&platformName=%s&version=%s&platformType=%s", proxyID, clusterName, odigosVersion, consts.PlatformTypeK8sString),
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		select {
		case <-w.ctx.Done():
			logger.Log.Warn("Stopping reconnect attempts as context was canceled")
			return nil
		default:
			delay := baseDelay * time.Duration(attempt)
			logger.Log.Info("Attempting WebSocket reconnect", "attempt", attempt, "delay", delay)
			time.Sleep(delay)

			conn, _, err := websocket.Dial(w.ctx, u.String(), &websocket.DialOptions{})
			if err == nil {
				logger.Log.Info("WebSocket reconnect successful")
				return conn
			}

		}
	}

	logger.Log.Error("Max reconnect attempts reached, stopping WebSocket client")
	return nil
}
