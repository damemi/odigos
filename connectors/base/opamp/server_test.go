package opamp

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	opampclient "github.com/open-telemetry/opamp-go/client"
	clienttypes "github.com/open-telemetry/opamp-go/client/types"
	"github.com/open-telemetry/opamp-go/protobufs"
	servertypes "github.com/open-telemetry/opamp-go/server/types"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectormetrics"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
)

const desiredYAML = "receivers:\n    otlp: {}\n"

func testKey() store.CollectorKey {
	return store.CollectorKey{Region: testRegion}
}

// fakeCollectors stands in for the managed_collectors accessors.
type fakeCollectors struct {
	mu sync.Mutex

	record   *store.CollectorRecord
	getErr   error
	liveness []livenessWrite
	pushes   []pushWrite
	applied  []string
	statuses []statusWrite
	metrics  []collectormetrics.Record
	// firstReady records every latch write, so a test can assert both that becoming ready opens the
	// cutover gate and that nothing else does.
	firstReady []time.Time
	activated  []string
	readyErr   error
}

func (f *fakeCollectors) RecordCollectorCredentialConnected(_ context.Context, collectorID, credentialID string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.activated = append(f.activated, collectorID+"/"+credentialID)
	return nil
}

func TestConnectedSessionRecordsCredentialActivation(t *testing.T) {
	f := &fakeCollectors{}
	s := &Server{Collectors: f, sessions: make(map[servertypes.Connection]*session)}
	s.track(context.Background(), nil, &session{identity: collectorgateway.Identity{
		Binding: collectorcredential.Binding{CollectorID: "collector-1"}, CredentialID: "credential-2",
	}})
	if len(f.activated) != 1 || f.activated[0] != "collector-1/credential-2" {
		t.Fatalf("credential activation = %v", f.activated)
	}
}

func (f *fakeCollectors) RecordCollectorFirstReady(_ context.Context, _ store.CollectorKey, readyAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readyErr != nil {
		return f.readyErr
	}
	f.firstReady = append(f.firstReady, readyAt)
	return nil
}

func (f *fakeCollectors) readyLatches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.firstReady)
}

type livenessWrite struct {
	instanceUID string
	agentHealth string
}
type pushWrite struct{ hash string }
type statusWrite struct {
	status  collectorstatus.Status
	message string
}

func newFakeCollectors() *fakeCollectors {
	return &fakeCollectors{record: &store.CollectorRecord{
		Key:         testKey(),
		NetworkID:   testNetwork,
		Kind:        "aws.fargate-collector",
		Mode:        "automatic",
		Origin:      "odigos",
		CollectorID: testCollectorID,
	}}
}

func (f *fakeCollectors) GetManagedCollector(_ context.Context, key store.CollectorKey) (store.CollectorRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return store.CollectorRecord{}, f.getErr
	}
	if f.record == nil || f.record.Key != key {
		return store.CollectorRecord{}, store.ErrCollectorNotFound
	}
	return *f.record, nil
}

func (f *fakeCollectors) RecordCollectorLiveness(_ context.Context, _ store.CollectorKey, instanceUID string, _, _ *time.Time, agentHealth string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.liveness = append(f.liveness, livenessWrite{instanceUID, agentHealth})
	return nil
}

func (f *fakeCollectors) RecordCollectorConfigPush(_ context.Context, _ store.CollectorKey, configHash string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushes = append(f.pushes, pushWrite{configHash})
	return nil
}

func (f *fakeCollectors) RecordCollectorConfigApplied(_ context.Context, _ store.CollectorKey, configHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied = append(f.applied, configHash)
	return nil
}

func (f *fakeCollectors) RecordCollectorMetrics(_ context.Context, _ store.CollectorKey, snapshot collectormetrics.Snapshot, observedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metrics = append(f.metrics, collectormetrics.Record{Snapshot: snapshot, ObservedAt: observedAt})
	return nil
}

func (f *fakeCollectors) SetCollectorStatus(_ context.Context, _ store.CollectorKey, status collectorstatus.Status, message string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, statusWrite{status, message})
	return nil
}

func (f *fakeCollectors) snapshot() ([]livenessWrite, []pushWrite, []statusWrite) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]livenessWrite{}, f.liveness...),
		append([]pushWrite{}, f.pushes...),
		append([]statusWrite{}, f.statuses...)
}

