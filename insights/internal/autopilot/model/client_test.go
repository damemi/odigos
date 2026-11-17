package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

func TestCompletionsURL(t *testing.T) {
	cases := map[string]string{
		"http://gw:4000":                     "http://gw:4000/v1/chat/completions",
		"http://gw:4000/":                    "http://gw:4000/v1/chat/completions",
		"http://gw:4000/v1":                  "http://gw:4000/v1/chat/completions",
		"http://gw:4000/v1/":                 "http://gw:4000/v1/chat/completions",
		"http://gw:4000/v1/chat/completions": "http://gw:4000/v1/chat/completions",
	}
	for in, want := range cases {
		if got := completionsURL(in); got != want {
			t.Fatalf("completionsURL(%q)=%q want %q", in, got, want)
		}
	}
}

func TestCompleteAnalysis(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("auth %s", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if req["model"] != "autopilot" {
			t.Fatalf("model %v", req["model"])
		}
		msgs, _ := req["messages"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("messages %#v", req["messages"])
		}
		sys, _ := msgs[0].(map[string]any)
		user, _ := msgs[1].(map[string]any)
		if sys["role"] != "system" || sys["content"] != schema.SystemPrompt {
			t.Fatalf("system %#v", sys)
		}
		if user["role"] != "user" || user["content"] != `{"transactionId":"t"}` {
			t.Fatalf("user %#v", user)
		}
		rf, _ := req["response_format"].(map[string]any)
		if rf["type"] != "json_schema" {
			t.Fatalf("response_format %#v", req["response_format"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": `{"ok":true}`}},
			},
		})
	}))
	defer srv.Close()

	// Override endpoint for httptest; production uses LiteLLMServiceURL.
	c := New(Config{APIKey: "test-key"})
	c.endpoint = srv.URL + "/v1/chat/completions"
	got, err := c.CompleteAnalysis(context.Background(), `{"transactionId":"t"}`)
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"ok":true}` {
		t.Fatalf("content %q", got)
	}
}

func TestCompleteAnalysisCancelsInFlightRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	serverObservedCancel := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(requestStarted)
		<-r.Context().Done()
		close(serverObservedCancel)
	}))
	defer srv.Close()

	c := New(Config{APIKey: "test-key"})
	c.endpoint = srv.URL + "/v1/chat/completions"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := c.CompleteAnalysis(ctx, `{"transactionId":"t"}`)
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach the server")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CompleteAnalysis error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CompleteAnalysis did not return after cancellation")
	}
	select {
	case <-serverObservedCancel:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not observe request cancellation")
	}
}

func TestConfigEnabled(t *testing.T) {
	if (Config{}).Enabled() {
		t.Fatal("empty config should be disabled")
	}
	if !(Config{APIKey: "sk-litellm"}).Enabled() {
		t.Fatal("master key should enable client")
	}
}

func TestCompleteAnalysisErrorTypes(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		check   func(t *testing.T, err error)
	}{
		{
			name: "rate limited",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			check: func(t *testing.T, err error) {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || !httpErr.QuotaExceeded() || httpErr.RetryAfter != 30*time.Second {
					t.Fatalf("err = %#v, want 429 HTTPError with 30s retry-after", err)
				}
			},
		},
		{
			name: "budget exceeded",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"budget_exceeded","message":"Budget has been exceeded"}}`))
			},
			check: func(t *testing.T, err error) {
				var httpErr *HTTPError
				if !errors.As(err, &httpErr) || !httpErr.QuotaExceeded() {
					t.Fatalf("err = %v, want quota HTTPError", err)
				}
			},
		},
		{
			name: "empty content",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":""}}]}`))
			},
			check: func(t *testing.T, err error) {
				if !errors.Is(err, ErrInvalidResponse) {
					t.Fatalf("err = %v, want ErrInvalidResponse", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			c := New(Config{APIKey: "test-key"})
			c.endpoint = srv.URL + "/v1/chat/completions"
			_, err := c.CompleteAnalysis(context.Background(), `{}`)
			tc.check(t, err)
		})
	}
}
