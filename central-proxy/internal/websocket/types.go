package websocketserver

import (
	"context"

	"github.com/coder/websocket"
	"github.com/odigos-io/central-proxy/internal/graphql"
)

type WebSocketMessageType string

const (
	MessageTypeGraphQLQuery    WebSocketMessageType = "graphql_query"
	MessageTypeGraphQLResponse WebSocketMessageType = "graphql_response"
	MessageTypeSSEEvent        WebSocketMessageType = "sse_event"
)

type WebSocketClient struct {
	ctx       context.Context
	url       string
	conn      *websocket.Conn
	gqlClient *graphql.GraphQLClient
}

type WebSocketMessageFromCentralized struct {
	Type      WebSocketMessageType `json:"type"`
	Query     string               `json:"query,omitempty"`
	Variables map[string]any       `json:"variables,omitempty"`
	RequestId string               `json:"request_id,omitempty"`
}

type WebSocketMessageToCentralized struct {
	Type      WebSocketMessageType `json:"type"`
	RequestId string               `json:"request_id"`
	Data      any                  `json:"data"`
}