func (f *fakeCollectors) appliedHashes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.applied...)
}

// fakeDesired stands in for the reconcile loop's configuration source.
type fakeDesired struct {
	mu  sync.Mutex
	doc collectorconfig.Document
	err error
}

func newFakeDesired() *fakeDesired {
	doc, problems := collectorconfig.Render(collectorconfig.Input{MemoryMiB: 2048})
	if collectorstatus.HasError(problems) {
		panic(problems)
	}
	return &fakeDesired{doc: doc}
}

func (f *fakeDesired) DesiredConfig(context.Context, store.CollectorKey) (collectorconfig.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.doc, f.err
}

func (f *fakeDesired) set(doc collectorconfig.Document, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.doc, f.err = doc, err
}

// serve mounts the OpAMP server on a real HTTP server so a real client can connect.
func serve(t *testing.T, s *Server) string {
	t.Helper()
	handler, err := s.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/v1/opamp", handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/opamp"
}

// agent is a real OpAMP client standing in for the managed Collector's Supervisor.
type agent struct {
	client   opampclient.OpAMPClient
	mu       sync.Mutex
	received []*protobufs.AgentRemoteConfig
	errors   []*protobufs.ServerErrorResponse
}

// connect dials the server the way the Supervisor does: the gateway's asserted identity on the
// handshake, and the description reported once connected.
func connect(t *testing.T, url string, header http.Header, desc map[string]string) *agent {
	t.Helper()

	a := &agent{client: opampclient.NewWebSocket(nil)}
	// The Supervisor reports health, and opamp-go refuses to start with the capability declared and no
	// health set — so this mirrors what a real managed Collector does.
	if err := a.client.SetHealth(&protobufs.ComponentHealth{Healthy: true, Status: "Running"}); err != nil {
		t.Fatalf("SetHealth: %v", err)
	}
	settings := clienttypes.StartSettings{
		OpAMPServerURL: url,
		Header:         header,
		InstanceUid:    testInstanceUID(),
		Capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsHealth |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsEffectiveConfig,
		Callbacks: clienttypes.Callbacks{
			OnMessage: func(_ context.Context, msg *clienttypes.MessageData) {
				a.mu.Lock()
				defer a.mu.Unlock()
				if msg.RemoteConfig != nil {
					a.received = append(a.received, msg.RemoteConfig)
				}
			},
			OnError: func(_ context.Context, err *protobufs.ServerErrorResponse) {
				a.mu.Lock()
				defer a.mu.Unlock()
				a.errors = append(a.errors, err)
			},
		},
	}
	if desc != nil {
		if err := a.client.SetAgentDescription(description(desc)); err != nil {
			t.Fatalf("SetAgentDescription: %v", err)
		}
	}
	if err := a.client.Start(context.Background(), settings); err != nil {
		t.Fatalf("start the agent: %v", err)
	}
	t.Cleanup(func() { _ = a.client.Stop(context.Background()) })
	return a
}

func (a *agent) configs() []*protobufs.AgentRemoteConfig {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*protobufs.AgentRemoteConfig{}, a.received...)
}

func (a *agent) serverErrors() []*protobufs.ServerErrorResponse {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]*protobufs.ServerErrorResponse{}, a.errors...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// testInstanceUID is a Collector task's OpAMP instance id. A replaced task gets a new one, which is why
// it is recorded but is not the Collector's identity — that is collector_id, which survives replacement.
func testInstanceUID() clienttypes.InstanceUid {
	var uid clienttypes.InstanceUid
	for i := range uid {
		uid[i] = byte(i + 1)
	}
	return uid
}

func supervisorDescription() map[string]string {
	return map[string]string{
		AttrConnectorID:   testConnectorUID,
		AttrCloudProvider: "aws",
		AttrServiceName:   ServiceNameManagedCollector,
	}
}

