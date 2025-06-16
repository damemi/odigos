package go_hooks

import (
	"context"
)

// GetTraceContext extracts the trace.SpanContext from the provided context.
// If no span is found in the context, it returns an empty SpanContext.
//
//go:noinline
func GetTraceContext(ctx context.Context) []byte {
	traceId := []byte("00-00000000000000000000000000000000-0000000000000000-00")
	return traceId
}

func secondHelper(i int) bool {
	return i%2 == 0
}
