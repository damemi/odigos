package model

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidResponse marks a 2xx completion whose body could not be used: it
// did not decode, or it carried no content. Retrying the same input usually
// gives the same result.
var ErrInvalidResponse = errors.New("invalid completions response")

// HTTPError is a non-2xx response from odigos-litellm.
type HTTPError struct {
	StatusCode int
	Body       string
	// RetryAfter is the parsed Retry-After header, or zero when absent.
	RetryAfter time.Duration
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("chat completions: HTTP %d: %s", e.StatusCode, e.Body)
}

// QuotaExceeded reports whether the provider refused the call for rate or
// budget reasons. LiteLLM uses 429 for rate limits and a 400 with a
// budget_exceeded type for spend limits.
func (e *HTTPError) QuotaExceeded() bool {
	if e.StatusCode == http.StatusTooManyRequests {
		return true
	}
	body := strings.ToLower(e.Body)
	return e.StatusCode == http.StatusBadRequest &&
		(strings.Contains(body, "budget_exceeded") || strings.Contains(body, "exceeded budget"))
}

func parseRetryAfter(h string) time.Duration {
	h = strings.TrimSpace(h)
	if h == "" {
		return 0
	}
	if secs, err := strconv.Atoi(h); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
