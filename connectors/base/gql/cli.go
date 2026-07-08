package gql

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/99designs/gqlgen/graphql/executor"
	"github.com/coder/websocket"

	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var cliLog = connlog.Subsystem("cli")

const cliMaxMessageSize = 10 * 1024 * 1024 // 10 MB, matching the VM agent / Central client.

// CLI WebSocket message types (the vm-agent odictl JSON protocol).
const (
	cliMsgGraphQLQuery    = "graphql_query"
	cliMsgGraphQLResponse = "graphql_response"
	cliMsgDiagnoseRequest = "diagnose_bundle_request"
)

// cliMessageFromClient is a frame odictl sends (a GraphQL query to execute).
type cliMessageFromClient struct {
	Type      string         `json:"type"`
	Query     string         `json:"query,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
	RequestID string         `json:"request_id,omitempty"`
}

// cliMessageToClient is a frame the connector sends back (the GraphQL response).
type cliMessageToClient struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Data      any    `json:"data"`
}

// CLIHandler serves the odictl CLI WebSocket protocol at /ws: it accepts graphql_query frames, runs
// them against the agents-api executor, and replies with graphql_response frames. This mirrors the VM
// agent's CLI server (handleCLIConnection), so the existing odictl connects unchanged — exposed on the
// connector pod at port 23678 and reached via `kubectl port-forward`. Unsupported connector operations
// surface as the resolver's normal GraphQL errors; diagnose-bundle requests are rejected (the connector
// has no diagnose bundle), which makes odictl fall back to local generation.
func CLIHandler(exec *executor.Executor) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			cliLog.Error("websocket accept failed", "error", err)
			return
		}
		cliLog.Info("odictl client connected", "remote", r.RemoteAddr)
		defer func() {
			conn.Close(websocket.StatusNormalClosure, "connection closed")
			cliLog.Debug("odictl client disconnected", "remote", r.RemoteAddr)
		}()
		conn.SetReadLimit(cliMaxMessageSize)

		ctx := r.Context()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				cliLog.Debug("odictl read ended", "remote", r.RemoteAddr, "error", err)
				return
			}
			handleCLIMessage(ctx, conn, exec, data)
		}
	}
}

func handleCLIMessage(ctx context.Context, conn *websocket.Conn, exec *executor.Executor, data []byte) {
	var msg cliMessageFromClient
	if err := json.Unmarshal(data, &msg); err != nil {
		cliLog.Error("bad cli message", "error", err)
		return
	}

	switch msg.Type {
	case cliMsgGraphQLQuery:
		op := ExtractOperationName(msg.Query)
		cliLog.Debug("cli graphql query", "request_id", msg.RequestID, "operation", op)
		// The odictl CLI endpoint is reachable only by a local operator (kubectl port-forward into the
		// pod), so it is treated as an admin caller and may run mutations.
		resp := RunOperation(ctx, exec, msg.Query, msg.Variables, "cli", true)
		writeCLIMessage(ctx, conn, cliMessageToClient{Type: cliMsgGraphQLResponse, RequestID: msg.RequestID, Data: resp})
	case cliMsgDiagnoseRequest:
		cliLog.Debug("cli diagnose bundle rejected", "request_id", msg.RequestID)
		writeCLIMessage(ctx, conn, cliMessageToClient{
			Type:      cliMsgGraphQLResponse,
			RequestID: msg.RequestID,
			Data:      map[string]any{"errors": []map[string]any{{"message": "diagnose bundle is not supported on the connector"}}},
		})
	default:
		cliLog.Warn("ignoring unknown cli message type", "type", msg.Type, "request_id", msg.RequestID)
	}
}

func writeCLIMessage(ctx context.Context, conn *websocket.Conn, msg cliMessageToClient) {
	out, err := json.Marshal(msg)
	if err != nil {
		cliLog.Error("marshal cli response failed", "request_id", msg.RequestID, "error", err)
		return
	}
	if err := conn.Write(ctx, websocket.MessageText, out); err != nil {
		cliLog.Error("write cli response failed", "request_id", msg.RequestID, "error", err)
	}
}
