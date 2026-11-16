package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// newTestRouter builds the router with no repo/engine wired. Registration must
// not panic (this catches gin route conflicts), and write endpoints must return
// 503 until SetAdmin is called.
func newTestRouter(t *testing.T) http.Handler {
	t.Helper()
	commonlogger.Init("error", "insights-test")
	engine, _ := NewRouter(nil, commonlogger.LoggerCompat())
	return engine
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestRouterRegisters ensures the full route table builds without gin conflicts.
func TestRouterRegisters(t *testing.T) {
	h := newTestRouter(t)
	if got := do(t, h, http.MethodGet, "/healthz", "").Code; got != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", got)
	}
	if got := do(t, h, http.MethodGet, "/api/v1/docs", "").Code; got != http.StatusOK {
		t.Errorf("GET /api/v1/docs = %d, want 200", got)
	}
	if got := do(t, h, http.MethodGet, "/api/v1/catalog", "").Code; got != http.StatusOK {
		t.Errorf("GET /api/v1/catalog = %d, want 200", got)
	}
}

// TestWritesReturn503BeforeAdmin verifies engine-backed writes are gated until
// SetAdmin is wired.
func TestWritesReturn503BeforeAdmin(t *testing.T) {
	h := newTestRouter(t)
	cases := []struct {
		method, path, body string
	}{
		{http.MethodPut, "/api/v1/guardrails", `{"scope_key":"ns/svc","rules":[]}`},
		{http.MethodDelete, "/api/v1/guardrails?scope=transaction&scope_key=42", ""},
		{http.MethodPost, "/api/v1/guardrails/seed", `{"scope_key":"ns/svc","items":{}}`},
		{http.MethodPost, "/api/v1/guardrails/violations/accept", `{"scope_key":"ns/svc","rule_key":"allowed_egress","offending":"x"}`},
		{http.MethodPut, "/api/v1/policies", `{"scope":"global"}`},
		{http.MethodPut, "/api/v1/learning-policies", `{"class":"D2_egress"}`},
		{http.MethodPost, "/api/v1/anomalies", `{"resolution":"accept","items":[]}`},
		{http.MethodPost, "/api/v1/anomalies/1/sig", `{"resolution":"accept"}`},
		{http.MethodPut, "/api/v1/system-settings", `{}`},
		{http.MethodPost, "/api/v1/transactions/1/baseline/D2_egress/reset", ""},
		{http.MethodPost, "/api/v1/transactions/1/baselines/reset", ""},
		{http.MethodGet, "/api/v1/recommendations", ""},
		{http.MethodGet, "/api/v1/recommendations/abc", ""},
		{http.MethodPost, "/api/v1/recommendations/apply", `{"items":[{"id":"abc"}]}`},
		{http.MethodPost, "/api/v1/recommendations/dismiss", `{"ids":["abc"]}`},
		{http.MethodPost, "/api/v1/recommendations/restore", `{"ids":["abc"]}`},
		{http.MethodPost, "/api/v1/recommendations/revert", `{"ids":["abc"]}`},
		{http.MethodPost, "/api/v1/recommendations/preview", `{"transaction_id":1,"spec":{}}`},
		{http.MethodPost, "/api/v1/recommendations/recompute", ""},
	}
	for _, tc := range cases {
		if got := do(t, h, tc.method, tc.path, tc.body).Code; got != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.path, got)
		}
	}
}
