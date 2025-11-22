package websocketserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/odigos-io/central-proxy/internal/config"
	"github.com/odigos-io/central-proxy/internal/consts"
	"github.com/odigos-io/central-proxy/internal/graphql"
	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/central-proxy/kube"
)

// NewWebSocketClient creates a WebSocket client
func NewWebSocketClient(ctx context.Context, client *kube.Client, wsURL string, clusterName string, odigosVersion string, tlsConfig *tls.Config) (*WebSocketClient, error) {
	proxyID, err := config.GetOdigosDeploymentId(ctx, client)
	if err != nil {
		return nil, err
	}

	scheme := "ws"
	if strings.HasSuffix(wsURL, ":443") {
		logger.Log.Info("Using wss scheme", "wsURL", wsURL)
		scheme = "wss"
	}

	u := url.URL{
		Scheme:   scheme,
		Host:     wsURL,
		Path:     "/ws/proxy",
		RawQuery: fmt.Sprintf("proxyID=%s&platformName=%s&version=%s&platformType=%s", proxyID, clusterName, odigosVersion, consts.PlatformTypeK8sString),
	}
	transport := &http.Transport{
		TLSNextProto:   map[string]func(string, *tls.Conn) http.RoundTripper{},
		TLSClientConfig: tlsConfig,
	}
	dialer := websocket.DialOptions{
		HTTPClient: &http.Client{Transport: transport},
	}

	conn, _, err := websocket.Dial(ctx, u.String(), &dialer)
	if err != nil {
		return nil, err
	}

	// Increase WebSocket Read Limit (default 32 KB is too low)
	const maxMessageSize = 10 * 1024 * 1024 // 10 MB
	conn.SetReadLimit(maxMessageSize)

	gqlClient := graphql.NewGraphQLClient()

	now := time.Now()
	wsClient := &WebSocketClient{
		conn:        conn,
		url:         wsURL,
		gqlClient:   gqlClient,
		ctx:         ctx,
		proxyID:     proxyID,
		clusterName: clusterName,
		version:     odigosVersion,
		connectedAt: now,
		tlsConfig:   tlsConfig,
	}

	logger.Log.Info("WebSocket connection established",
		"proxyID", proxyID,
		"clusterName", clusterName,
		"version", odigosVersion,
		"url", u.String())

	return wsClient, nil
}

func (w *WebSocketClient) Send(data WebSocketMessageToCentralized) error {
	if w.conn == nil {
		logger.Log.Warn("Attempted to send WebSocket message but connection is nil",
			"proxyID", w.proxyID,
			"messageType", data.Type)
		return nil
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	err = w.conn.Write(w.ctx, websocket.MessageText, jsonData)
	if err != nil {
		logger.Log.Error("Failed to send WebSocket message",
			"error", err,
			"proxyID", w.proxyID,
			"messageType", data.Type,
			"requestID", data.RequestId)
	}
	return err
}

func (w *WebSocketClient) Run(kubeClient *kube.Client) {
	logger.Log.Info("WebSocket client read loop started",
		"proxyID", w.proxyID,
		"clusterName", w.clusterName,
		"version", w.version,
		"url", w.url)

	for {
		select {
		case <-w.ctx.Done():
			duration := time.Since(w.connectedAt)
			logger.Log.Info("WebSocket client context canceled",
				"proxyID", w.proxyID,
				"clusterName", w.clusterName,
				"duration", duration)
			if w.conn != nil {
				w.conn.Close(websocket.StatusNormalClosure, "context canceled")
			}
			return

		default:

			_, message, err := w.conn.Read(w.ctx)
			if err != nil {
				duration := time.Since(w.connectedAt)
				logger.Log.Error("WebSocket read failed, connection lost",
					"error", err,
					"proxyID", w.proxyID,
					"clusterName", w.clusterName,
					"duration", duration)
				if w.conn != nil {
					w.conn.Close(websocket.StatusInternalError, "read error")
				}
				logger.Log.Info("Starting WebSocket reconnection process",
					"proxyID", w.proxyID,
					"clusterName", w.clusterName)
				newConn := w.handleReconnect(kubeClient)
				if newConn == nil {
					logger.Log.Error("WebSocket reconnection failed, stopping client",
						"proxyID", w.proxyID,
						"clusterName", w.clusterName)
					return
				}
				w.conn = newConn
				w.connectedAt = time.Now()
				logger.Log.Info("WebSocket reconnected, resuming read loop",
					"proxyID", w.proxyID,
					"clusterName", w.clusterName)
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

	scheme := "ws"
	if strings.HasSuffix(w.url, ":443") {
		scheme = "wss"
	}

	u := url.URL{
		Scheme:   scheme,
		Host:     w.url,
		Path:     "/ws/proxy",
		RawQuery: fmt.Sprintf("proxyID=%s&platformName=%s&version=%s&platformType=%s", proxyID, clusterName, odigosVersion, consts.PlatformTypeK8sString),
	}

	for attempt := 1; attempt <= maxRetries; attempt++ {
		select {
		case <-w.ctx.Done():
			logger.Log.Warn("Stopping reconnect attempts as context was canceled",
				"proxyID", proxyID,
				"clusterName", clusterName,
				"attempt", attempt)
			return nil
		default:
			delay := baseDelay * time.Duration(attempt)
			logger.Log.Info("Attempting WebSocket reconnect",
				"attempt", attempt,
				"delay", delay,
				"proxyID", proxyID,
				"clusterName", clusterName,
				"version", odigosVersion,
				"url", u.String())
			time.Sleep(delay)
			transport := &http.Transport{
				TLSNextProto:   map[string]func(string, *tls.Conn) http.RoundTripper{},
				TLSClientConfig: w.tlsConfig,
			}
			dialer := websocket.DialOptions{
				HTTPClient: &http.Client{Transport: transport},
			}
			conn, _, err := websocket.Dial(w.ctx, u.String(), &dialer)

			if err == nil {
				logger.Log.Info("WebSocket reconnect successful",
					"attempt", attempt,
					"proxyID", proxyID,
					"clusterName", clusterName,
					"version", odigosVersion,
					"url", u.String())
				return conn
			}

			logger.Log.Warn("WebSocket reconnect attempt failed",
				"attempt", attempt,
				"error", err,
				"proxyID", proxyID,
				"clusterName", clusterName,
				"url", u.String())

		}
	}

	logger.Log.Error("Max reconnect attempts reached, stopping WebSocket client",
		"maxRetries", maxRetries,
		"proxyID", proxyID,
		"clusterName", clusterName,
		"url", u.String())
	return nil
}
