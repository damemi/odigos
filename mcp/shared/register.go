// Package shared holds the MCP tools that work identically on every Odigos
// platform (the k8s control plane and the VM agent). Each tool calls the
// provider.Backend the host binary binds — no kube, no GraphQL, no platform
// imports — so the same tool serves k8s and VM from one source of truth.
//
// Host binaries create an mcp-go server (with their own middleware), then call
// Register(server, backend) to mount the shared catalog, and finally add any
// platform-specific tools on the same server.
package shared

import (
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos-enterprise/mcp/provider"
)

// Register mounts every shared (cross-platform) read tool onto s, wired to b.
// Write tools, when added, are wrapped by the host with its own readonly gate
// (that check is platform-specific, so it stays on the consumer side).
func Register(s *server.MCPServer, b provider.Backend) {
	registerSources(s, b)
	registerDestinations(s, b)
	registerActions(s, b)
	registerRules(s, b)
}
