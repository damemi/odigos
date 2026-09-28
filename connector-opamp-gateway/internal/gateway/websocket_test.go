package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
)

// wsUpstream is a stand-in connector OpAMP server: it accepts the upgrade and echoes frames, which is
// enough to prove the gateway carries a real bidirectional session rather than just a 101.
type wsUpstream struct {
	*httptest.Server
	mu         sync.Mutex
	lastHeader http.Header
	accepted   int
}

func newWSUpstream(t *testing.T) *wsUpstream {
	t.Helper()
	up := &wsUpstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.mu.Lock()
		up.accepted++
		up.lastHeader = r.Header.Clone()
		up.mu.Unlock()

		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, msg, err := c.Read(r.Context())
			if err != nil {
				return
			}
			if err := c.Write(r.Context(), typ, msg); err != nil {
				return
			}
		}
	}))
	t.Cleanup(up.Close)
	return up
}

func (u *wsUpstream) header() http.Header {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastHeader.Clone()
}

// dial opens an OpAMP WebSocket through the gateway, the way the Supervisor does: the credential on
// the Authorization header of the handshake.
func dial(t *testing.T, gatewayURL, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h := http.Header{}
	if token != "" {
		h.Set("Authorization", "Bearer "+token)
	}
	return websocket.Dial(ctx, strings.Replace(gatewayURL, "http://", "ws://", 1)+"/v1/opamp",
		&websocket.DialOptions{HTTPHeader: h})
}

func echo(t *testing.T, c *websocket.Conn, msg string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := c.Write(ctx, websocket.MessageBinary, []byte(msg)); err != nil {
		return err
	}
	_, got, err := c.Read(ctx)
	if err != nil {
		return err
	}
	if string(got) != msg {
		t.Fatalf("echo returned %q, want %q", got, msg)
	}
	return nil
}

// serveGateway puts a Handler behind a real HTTP server so a real client can dial it.
func serveGateway(t *testing.T, h *Handler) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/v1/opamp", h)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// waitFor polls until cond holds, so a test never depends on the handler having reached a particular
// line by the time Dial returned.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// expectClosed asserts a session was torn down from the gateway side.
func expectClosed(t *testing.T, c *websocket.Conn, what string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := c.Read(ctx); err == nil {
		t.Fatalf("%s: the session is still readable", what)
	} else if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("%s: the session was never closed", what)
	}
}

// TestWebSocketRoutingEndToEnd is the "authenticated WebSocket routing" criterion end to end: a real
// upgrade through the gateway, a real frame each way, and the identity contract intact on arrival.
func TestWebSocketRoutingEndToEnd(t *testing.T) {
	up := newWSUpstream(t)
	h := newHandler(t, newFakeCredentials(activeRecord(t)), newFakeConnectors(registeredConnector()), upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, resp, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v (response %v)", err, resp)
	}
	defer c.CloseNow()

	if err := echo(t, c, "opamp-frame"); err != nil {
		t.Fatalf("echo through the gateway: %v", err)
	}

	hdr := up.header()
	if got := hdr.Get("Authorization"); got != "" {
		t.Errorf("the credential reached the connector over the upgraded connection: %q", got)
	}
	id, ok := collectorgateway.ParseIdentity(hdr)
	if !ok || id.Binding.CollectorID != testCollectorID || id.CredentialID != testCredID {
		t.Errorf("identity on the upgrade request: %+v (ok=%v)", id, ok)
	}
	if h.SessionRegistry().Len() != 1 {
		t.Errorf("open sessions = %d, want 1", h.SessionRegistry().Len())
	}
}

// TestRefusedWebSocketNeverUpgrades: a bad credential must be turned away on the handshake, before the
// upgrade. After a 101 the gateway is a byte pipe, so this is the last point at which it can refuse.
func TestRefusedWebSocketNeverUpgrades(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	creds.set(func(r *collectorcredential.Record) { r.State = collectorcredential.StateRevoked })
	h := newHandler(t, creds, newFakeConnectors(registeredConnector()), upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, resp, err := dial(t, gw, testToken)
	if err == nil {
		c.CloseNow()
		t.Fatal("a revoked credential completed a WebSocket upgrade")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("handshake response = %v, want 401", resp)
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if up.accepted != 0 {
		t.Error("the connector saw a connection the gateway refused")
	}
}

// TestRevocationClosesAnOpenSession is the property that makes revocation mean anything. An OpAMP
// session lives for days and keeps receiving configuration pushes, each carrying the destination
// credential — so refusing only the NEXT handshake would leave a compromised holder connected for as
// long as it avoided reconnecting.
func TestRevocationClosesAnOpenSession(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())
	h := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	if err := echo(t, c, "before"); err != nil {
		t.Fatalf("session not working before revocation: %v", err)
	}
	waitFor(t, "the session to register", func() bool { return h.SessionRegistry().Len() == 1 })

	creds.set(func(r *collectorcredential.Record) {
		r.State = collectorcredential.StateRevoked
		revoked := testNow
		r.RevokedAt = &revoked
		r.RevokedReason = collectorcredential.ReasonCompromise
	})

	rv := &Revalidator{Credentials: creds, Connectors: conns, Sessions: h.SessionRegistry(),
		Log: quietLogger(), Now: func() time.Time { return testNow }}
	if closed := rv.Sweep(context.Background()); closed != 1 {
		t.Fatalf("sweep closed %d sessions, want 1", closed)
	}

	expectClosed(t, c, "after revocation")
	waitFor(t, "the session to deregister", func() bool { return h.SessionRegistry().Len() == 0 })

	// And the reconnect the Supervisor makes next is refused, so the Collector cannot simply come back.
	if _, resp, err := dial(t, gw, testToken); err == nil {
		t.Fatal("a revoked credential reconnected")
	} else if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reconnect response = %v, want 401", resp)
	}
}

