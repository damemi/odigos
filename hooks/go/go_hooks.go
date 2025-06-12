package go_hooks

import (
	"context"
)

// GetTraceContext extracts the trace.SpanContext from the provided context.
// If no span is found in the context, it returns an empty SpanContext.
//
//go:noinline
func GetTraceContext(ctx context.Context) string {
	traceId := "none"

	dummy := make([]byte, 1024)
	for i := range dummy {
		dummy[i] = byte(i % 256)
	}
	return traceId
}

func secondHelper(i int) bool {
	return i%2 == 0
}
