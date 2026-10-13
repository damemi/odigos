// Package sinktest is the contract test every backend runs: build from a
// valid config, deliver to a fake HTTP backend, and classify outcomes the way
// the dispatcher expects. A new sink passes by calling Run from its own test.
package sinktest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/notify"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Case is one backend under test. Config must be a valid JSON config whose
// URL-bearing field(s) point at the server URL passed to the function.
type Case struct {
	Descriptor notify.Descriptor
	Factory    notify.Factory
	// Config returns a valid config for the given fake backend URL.
	Config func(serverURL string) map[string]any
}

// Events returns two representative events (one per kind) for rendering.
func Events() []notify.Event {
	now := time.Now().UTC().Truncate(time.Second)
	return []notify.Event{
		{
			Key: repo.AnomalyFindingKey(42, "sig-1"), Kind: notify.KindAnomaly, Action: repo.DispatchFiring,
			Service: "checkout", Namespace: "shop", Title: "POST /checkout", Summary: "Transaction deviated from its baseline: unexpected DB table access.",
			Offending: "users_pii", Score: 80, Severity: "critical", Status: "open", Occurrences: 3,
			FirstSeen: now.Add(-time.Minute), LastSeen: now, LastTraceID: "abc123",
			TransactionID: 42, Signature: "sig-1", PolicyID: 1,
			Labels: map[string]string{"operation": "POST /checkout", "triggered_classes": "D6_db_access"},
		},
		{
			Key: repo.ViolationFindingKey("shop/payments", "allowed_callers", "shop/frontend"), Kind: notify.KindViolation, Action: repo.DispatchFiring,
			Service: "payments", Namespace: "shop", Title: "Allowed callers", Summary: "Allowed callers broken by shop/frontend.",
			Offending: "shop/frontend", Score: 55, Severity: "high", Status: "open", Occurrences: 10,
			FirstSeen: now.Add(-time.Hour), LastSeen: now,
			ScopeKey: "shop/payments", RuleKey: "allowed_callers",
			Labels: map[string]string{"rule": "allowed_callers"},
		},
	}
}

// Run executes the contract suite.
func Run(t *testing.T, c Case) {
	t.Helper()

	t.Run("descriptor", func(t *testing.T) {
		if c.Descriptor.Type == "" || c.Descriptor.Name == "" {
			t.Fatalf("descriptor needs type and name: %+v", c.Descriptor)
		}
		seen := map[string]bool{}
		for _, f := range c.Descriptor.Fields {
			if f.Key == "" || f.Label == "" || f.Type == "" {
				t.Errorf("field %+v missing key/label/type", f)
			}
			if seen[f.Key] {
				t.Errorf("duplicate field key %q", f.Key)
			}
			seen[f.Key] = true
			if f.Type == notify.FieldSelect && len(f.Options) == 0 {
				t.Errorf("select field %q has no options", f.Key)
			}
		}
	})

	t.Run("rejects missing required", func(t *testing.T) {
		var required []string
		for _, f := range c.Descriptor.Fields {
			if f.Required && f.Default == "" {
				required = append(required, f.Key)
			}
		}
		if len(required) == 0 {
			t.Skip("no required fields")
		}
		if _, err := notify.ValidateConfig(c.Descriptor.Fields, json.RawMessage(`{}`)); err == nil {
			t.Fatalf("expected validation error for empty config (required: %v)", required)
		}
	})

	for _, tc := range []struct {
		name      string
		status    int
		wantErr   bool
		retryable bool
	}{
		{"ok", http.StatusOK, false, false},
		{"accepted", http.StatusAccepted, false, false},
		{"server error is retryable", http.StatusInternalServerError, true, true},
		{"rate limit is retryable", http.StatusTooManyRequests, true, true},
		{"bad request is permanent", http.StatusBadRequest, true, false},
		{"unauthorized is permanent", http.StatusUnauthorized, true, false},
	} {
		t.Run("deliver/"+tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != http.MethodPost {
					t.Errorf("method = %s, want POST", r.Method)
				}
				if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
					t.Errorf("content-type = %q", ct)
				}
				var body any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("body is not JSON: %v", err)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"detail":"` + tc.name + `"}`))
			}))
			defer srv.Close()

			sink := build(t, c, srv.URL)
			events := Events()
			if c.Descriptor.Capabilities.MaxBatch > 0 && c.Descriptor.Capabilities.MaxBatch < len(events) {
				events = events[:c.Descriptor.Capabilities.MaxBatch]
			}
			results := sink.Deliver(context.Background(), events, nil)
			if len(results) != len(events) {
				t.Fatalf("results = %d, want %d", len(results), len(events))
			}
			for i, r := range results {
				if r.FindingKey != events[i].Key {
					t.Errorf("result[%d].FindingKey = %q, want %q", i, r.FindingKey, events[i].Key)
				}
				if (r.Err != nil) != tc.wantErr {
					t.Errorf("result[%d].Err = %v, wantErr %v", i, r.Err, tc.wantErr)
				}
				if r.Err != nil && r.Err.Retryable != tc.retryable {
					t.Errorf("result[%d].Err.Retryable = %v, want %v", i, r.Err.Retryable, tc.retryable)
				}
				if r.HTTPStatus != tc.status {
					t.Errorf("result[%d].HTTPStatus = %d, want %d", i, r.HTTPStatus, tc.status)
				}
			}
			if calls.Load() == 0 {
				t.Fatal("backend was never called")
			}
		})
	}

	t.Run("deliver/connection refused is retryable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		sink := build(t, c, url)
		events := Events()[:1]
		results := sink.Deliver(context.Background(), events, nil)
		if len(results) != 1 || results[0].Err == nil || !results[0].Err.Retryable {
			t.Fatalf("want one retryable error, got %+v", results)
		}
	})

	t.Run("deliver/resolved renders", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		defer srv.Close()
		sink := build(t, c, srv.URL)
		ev := Events()[0]
		ev.Action = repo.DispatchResolved
		ev.Status = "accepted"
		results := sink.Deliver(context.Background(), []notify.Event{ev}, nil)
		if len(results) != 1 || results[0].Err != nil {
			t.Fatalf("resolved delivery failed: %+v", results)
		}
	})

	t.Run("test", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
		defer srv.Close()
		if err := build(t, c, srv.URL).Test(context.Background()); err != nil {
			t.Fatalf("Test() = %v", err)
		}
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
		defer bad.Close()
		if err := build(t, c, bad.URL).Test(context.Background()); err == nil {
			t.Fatal("Test() against 401 backend returned nil")
		}
	})
}

func build(t *testing.T, c Case, serverURL string) notify.Sink {
	t.Helper()
	raw, err := json.Marshal(c.Config(serverURL))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := notify.ValidateConfig(c.Descriptor.Fields, raw)
	if err != nil {
		t.Fatalf("config rejected: %v", err)
	}
	sink, err := c.Factory(cfg, notify.Deps{HTTPClient: &http.Client{Timeout: 5 * time.Second}})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	return sink
}
