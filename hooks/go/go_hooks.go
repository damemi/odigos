package go_hooks

import (
	"context"
)

// GetTraceContext extracts the trace.SpanContext from the provided context.
// If no span is found in the context, it returns an empty SpanContext.
//
//go:noinline
func GetTraceContext(ctx context.Context) string {
	var traceId string
	return traceId
}
