package shared

import (
	"github.com/mark3labs/mcp-go/server"

	"github.com/odigos-io/odigos-enterprise/mcp/provider"
)

// registerDestinations adds the cross-platform list_destinations tool. Like the
// other shared list tools it goes through registerListTool, so the host's backend
// supplies the data (k8s from the OSS frontend services, VM from its config API)
// and the output is the canonical Destination spec.
func registerDestinations(mcpServer *server.MCPServer, backend provider.Backend) {
	registerListTool(mcpServer, "list_destinations",
		"List all configured Odigos Destinations (telemetry backends): namespace, name, and the canonical Destination spec (type, exported signals, config).",
		backend.ListDestinations)
}
