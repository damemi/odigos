package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/odigos-io/central-proxy/internal/config"
	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/central-proxy/internal/snapshot"
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

	// Get ignored namespaces from Odigos config
	ignoredNamespaces, err := config.GetIgnoredNamespaces(ctx, client)
	if err != nil {
		logger.Fatal("Failed to get ignored namespaces from ConfigMap", err)
	}
	logger.Log.Info("Configured ignored namespaces for snapshots", "namespaces", ignoredNamespaces)

	// Supervisor for the WebSocket connection
	// The supervisor is responsible for restarting the WebSocket connection if it fails
	// e.g if the backend is down, the supervisor will restart the WebSocket connection
	go superviseWebSocketConnection(ctx, client, ignoredNamespaces)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	logger.Log.Info("Shutting down Centralized Proxy...")
}

func superviseWebSocketConnection(ctx context.Context, client *kube.Client, ignoredNamespaces []string) {
	supervisorRestartDelay := 5 * time.Second

	for {
		select {
		case <-ctx.Done():
			logger.Log.Info("WebSocket supervisor shutting down")
			return
		default:
			err := startWebSocketConnection(ctx, client, ignoredNamespaces)

			if err != nil {
				logger.Log.Warn("WebSocket connection failed, supervisor will retry",
					"delay", supervisorRestartDelay.String())

				select {
				case <-ctx.Done():
					return
				case <-time.After(supervisorRestartDelay):
					// Wait before restarting to prevent spam restarts when backend is down.
					// Use select instead of time.Sleep() to respect context cancellation -
					// if shutdown signal comes during the delay, exit immediately rather than waiting.
				}
			}
		}
	}
}

func startWebSocketConnection(ctx context.Context, client *kube.Client, ignoredNamespaces []string) error {
	centralBackendURL, clusterName, err := config.GetCentralBackendURLAndClusterName(ctx, client)
	if err != nil {
		return err
	}
	odigosVersion, err := config.GetOdigosVersion(ctx, client)
	if err != nil {
		return err
	}

	tlsConfig, err := config.GetTLSConfig()
	if err != nil {
		return err
	}

	wsClient, err := websocketserver.NewWebSocketClient(ctx, client, centralBackendURL, clusterName, odigosVersion, tlsConfig)
	if err != nil {
		return err
	}

	logger.Log.Info("WebSocket client connected, starting read loop",
		"clusterName", clusterName)

	// Start cluster snapshot runner
	snapshotRunner := snapshot.NewRunner(
		ctx,
		wsClient.GetProxyID(),
		wsClient.GetClusterName(),
		client,
		wsClient,
		ignoredNamespaces,
	)
	go snapshotRunner.Start()

	// Start SSE forwarder (connects to frontend backend and forwards over WS)
	sseforwarder.Start(ctx, wsClient)

	// This blocks until the connection fails completely (should rarely happen now with infinite retries)
	wsClient.Run(client)

	// If we reach here, the WebSocket client stopped completely
	logger.Log.Error("WebSocket client stopped completely - will be restarted by supervisor")
	return err
}
