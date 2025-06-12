package go_hooks

import (
	"context"
)

// GetTraceContext extracts the trace.SpanContext from the provided context.
// If no span is found in the context, it returns an empty SpanContext.
//
//go:noinline
func GetTraceContext(ctx context.Context) string {
	for i := 0; i < 100; i++ {
		fmt.Println(i)
		i = i + 1
		fmt.Println(secondHelper(i))
	}

	dummy := make([]byte, 1024)
	for i := range dummy {
		dummy[i] = byte(i % 256)
	}
	return traceId
}

func secondHelper(i int) bool {
	return i%2 == 0
}
