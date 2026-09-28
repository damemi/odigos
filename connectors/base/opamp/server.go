package opamp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/open-telemetry/opamp-go/protobufs"
	"github.com/open-telemetry/opamp-go/server"
	"github.com/open-telemetry/opamp-go/server/types"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
	opampendpoint "github.com/odigos-io/odigos-enterprise/connectors/opamp"
)

var opampLog = connlog.Subsystem("opamp")

// DefaultAddr is the connector's OpAMP listener. Reached only from connector-opamp-gateway pods, which
// a NetworkPolicy is what enforces — this port has no authentication of its own, by design: the gateway
// authenticated the credential and the identity it asserts is what this server acts on.
const DefaultAddr = ":4320"

// Collectors is the store surface this server needs. An interface so the decisions can be exercised
// without a database.
type Collectors interface {
	GetManagedCollector(ctx context.Context, key store.CollectorKey) (store.CollectorRecord, error)
	RecordCollectorLiveness(ctx context.Context, key store.CollectorKey, instanceUID string, connectedAt, lastSeenAt *time.Time, agentHealth string) error
	RecordCollectorConfigPush(ctx context.Context, key store.CollectorKey, configHash string, pushedAt time.Time) error
	SetCollectorStatus(ctx context.Context, key store.CollectorKey, status collectorstatus.Status, message string) error
}

// Desired renders what a Collector should be running. Supplied by the reconcile loop, which owns the
// destinations, sizing and transport this depends on.
type Desired interface {
	DesiredConfig(ctx context.Context, key store.CollectorKey) (collectorconfig.Document, error)
}

// Server is the connector's OpAMP endpoint.
type Server struct {
	Self       Self
	Collectors Collectors
	Desired    Desired
	// Addr defaults to DefaultAddr, Path to the OpAMP default the gateway proxies to.
	Addr string
	Path string

	opamp server.OpAMPServer
	http  *http.Server

	mu       sync.Mutex
	sessions map[types.Connection]*session
}

// session is one Collector's connection. Its fields are touched only from that connection's callbacks,
// which opamp-go guarantees are never concurrent for one connection.
type session struct {
	identity collectorgateway.Identity
	key      store.CollectorKey
	// descriptionVerified gates the first push. Nothing is sent until the agent has identified itself
	// and been checked against the binding — otherwise "verify rather than trust" describes a check that
	// happens after the configuration, and its destination credential, have already gone out.
	descriptionVerified bool
	// refused latches a description that disagreed with the binding. Latched rather than re-evaluated
	// because an agent that has once claimed to be another connector's Collector does not get a second
	// chance to answer differently on the next message.
	refused     Refusal
	connectedAt time.Time
	// lastReport retains fields OpAMP is allowed to omit from later messages. In particular, older
	// Supervisors report RemoteConfigStatus when a configuration changes and omit it from periodic
	// heartbeats. Forgetting the last status leaves an applied Collector permanently Deploying after an
	// infrastructure observer temporarily reports a fault, because no later heartbeat can reassert Ready.
	lastReport Report
	// lastSentConfigHash prevents a Supervisor that does not report RemoteConfigStatus from being
	// configured on every heartbeat. It is deliberately scoped to a connection: a reconnect gets one
	// fresh delivery, while a connected agent that missed the first response cannot turn its own
	// heartbeat into a config-push loop.
	lastSentConfigHash string
}

// New builds a server. Start it with Start.
func New(self Self, collectors Collectors, desired Desired) *Server {
	return &Server{
		Self:       self,
		Collectors: collectors,
		Desired:    desired,
		sessions:   map[types.Connection]*session{},
	}
}

// Handler returns the OpAMP HTTP handler, for mounting on a server the caller owns.
func (s *Server) Handler() (http.HandlerFunc, error) {
	s.opamp = server.New(opampLogger{})
	handler, _, err := s.opamp.Attach(server.Settings{
		Callbacks: types.Callbacks{OnConnecting: s.onConnecting},
	})
	if err != nil {
		return nil, fmt.Errorf("attaching the OpAMP server: %w", err)
	}
	return http.HandlerFunc(handler), nil
}

