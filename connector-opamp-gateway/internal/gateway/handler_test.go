package gateway

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorgateway"
)

// quietLogger keeps refusal logging out of test output while still exercising every log call — a nil
// logger would leave the deny path's formatting untested.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// upstreamRecorder is a stand-in connector OpAMP server that reports what actually reached it.
type upstreamRecorder struct {
	*httptest.Server
	lastHeader http.Header
	hits       int
}

func newUpstreamRecorder(t *testing.T) *upstreamRecorder {
	t.Helper()
	rec := &upstreamRecorder{}
	rec.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.hits++
		rec.lastHeader = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("served"))
	}))
	t.Cleanup(rec.Close)
	return rec
}

func newHandler(t *testing.T, creds Credentials, conns Connectors, up Upstream) *Handler {
	t.Helper()
	h := New(creds, conns, up, quietLogger())
	h.Now = func() time.Time { return testNow }
	return h
}

func request(token string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/opamp", strings.NewReader(""))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// TestAuthenticatedRequestReachesItsConnector is the happy path, and the header assertions are the
// "no token leakage" criterion: the credential must stop at the gateway, and the connector must
// receive an identity it can act on.
func TestAuthenticatedRequestReachesItsConnector(t *testing.T) {
	up := newUpstreamRecorder(t)
	creds := newFakeCredentials(activeRecord(t))
	h := newHandler(t, creds, newFakeConnectors(registeredConnector()), upstreamTo(up.URL))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(testToken))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	if up.hits != 1 {
		t.Fatalf("upstream hits = %d, want 1", up.hits)
	}

	// The credential buys the customer's destination credential out of a configuration push. It has no
	// business on the second hop, and the connector has no use for it.
	if got := up.lastHeader.Get("Authorization"); got != "" {
		t.Errorf("the credential was forwarded to the connector: %q", got)
	}

	id, ok := collectorgateway.ParseIdentity(up.lastHeader)
	if !ok {
		t.Fatalf("the connector received no usable identity: %v", up.lastHeader)
	}
	want := collectorgateway.Identity{Binding: activeRecord(t).Binding, CredentialID: testCredID}
	if id != want {
		t.Errorf("forwarded identity:\n got %+v\nwant %+v", id, want)
	}
}

// TestClientCannotChooseItsOwnConnector: routing comes from authenticated state alone. A Collector
// that sets the identity headers itself must be routed exactly as if it had not.
func TestClientCannotChooseItsOwnConnector(t *testing.T) {
	up := newUpstreamRecorder(t)
	h := newHandler(t, newFakeCredentials(activeRecord(t)), newFakeConnectors(registeredConnector()), upstreamTo(up.URL))

	r := request(testToken)
	r.Header.Set(collectorgateway.HeaderConnectorUID, "uid-belonging-to-someone-else")
	r.Header.Set(collectorgateway.HeaderCollectorID, "col-belonging-to-someone-else")
	r.Header.Set(collectorgateway.HeaderAccountID, "999999999999")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	id, ok := collectorgateway.ParseIdentity(up.lastHeader)
	if !ok {
		t.Fatal("no identity reached the connector")
	}
	if id.Binding.ConnectorUID != testConnUID || id.Binding.CollectorID != testCollectorID || id.Binding.AccountID != testAccount {
		t.Errorf("a forged identity survived the gateway: %+v", id)
	}
}

// TestRefusals covers every way a handshake is turned away. The status code is the whole answer: a
// refusal must not tell the caller which check failed.
func TestRefusals(t *testing.T) {
	past := testNow.Add(-time.Hour)

	cases := []struct {
		name   string
		token  string
		mutate func(*collectorcredential.Record)
		conn   *collectorgateway.Connector
		status int
	}{
		{name: "no credential", token: "", conn: registeredConnector(), status: http.StatusUnauthorized},
		{name: "wrong credential", token: "not-the-token", conn: registeredConnector(), status: http.StatusUnauthorized},
		{
			name: "revoked credential", token: testToken, conn: registeredConnector(),
			mutate: func(r *collectorcredential.Record) {
				r.State = collectorcredential.StateRevoked
				r.RevokedAt = &past
				r.RevokedReason = collectorcredential.ReasonCompromise
			},
			status: http.StatusUnauthorized,
		},
		{
			name: "credential never installed", token: testToken, conn: registeredConnector(),
			mutate: func(r *collectorcredential.Record) { r.State = collectorcredential.StatePending },
			status: http.StatusUnauthorized,
		},
		{
			name: "expired credential", token: testToken, conn: registeredConnector(),
			mutate: func(r *collectorcredential.Record) { r.ExpiresAt = &past },
			status: http.StatusUnauthorized,
		},
		{
			// A real credential for one account aimed at a connector that owns another. The check this
			// whole handshake exists for.
			name: "credential bound to a different account", token: testToken,
			conn:   &collectorgateway.Connector{UID: testConnUID, Name: "aws-other", Provider: "aws", AccountID: "999999999999"},
			status: http.StatusUnauthorized,
		},
		{
			name: "credential bound to a different provider", token: testToken,
			conn:   &collectorgateway.Connector{UID: testConnUID, Name: "azure-prod", Provider: "azure", AccountID: testAccount},
			status: http.StatusUnauthorized,
		},
		{
			// The connector-recreation case: the CR came back with a new UID, so nothing answers to the
			// one this credential names. Not the Collector's fault, so not a 401.
			name: "connector is not registered", token: testToken, conn: nil, status: http.StatusServiceUnavailable,
		},
		{
			name: "connector is being deleted", token: testToken,
			conn: &collectorgateway.Connector{
				UID: testConnUID, Name: "aws-prod", Provider: "aws", AccountID: testAccount, Terminating: true,
			},
			status: http.StatusServiceUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			up := newUpstreamRecorder(t)
			creds := newFakeCredentials(activeRecord(t))
			if tc.mutate != nil {
				creds.set(tc.mutate)
			}
			h := newHandler(t, creds, newFakeConnectors(tc.conn), upstreamTo(up.URL))

			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(tc.token))

			if w.Code != tc.status {
				t.Errorf("status = %d, want %d", w.Code, tc.status)
			}
			if up.hits != 0 {
				t.Error("a refused connection still reached the connector")
			}
			// Nothing about which check failed may reach the caller: telling "unknown" apart from
			// "revoked" confirms to whoever is probing that a token was once real.
			if body := w.Body.String(); body != "" {
				t.Errorf("a refusal disclosed a reason: %q", body)
			}
			if tc.status == http.StatusUnauthorized {
				if got := w.Header().Get("WWW-Authenticate"); got != `Bearer realm="odigos-opamp"` {
					t.Errorf("WWW-Authenticate = %q", got)
				}
			}
		})
	}
}

