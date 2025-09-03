package sseforwarder

import (
	"context"
	"encoding/json"

	"github.com/odigos-io/central-proxy/internal/config"
	"github.com/odigos-io/central-proxy/internal/logger"
	sseclient "github.com/odigos-io/central-proxy/internal/sseclient"
	websocketserver "github.com/odigos-io/central-proxy/internal/websocket"
)

// Start connects to the frontend backend SSE endpoint and forwards events over the websocket client.
func Start(ctx context.Context, wsClient *websocketserver.WebSocketClient) {
	sseURL := config.FrontendEventsURL()
	sse := &sseclient.Client{
		URL: sseURL,
		OnMessage: func(b []byte) {
			var parsed any
			if err := json.Unmarshal(b, &parsed); err == nil {
				_ = wsClient.Send(websocketserver.WebSocketMessageToCentralized{Type: websocketserver.MessageTypeSSEEvent, RequestId: "", Data: parsed})
			} else {
				_ = wsClient.Send(websocketserver.WebSocketMessageToCentralized{Type: websocketserver.MessageTypeSSEEvent, RequestId: "", Data: string(b)})
			}
			logger.Log.Info("SSE event received", "size", len(b))
		},
	}

	go func() {
		if err := sse.Run(ctx); err != nil {
			logger.Log.Error("SSE client stopped", "error", err)
		}
	}()
}