// TestConnectorDeletionClosesOpenSessions: teardown revokes credentials before deleting the connector,
// but a session already open would otherwise keep being handed a configuration on the way down.
func TestConnectorDeletionClosesOpenSessions(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())
	h := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "the session to register", func() bool { return h.SessionRegistry().Len() == 1 })

	conns.set(nil) // the CR is gone

	rv := &Revalidator{Credentials: creds, Connectors: conns, Sessions: h.SessionRegistry(),
		Log: quietLogger(), Now: func() time.Time { return testNow }}
	if closed := rv.Sweep(context.Background()); closed != 1 {
		t.Fatalf("sweep closed %d sessions, want 1", closed)
	}
	expectClosed(t, c, "after the connector was deleted")
}

// TestGatewayRestartDropsSessionsAndTheCollectorReconnects: a replica going away must not need the
// Collector to do anything special, and must not leave http.Server.Shutdown waiting on WebSockets that
// will never close on their own.
func TestGatewayRestartDropsSessionsAndTheCollectorReconnects(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())

	first := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, first)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "the session to register", func() bool { return first.SessionRegistry().Len() == 1 })

	if closed := first.SessionRegistry().CloseAll(); closed != 1 {
		t.Fatalf("CloseAll signalled %d sessions, want 1", closed)
	}
	expectClosed(t, c, "after the gateway shut down")

	// The Supervisor reconnects through another replica, which shares nothing but the credential store.
	// Session state is per replica on purpose — nothing has to be handed over.
	second := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw2 := serveGateway(t, second)

	c2, _, err := dial(t, gw2, testToken)
	if err != nil {
		t.Fatalf("reconnect through a second replica: %v", err)
	}
	defer c2.CloseNow()
	if err := echo(t, c2, "after-restart"); err != nil {
		t.Fatalf("the reconnected session does not carry traffic: %v", err)
	}
}

// TestConnectorRestartLetsTheCollectorReconnect: when a connector pod restarts its sessions drop, and
// the Collector must be able to come straight back — the credential is unchanged and so is the route.
func TestConnectorRestartLetsTheCollectorReconnect(t *testing.T) {
	up := newWSUpstream(t)
	h := newHandler(t, newFakeCredentials(activeRecord(t)), newFakeConnectors(registeredConnector()), upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if err := echo(t, c, "before"); err != nil {
		t.Fatalf("echo: %v", err)
	}
	// The connector drops the connection, as a restarting pod does.
	c.CloseNow()
	waitFor(t, "the session to deregister", func() bool { return h.SessionRegistry().Len() == 0 })

	c2, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("reconnect after a connector restart: %v", err)
	}
	defer c2.CloseNow()
	if err := echo(t, c2, "after"); err != nil {
		t.Fatalf("the reconnected session does not carry traffic: %v", err)
	}
}

// TestManyReplicasShareOneCredential: a Collector scaled to N tasks opens N sessions under one
// credential. Treating the second as a replay would break scale-out at the moment a customer's traffic
// grew enough to need it (§6.3).
func TestManyReplicasShareOneCredential(t *testing.T) {
	const replicas = 4

	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())
	h := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, h)

	for i := 0; i < replicas; i++ {
		c, _, err := dial(t, gw, testToken)
		if err != nil {
			t.Fatalf("replica %d could not connect: %v", i, err)
		}
		defer c.CloseNow()
	}
	waitFor(t, "every replica to register", func() bool { return h.SessionRegistry().Len() == replicas })

	// And a sweep costs one read per CREDENTIAL, not per connection: otherwise scaling a Collector out
	// multiplies the store's read rate by the replica count for no extra information.
	rv := &Revalidator{Credentials: creds, Connectors: conns, Sessions: h.SessionRegistry(),
		Log: quietLogger(), Now: func() time.Time { return testNow }}
	before := creds.byIDCallCount()
	if closed := rv.Sweep(context.Background()); closed != 0 {
		t.Errorf("sweep closed %d healthy sessions", closed)
	}
	if got := creds.byIDCallCount() - before; got != 1 {
		t.Errorf("sweep made %d credential reads for %d sessions on one credential, want 1", got, replicas)
	}
}

