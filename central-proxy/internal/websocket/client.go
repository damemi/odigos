package websocketserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
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

const (
	healthCheckInterval = 30 * time.Second

	// pingTimeout bounds the wait for a pong. Without it the ping waits on the
	// root context, so a silently broken path (packets dropped with no RST) parks
	// the health check forever and the only remaining detection is the kernel
	// retransmission timeout, which takes ~15 minutes.
	pingTimeout = 10 * time.Second

	// writeTimeout bounds a single message write. A write to a black-holed socket
	// otherwise blocks indefinitely while holding the connection's write lock,
	// which starves the health check ping.
	writeTimeout = 30 * time.Second
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
	dialer := websocket.DialOptions{
		HTTPClient:           newHTTPClient(tlsConfig),
		CompressionMode:      websocket.CompressionContextTakeover,
		CompressionThreshold: 512,
	}

	conn, _, err := websocket.Dial(ctx, u.String(), &dialer)
	if err != nil {
		return nil, err
	}

	// Increase WebSocket Read Limit (default 32 KB is too low)
	conn.SetReadLimit(int64(MaxMessageSize))

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

// currentConn returns the active connection. The pointer is swapped on reconnect,
// so readers must go through here rather than touching w.conn directly.
func (w *WebSocketClient) currentConn() *websocket.Conn {
	w.connMu.Lock()
	defer w.connMu.Unlock()
	return w.conn
}

func (w *WebSocketClient) setConn(conn *websocket.Conn) {
	w.connMu.Lock()
	defer w.connMu.Unlock()
	w.conn = conn
}

func (w *WebSocketClient) Send(data WebSocketMessageToCentralized) error {
	conn := w.currentConn()
	if conn == nil {
		logger.Log.Warn("Attempted to send WebSocket message but connection is nil",
			"proxyID", w.proxyID,
			"messageType", data.Type)
		return nil
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	// coder/websocket serializes concurrent writes internally and honors the
	// context while waiting for its write lock, so the deadline covers both
	// waiting for the lock and the write itself.
	writeCtx, cancel := context.WithTimeout(w.ctx, writeTimeout)
	defer cancel()

	err = conn.Write(writeCtx, websocket.MessageText, jsonData)
	if err != nil {
		logger.Log.Error("Failed to send WebSocket message",
			"error", err,
			"proxyID", w.proxyID,
			"messageType", data.Type,
			"requestID", data.RequestId,
			"size", len(jsonData))
	}
	return err
}

// SendSnapshot sends a cluster snapshot over the WebSocket connection.
func (w *WebSocketClient) SendSnapshot(msg *WebSocketMessageToCentralized) error {
	return w.Send(*msg)
}

// GetProxyID returns the proxy ID (used as cluster ID for snapshots)
func (w *WebSocketClient) GetProxyID() string {
	return w.proxyID
}

// GetClusterName returns the cluster name
func (w *WebSocketClient) GetClusterName() string {
	return w.clusterName
}

func (w *WebSocketClient) Run(kubeClient *kube.Client) {
	logger.Log.Info("WebSocket client read loop started",
		"proxyID", w.proxyID,
		"clusterName", w.clusterName,
		"version", w.version,
		"url", w.url)

	// Start health monitoring to detect dead connections early.
	// Pings every 30s - if ping fails, forces connection close to trigger reconnection.
	// This prevents "zombie" connections that appear connected but can't send/receive data.
	w.startHealthCheck(w.currentConn())

	for {
		select {
		case <-w.ctx.Done():
			duration := time.Since(w.connectedAt)
			logger.Log.Info("WebSocket client context canceled",
				"proxyID", w.proxyID,
				"clusterName", w.clusterName,
				"duration", duration)
			if conn := w.currentConn(); conn != nil {
				conn.Close(websocket.StatusNormalClosure, "context canceled")
			}
			return

		default:
			conn := w.currentConn()

			_, message, err := conn.Read(w.ctx)
			if err != nil {
				duration := time.Since(w.connectedAt)
				logger.Log.Error("WebSocket read failed, connection lost",
					"error", err,
					"proxyID", w.proxyID,
					"clusterName", w.clusterName,
					"duration", duration)
				conn.Close(websocket.StatusInternalError, "read error")
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
				w.setConn(newConn)
				w.connectedAt = time.Now()
				logger.Log.Info("WebSocket reconnected, resuming read loop",
					"proxyID", w.proxyID,
					"clusterName", w.clusterName)
				// Each connection gets its own health monitor. The previous one exits
				// on its next tick once it sees the connection has been replaced.
				w.startHealthCheck(newConn)
				continue
			}

			// Process each message concurrently to avoid blocking the read loop.
			// This allows multiple GraphQL requests to be processed in parallel.
			go w.handleMessage(message)
		}
	}
}

// startHealthCheck runs a background goroutine that pings conn every healthCheckInterval.
// This detects "zombie" connections that appear connected but can't actually send/receive data.
// If a ping fails, it forces the connection to close, which triggers the main read loop to reconnect.
// The goroutine is bound to a single connection and exits once that connection fails or is replaced.
func (w *WebSocketClient) startHealthCheck(conn *websocket.Conn) {
	if conn == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(healthCheckInterval)
		defer ticker.Stop()

		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				// A reconnect may have swapped in a new connection, which has its own
				// monitor. Without this the goroutine would outlive its connection and
				// one would accumulate per reconnect.
				if w.currentConn() != conn {
					return
				}

				// Ping waits for the pong until the context is done, so this deadline is
				// what makes a silently broken path detectable at all.
				pingCtx, cancel := context.WithTimeout(w.ctx, pingTimeout)
				err := conn.Ping(pingCtx)
				cancel()

				if err != nil {
					logger.Log.Warn("WebSocket health check failed - connection may be dead",
						"error", err,
						"proxyID", w.proxyID,
						"clusterName", w.clusterName)
					// Force close connection to trigger reconnection in main read loop
					conn.Close(websocket.StatusInternalError, "health check failed")
					return // Exit health check goroutine, will be restarted after reconnect
				}

				logger.Log.Debug("WebSocket health check passed",
					"proxyID", w.proxyID,
					"clusterName", w.clusterName)
			}
		}
	}()
}