// TestConfigurationReachesTheCollector is the happy path end to end, over a real OpAMP WebSocket with a
// real client: the identity the gateway asserted, the description verified against it, and the rendered
// configuration delivered with its hash.
func TestConfigurationReachesTheCollector(t *testing.T) {
	collectors, desired := newFakeCollectors(), newFakeDesired()
	url := serve(t, New(self(), collectors, desired))

	a := connect(t, url, headersFor(identity(nil)), supervisorDescription())
	waitFor(t, "the configuration to arrive", func() bool { return len(a.configs()) > 0 })

	got := a.configs()[0]
	if string(got.GetConfigHash()) != desired.doc.Hash {
		t.Errorf("config hash = %q, want %q", got.GetConfigHash(), desired.doc.Hash)
	}
	body := string(got.GetConfig().GetConfigMap()[""].GetBody())
	if body != desired.doc.YAML {
		t.Errorf("the Collector received a different document than was rendered:\n%s", body)
	}

	// Reports persist: the liveness write is what the reporting surface and the deferred scaling read.
	waitFor(t, "liveness to be recorded", func() bool {
		liveness, _, _ := collectors.snapshot()
		return len(liveness) > 0
	})
	_, pushes, statuses := collectors.snapshot()
	if len(pushes) == 0 || pushes[0].hash != desired.doc.Hash {
		t.Errorf("the push was not recorded: %+v", pushes)
	}
	if len(statuses) == 0 {
		t.Error("no status was recorded")
	}
}

// TestSteadyStateSendsNothing: an agent that reconnects already running the desired configuration is
// left alone. Pushing here would be a write per reconnect for no change.
func TestSteadyStateSendsNothing(t *testing.T) {
	collectors, desired := newFakeCollectors(), newFakeDesired()
	url := serve(t, New(self(), collectors, desired))

	a := &agent{client: opampclient.NewWebSocket(nil)}
	if err := a.client.SetAgentDescription(description(supervisorDescription())); err != nil {
		t.Fatal(err)
	}
	err := a.client.Start(context.Background(), clienttypes.StartSettings{
		OpAMPServerURL: url,
		Header:         headersFor(identity(nil)),
		InstanceUid:    testInstanceUID(),
		Capabilities: protobufs.AgentCapabilities_AgentCapabilities_AcceptsRemoteConfig |
			protobufs.AgentCapabilities_AgentCapabilities_ReportsRemoteConfig,
		// Already applied the desired configuration, which is what a reconnecting Collector reports.
		RemoteConfigStatus: &protobufs.RemoteConfigStatus{
			LastRemoteConfigHash: []byte(desired.doc.Hash),
			Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		},
		Callbacks: clienttypes.Callbacks{
			OnMessage: func(_ context.Context, msg *clienttypes.MessageData) {
				a.mu.Lock()
				defer a.mu.Unlock()
				if msg.RemoteConfig != nil {
					a.received = append(a.received, msg.RemoteConfig)
				}
			},
		},
	})
	if err != nil {
		t.Fatalf("start the agent: %v", err)
	}
	t.Cleanup(func() { _ = a.client.Stop(context.Background()) })

	// Wait for the exchange to have happened at all, then assert nothing was pushed.
	waitFor(t, "a status to be recorded", func() bool {
		_, _, statuses := collectors.snapshot()
		return len(statuses) > 0
	})
	if configs := a.configs(); len(configs) != 0 {
		t.Errorf("a Collector already running the desired configuration was sent %d more", len(configs))
	}
	if _, pushes, _ := collectors.snapshot(); len(pushes) != 0 {
		t.Errorf("a push was recorded for a Collector that needed none: %+v", pushes)
	}
	if applied := collectors.appliedHashes(); len(applied) == 0 || applied[len(applied)-1] != desired.doc.Hash {
		t.Errorf("the applied configuration was not persisted: %+v", applied)
	}
	_, _, statuses := collectors.snapshot()
	if last := statuses[len(statuses)-1]; last.status != collectorstatus.StatusReady {
		t.Errorf("status = %q, want %q", last.status, collectorstatus.StatusReady)
	}
}

