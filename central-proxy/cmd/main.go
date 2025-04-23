package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/odigos-io/central-proxy/internal/config"
	"github.com/odigos-io/central-proxy/internal/logger"
	websocketserver "github.com/odigos-io/central-proxy/internal/websocket"
	"github.com/odigos-io/central-proxy/kube"
)

func main() {

	logger.InitLogger()
	logger.Log.Info("Starting Centralized Proxy...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := kube.CreateClient()
	if err != nil {
		logger.Fatal("[ERROR] Failed to create Kubernetes client", err)
	}
	centralBackendURL, err := config.GetCentralBackendURL(ctx, client)
	if err != nil {
		logger.Fatal("[ERROR] Failed to get WebSocket URL from ConfigMap:", err)
	}

	wsClient, err := websocketserver.NewWebSocketClient(ctx, client, centralBackendURL)
	if err != nil {
		logger.Fatal("[ERROR] Failed to init WebSocket Client: %v", err)
	}
	go wsClient.Run(client)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	logger.Log.Info("Shutting down Centralized Proxy...")
}
