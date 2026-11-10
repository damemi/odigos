package anypoint

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a non-2xx response from an Anypoint API. Message is the platform's own error text when the body
// carried one; it never includes request credentials (the token and client secret travel in headers and the
// form body, neither of which is echoed here).
type APIError struct {
	Method     string
	Path       string
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Message)
}

// newAPIError extracts the platform's message from the common Anypoint error shapes ({"message"},
// {"error_description"}, {"error"}), falling back to a bounded slice of the raw body.
func newAPIError(method, path string, status int, body []byte) *APIError {
	var shaped struct {
		Message          string `json:"message"`
		ErrorDescription string `json:"error_description"`
		Error            any    `json:"error"`
	}
	msg := ""
	if json.Unmarshal(body, &shaped) == nil {
		switch {
		case shaped.Message != "":
			msg = shaped.Message
		case shaped.ErrorDescription != "":
			msg = shaped.ErrorDescription
		case shaped.Error != nil:
			if s, ok := shaped.Error.(string); ok {
				msg = s
			}
		}
	}
	if msg == "" {
		msg = strings.TrimSpace(string(body))
		if len(msg) > 300 {
			msg = msg[:300] + "…"
		}
	}
	return &APIError{Method: method, Path: path, StatusCode: status, Message: msg}
}

// ErrIncomplete marks a list response that carries fewer items than the total it reports. The items returned
// alongside it are valid; what the caller must not do is treat the list as everything — discovery would prune,
// and a pruned instrumented application would be rolled back. The client does not page because the APIs'
// paging parameters are not confirmed; a truncated answer is surfaced instead of silently trusted.
var ErrIncomplete = errors.New("list response is incomplete")

func checkComplete(path string, got int, total *int) error {
	if total != nil && *total > got {
		return fmt.Errorf("%w: GET %s returned %d of %d items", ErrIncomplete, path, got, *total)
	}
	return nil
}

// StatusCode returns the HTTP status of an APIError anywhere in err's chain, or 0.
func StatusCode(err error) int {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// IsAuthErr reports whether err is an authentication or authorization failure: bad credentials (401) or a
// Connected App missing a scope for the business group or environment (403).
func IsAuthErr(err error) bool {
	code := StatusCode(err)
	return code == http.StatusUnauthorized || code == http.StatusForbidden
}

// IsNotFound reports whether err is a 404.
func IsNotFound(err error) bool { return StatusCode(err) == http.StatusNotFound }

// Summarize returns a concise, secret-free description of an Anypoint error for reports and logs.
func Summarize(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.Message == "" {
			return fmt.Sprintf("HTTP %d", apiErr.StatusCode)
		}
		return fmt.Sprintf("HTTP %d: %s", apiErr.StatusCode, apiErr.Message)
	}
	return err.Error()
}
