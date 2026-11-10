package opamp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorgateway"
)

const (
	testConnectorUID = "uid-1"
	testCollectorID  = "col_1"
	testRegion       = "us-east-1"
	testNetwork      = "vpc-1"
	testAccount      = "123456789012"
)

func self() Self {
	return Self{ConnectorUID: testConnectorUID, Provider: "aws", AccountID: testAccount}
}

func identity(mutate func(*collectorgateway.Identity)) collectorgateway.Identity {
	id := collectorgateway.Identity{
		CredentialID: "cred_1",
		Binding: collectorcredential.Binding{
			CollectorID:  testCollectorID,
			ConnectorUID: testConnectorUID,
			Provider:     "aws",
			AccountID:    testAccount,
			Region:       testRegion,
			NetworkID:    testNetwork,
		},
	}
	if mutate != nil {
		mutate(&id)
	}
	return id
}

func headersFor(id collectorgateway.Identity) http.Header {
	h := http.Header{}
	id.SetHeaders(h)
	return h
}

func description(attrs map[string]string) *protobufs.AgentDescription {
	desc := &protobufs.AgentDescription{}
	for k, v := range attrs {
		desc.IdentifyingAttributes = append(desc.IdentifyingAttributes, &protobufs.KeyValue{
			Key:   k,
			Value: &protobufs.AnyValue{Value: &protobufs.AnyValue_StringValue{StringValue: v}},
		})
	}
	return desc
}

// TestAdmitRequiresTheBindingToNameThisConnector is the "binding authoritative" criterion at the
// handshake. Every field of the binding was fixed when the credential was issued and the gateway matched
// all of them; this re-checks the ones that say the connection reached the right process, because a
// gateway that routed wrongly would otherwise be handed a configuration carrying another account's
// destination credential.
func TestAdmitRequiresTheBindingToNameThisConnector(t *testing.T) {
	cases := []struct {
		name string
		id   collectorgateway.Identity
		self Self
		want Refusal
	}{
		{name: "this connector's Collector", id: identity(nil), self: self()},
		{
			name: "a binding naming another connector",
			id:   identity(func(i *collectorgateway.Identity) { i.Binding.ConnectorUID = "uid-someone-else" }),
			self: self(), want: RefusalForeignConnector,
		},
		{
			name: "a binding for another cloud",
			id:   identity(func(i *collectorgateway.Identity) { i.Binding.Provider = "azure" }),
			self: self(), want: RefusalProviderMismatch,
		},
		{
			// The cross-account case. Everything else can be right and this still has to refuse.
			name: "a binding for another account",
			id:   identity(func(i *collectorgateway.Identity) { i.Binding.AccountID = "999999999999" }),
			self: self(), want: RefusalAccountMismatch,
		},
		{
			// A connector that does not know its own UID cannot tell its own Collectors from anyone
			// else's, so it admits none.
			name: "a connector with no identity of its own",
			id:   identity(nil), self: Self{Provider: "aws"}, want: RefusalForeignConnector,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, refusal := Admit(headersFor(tc.id), tc.self)
			if refusal != tc.want {
				t.Fatalf("refusal = %q, want %q", refusal, tc.want)
			}
			if tc.want == RefusalNone && got != tc.id {
				t.Errorf("admitted identity = %+v, want %+v", got, tc.id)
			}
			if tc.want != RefusalNone && got != (collectorgateway.Identity{}) {
				t.Errorf("a refused connection carried an identity: %+v", got)
			}
		})
	}
}

// TestAdmitRefusesWithoutAGatewayIdentity: only the gateway can produce one, and a NetworkPolicy admits
// only the gateway to this port. Something arriving without one is a misconfigured deployment or
// something that got past it — either way it gets nothing.
func TestAdmitRefusesWithoutAGatewayIdentity(t *testing.T) {
	for name, h := range map[string]http.Header{
		"no headers at all": {},
		"a partial identity": func() http.Header {
			h := headersFor(identity(nil))
			h.Del(collectorgateway.HeaderNetworkID)
			return h
		}(),
		"no credential id": func() http.Header {
			h := headersFor(identity(nil))
			h.Del(collectorgateway.HeaderCredentialID)
			return h
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			if _, refusal := Admit(h, self()); refusal != RefusalNoIdentity {
				t.Errorf("refusal = %q, want %q", refusal, RefusalNoIdentity)
			}
		})
	}
}

