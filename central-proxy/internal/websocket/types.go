package websocketserver

import (
	"context"
	"crypto/tls"
	"time"

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
	ctx         context.Context
	url         string
	conn        *websocket.Conn
	gqlClient   *graphql.GraphQLClient
	proxyID     string
	clusterName string
	version     string
	connectedAt time.Time
	tlsConfig   *tls.Config
}

type WebSocketMessageFromCentralized struct {
	Type      WebSocketMessageType `json:"type"`
	Query     string               `json:"query,omitempty"`
	Variables map[string]any       `json:"variables,omitempty"`
	RequestId string               `json:"request_id,omitempty"`
	IsAdmin   bool                 `json:"is_admin,omitempty"`
}

type WebSocketMessageToCentralized struct {
	Type      WebSocketMessageType `json:"type"`
	RequestId string               `json:"request_id"`
	Data      any                  `json:"data"`
}
