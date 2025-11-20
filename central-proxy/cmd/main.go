package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/odigos-io/central-proxy/internal/config"
	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/central-proxy/internal/sseforwarder"
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
		logger.Fatal("Failed to create Kubernetes client", err)
	}
	centralBackendURL, clusterName, err := config.GetCentralBackendURLAndClusterName(ctx, client)
	if err != nil {
		logger.Fatal("Failed to get WebSocket URL from ConfigMap", err)
	}
	odigosVersion, err := config.GetOdigosVersion(ctx, client)
	if err != nil {
		logger.Fatal("Failed to get Odigos Version from ConfigMap", err)
	}

	logger.Log.Info("Initializing WebSocket client",
		"centralBackendURL", centralBackendURL,
		"clusterName", clusterName,
		"odigosVersion", odigosVersion)

	wsClient, err := websocketserver.NewWebSocketClient(ctx, client, centralBackendURL, clusterName, odigosVersion)
	if err != nil {
		logger.Fatal("Failed to init WebSocket Client", err)
	}

	logger.Log.Info("Starting WebSocket client read loop goroutine",
		"clusterName", clusterName)
	go wsClient.Run(client)

	// Start SSE forwarder (connects to frontend backend and forwards over WS)
	sseforwarder.Start(ctx, wsClient)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	logger.Log.Info("Shutting down Centralized Proxy...")
}
