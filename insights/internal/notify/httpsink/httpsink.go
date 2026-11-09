// Package httpsink is the shared HTTP plumbing for backends that are a JSON
// POST: it sends the request, bounds the response body, and classifies the
// outcome into retryable vs. permanent so every sink agrees on what a 429 or
// a connection reset means.
package httpsink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/odigos-io/odigos/insights/internal/notify"
)

// maxBodyBytes bounds how much of an error response is kept for the audit.
const maxBodyBytes = 4 << 10

// Response is a bounded view of what the backend answered.
type Response struct {
	Status int
	Body   []byte
}

// PostJSON marshals body and POSTs it with the given extra headers. It returns
// the response (when one arrived) and a classified DeliveryError on any
// transport error or non-2xx status.
func PostJSON(ctx context.Context, client *http.Client, url string, body any, headers http.Header) (Response, *notify.DeliveryError) {
	raw, err := json.Marshal(body)
	if err != nil {
		return Response{}, notify.Permanent("encode payload: %v", err)
	}
	return PostRaw(ctx, client, url, raw, headers)
}

// PostRaw is PostJSON for an already-encoded body.
func PostRaw(ctx context.Context, client *http.Client, url string, raw []byte, headers http.Header) (Response, *notify.DeliveryError) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return Response{}, notify.Permanent("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "odigos-insights-notifier")
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, Classify(0, err, nil)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	out := Response{Status: resp.StatusCode, Body: b}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return out, nil
	}
	return out, Classify(resp.StatusCode, nil, b)
}

// Classify maps an HTTP outcome to a DeliveryError: transport errors,
// timeouts, 408/429 and 5xx are retryable; any other 4xx is permanent (bad
// credentials or payload will not fix themselves by retrying).
func Classify(status int, err error, body []byte) *notify.DeliveryError {
	if err != nil {
		msg := err.Error()
		if errors.Is(err, context.Canceled) {
			return &notify.DeliveryError{Retryable: true, Msg: "canceled"}
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return &notify.DeliveryError{Retryable: true, Msg: "timeout: " + msg}
		}
		return &notify.DeliveryError{Retryable: true, Msg: msg}
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(status)
	}
	switch {
	case status == http.StatusTooManyRequests, status == http.StatusRequestTimeout, status >= 500:
		return &notify.DeliveryError{Retryable: true, HTTPStatus: status, Msg: msg}
	default:
		return &notify.DeliveryError{Retryable: false, HTTPStatus: status, Msg: msg}
	}
}

// BasicAuth returns an Authorization header for the pair, or nil when the
// username is empty.
func BasicAuth(username, password string) http.Header {
	if username == "" {
		return nil
	}
	req := &http.Request{Header: http.Header{}}
	req.SetBasicAuth(username, password)
	return req.Header
}

// Describe is a short human form of a response for error messages.
func Describe(r Response) string {
	return fmt.Sprintf("http %d: %s", r.Status, strings.TrimSpace(string(r.Body)))
}