// TestDescriptionMismatchIsRefusedOverTheWire: an agent claiming another connector is told so and
// disconnected, and never sees a configuration — which carries the customer's destination credential.
func TestDescriptionMismatchIsRefusedOverTheWire(t *testing.T) {
	collectors, desired := newFakeCollectors(), newFakeDesired()
	url := serve(t, New(self(), collectors, desired))

	a := connect(t, url, headersFor(identity(nil)), map[string]string{
		AttrConnectorID:   "uid-someone-else",
		AttrCloudProvider: "aws",
	})

	waitFor(t, "the refusal to arrive", func() bool { return len(a.serverErrors()) > 0 })
	if msg := a.serverErrors()[0].GetErrorMessage(); !strings.Contains(msg, AttrConnectorID) {
		t.Errorf("the refusal does not name the attribute that disagreed: %s", msg)
	}
	if configs := a.configs(); len(configs) != 0 {
		t.Fatalf("a Collector claiming another connector was sent a configuration")
	}

	_, _, statuses := collectors.snapshot()
	if len(statuses) == 0 || statuses[len(statuses)-1].status != collectorstatus.StatusError {
		t.Errorf("statuses = %+v, want an Error", statuses)
	}
}

// TestUnknownIdentityIsRefusedAtTheHandshake: without the gateway's asserted identity there is nothing
// to route on, and this port has no authentication of its own — the NetworkPolicy is the boundary.
func TestUnknownIdentityIsRefusedAtTheHandshake(t *testing.T) {
	s := New(self(), newFakeCollectors(), newFakeDesired())

	// Exercised directly rather than over a socket: opamp-go's client retries a rejected handshake, so
	// the assertion that matters is the response the server produces for one.
	for name, h := range map[string]http.Header{
		"no identity": {},
		"a binding naming another connector": headersFor(
			identity(func(i *collectorgateway.Identity) { i.Binding.ConnectorUID = "uid-someone-else" })),
	} {
		t.Run(name, func(t *testing.T) {
			resp := s.onConnecting(&http.Request{Header: h, RemoteAddr: "10.0.0.1:1234"})
			if resp.Accept {
				t.Fatal("the connection was accepted")
			}
			if resp.HTTPStatusCode != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", resp.HTTPStatusCode)
			}
		})
	}
}

// TestUnrecordedCollectorIsToldToRetry: the deployer may not have recorded the row yet. 503 keeps the
// Supervisor retrying rather than treating it as a dead credential.
func TestUnrecordedCollectorIsToldToRetry(t *testing.T) {
	collectors := newFakeCollectors()
	collectors.record = nil
	s := New(self(), collectors, newFakeDesired())

	resp := s.onConnecting(&http.Request{Header: headersFor(identity(nil))})
	if resp.Accept {
		t.Fatal("a Collector with no recorded row was accepted")
	}
	if resp.HTTPStatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.HTTPStatusCode)
	}

	// A store failure is likewise not an authentication answer.
	collectors.record, collectors.getErr = nil, errors.New("connection refused")
	if resp := s.onConnecting(&http.Request{Header: headersFor(identity(nil))}); resp.HTTPStatusCode != http.StatusServiceUnavailable {
		t.Errorf("a store outage answered %d, want 503", resp.HTTPStatusCode)
	}
}

// TestRecordedCollectorMismatchIsRefused: two Collectors claiming one network, where serving either the
// other's configuration is worse than serving neither.
func TestRecordedCollectorMismatchIsRefused(t *testing.T) {
	collectors := newFakeCollectors()
	collectors.record.CollectorID = "col_someone-else"
	s := New(self(), collectors, newFakeDesired())

	resp := s.onConnecting(&http.Request{Header: headersFor(identity(nil))})
	if resp.Accept || resp.HTTPStatusCode != http.StatusUnauthorized {
		t.Errorf("accept = %v status = %d, want refused with 401", resp.Accept, resp.HTTPStatusCode)
	}
}

// stubConn is a types.Connection for driving the callbacks directly. The interface needs a comparable
// type because opamp-go keys sessions by it.
type stubConn struct{ id int }

func (stubConn) Connection() net.Conn                                 { return nil }
func (stubConn) Send(context.Context, *protobufs.ServerToAgent) error { return nil }
func (stubConn) Disconnect() error                                    { return nil }

