// Package gateway is the authenticating reverse proxy in front of every connector's OpAMP server.
//
// One public endpoint serves the whole managed-Collector fleet across AWS, Azure and GCP. A Collector
// presents a bearer credential on the handshake; the gateway resolves it to an immutable binding,
// confirms the connector it names is still there and still owns that account, and proxies the
// connection to that connector's OpAMP port. Nothing the client sends decides where it goes.
//
// See collector/managed/OPAMP_ROUTING.md, and MANAGED-COLLECTOR.md §6.3-6.4 for the credential.
package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorgateway"
)

// ErrCredentialNotFound is what Credentials returns when no row matches. Distinguished from a store
// failure because they must produce different answers: an unknown credential is a refusal, a database
// that cannot be reached is "try again" — answering the second with 401 would turn a brief outage into
// a fleet of Collectors told their credentials are invalid.
var ErrCredentialNotFound = errors.New("no credential matches the presented token")

// Credentials reads connector_platform.collector_credentials. The gateway never writes a credential
// value and never reads one back; the store holds hashes.
type Credentials interface {
	// ByTokenHash resolves the SHA-256 of a presented credential. Returns ErrCredentialNotFound when
	// nothing matches.
	ByTokenHash(ctx context.Context, tokenHash string) (*collectorcredential.Record, error)
	// ByID re-reads a known credential, for revalidating a session that is already open.
	ByID(ctx context.Context, collectorID, credentialID string) (*collectorcredential.Record, error)
	// MarkActivated records the first successful connection under a credential. Idempotent: it must
	// leave an already-set activated_at alone, because that timestamp is what says a rotation completed
	// and the outgoing credential may be revoked (§6.4).
	MarkActivated(ctx context.Context, collectorID, credentialID string, at time.Time) error
}

// Connectors resolves a connector CR UID to its registration. Backed by a watch, so this is a cache
// read on the handshake path rather than an API call per connection.
type Connectors interface {
	ByUID(uid string) (*collectorgateway.Connector, bool)
	// HasSynced reports whether the watch has finished its initial list. Before that, "not found" means
	// "not loaded yet" and must not be reported as an unknown connector.
	HasSynced() bool
}

// Upstream resolves where a connector's OpAMP server listens.
type Upstream func(*collectorgateway.Connector) (*url.URL, error)

// Handler serves the public OpAMP endpoint.
type Handler struct {
	Credentials Credentials
	Connectors  Connectors
	Upstream    Upstream
	Log         *slog.Logger
	// Now is injectable so expiry is testable without sleeping. Defaults to time.Now.
	Now func() time.Time

	sessions *Sessions
}

// New builds a Handler with its session registry. Use Sessions to revalidate open connections.
func New(creds Credentials, conns Connectors, up Upstream, log *slog.Logger) *Handler {
	return &Handler{
		Credentials: creds,
		Connectors:  conns,
		Upstream:    up,
		Log:         log,
		Now:         time.Now,
		sessions:    NewSessions(),
	}
}

// Sessions exposes the open-connection registry so a revalidation loop can close sessions whose
// credential stopped being acceptable.
func (h *Handler) SessionRegistry() *Sessions { return h.sessions }

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Before the watch has listed, an unregistered UID is indistinguishable from one not yet cached.
	// Refusing here would fail every Collector in the fleet for the first seconds after a restart —
	// exactly when the whole fleet is reconnecting.
	if !h.Connectors.HasSynced() {
		h.deny(w, r, http.StatusServiceUnavailable, "connector-cache-warming", nil, "")
		return
	}

	metrics := r.URL.Path == collectorgateway.MetricsPath
	var tokenHash string
	var rec *collectorcredential.Record
	var err error
	if metrics {
		collectorID, credentialID, ok := collectorgateway.ParseMetricsAuthorization(r.Header.Get("Authorization"))
		if !ok {
			h.deny(w, r, http.StatusUnauthorized, collectorgateway.RefusalNoCredential, nil, "")
			return
		}
		rec, err = h.Credentials.ByID(r.Context(), collectorID, credentialID)
		if err == nil && rec != nil {
			id := collectorgateway.Identity{Binding: rec.Binding, CredentialID: rec.CredentialID}
			if !collectorgateway.VerifyMetricsAuthorization(r.Header.Get("Authorization"), id, rec.TokenSHA256) {
				h.deny(w, r, http.StatusUnauthorized, collectorgateway.RefusalHashMismatch, nil, "")
				return
			}
			tokenHash = rec.TokenSHA256
		}
	} else {
		var refusal collectorgateway.Refusal
		tokenHash, refusal = collectorgateway.PresentedCredential(r.Header)
		if refusal != collectorgateway.RefusalNone {
			h.deny(w, r, http.StatusUnauthorized, refusal, nil, "")
			return
		}
		rec, err = h.Credentials.ByTokenHash(r.Context(), tokenHash)
	}
	switch {
	case errors.Is(err, ErrCredentialNotFound):
		rec = nil
	case err != nil:
		// A store failure is not an authentication answer. 503 keeps the Supervisor retrying instead of
		// treating a database blip as a dead credential.
		h.log().Error("credential lookup failed",
			"fingerprint", collectorcredential.FingerprintOfHash(tokenHash), "error", err)
		h.deny(w, r, http.StatusServiceUnavailable, "credential-store-unavailable", nil, "")
		return
	}

	var conn *collectorgateway.Connector
	if rec != nil {
		if c, ok := h.Connectors.ByUID(rec.Binding.ConnectorUID); ok {
			conn = c
		}
	}

	decision := collectorgateway.Authorize(tokenHash, rec, conn, h.now())
	switch decision.Outcome {
	case collectorgateway.OutcomeRefuse:
		h.deny(w, r, http.StatusUnauthorized, decision.Refusal, rec, tokenHash)
		return
	case collectorgateway.OutcomeUnavailable:
		h.deny(w, r, http.StatusServiceUnavailable, decision.Refusal, rec, tokenHash)
		return
	}

	h.proxy(w, r, decision.Identity, conn, tokenHash)
}

