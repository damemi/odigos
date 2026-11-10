package graphql

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/odigos-io/central-proxy/internal/logger"
	"github.com/odigos-io/odigos/api/k8sconsts"
)

const testToken = "test-sa-token-value"

// captureHeaders runs one forwarded request against a fake frontend and
// returns the headers it received.
func captureHeaders(t *testing.T, c *GraphQLClient, isAdmin bool) http.Header {
	t.Helper()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FRONTEND_BACKEND_URL", srv.URL)

	if _, err := c.ForwardGraphQLRequest(context.Background(), "req", "{ x }", nil, isAdmin); err != nil {
		t.Fatalf("forward: %v", err)
	}
	return got
}

func writeToken(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tokenPathEnvVar, path)
}

func TestAdminWithTokenSendsBothHeaders(t *testing.T) {
	writeToken(t)
	h := captureHeaders(t, NewGraphQLClient(), true)
	if h.Get(k8sconsts.CentralProxyAdminOverrideHeader) != "true" {
		t.Errorf("admin header = %q, want true", h.Get(k8sconsts.CentralProxyAdminOverrideHeader))
	}
	if h.Get(k8sconsts.CentralProxyUITokenHeader) != testToken {
		t.Errorf("token header = %q, want trimmed token", h.Get(k8sconsts.CentralProxyUITokenHeader))
	}
}

func TestTokenIsReReadPerRequest(t *testing.T) {
	writeToken(t)
	c := NewGraphQLClient()
	captureHeaders(t, c, true)
	if err := os.WriteFile(os.Getenv(tokenPathEnvVar), []byte("rotated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := captureHeaders(t, c, true).Get(k8sconsts.CentralProxyUITokenHeader); got != "rotated" {
		t.Errorf("token header = %q, want rotated", got)
	}
}

func TestNonAdminSendsNeitherHeader(t *testing.T) {
	writeToken(t)
	h := captureHeaders(t, NewGraphQLClient(), false)
	if _, ok := h[k8sconsts.CentralProxyAdminOverrideHeader]; ok {
		t.Error("admin header sent for non-admin")
	}
	if _, ok := h[k8sconsts.CentralProxyUITokenHeader]; ok {
		t.Error("token header sent for non-admin")
	}
}

func TestAdminWithMissingTokenFileSendsOldHeaderAndWarnsOnce(t *testing.T) {
	var logs bytes.Buffer
	prev := logger.Log
	logger.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	t.Cleanup(func() { logger.Log = prev })
	t.Setenv(tokenPathEnvVar, filepath.Join(t.TempDir(), "missing"))

	c := NewGraphQLClient()
	for i := 0; i < 3; i++ {
		h := captureHeaders(t, c, true)
		if h.Get(k8sconsts.CentralProxyAdminOverrideHeader) != "true" {
			t.Errorf("admin header = %q, want true", h.Get(k8sconsts.CentralProxyAdminOverrideHeader))
		}
		if _, ok := h[k8sconsts.CentralProxyUITokenHeader]; ok {
			t.Error("token header sent without a token file")
		}
	}
	if n := strings.Count(logs.String(), `"level":"WARN"`); n != 1 {
		t.Errorf("warnings = %d, want 1; logs: %s", n, logs.String())
	}
}

func TestTokenNeverLogged(t *testing.T) {
	var logs bytes.Buffer
	prev := logger.Log
	logger.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	t.Cleanup(func() { logger.Log = prev })
	writeToken(t)
	captureHeaders(t, NewGraphQLClient(), true)
	if strings.Contains(logs.String(), testToken) {
		t.Error("token leaked into logs")
	}
}

func TestEmptyTokenFileTreatedAsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tokenPathEnvVar, path)
	h := captureHeaders(t, NewGraphQLClient(), true)
	if _, ok := h[k8sconsts.CentralProxyUITokenHeader]; ok {
		t.Error("empty token sent")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	writeToken(t)
	var leaked bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get(k8sconsts.CentralProxyUITokenHeader) != ""
	}))
	t.Cleanup(other.Close)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FRONTEND_BACKEND_URL", srv.URL)
	_, _ = NewGraphQLClient().ForwardGraphQLRequest(context.Background(), "req", "{ x }", nil, true)
	if leaked {
		t.Error("token replayed to redirect target")
	}
}

func TestWarnsAgainAfterRecovery(t *testing.T) {
	var logs bytes.Buffer
	prev := logger.Log
	logger.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	t.Cleanup(func() { logger.Log = prev })
	path := filepath.Join(t.TempDir(), "token")
	t.Setenv(tokenPathEnvVar, path)
	c := NewGraphQLClient()
	captureHeaders(t, c, true) // missing -> warn
	if err := os.WriteFile(path, []byte(testToken), 0o600); err != nil {
		t.Fatal(err)
	}
	captureHeaders(t, c, true) // recovered
	_ = os.Remove(path)
	captureHeaders(t, c, true) // missing again -> warn
	if n := strings.Count(logs.String(), `"level":"WARN"`); n != 2 {
		t.Errorf("warnings = %d, want 2", n)
	}
}