// exchange drives one connection's callbacks the way opamp-go would, and returns what the server
// answered. Direct rather than over a socket so a message shape a real client will not produce — a first
// message with no description — can still be exercised.
func exchange(t *testing.T, s *Server, header http.Header, messages ...*protobufs.AgentToServer) []*protobufs.ServerToAgent {
	t.Helper()

	resp := s.onConnecting(&http.Request{Header: header})
	if !resp.Accept {
		t.Fatalf("the connection was refused with %d", resp.HTTPStatusCode)
	}
	conn := stubConn{id: 1}
	resp.ConnectionCallbacks.OnConnected(context.Background(), conn)
	defer resp.ConnectionCallbacks.OnConnectionClose(conn)

	out := make([]*protobufs.ServerToAgent, 0, len(messages))
	for _, msg := range messages {
		out = append(out, resp.ConnectionCallbacks.OnMessage(context.Background(), conn, msg))
	}
	return out
}

// TestNoConfigurationBeforeTheDescriptionIsVerified is the "verify rather than trust" property at its
// sharpest: an agent that has not identified itself gets nothing.
//
// The description arrives after the connection is established, so there is a window in which the server
// knows only what the gateway asserted. Sending a configuration in that window would hand out the
// customer's destination credential before the thing on the other end had shown it is the Collector the
// binding describes — which makes the check that follows decorative.
func TestNoConfigurationBeforeTheDescriptionIsVerified(t *testing.T) {
	collectors, desired := newFakeCollectors(), newFakeDesired()
	s := New(self(), collectors, desired)

	// First message carries health but no description — nothing may be sent.
	first := &protobufs.AgentToServer{
		InstanceUid: []byte{0x01},
		Health:      &protobufs.ComponentHealth{Healthy: true},
	}
	// Second carries the description, and only then is the configuration due.
	second := &protobufs.AgentToServer{
		InstanceUid:      []byte{0x01},
		AgentDescription: description(supervisorDescription()),
	}

	answers := exchange(t, s, headersFor(identity(nil)), first, second)

	if answers[0].GetRemoteConfig() != nil {
		t.Error("a configuration was sent to an agent that had not identified itself")
	}
	if answers[0].GetErrorResponse() != nil {
		t.Error("a missing description was answered as an error; the first message may legitimately carry none")
	}
	if answers[1].GetRemoteConfig() == nil {
		t.Fatal("no configuration was sent after the description was verified")
	}
	if string(answers[1].GetRemoteConfig().GetConfigHash()) != desired.doc.Hash {
		t.Errorf("config hash = %q, want %q", answers[1].GetRemoteConfig().GetConfigHash(), desired.doc.Hash)
	}

	// Liveness is recorded from the very first message, before verification. Reporting that a Collector
	// is connected is not the same as configuring it, and an agent stuck without a description is
	// exactly the thing an operator needs to be able to see.
	liveness, _, _ := collectors.snapshot()
	if len(liveness) < 2 {
		t.Errorf("liveness writes = %d, want one per message", len(liveness))
	}
}

// TestAReadyCollectorReconnectsWithoutRepeatingItsDescription covers OpAMP delta state across a
// connector restart. The same Supervisor process reconnects through the gateway, but does not repeat a
// description that did not change. FirstReady proves that this Collector already passed the gate; the
// handshake still re-verifies its credential binding before the session is admitted.
func TestAReadyCollectorReconnectsWithoutRepeatingItsDescription(t *testing.T) {
	collectors, desired := newFakeCollectors(), newFakeDesired()
	readyAt := time.Now().Add(-time.Minute)
	collectors.record.FirstReadyAt = &readyAt
	collectors.record.AppliedConfigHash = desired.doc.Hash
	s := New(self(), collectors, desired)
	uid := testInstanceUID()

	answers := exchange(t, s, headersFor(identity(nil)), &protobufs.AgentToServer{
		InstanceUid: uid[:],
		Health:      &protobufs.ComponentHealth{Healthy: true, Status: "StatusOK"},
	})

	if answers[0].GetRemoteConfig() != nil {
		t.Fatal("an already-applied configuration was unnecessarily sent again after reconnect")
	}
	_, _, statuses := collectors.snapshot()
	if len(statuses) == 0 || statuses[len(statuses)-1].status != collectorstatus.StatusReady {
		t.Fatalf("statuses = %+v, want the reconnected Collector Ready after acknowledging config", statuses)
	}
}

