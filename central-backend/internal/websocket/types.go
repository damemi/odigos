package websocketserver

import (
	"encoding/json"
	"os"
	"strconv"
)

// DefaultMaxMessageSize is the maximum size of a WebSocket message (32MB).
// Large-cluster GraphQL responses (e.g. GetWorkloads) and snapshots can exceed 10MB.
// Messages larger than this will be batched if possible.
const DefaultMaxMessageSize = 32 * 1024 * 1024

// MaxMessageSize can be configured via MAX_MESSAGE_SIZE environment variable.
var MaxMessageSize = DefaultMaxMessageSize

func init() {
	if envVal := os.Getenv("MAX_MESSAGE_SIZE"); envVal != "" {
		if val, err := strconv.Atoi(envVal); err == nil && val > 0 {
			MaxMessageSize = val
		}
	}
}

type WebSocketMessageToProxy struct {
	Type      WebSocketMessageType `json:"type"`
	Query     string               `json:"query,omitempty"`
	Variables GraphQLVariables     `json:"variables,omitempty"`
	RequestId string               `json:"request_id,omitempty"`
	IsAdmin   bool                 `json:"is_admin,omitempty"`
}

type WebsocketMessageFromProxy struct {
	Type      WebSocketMessageType `json:"type"`
	Data      json.RawMessage      `json:"data,omitempty"`
	RequestId string               `json:"request_id"`
}

type WebSocketMessageType string

// GraphQLVariables is an alias kept compatible with map[string]any for easy interop
// with gqlgen and other request payloads.
type GraphQLVariables = map[string]any

const (
	MessageTypeGraphQLQuery    WebSocketMessageType = "graphql_query"
	MessageTypeSSEEvent        WebSocketMessageType = "sse_event"
	MessageTypeClusterSnapshot WebSocketMessageType = "cluster_snapshot"
)