// proxy forwards an authenticated connection to its connector and holds it open.
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, id collectorgateway.Identity, conn *collectorgateway.Connector, tokenHash string) {
	target, err := h.Upstream(conn)
	if err != nil {
		h.log().Error("cannot resolve a connector's OpAMP address",
			"connector", conn.Name, "connector_uid", conn.UID, "error", err)
		h.deny(w, r, http.StatusServiceUnavailable, "upstream-unresolvable", nil, "")
		return
	}

	// The session's own cancel is what revocation pulls. Derived from the request context so a client
	// that goes away still tears everything down.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	session := h.sessions.Add(id, cancel)
	defer h.sessions.Remove(session)

	// Best-effort, and deliberately not fatal: activated_at drives rotation bookkeeping, not admission.
	// Failing an authenticated connection because a bookkeeping write did not land would take a
	// Collector's configuration plane down over a column. A stalled rotation is the safe direction —
	// it leaves the outgoing credential valid rather than revoking one still in use.
	if err := h.Credentials.MarkActivated(ctx, id.Binding.CollectorID, id.CredentialID, h.now()); err != nil {
		h.log().Warn("could not record credential activation; a rotation may not complete until it does",
			"collector_id", id.Binding.CollectorID, "credential_id", id.CredentialID, "error", err)
	}

	h.log().Info("routing an authenticated Collector",
		"collector_id", id.Binding.CollectorID,
		"credential_id", id.CredentialID,
		"connector", conn.Name,
		"connector_uid", conn.UID,
		"provider", id.Binding.Provider,
		"region", id.Binding.Region,
		"network_id", id.Binding.NetworkID,
		"upstream", target.String(),
	)

	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Apply is the whole trust boundary in one call: drop anything the client sent in the
			// gateway's header namespace, drop the credential, then assert the authenticated identity.
			id.Apply(pr.Out.Header)
			if r.URL.Path != collectorgateway.MetricsPath {
				pr.Out.Header.Set(collectorgateway.HeaderMetricsAuthorization, collectorgateway.MetricsAuthorization(id, tokenHash))
			}
		},
		// Streaming, not buffering: OpAMP over WebSocket is a long-lived bidirectional connection, and
		// the plain-HTTP transport is request/response with pushes that must not sit in a buffer.
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(h.log().Handler(), slog.LevelWarn),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			// A cancelled context is this gateway closing the session (revocation, shutdown), not a
			// fault. Logging it as an error would make every routine revocation look like a failure.
			if errors.Is(err, context.Canceled) {
				return
			}
			h.log().Warn("connector OpAMP upstream failed",
				"connector", conn.Name, "connector_uid", conn.UID, "error", err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r.WithContext(ctx))
}

// deny ends a handshake without telling the caller why.
//
// Every refusal is one status code and one empty body. Distinguishing "unknown credential" from
// "revoked" confirms to whoever is probing that a token was once real, and a Collector cannot act on
// the difference — all of them mean it is not getting configuration. The reason goes to the log, with
// the fingerprint that lets an operator find the Collector.
func (h *Handler) deny(w http.ResponseWriter, r *http.Request, status int, reason collectorgateway.Refusal, rec *collectorcredential.Record, tokenHash string) {
	attrs := []any{
		"reason", string(reason),
		"status", status,
		"remote_addr", r.RemoteAddr,
	}
	if tokenHash != "" {
		attrs = append(attrs, "fingerprint", collectorcredential.FingerprintOfHash(tokenHash))
	}
	if rec != nil {
		attrs = append(attrs,
			"collector_id", rec.Binding.CollectorID,
			"credential_id", rec.CredentialID,
			"connector_uid", rec.Binding.ConnectorUID,
			"state", string(rec.State),
		)
	}
	h.log().Warn("refused an OpAMP connection", attrs...)

	if status == http.StatusUnauthorized {
		// RFC 7235: a 401 carries a challenge. Bare scheme and realm — a challenge is not a place to
		// explain which of the checks failed.
		w.Header().Set("WWW-Authenticate", `Bearer realm="odigos-opamp"`)
	}
	w.WriteHeader(status)
}

func (h *Handler) log() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}