// TestMissingRemoteConfigStatusDoesNotPushOnEveryHeartbeat covers older Supervisor builds that
// advertise ReportsRemoteConfig but omit the status from their periodic reports. They still need a
// delivery on each new connection, but sending the same document per heartbeat keeps the Collector
// permanently Deploying and starves the data plane with needless config writes.
func TestMissingRemoteConfigStatusDoesNotPushOnEveryHeartbeat(t *testing.T) {
	collectors, desired := newFakeCollectors(), newFakeDesired()
	s := New(self(), collectors, desired)

	heartbeat := &protobufs.AgentToServer{
		AgentDescription: description(supervisorDescription()),
		Health:           &protobufs.ComponentHealth{Healthy: true, Status: "StatusOK"},
	}
	answers := exchange(t, s, headersFor(identity(nil)), heartbeat, heartbeat, heartbeat)

	if answers[0].GetRemoteConfig() == nil {
		t.Fatal("the first report did not receive a configuration")
	}
	for i, answer := range answers[1:] {
		if answer.GetRemoteConfig() != nil {
			t.Errorf("heartbeat %d re-sent the same configuration without an acknowledgement", i+2)
		}
	}

	_, pushes, _ := collectors.snapshot()
	if len(pushes) != 1 {
		t.Errorf("configuration pushes = %d, want 1", len(pushes))
	}
}

// TestAnOmittedStatusMeansUnchanged covers OpAMP's delta semantics. Once the Supervisor has said a
// configuration is applied (or rejected), later health-only heartbeats must keep that verdict. If the
// server forgets it, an infrastructure warning can overwrite Ready and remain stuck forever even after
// AWS recovers, because no subsequent heartbeat knows it should reassert Ready.
func TestAnOmittedStatusMeansUnchanged(t *testing.T) {
	for name, state := range map[string]protobufs.RemoteConfigStatuses{
		"applied":  protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED,
		"rejected": protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
	} {
		t.Run(name, func(t *testing.T) {
			collectors, desired := newFakeCollectors(), newFakeDesired()
			s := New(self(), collectors, desired)
			first := &protobufs.AgentToServer{
				AgentDescription: description(supervisorDescription()),
				Health:           &protobufs.ComponentHealth{Healthy: true, Status: "StatusOK"},
				RemoteConfigStatus: &protobufs.RemoteConfigStatus{
					LastRemoteConfigHash: []byte(desired.doc.Hash),
					Status:               state,
					ErrorMessage:         "bad exporter",
				},
			}
			heartbeat := &protobufs.AgentToServer{
				Health: &protobufs.ComponentHealth{Healthy: true, Status: "StatusOK"},
			}

			exchange(t, s, headersFor(identity(nil)), first, heartbeat)
			_, _, statuses := collectors.snapshot()
			if len(statuses) < 2 {
				t.Fatalf("status writes = %d, want one for the report and one for the heartbeat", len(statuses))
			}
			want := collectorstatus.StatusReady
			if state == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED {
				want = collectorstatus.StatusDegraded
			}
			if got := statuses[len(statuses)-1].status; got != want {
				t.Errorf("heartbeat status = %q, want retained %q", got, want)
			}
		})
	}
}

// TestARefusedDescriptionIsLatched: an agent that has once claimed to be another connector's Collector
// does not get a second chance to answer differently on the next message.
func TestARefusedDescriptionIsLatched(t *testing.T) {
	s := New(self(), newFakeCollectors(), newFakeDesired())

	answers := exchange(t, s, headersFor(identity(nil)),
		&protobufs.AgentToServer{AgentDescription: description(map[string]string{AttrConnectorID: "uid-someone-else"})},
		// Now telling the truth. Too late.
		&protobufs.AgentToServer{AgentDescription: description(supervisorDescription())},
	)

	for i, answer := range answers {
		if answer.GetRemoteConfig() != nil {
			t.Errorf("message %d was answered with a configuration", i)
		}
		if answer.GetErrorResponse() == nil {
			t.Errorf("message %d was not answered with the refusal", i)
		}
	}
}