// TestVerifyCollectorMatchesTheRecordedIdentity: the regional record must match both the credential's
// host network and Collector identity before configuration is released.
func TestVerifyCollectorMatchesTheRecordedIdentity(t *testing.T) {
	id := identity(nil)

	if got := VerifyCollector(id, &Collector{NetworkID: testNetwork, CollectorID: testCollectorID}); got != RefusalNone {
		t.Errorf("a matching Collector was refused: %q", got)
	}
	if got := VerifyCollector(id, &Collector{NetworkID: "vpc-other", CollectorID: testCollectorID}); got != RefusalCollectorMismatch {
		t.Errorf("refusal = %q, want %q for a host-network mismatch", got, RefusalCollectorMismatch)
	}
	if got := VerifyCollector(id, &Collector{NetworkID: testNetwork, CollectorID: "col_someone-else"}); got != RefusalCollectorMismatch {
		t.Errorf("refusal = %q, want %q", got, RefusalCollectorMismatch)
	}
	if got := VerifyCollector(id, nil); got != RefusalUnknownCollector {
		t.Errorf("refusal = %q, want %q", got, RefusalUnknownCollector)
	}
	// A regional record without host metadata is malformed and must not admit an arbitrary credential
	// network. The host binding is required even before collector_id is recorded.
	if got := VerifyCollector(id, &Collector{}); got != RefusalCollectorMismatch {
		t.Errorf("a Collector with no recorded host network got refusal %q, want %q", got, RefusalCollectorMismatch)
	}
}

// TestCredentialIsNotReadjudicated: the gateway is the authority on which credentials are live, and
// during a rotation two are — the outgoing one keeps working while the old tasks drain. Refusing a
// credential id that differs from the recorded active one would disconnect exactly the Collector a
// rotation is trying not to disturb.
func TestCredentialIsNotReadjudicated(t *testing.T) {
	outgoing := identity(func(i *collectorgateway.Identity) { i.CredentialID = "cred_outgoing" })
	if got := VerifyCollector(outgoing, &Collector{NetworkID: testNetwork, CollectorID: testCollectorID}); got != RefusalNone {
		t.Errorf("a draining Collector on the outgoing credential was refused: %q", got)
	}
}

// TestDescriptionMismatchRejected is the criterion in its own right. The description arrives after the
// connection is established and every attribute is written by the agent, so it can only confirm — and a
// disagreement means the process on the other end is not the Collector the binding describes.
func TestDescriptionMismatchRejected(t *testing.T) {
	id := identity(nil)

	cases := []struct {
		name string
		desc *protobufs.AgentDescription
		want Refusal
	}{
		{
			name: "agrees with the binding",
			desc: description(map[string]string{
				AttrConnectorID:   testConnectorUID,
				AttrCloudProvider: "aws",
				AttrServiceName:   ServiceNameManagedCollector,
			}),
		},
		{
			name: "the provider is not reported at all",
			desc: description(map[string]string{AttrConnectorID: testConnectorUID}),
		},
		{
			name: "provider casing differs",
			desc: description(map[string]string{AttrConnectorID: testConnectorUID, AttrCloudProvider: "AWS"}),
		},
		{
			// The one that matters: an agent claiming to belong to a different connector.
			name: "claims a different connector",
			desc: description(map[string]string{AttrConnectorID: "uid-someone-else"}),
			want: RefusalDescriptionMismatch,
		},
		{
			// Agrees about the connector and not about the cloud. Two independent statements of the same
			// fact, and an agent that fails one is not the agent the binding describes.
			name: "claims a different cloud",
			desc: description(map[string]string{AttrConnectorID: testConnectorUID, AttrCloudProvider: "gcp"}),
			want: RefusalDescriptionMismatch,
		},
		{name: "reports no connector at all", desc: description(map[string]string{AttrServiceName: "x"}), want: RefusalDescriptionMissing},
		{name: "reports an empty connector", desc: description(map[string]string{AttrConnectorID: ""}), want: RefusalDescriptionMissing},
		{name: "no description yet", desc: nil, want: RefusalDescriptionMissing},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := VerifyDescription(id, tc.desc); got != tc.want {
				t.Errorf("refusal = %q, want %q", got, tc.want)
			}
		})
	}
}

func status(hash string, state protobufs.RemoteConfigStatuses, errMsg string) *protobufs.RemoteConfigStatus {
	return &protobufs.RemoteConfigStatus{
		LastRemoteConfigHash: []byte(hash),
		Status:               state,
		ErrorMessage:         errMsg,
	}
}