// TestStoreOutageIsNotAnAuthenticationFailure: answering 401 when the database is unreachable would
// tell an entire fleet its credentials are invalid over a blip, and the Supervisor has no way to tell
// that apart from a real revocation.
func TestStoreOutageIsNotAnAuthenticationFailure(t *testing.T) {
	up := newUpstreamRecorder(t)
	creds := newFakeCredentials(activeRecord(t))
	creds.setErr(errors.New("connection refused"))
	h := newHandler(t, creds, newFakeConnectors(registeredConnector()), upstreamTo(up.URL))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(testToken))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if up.hits != 0 {
		t.Error("a connection was proxied without a credential check")
	}
}

// TestUnsyncedWatchRefusesNothingPermanently: on a restart the whole fleet reconnects at once. Before
// the watch has listed, every connector looks unregistered — reporting that as a refusal would fail
// the fleet at precisely the moment it is all arriving.
func TestUnsyncedWatchAnswers503(t *testing.T) {
	up := newUpstreamRecorder(t)
	conns := newFakeConnectors(registeredConnector())
	conns.setSynced(false)
	h := newHandler(t, newFakeCredentials(activeRecord(t)), conns, upstreamTo(up.URL))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(testToken))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}

	// And once it syncs, the same request succeeds — the refusal was about readiness, not the credential.
	conns.setSynced(true)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request(testToken))
	if w.Code != http.StatusOK {
		t.Errorf("after sync: status = %d, want 200", w.Code)
	}
}

// TestUnresolvableUpstreamIsReportedNotGuessed: until connector-runtime labels the connector Service
// and exposes its OpAMP port, there is nowhere to route. The gateway must say so rather than
// synthesize an address that nothing answers on.
func TestUnresolvableUpstreamIsReportedNotGuessed(t *testing.T) {
	h := newHandler(t, newFakeCredentials(activeRecord(t)), newFakeConnectors(registeredConnector()), upstreamUnresolvable())

	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(testToken))

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestActivationIsRecordedOnceForTheAuthenticatingCredential: activated_at names WHICH issuance was
// seen working, which is what lets a rotation revoke the outgoing credential (§6.4). Recording the
// wrong one, or none, stalls every rotation.
func TestActivationIsRecordedForTheAuthenticatingCredential(t *testing.T) {
	up := newUpstreamRecorder(t)
	creds := newFakeCredentials(activeRecord(t))
	h := newHandler(t, creds, newFakeConnectors(registeredConnector()), upstreamTo(up.URL))

	h.ServeHTTP(httptest.NewRecorder(), request(testToken))

	if creds.activationCount() != 1 {
		t.Fatalf("activations = %d, want 1", creds.activationCount())
	}
	got := creds.activations[0]
	if got.collectorID != testCollectorID || got.credentialID != testCredID {
		t.Errorf("activation recorded for (%q, %q), want (%q, %q)",
			got.collectorID, got.credentialID, testCollectorID, testCredID)
	}
	if !got.at.Equal(testNow) {
		t.Errorf("activation time = %v, want %v", got.at, testNow)
	}
}

// TestActivationFailureDoesNotBlockTheConnection: activated_at is bookkeeping, not admission. Failing
// an authenticated Collector because a column could not be written would take its configuration plane
// down over a rotation detail — and a stalled rotation is the safe direction, since it leaves the
// outgoing credential valid rather than revoking one still in use.
func TestActivationFailureDoesNotBlockTheConnection(t *testing.T) {
	up := newUpstreamRecorder(t)
	creds := newFakeCredentials(activeRecord(t))
	creds.activationErr = errors.New("write failed")
	h := newHandler(t, creds, newFakeConnectors(registeredConnector()), upstreamTo(up.URL))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(testToken))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 — a bookkeeping failure refused a valid Collector", w.Code)
	}
	if up.hits != 1 {
		t.Errorf("upstream hits = %d, want 1", up.hits)
	}
}

// TestSupersededCredentialIsAccepted: the rotation drain window. The outgoing credential keeps working
// while the old tasks are replaced, or every routine rotation is an outage.
func TestSupersededCredentialIsAccepted(t *testing.T) {
	up := newUpstreamRecorder(t)
	creds := newFakeCredentials(activeRecord(t))
	creds.set(func(r *collectorcredential.Record) {
		r.State = collectorcredential.StateSuperseded
		r.SupersededBy = "cred-2"
	})
	h := newHandler(t, creds, newFakeConnectors(registeredConnector()), upstreamTo(up.URL))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(testToken))

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
}