// TestSweepKeepsSessionsOpenWhenTheStoreIsDown: closing sessions on a database blip would disconnect
// the whole fleet, and every one of them would then fail to reconnect for the same reason. Leaving
// them open costs at most one more sweep interval of a revoked credential.
func TestSweepKeepsSessionsOpenWhenTheStoreIsDown(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())
	h := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "the session to register", func() bool { return h.SessionRegistry().Len() == 1 })

	creds.setErr(errors.New("connection refused"))

	rv := &Revalidator{Credentials: creds, Connectors: conns, Sessions: h.SessionRegistry(),
		Log: quietLogger(), Now: func() time.Time { return testNow }}
	if closed := rv.Sweep(context.Background()); closed != 0 {
		t.Fatalf("sweep closed %d sessions during a store outage", closed)
	}
	if err := echo(t, c, "still-here"); err != nil {
		t.Fatalf("a healthy session was disrupted by a store outage: %v", err)
	}
}

// TestSweepLeavesAHealthySessionAlone guards the other direction: a sweep that closed working sessions
// would be a scheduled fleet-wide disconnection.
func TestSweepLeavesAHealthySessionAlone(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())
	h := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "the session to register", func() bool { return h.SessionRegistry().Len() == 1 })

	rv := &Revalidator{Credentials: creds, Connectors: conns, Sessions: h.SessionRegistry(),
		Log: quietLogger(), Now: func() time.Time { return testNow }}
	for i := 0; i < 3; i++ {
		if closed := rv.Sweep(context.Background()); closed != 0 {
			t.Fatalf("sweep %d closed a healthy session", i)
		}
	}
	if err := echo(t, c, "still-here"); err != nil {
		t.Fatalf("a healthy session stopped working across sweeps: %v", err)
	}
}

// TestRevocationClosesSessionsEvenWhileTheWatchIsCold: the cache-warming allowance exists so a restart
// does not drop the fleet over connectors that are merely not listed yet. It must not extend to the
// credential — a compromise revocation would otherwise be waiting out precisely that window.
func TestRevocationClosesSessionsEvenWhileTheWatchIsCold(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())
	h := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "the session to register", func() bool { return h.SessionRegistry().Len() == 1 })

	creds.set(func(r *collectorcredential.Record) {
		r.State = collectorcredential.StateRevoked
		revoked := testNow
		r.RevokedAt = &revoked
		r.RevokedReason = collectorcredential.ReasonCompromise
	})
	conns.setSynced(false) // the gateway restarted, or the watch relisted

	rv := &Revalidator{Credentials: creds, Connectors: conns, Sessions: h.SessionRegistry(),
		Log: quietLogger(), Now: func() time.Time { return testNow }}
	if closed := rv.Sweep(context.Background()); closed != 1 {
		t.Fatalf("sweep closed %d sessions, want 1 — a revocation waited out the cache warm-up", closed)
	}
	expectClosed(t, c, "after revocation during a cold watch")
}

// TestColdWatchDoesNotCloseSessionsOverAnUnlistedConnector is the other half: while the watch is cold
// every connector looks deleted, and closing on that would drop the fleet on every restart.
func TestColdWatchDoesNotCloseSessionsOverAnUnlistedConnector(t *testing.T) {
	up := newWSUpstream(t)
	creds := newFakeCredentials(activeRecord(t))
	conns := newFakeConnectors(registeredConnector())
	h := newHandler(t, creds, conns, upstreamTo(up.URL))
	gw := serveGateway(t, h)

	c, _, err := dial(t, gw, testToken)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	waitFor(t, "the session to register", func() bool { return h.SessionRegistry().Len() == 1 })

	conns.set(nil)         // not listed yet
	conns.setSynced(false) // ...because the watch has not finished listing

	rv := &Revalidator{Credentials: creds, Connectors: conns, Sessions: h.SessionRegistry(),
		Log: quietLogger(), Now: func() time.Time { return testNow }}
	if closed := rv.Sweep(context.Background()); closed != 0 {
		t.Fatalf("sweep closed %d sessions while the watch was still listing", closed)
	}
	if err := echo(t, c, "still-here"); err != nil {
		t.Fatalf("a healthy session was dropped over a cold watch: %v", err)
	}
}