// Start listens until ctx is done.
//
// Started by base.Run only when the connector has a managed-Collector capability: a connector with none
// has no Collector to configure, and a listener nothing connects to is a port to explain rather than a
// feature.
func (s *Server) Start(ctx context.Context) error {
	handler, err := s.Handler()
	if err != nil {
		return err
	}

	addr, path := s.Addr, s.Path
	if addr == "" {
		addr = DefaultAddr
	}
	if path == "" {
		// The path the gateway proxies to, from the same constant the endpoint is validated against.
		path = opampendpoint.DefaultPath
	}

	mux := http.NewServeMux()
	mux.Handle(path, handler)
	s.http = &http.Server{
		Addr:    addr,
		Handler: mux,
		// No read or write timeout: an OpAMP session is long-lived and either would sever it mid-stream.
		// ReadHeaderTimeout still bounds a connection that opens and then stalls.
		ReadHeaderTimeout: 10 * time.Second,
	}

	opampLog.Info("serving the managed-Collector OpAMP endpoint", "addr", addr, "path", path)
	errCh := make(chan error, 1)
	go func() {
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.http.Shutdown(shutdownCtx)
}

// onConnecting admits a connection from the identity the gateway asserted.
//
// The refusal is a status code and nothing else. A Collector cannot act on the difference between "your
// binding names another connector" and "nothing is recorded for your region", and spelling it out to
// whatever is on the other end tells a prober which connector it reached.
func (s *Server) onConnecting(r *http.Request) types.ConnectionResponse {
	identity, refusal := Admit(r.Header, s.Self)
	if refusal != RefusalNone {
		opampLog.Warn("refused an OpAMP connection",
			"reason", string(refusal), "remote_addr", r.RemoteAddr)
		return types.ConnectionResponse{Accept: false, HTTPStatusCode: http.StatusUnauthorized}
	}

	// Region is the Collector identity. The credential's network is host metadata and is validated
	// against the regional record below.
	key := store.CollectorKey{Region: identity.Binding.Region}
	rec, err := s.Collectors.GetManagedCollector(r.Context(), key)
	switch {
	case errors.Is(err, store.ErrCollectorNotFound):
		// Nothing recorded for this region yet — the deployer may not have finished. 503 keeps the
		// Supervisor retrying rather than treating it as a dead credential.
		opampLog.Warn("no Collector recorded for a connecting identity",
			"collector_id", identity.Binding.CollectorID, "region", key.Region, "network", identity.Binding.NetworkID)
		return types.ConnectionResponse{Accept: false, HTTPStatusCode: http.StatusServiceUnavailable}
	case err != nil:
		opampLog.Error("reading the Collector for a connecting identity", "region", key.Region, "error", err)
		return types.ConnectionResponse{Accept: false, HTTPStatusCode: http.StatusServiceUnavailable}
	}

	if refusal := VerifyCollector(identity, &Collector{
		Region:      rec.Key.Region,
		NetworkID:   rec.NetworkID,
		CollectorID: rec.CollectorID,
		ConfigHash:  rec.ConfigHash,
	}); refusal != RefusalNone {
		opampLog.Warn("refused an OpAMP connection",
			"reason", string(refusal), "collector_id", identity.Binding.CollectorID, "network", identity.Binding.NetworkID)
		return types.ConnectionResponse{Accept: false, HTTPStatusCode: http.StatusUnauthorized}
	}

	sess := &session{identity: identity, key: key, connectedAt: time.Now().UTC()}
	return types.ConnectionResponse{
		Accept: true,
		ConnectionCallbacks: types.ConnectionCallbacks{
			OnConnected:       func(ctx context.Context, conn types.Connection) { s.track(conn, sess) },
			OnMessage:         s.onMessage,
			OnConnectionClose: s.untrack,
		},
	}
}

func (s *Server) track(conn types.Connection, sess *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[conn] = sess

	opampLog.Info("managed Collector connected",
		"collector_id", sess.identity.Binding.CollectorID,
		"credential_id", sess.identity.CredentialID,
		"region", sess.key.Region, "network", sess.identity.Binding.NetworkID)
}

func (s *Server) untrack(conn types.Connection) {
	s.mu.Lock()
	sess := s.sessions[conn]
	delete(s.sessions, conn)
	s.mu.Unlock()

	if sess != nil {
		opampLog.Info("managed Collector disconnected",
			"collector_id", sess.identity.Binding.CollectorID, "network", sess.identity.Binding.NetworkID)
	}
}

func (s *Server) session(conn types.Connection) *session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[conn]
}

