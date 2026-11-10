package websocketserver

import (
	"os"
	"strconv"
)

// DefaultMaxMessageSize is the maximum size of a WebSocket message (32MB).
// Large-cluster GraphQL responses (e.g. GetWorkloads) and snapshots can exceed 10MB.
const DefaultMaxMessageSize = 32 * 1024 * 1024

// MaxMessageSize can be configured via MAX_MESSAGE_SIZE (bytes).
var MaxMessageSize = DefaultMaxMessageSize

func init() {
	if envVal := os.Getenv("MAX_MESSAGE_SIZE"); envVal != "" {
		if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
			MaxMessageSize = val
		}
	}
}
