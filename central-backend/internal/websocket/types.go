package websocketserver

import "encoding/json"

type WebSocketMessageToProxy struct {
	Type      WebSocketMessageType `json:"type"`
	Query     string               `json:"query,omitempty"`
	Variables GraphQLVariables     `json:"variables,omitempty"`
	RequestId string               `json:"request_id,omitempty"`
	IsAdmin   bool                 `json:"is_admin,omitempty"`
}

type WebsocketMessageFromProxy struct {
	Type      WebSocketMessageType `json:"type"`
	Data      json.RawMessage      `json:"data"`
	RequestId string               `json:"request_id"`
}

type WebSocketMessageType string

// GraphQLVariables is an alias kept compatible with map[string]any for easy interop
// with gqlgen and other request payloads.
type GraphQLVariables = map[string]any

const (
	MessageTypeGraphQLQuery WebSocketMessageType = "graphql_query"
	MessageTypeSSEEvent     WebSocketMessageType = "sse_event"
)
