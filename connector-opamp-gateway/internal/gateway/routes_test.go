package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
)

func TestMetricsRouteAuthenticatesAndPreservesPayload(t *testing.T) {
	const payload = "otlp-protobuf-payload"
	var hits int
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/metrics" {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != payload || r.Header.Get("Content-Type") != "application/x-protobuf" {
			t.Errorf("metrics payload changed: body=%q content-type=%q", body, r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("credential reached connector")
		}
		id, ok := collectorgateway.ParseIdentity(r.Header)
		if !ok || id.Binding != activeRecord(t).Binding || id.CredentialID != testCredID {
			t.Errorf("incorrect authenticated identity: %+v", id)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()
	h := newHandler(t, newFakeCredentials(activeRecord(t)), newFakeConnectors(registeredConnector()), upstreamTo(up.URL))
	mux := http.NewServeMux()
	RegisterCollectorRoutes(mux, "/v1/opamp", h)

	for _, tc := range []struct {
		name, token, method string
		status              int
	}{
		{"authenticated", strings.TrimPrefix(collectorgateway.MetricsAuthorization(collectorgateway.Identity{Binding: activeRecord(t).Binding, CredentialID: testCredID}, activeRecord(t).TokenSHA256), "Bearer "), http.MethodPost, http.StatusOK},
		{"OpAMP credential refused", testToken, http.MethodPost, http.StatusUnauthorized},
		{"missing credential", "", http.MethodPost, http.StatusUnauthorized},
		{"invalid credential", "invalid", http.MethodPost, http.StatusUnauthorized},
		{"wrong method", testToken, http.MethodGet, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/v1/metrics", strings.NewReader(payload))
			r.Header.Set("Content-Type", "application/x-protobuf")
			r.Header.Set(collectorgateway.HeaderCollectorID, "forged-collector")
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.status {
				t.Errorf("status=%d, want %d", w.Code, tc.status)
			}
		})
	}
	if hits != 1 {
		t.Errorf("upstream hits=%d, want 1", hits)
	}
}

func TestScopedMetricsCredentialCannotConfigureCollector(t *testing.T) {
	up := newUpstreamRecorder(t)
	rec := activeRecord(t)
	id := collectorgateway.Identity{Binding: rec.Binding, CredentialID: rec.CredentialID}
	h := newHandler(t, newFakeCredentials(rec), newFakeConnectors(registeredConnector()), upstreamTo(up.URL))
	r := request("")
	r.Header.Set("Authorization", collectorgateway.MetricsAuthorization(id, rec.TokenSHA256))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized || up.hits != 0 {
		t.Errorf("metrics credential reached configuration endpoint: status=%d hits=%d", w.Code, up.hits)
	}
}

func TestOpAMPForwardsOnlyDerivedMetricsCredential(t *testing.T) {
	up := newUpstreamRecorder(t)
	rec := activeRecord(t)
	h := newHandler(t, newFakeCredentials(rec), newFakeConnectors(registeredConnector()), upstreamTo(up.URL))
	r := request(testToken)
	r.Header.Set(collectorgateway.HeaderMetricsAuthorization, "attacker-supplied")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	id := collectorgateway.Identity{Binding: rec.Binding, CredentialID: rec.CredentialID}
	if w.Code != http.StatusOK || !collectorgateway.VerifyMetricsAuthorization(up.lastHeader.Get(collectorgateway.HeaderMetricsAuthorization), id, rec.TokenSHA256) {
		t.Error("gateway did not replace the untrusted metrics credential with a valid scoped credential")
	}
	if up.lastHeader.Get("Authorization") != "" {
		t.Error("OpAMP credential forwarded")
	}
}

func TestMetricsCredentialFollowsStoredLifetime(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*collectorcredential.Record)
	}{
		{"revoked", func(r *collectorcredential.Record) { r.State = collectorcredential.StateRevoked }},
		{"expired", func(r *collectorcredential.Record) { when := testNow.Add(-time.Second); r.ExpiresAt = &when }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := activeRecord(t)
			id := collectorgateway.Identity{Binding: rec.Binding, CredentialID: rec.CredentialID}
			auth := collectorgateway.MetricsAuthorization(id, rec.TokenSHA256)
			tc.mutate(rec)
			h := newHandler(t, newFakeCredentials(rec), newFakeConnectors(registeredConnector()), nil)
			r := httptest.NewRequest(http.MethodPost, collectorgateway.MetricsPath, nil)
			r.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusUnauthorized {
				t.Errorf("status=%d, want 401", w.Code)
			}
		})
	}
}