// TestRejectedConfigurationIsNotResent, over the callbacks: the agent refuses the configuration, and the
// server neither re-sends it nor reports the Collector as broken — it is running the last configuration
// that applied cleanly, which is what the Supervisor's rollback left it on.
func TestRejectedConfigurationIsNotResent(t *testing.T) {
	collectors, desired := newFakeCollectors(), newFakeDesired()
	s := New(self(), collectors, desired)

	rejection := &protobufs.AgentToServer{
		AgentDescription: description(supervisorDescription()),
		RemoteConfigStatus: &protobufs.RemoteConfigStatus{
			LastRemoteConfigHash: []byte(desired.doc.Hash),
			Status:               protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED,
			ErrorMessage:         `line 12: unknown exporter "otlp/vendor"`,
		},
	}

	answers := exchange(t, s, headersFor(identity(nil)), rejection, rejection, rejection)
	for i, answer := range answers {
		if answer.GetRemoteConfig() != nil {
			t.Errorf("message %d re-sent the configuration the agent had just refused", i)
		}
	}

	_, pushes, statuses := collectors.snapshot()
	if len(pushes) != 0 {
		t.Errorf("a push was recorded for a configuration that was never sent: %+v", pushes)
	}
	last := statuses[len(statuses)-1]
	if last.status != collectorstatus.StatusDegraded {
		t.Errorf("status = %q, want %q — the Collector is working, on the wrong configuration",
			last.status, collectorstatus.StatusDegraded)
	}
	if !strings.Contains(last.message, `unknown exporter "otlp/vendor"`) {
		t.Errorf("the recorded message drops the agent's own words: %s", last.message)
	}

	// A genuinely different configuration is sent normally — that is how the rejection gets fixed.
	fixed, problems := collectorconfig.Render(collectorconfig.Input{MemoryMiB: 4096})
	if collectorstatus.HasError(problems) {
		t.Fatal(problems)
	}
	desired.set(fixed, nil)

	after := exchange(t, s, headersFor(identity(nil)), rejection)
	if after[0].GetRemoteConfig() == nil {
		t.Error("a corrected configuration was withheld because an older one had been rejected")
	}
}

// TestBecomingReadyOpensTheCutoverGate.
//
// This server is the only party that can know a Collector has actually worked — it connected, identified
// itself against its binding, and is running the configuration Odigos wants — and nothing reading the
// infrastructure can see any of that. Until the latch lands, every workload on the network keeps
// exporting straight to the destination.
func TestBecomingReadyOpensTheCutoverGate(t *testing.T) {
	collectors := newFakeCollectors()
	srv := New(self(), collectors, newFakeDesired())

	srv.setStatus(context.Background(), &session{key: testKey()}, collectorstatus.StatusReady, "ready")

	if got := collectors.readyLatches(); got != 1 {
		t.Fatalf("latched %d times on Ready, want once", got)
	}
}

// TestOnlyReadyOpensIt. Every other status is a Collector that has not worked yet, and opening the gate
// on one would re-point a fleet at something that is deploying, degraded or being refused.
func TestOnlyReadyOpensIt(t *testing.T) {
	for _, status := range []collectorstatus.Status{
		collectorstatus.StatusPending,
		collectorstatus.StatusDeploying,
		collectorstatus.StatusDegraded,
		collectorstatus.StatusError,
		collectorstatus.StatusAwaitingCustomer,
	} {
		t.Run(string(status), func(t *testing.T) {
			collectors := newFakeCollectors()
			srv := New(self(), collectors, newFakeDesired())

			srv.setStatus(context.Background(), &session{key: testKey()}, status, "not ready")

			if got := collectors.readyLatches(); got != 0 {
				t.Errorf("status %q latched the cutover gate %d time(s)", status, got)
			}
		})
	}
}

// TestAFailedLatchDoesNotFailTheExchange. A latch that could not be written costs the network a rollout
// for one pass — the next Ready writes it. Failing the message exchange over it would cost the Collector
// its configuration plane, which is a far worse trade for a fact that is about to be true again.
func TestAFailedLatchDoesNotFailTheExchange(t *testing.T) {
	collectors := newFakeCollectors()
	collectors.readyErr = errors.New("the database is unreachable")
	srv := New(self(), collectors, newFakeDesired())

	// setStatus returns nothing and must not panic; the status write still happened.
	srv.setStatus(context.Background(), &session{key: testKey()}, collectorstatus.StatusReady, "ready")

	collectors.mu.Lock()
	wrote := len(collectors.statuses)
	collectors.mu.Unlock()
	if wrote == 0 {
		t.Error("the status was not recorded when the latch failed")
	}
}
