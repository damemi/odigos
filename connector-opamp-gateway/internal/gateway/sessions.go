package gateway

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorgateway"
)

// Session is one open, authenticated Collector connection this replica is holding.
type Session struct {
	id       uint64
	Identity collectorgateway.Identity
	OpenedAt time.Time
	cancel   context.CancelFunc
}

// Sessions is the set of connections a gateway replica currently holds open.
//
// It exists because authenticating the handshake is not enough. An OpAMP session is long-lived: once
// upgraded, it stays open for days and keeps receiving configuration pushes — which carry the
// customer's destination credential (§6.3). A revocation that only takes effect on the next handshake
// would leave a compromised credential's holder connected for as long as it avoided reconnecting,
// which is to say indefinitely. Closing open sessions is what makes revocation mean anything.
//
// Each replica holds its own sessions; a Collector whose session is closed reconnects through whatever
// replica the edge picks and is refused there too, because the refusal is in the shared store.
type Sessions struct {
	mu   sync.Mutex
	next uint64
	open map[uint64]*Session
}

func NewSessions() *Sessions { return &Sessions{open: map[uint64]*Session{}} }

// Add registers a connection and returns its handle. cancel tears the proxied connection down.
func (s *Sessions) Add(id collectorgateway.Identity, cancel context.CancelFunc) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	sess := &Session{id: s.next, Identity: id, OpenedAt: time.Now(), cancel: cancel}
	s.open[sess.id] = sess
	return sess
}

// Remove deregisters a connection that ended on its own.
func (s *Sessions) Remove(sess *Session) {
	if sess == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.open, sess.id)
}

// Len is the number of open sessions.
func (s *Sessions) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.open)
}

// Snapshot copies the open set so a sweep can do I/O without holding the lock.
func (s *Sessions) Snapshot() []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Session, 0, len(s.open))
	for _, sess := range s.open {
		out = append(out, sess)
	}
	return out
}

// Close tears down one session. Removing it is left to the handler's deferred Remove, so a session is
// deregistered exactly once by whoever owns it.
func (s *Sessions) Close(sess *Session) {
	if sess != nil && sess.cancel != nil {
		sess.cancel()
	}
}

// CloseAll tears down every open session. Used on shutdown: a gateway replica terminating holds
// WebSockets that would otherwise keep http.Server.Shutdown waiting until its timeout, and every one
// of those Collectors reconnects through another replica in the meantime. Dropping them deliberately
// is faster for the fleet than being killed mid-drain.
func (s *Sessions) CloseAll() int {
	open := s.Snapshot()
	for _, sess := range open {
		s.Close(sess)
	}
	// The count of sessions signalled, not of sessions gone: each is deregistered by the handler that
	// owns it, once its proxy call unwinds.
	return len(open)
}

// DefaultRevalidateInterval bounds how long a revoked credential can keep an already-open session.
//
// It is a trade against load: every sweep is one credential read per DISTINCT credential, not per
// connection, so a fleet of thousands of Collectors is thousands of point lookups a minute. Thirty
// seconds keeps the compromise window short enough to be worth having without making the store
// carry a per-connection query rate.
const DefaultRevalidateInterval = 30 * time.Second

// Revalidator re-checks open sessions and closes the ones whose credential or connector stopped being
// acceptable: revoked, expired, superseded past its drain, or a connector deleted underneath it.
type Revalidator struct {
	Credentials Credentials
	Connectors  Connectors
	Sessions    *Sessions
	Interval    time.Duration
	Log         *slog.Logger
	Now         func() time.Time
}

// Run sweeps until ctx is done.
func (rv *Revalidator) Run(ctx context.Context) {
	interval := rv.Interval
	if interval <= 0 {
		interval = DefaultRevalidateInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rv.Sweep(ctx)
		}
	}
}

// Sweep re-authorizes every open session once and returns how many it closed. Separate from Run so the
// behaviour is testable without waiting on a ticker.
func (rv *Revalidator) Sweep(ctx context.Context) int {
	sessions := rv.Sessions.Snapshot()
	if len(sessions) == 0 {
		return 0
	}

	// A Collector scaled to N replicas opens N sessions on ONE credential (§6.3), so the sweep is keyed
	// by credential rather than by connection. Without this a scale-out multiplies the store's read
	// rate by the replica count for no additional information.
	type key struct{ collectorID, credentialID string }
	verdicts := map[key]collectorgateway.Decision{}

	closed := 0
	for _, sess := range sessions {
		k := key{sess.Identity.Binding.CollectorID, sess.Identity.CredentialID}
		decision, seen := verdicts[k]
		if !seen {
			decision = rv.authorize(ctx, sess.Identity)
			verdicts[k] = decision
		}
		if decision.Accepted() {
			continue
		}
		rv.log().Info("closing an open Collector session",
			"reason", string(decision.Refusal),
			"collector_id", k.collectorID,
			"credential_id", k.credentialID,
			"connector_uid", sess.Identity.Binding.ConnectorUID,
			"open_for", time.Since(sess.OpenedAt).Truncate(time.Second).String(),
		)
		rv.Sessions.Close(sess)
		closed++
	}
	return closed
}

// authorize re-runs the admission decision for an open session.
//
// It calls the same collectorgateway.Authorize the handshake does, passing the row's own hash. That is
// the point: admission and revalidation cannot drift into disagreeing about what an acceptable
// credential is, because there is only one function that decides.
func (rv *Revalidator) authorize(ctx context.Context, id collectorgateway.Identity) collectorgateway.Decision {
	rec, err := rv.Credentials.ByID(ctx, id.Binding.CollectorID, id.CredentialID)
	if err != nil {
		if errors.Is(err, ErrCredentialNotFound) {
			// The row is gone. Nothing deletes a credential in normal operation — revocation sets a
			// state — so this is a purge or a lost database, and an open session on a credential the
			// store no longer knows about is one nothing can revoke.
			return collectorgateway.Decision{
				Outcome: collectorgateway.OutcomeRefuse,
				Refusal: collectorgateway.RefusalUnknownCredential,
			}
		}
		// A store failure must not close working sessions. Leaving them open costs at most one more
		// sweep interval of a revoked credential; closing them would disconnect the entire fleet on a
		// database blip, and every one of them would then fail to reconnect for the same reason.
		rv.log().Warn("could not revalidate a session; leaving it open until the next sweep",
			"collector_id", id.Binding.CollectorID, "credential_id", id.CredentialID, "error", err)
		return collectorgateway.Decision{Outcome: collectorgateway.OutcomeAccept, Identity: id}
	}

	var conn *collectorgateway.Connector
	if c, ok := rv.Connectors.ByUID(rec.Binding.ConnectorUID); ok {
		conn = c
	}

	decision := collectorgateway.Authorize(rec.TokenSHA256, rec, conn, rv.now())

	// Before the watch has listed, "not found" means "not loaded yet", and closing on it would drop
	// every session on any restart. Suppress ONLY the connector-derived verdict: a revoked or expired
	// credential must still close its session while the cache is cold, which is exactly the window a
	// compromise revocation would otherwise be waiting out.
	if decision.Outcome == collectorgateway.OutcomeUnavailable && !rv.Connectors.HasSynced() {
		return collectorgateway.Decision{Outcome: collectorgateway.OutcomeAccept, Identity: id}
	}
	return decision
}

func (rv *Revalidator) now() time.Time {
	if rv.Now != nil {
		return rv.Now()
	}
	return time.Now()
}

func (rv *Revalidator) log() *slog.Logger {
	if rv.Log != nil {
		return rv.Log
	}
	return slog.Default()
}