// TestPushOnlyOnHashChanges is two criteria at once: steady state is zero pushes, and a rejected
// configuration keeps the last known good.
func TestPushOnlyOnHashChanges(t *testing.T) {
	const desired = "hash-desired"

	cases := []struct {
		name       string
		desired    string
		status     *protobufs.RemoteConfigStatus
		wantSend   bool
		wantReason PushReason
	}{
		{
			name: "first connection, nothing reported", desired: desired, status: nil,
			wantSend: true, wantReason: PushFirstConfig,
		},
		{
			name: "reported an empty hash", desired: desired, status: status("", protobufs.RemoteConfigStatuses_RemoteConfigStatuses_UNSET, ""),
			wantSend: true, wantReason: PushFirstConfig,
		},
		{
			name:    "running something else",
			desired: desired, status: status("hash-old", protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""),
			wantSend: true, wantReason: PushHashChanged,
		},
		{
			// The steady state, and the common case. A reconcile that changes nothing sends nothing.
			name:    "already running the desired configuration",
			desired: desired, status: status(desired, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED, ""),
			wantReason: HoldApplied,
		},
		{
			name:    "mid-apply of the desired configuration",
			desired: desired, status: status(desired, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING, ""),
			wantReason: HoldApplying,
		},
		{
			// Re-sending would loop the agent through the same validation failure for as long as the
			// desired configuration stayed wrong, while the Collector correctly kept running the last
			// one that worked.
			name:       "the agent rejected this exact configuration",
			desired:    desired,
			status:     status(desired, protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "line 12: unknown exporter"),
			wantReason: HoldRejected,
		},
		{
			// A rejection of an OLD configuration is not a reason to withhold a new one — the new one is
			// how the rejection gets fixed.
			name:     "the agent rejected an older configuration",
			desired:  desired,
			status:   status("hash-old", protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "line 12: unknown exporter"),
			wantSend: true, wantReason: PushHashChanged,
		},
		{
			name: "nothing has been rendered", desired: "", status: nil,
			wantReason: HoldNothingRendered,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecidePush(tc.desired, tc.status)
			if got.Send != tc.wantSend {
				t.Errorf("Send = %v, want %v", got.Send, tc.wantSend)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if tc.wantReason == HoldRejected && got.Error == "" {
				t.Error("a rejection carried no message, so nobody can see what the Collector objected to")
			}
		})
	}
}

// TestRejectionMessageIsVerbatim: it comes from the Collector's own configuration validation, and
// paraphrasing a parser error loses the line number that makes it actionable.
func TestRejectionMessageIsVerbatim(t *testing.T) {
	const verbatim = `failed to build pipelines: exporter "otlp" not found in config, line 12`

	push := DecidePush("h", status("h", protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, verbatim))
	if push.Error != verbatim {
		t.Errorf("Error = %q, want the agent's own message %q", push.Error, verbatim)
	}
	if msg := holdMessage(push, Report{}); !strings.Contains(msg, verbatim) {
		t.Errorf("the reported message drops the agent's own words: %s", msg)
	}
	// And it must say the Collector is still running something that works, or a reader assumes it is down.
	if msg := holdMessage(push, Report{}); !strings.Contains(msg, "last one that applied cleanly") {
		t.Errorf("the message does not say the Collector kept its last good configuration: %s", msg)
	}
}

// TestExtractReport reads what a Collector says about itself. OpAMP sends only what changed, so a
// message with no health means "health unchanged", not "unhealthy".
func TestExtractReport(t *testing.T) {
	msg := &protobufs.AgentToServer{
		InstanceUid: []byte{0x01, 0xab, 0xff},
		Health:      &protobufs.ComponentHealth{Healthy: true, Status: "Running"},
		EffectiveConfig: &protobufs.EffectiveConfig{
			ConfigMap: &protobufs.AgentConfigMap{ConfigMap: map[string]*protobufs.AgentConfigFile{
				"b.yaml": {Body: []byte("second")},
				"a.yaml": {Body: []byte("first")},
			}},
		},
		RemoteConfigStatus: status("h", protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED, "bad exporter"),
	}

	got := ExtractReport(msg)
	if got.InstanceUID != "01abff" {
		t.Errorf("InstanceUID = %q, want %q", got.InstanceUID, "01abff")
	}
	if !got.Healthy || got.AgentHealth != "Running" {
		t.Errorf("health = %v/%q", got.Healthy, got.AgentHealth)
	}
	if got.RemoteConfigError != "bad exporter" {
		t.Errorf("RemoteConfigError = %q", got.RemoteConfigError)
	}
	// Multi-file configurations are flattened in a stable order, so two reports of the same state
	// compare equal rather than differing by map iteration.
	if !strings.Contains(got.EffectiveConfig, "# a.yaml\nfirst") ||
		strings.Index(got.EffectiveConfig, "a.yaml") > strings.Index(got.EffectiveConfig, "b.yaml") {
		t.Errorf("effective config is not in a stable order:\n%s", got.EffectiveConfig)
	}

	empty := ExtractReport(&protobufs.AgentToServer{})
	if empty.Healthy || empty.AgentHealth != "" || empty.EffectiveConfig != "" {
		t.Errorf("a message carrying nothing was read as a report: %+v", empty)
	}
}

// TestUnhealthyReportCarriesTheAgentsOwnError: "Unhealthy" alone sends someone to look at the Collector
// with nothing to look for.
func TestUnhealthyReportCarriesTheAgentsOwnError(t *testing.T) {
	got := ExtractReport(&protobufs.AgentToServer{
		Health: &protobufs.ComponentHealth{Healthy: false, LastError: "connection refused to otlp.vendor:4317"},
	})
	if got.Healthy {
		t.Error("an unhealthy report read as healthy")
	}
	if !strings.Contains(got.AgentHealth, "connection refused") {
		t.Errorf("AgentHealth = %q, want the agent's own error", got.AgentHealth)
	}
}
