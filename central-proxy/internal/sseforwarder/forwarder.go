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
			var err error
			if err = json.Unmarshal(b, &parsed); err == nil {
				err = wsClient.Send(websocketserver.WebSocketMessageToCentralized{Type: websocketserver.MessageTypeSSEEvent, RequestId: "", Data: parsed})
			} else {
				err = wsClient.Send(websocketserver.WebSocketMessageToCentralized{Type: websocketserver.MessageTypeSSEEvent, RequestId: "", Data: string(b)})
			}

			if err != nil {
				logger.Log.Error("Failed to forward SSE event", "error", err, "size", len(b))
			} else {
				logger.Log.Debug("SSE event forwarded", "size", len(b))
			}
		},
	}

	go func() {
		if err := sse.Run(ctx); err != nil {
			logger.Log.Error("SSE client stopped", "error", err)
		}
	}()
}