func (w *WebSocketClient) handleMessage(message []byte) {
	var parsedMessage WebSocketMessageFromCentralized
	if err := json.Unmarshal(message, &parsedMessage); err != nil {
		logger.Log.Error("Failed to parse WebSocket message", "error", err)
		return
	}

	switch parsedMessage.Type {
	case MessageTypeGraphQLQuery:
		resp, err := w.gqlClient.ForwardGraphQLRequest(w.ctx, parsedMessage.RequestId, parsedMessage.Query, parsedMessage.Variables, parsedMessage.IsAdmin)
		if err != nil {
			logger.Log.Error("Failed GraphQL request", "error", err, "request_id", parsedMessage.RequestId)

			errorResponse := WebSocketMessageToCentralized{
				Type:      MessageTypeGraphQLResponse,
				RequestId: parsedMessage.RequestId,
				Data: map[string]any{
					"errors": []map[string]any{
						{"message": err.Error()},
					},
				},
			}
			if sendErr := w.Send(errorResponse); sendErr != nil {
				logger.Log.Error("Failed to send error response", "error", sendErr, "request_id", parsedMessage.RequestId)
			}
			return
		}

		// GraphQL-level errors (e.g. gqlparser "unknown field" on variable
		// coercion) now arrive inside `resp["errors"]` with full path/locations
		// rather than as a transport error. Log them so the proxy side is
		// diagnosable too instead of silently forwarding a failed operation.
		if gqlErrs, ok := resp["errors"]; ok {
			logger.Log.Error("GraphQL response contains errors", "errors", gqlErrs, "request_id", parsedMessage.RequestId)
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
	baseDelay := 2 * time.Second
	maxDelay := 30 * time.Second // Cap maximum delay

	for attempt := 1; ; attempt++ { // Infinite retries
		select {
		case <-w.ctx.Done():
			logger.Log.Warn("Stopping reconnect attempts as context was canceled",
				"attempt", attempt)
			return nil
		default:
			// Refresh configuration on each attempt in case backend URL changed
			proxyID, err := config.GetOdigosDeploymentId(w.ctx, kubeClient)
			if err != nil {
				logger.Log.Error("Failed to get proxyID during reconnect", "error", err, "attempt", attempt)
				continue
			}

			odigosVersion, err := config.GetOdigosVersion(w.ctx, kubeClient)
			if err != nil {
				logger.Log.Error("Failed to get version during reconnect", "error", err, "attempt", attempt)
				continue
			}

			centralBackendURL, clusterName, err := config.GetCentralBackendURLAndClusterName(w.ctx, kubeClient)
			if err != nil {
				logger.Log.Error("Failed to get backend URL during reconnect", "error", err, "attempt", attempt)
				continue
			}

			// Update stored URL if it changed
			w.url = centralBackendURL
			w.clusterName = clusterName
			w.version = odigosVersion

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

			// Linear backoff (2s×attempt) capped at 30s -
			// starts fast for quick recovery, then steady for persistent outages
			delay := baseDelay * time.Duration(attempt)
			if delay > maxDelay {
				delay = maxDelay
			}

			logger.Log.Info("Attempting WebSocket reconnect",
				"attempt", attempt,
				"delay", delay.String(),
				"proxyID", proxyID,
				"clusterName", clusterName,
				"version", odigosVersion,
				"url", u.String())
			time.Sleep(delay)
			dialer := websocket.DialOptions{
				HTTPClient:           newHTTPClient(w.tlsConfig),
				CompressionMode:      websocket.CompressionContextTakeover,
				CompressionThreshold: 512,
			}
			conn, _, err := websocket.Dial(w.ctx, u.String(), &dialer)

			if err == nil {
				// Set the same read limit as the initial connection
				conn.SetReadLimit(int64(MaxMessageSize))

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
}
