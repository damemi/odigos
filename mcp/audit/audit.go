// Package audit is the MCP tool-call audit log shared by every host (enterprise
// UI, VM agent), so the trail looks the same wherever the server runs.
package audit

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Logger is the minimal structured-logging surface the middleware needs (both host loggers satisfy it).
type Logger interface {
	Info(msg string, keysAndValues ...any)
}

// Middleware logs every tool call (component, tool, outcome, duration).
// Register it outermost so it also records calls a later middleware refuses
// (e.g. a write blocked in read-only mode, logged as "tool_error").
func Middleware(log Logger) server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			res, err := next(ctx, req)
			outcome := "ok"
			switch {
			case err != nil:
				outcome = "error"
			case res != nil && res.IsError:
				outcome = "tool_error"
			}
			log.Info("mcp tool call",
				"component", "mcp",
				"tool", req.Params.Name,
				"outcome", outcome,
				"duration_ms", time.Since(start).Milliseconds(),
			)
			return res, err
		}
	}
}
