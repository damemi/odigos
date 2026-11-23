package model

import (
	"net/http"
	"time"
)

// RetryAfter returns the wait a response's Retry-After header asks for, or
// zero when it has none. On-demand interrogations call the gateway through
// another client and map its errors onto HTTPError with this.
func RetryAfter(h http.Header) time.Duration {
	return parseRetryAfter(h.Get("Retry-After"))
}