// onMessage handles one report and decides what to send back.
func (s *Server) onMessage(ctx context.Context, conn types.Connection, msg *protobufs.AgentToServer) *protobufs.ServerToAgent {
	sess := s.session(conn)
	if sess == nil {
		// A message on a connection that was never tracked. Nothing to answer it with.
		return &protobufs.ServerToAgent{InstanceUid: msg.GetInstanceUid()}
	}

	report := ExtractReport(msg)
	if report.RemoteConfigStatus == nil {
		// OpAMP reports are deltas. An omitted RemoteConfigStatus means "unchanged", not "unknown".
		// Retaining it also preserves a rejection until the agent reports a different status, so a
		// healthy heartbeat cannot erase the validation error of the configuration it is still refusing.
		report.RemoteConfigStatus = sess.lastReport.RemoteConfigStatus
	}
	sess.lastReport = report
	s.persistReport(ctx, sess, report)

	// The description gate, before anything is sent. The description arrives after the connection is
	// established and is written entirely by the agent, so it can only confirm — but until it has
	// confirmed, this connection has not shown it is the Collector the binding describes, and the
	// configuration it would be given carries the customer's destination credential.
	if sess.refused == RefusalNone && report.Description != nil {
		if refusal := VerifyDescription(sess.identity, report.Description); refusal != RefusalNone {
			sess.refused = refusal
			opampLog.Warn("refused a managed Collector whose description disagrees with its binding",
				"reason", string(refusal),
				"collector_id", sess.identity.Binding.CollectorID,
				"connector_uid", sess.identity.Binding.ConnectorUID,
				"network", sess.identity.Binding.NetworkID)
			s.setStatus(ctx, sess, collectorstatus.StatusError, descriptionRefusalMessage(refusal))
		} else {
			sess.descriptionVerified = true
		}
	}

	if sess.refused != RefusalNone {
		// Answered with the protocol's own error response and NOT disconnected. Dropping the connection
		// here would drop this message with it — opamp-go sends the response after this callback returns
		// — and the message is the only thing that tells whoever deployed this Collector what is wrong.
		// The session is worth nothing to the agent regardless: the refusal is latched, so no
		// configuration is ever sent on it.
		return &protobufs.ServerToAgent{
			InstanceUid: msg.GetInstanceUid(),
			ErrorResponse: &protobufs.ServerErrorResponse{
				Type:         protobufs.ServerErrorResponseType_ServerErrorResponseType_BadRequest,
				ErrorMessage: descriptionRefusalMessage(sess.refused),
			},
		}
	}
	if !sess.descriptionVerified {
		// Waiting for the agent to identify itself. Not an error: the first message may legitimately
		// carry no description, and the next one will.
		return &protobufs.ServerToAgent{InstanceUid: msg.GetInstanceUid()}
	}

	return s.respondWithConfig(ctx, sess, msg, report)
}

// respondWithConfig sends the desired configuration when, and only when, it differs from what the agent
// is running.
func (s *Server) respondWithConfig(ctx context.Context, sess *session, msg *protobufs.AgentToServer, report Report) *protobufs.ServerToAgent {
	out := &protobufs.ServerToAgent{InstanceUid: msg.GetInstanceUid()}

	doc, err := s.Desired.DesiredConfig(ctx, sess.key)
	if err != nil {
		opampLog.Error("rendering the desired configuration for a Collector",
			"region", sess.key.Region, "error", err)
		s.setStatus(ctx, sess, collectorstatus.StatusError,
			"could not render this Collector's configuration: "+err.Error())
		return out
	}

	// Some Supervisor versions advertise ReportsRemoteConfig but omit RemoteConfigStatus from their
	// periodic AgentToServer messages. The protocol decision correctly asks for a first push in that
	// case, but repeating it on every heartbeat continually flips the reported state to Deploying.
	// A delivery is enough for this connection; a new connection deliberately gets one retry.
	if remoteConfigHash(report.RemoteConfigStatus) == "" && sess.lastSentConfigHash == doc.Hash {
		opampLog.Debug("waiting for a Collector configuration acknowledgement",
			"collector_id", sess.identity.Binding.CollectorID,
			"network", sess.identity.Binding.NetworkID,
			"hash", doc.Hash)
		return out
	}

	push := DecidePush(doc.Hash, report.RemoteConfigStatus)
	if !push.Send {
		s.setStatus(ctx, sess, holdStatus(push, report), holdMessage(push, report))
		return out
	}

	out.RemoteConfig = &protobufs.AgentRemoteConfig{
		Config: &protobufs.AgentConfigMap{
			// The empty key is OpAMP's convention for a single unnamed document, which is what the
			// Supervisor merges as $REMOTE_CONFIG.
			ConfigMap: map[string]*protobufs.AgentConfigFile{
				"": {Body: []byte(doc.YAML), ContentType: "text/yaml"},
			},
		},
		ConfigHash: []byte(doc.Hash),
	}

	opampLog.Info("pushing a Collector configuration",
		"collector_id", sess.identity.Binding.CollectorID,
		"region", sess.key.Region,
		"network", sess.identity.Binding.NetworkID,
		"reason", string(push.Reason),
		"hash", push.Hash)

	// Recorded as pushed at the moment it is sent, not when it is acknowledged: this is what the next
	// reconcile compares against, and a hash recorded only on success would re-send on every message
	// until the agent replied.
	if err := s.Collectors.RecordCollectorConfigPush(ctx, sess.key, doc.Hash, time.Now().UTC()); err != nil {
		opampLog.Warn("could not record a configuration push", "region", sess.key.Region, "error", err)
	}
	sess.lastSentConfigHash = doc.Hash
	s.setStatus(ctx, sess, collectorstatus.StatusDeploying, "applying a new configuration")
	return out
}

