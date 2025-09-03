package websocketserver

type WebSocketMessageToProxy struct {
	Type      string                 `json:"type"`
	Query     string                 `json:"query,omitempty"`
	Variables map[string]interface{} `json:"variables,omitempty"`
	RequestId string                 `json:"request_id,omitempty"`
}

type WebsocketMessageFromProxy struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data"`
	RequestId string      `json:"request_id"`
}

type WebSocketMessageType string

const (
	MessageTypeGraphQLQuery WebSocketMessageType = "graphql_query"
	MessageTypeSSEEvent     WebSocketMessageType = "sse_event"
)
