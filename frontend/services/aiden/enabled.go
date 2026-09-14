// Package aiden reverse-proxies the OpenClaw gateway WebSocket used by the
// in-UI Aiden chat. The browser talks to /api/aiden/ws on the Odigos UI; this
// package dials the in-cluster Aiden Service and injects the gateway token so
// it never leaves the cluster.
package aiden

import (
	"os"

	"github.com/odigos-io/odigos/api/k8sconsts"
)

// IsEnabled reports whether the UI should expose the Aiden chat. Helm sets
// AIDEN_GATEWAY_URL and AIDEN_GATEWAY_TOKEN on the UI pod when aiden.enabled
// is true.
func IsEnabled() bool {
	return os.Getenv(k8sconsts.AidenGatewayURLEnv) != "" && os.Getenv(k8sconsts.AidenGatewayTokenEnv) != ""
}

func gatewayURL() string {
	return os.Getenv(k8sconsts.AidenGatewayURLEnv)
}

func gatewayToken() string {
	return os.Getenv(k8sconsts.AidenGatewayTokenEnv)
}