func remoteConfigHash(status *protobufs.RemoteConfigStatus) string {
	if status == nil {
		return ""
	}
	return string(status.GetLastRemoteConfigHash())
}

// persistReport records what the Collector said about itself.
//
// Best-effort: a report that could not be written is a gap in reporting, and failing the message
// exchange over it would cost the Collector its configuration plane as well.
func (s *Server) persistReport(ctx context.Context, sess *session, report Report) {
	now := time.Now().UTC()
	var connectedAt *time.Time
	if !sess.connectedAt.IsZero() {
		connectedAt = &sess.connectedAt
	}
	if err := s.Collectors.RecordCollectorLiveness(ctx, sess.key,
		report.InstanceUID, connectedAt, &now, report.AgentHealth); err != nil {
		opampLog.Warn("could not record Collector liveness", "region", sess.key.Region, "error", err)
	}
}

func (s *Server) setStatus(ctx context.Context, sess *session, status collectorstatus.Status, message string) {
	if err := s.Collectors.SetCollectorStatus(ctx, sess.key, status, message); err != nil {
		opampLog.Warn("could not record Collector status", "region", sess.key.Region, "error", err)
	}
}

// holdStatus maps a decision not to push onto the Collector's reported state.
func holdStatus(push Push, report Report) collectorstatus.Status {
	switch push.Reason {
	case HoldRejected:
		// The Collector is running its last good configuration, not the one Odigos wants — working, in a
		// posture the customer has to be told about.
		return collectorstatus.StatusDegraded
	case HoldApplying:
		return collectorstatus.StatusDeploying
	case HoldNothingRendered:
		return collectorstatus.StatusPending
	}
	if !report.Healthy && report.AgentHealth != "" {
		return collectorstatus.StatusDegraded
	}
	return collectorstatus.StatusReady
}

// holdMessage explains it, carrying the agent's rejection VERBATIM: it comes from the Collector's own
// configuration validation, and paraphrasing a parser error loses the line number that makes it
// actionable.
func holdMessage(push Push, report Report) string {
	switch push.Reason {
	case HoldRejected:
		return "the Collector rejected this configuration and is still running the last one that " +
			"applied cleanly. It reported: " + push.Error
	case HoldApplying:
		return "the Collector is applying a new configuration"
	case HoldNothingRendered:
		return "no configuration has been rendered for this Collector yet"
	}
	if report.AgentHealth != "" {
		return report.AgentHealth
	}
	return "running the current configuration"
}

func descriptionRefusalMessage(refusal Refusal) string {
	if refusal == RefusalDescriptionMissing {
		return "this Collector reported no " + AttrConnectorID + " attribute, so Odigos cannot confirm it " +
			"is the Collector its credential describes"
	}
	return "this Collector reports a different " + AttrConnectorID + " than its credential is bound to, " +
		"so it is not the Collector Odigos would be configuring"
}

// opampLogger adapts the connector's logger to opamp-go's.
type opampLogger struct{}

func (opampLogger) Debugf(_ context.Context, format string, v ...any) {
	opampLog.Debug(fmt.Sprintf(format, v...))
}

func (opampLogger) Errorf(_ context.Context, format string, v ...any) {
	opampLog.Error(fmt.Sprintf(format, v...))
}
