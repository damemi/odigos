// Package opamp is the connector's OpAMP server: the config plane for the managed Collectors this
// connector owns.
//
// The gateway has already authenticated the connection and asserted an identity (collectorgateway).
// This package decides what that identity is allowed to be told, and what to do with what the Collector
// reports back. Everything in this file is pure — no I/O, no protobuf writing — so the decisions can be
// tested without a socket or a database; server.go is the wiring around them.
package opamp

import (
	"net/http"
	"strings"

	"github.com/open-telemetry/opamp-go/protobufs"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorgateway"
)

// Agent description attributes the managed Supervisor reports (collector/managed/supervisor.yaml).
const (
	// AttrConnectorID is the connector the Collector believes it belongs to. Verified, never trusted.
	AttrConnectorID   = "odigos.cloud.connector.id"
	AttrCloudProvider = "cloud.provider"
	AttrServiceName   = "service.name"
	// ServiceNameManagedCollector is what the managed Supervisor reports as service.name.
	ServiceNameManagedCollector = "odigos-managed-collector"
)

// Self is this connector's own identity, from its bootstrap config.
type Self struct {
	// ConnectorUID is the CR UID (config.Config.PlatformID). Every credential is bound to one, so a
	// connection whose binding names a different UID reached the wrong connector.
	ConnectorUID string
	Provider     string
	AccountID    string
}

// Collector is what this connector records about a Collector, projected from the managed_collectors row.
// A value rather than the row so the decisions below stay free of the store.
type Collector struct {
	Region string
	// NetworkID is the recorded host VPC/VNet. It is checked against the credential binding but does
	// not select the regional Collector record.
	NetworkID   string
	CollectorID string
	// ConfigHash is the hash of the configuration last pushed to this Collector.
	ConfigHash string
}

// Refusal is why a connection or a push was refused. Codes, not sentences — safe to log, and callers
// branch on these.
type Refusal string

const (
	RefusalNone Refusal = ""
	// RefusalNoIdentity: no gateway-asserted identity on the request, or an incomplete one. Only the
	// gateway can produce one, and a NetworkPolicy admits only the gateway to this port — so this is
	// either a misconfigured deployment or something that got past it.
	RefusalNoIdentity Refusal = "no-identity"
	// RefusalForeignConnector: the binding names a different connector. The gateway routed wrongly, or
	// this connector was recreated and the credential belongs to its predecessor.
	RefusalForeignConnector Refusal = "foreign-connector"
	RefusalProviderMismatch Refusal = "provider-mismatch"
	RefusalAccountMismatch  Refusal = "account-mismatch"
	// RefusalUnknownCollector: nothing is recorded for the identity's region. The connector has no
	// desired configuration to give and would be guessing at what this Collector is for.
	RefusalUnknownCollector Refusal = "unknown-collector"
	// RefusalCollectorMismatch: the recorded Collector for that region has a different identity. Two
	// Collectors claiming one region, and serving either one the other's configuration is worse than
	// serving neither.
	RefusalCollectorMismatch Refusal = "collector-mismatch"
	// RefusalDescriptionMissing / RefusalDescriptionMismatch: the Agent description disagrees with the
	// binding. It arrives AFTER the connection is established and is entirely client-controlled, so it
	// is a consistency check and never a routing input — but a disagreement means the thing on the other
	// end is not the Collector the binding describes.
	RefusalDescriptionMissing  Refusal = "description-missing"
	RefusalDescriptionMismatch Refusal = "description-mismatch"
)

// Admit reads the identity the gateway asserted and checks it belongs to this connector.
//
// The binding is authoritative: every field of it was fixed when the credential was issued and the
// gateway matched all of them before routing. This re-checks the two that say the connection reached the
// right process, because a gateway that routed wrongly would otherwise be handed a configuration
// carrying another account's destination credential.
func Admit(h http.Header, self Self) (collectorgateway.Identity, Refusal) {
	id, ok := collectorgateway.ParseIdentity(h)
	if !ok {
		return collectorgateway.Identity{}, RefusalNoIdentity
	}
	if self.ConnectorUID == "" || id.Binding.ConnectorUID != self.ConnectorUID {
		return collectorgateway.Identity{}, RefusalForeignConnector
	}
	if self.Provider != "" && id.Binding.Provider != self.Provider {
		return collectorgateway.Identity{}, RefusalProviderMismatch
	}
	if self.AccountID != "" && id.Binding.AccountID != self.AccountID {
		return collectorgateway.Identity{}, RefusalAccountMismatch
	}
	return id, RefusalNone
}

// VerifyCollector matches an admitted identity against the regional record and its host network.
//
// rec is nil when nothing is recorded. The credential id is deliberately NOT compared against the row's
// active_credential_id: the gateway is the authority on which credentials are live, and during a
// rotation two are — the outgoing one keeps working while the old tasks drain. Re-adjudicating it
// here would disconnect exactly the Collector a rotation is trying not to disturb.
func VerifyCollector(id collectorgateway.Identity, rec *Collector) Refusal {
	if rec == nil {
		return RefusalUnknownCollector
	}
	if rec.NetworkID != id.Binding.NetworkID {
		return RefusalCollectorMismatch
	}
	if rec.CollectorID != "" && rec.CollectorID != id.Binding.CollectorID {
		return RefusalCollectorMismatch
	}
	return RefusalNone
}

// VerifyDescription compares the Agent description against the binding.
//
// The description arrives after the connection is established and every attribute in it is written by
// the agent, so it can only ever confirm — it can never select. A mismatch means the process on the
// other end is not the Collector the binding describes, and it must not be handed that Collector's
// configuration: the configuration carries the customer's destination credential.
func VerifyDescription(id collectorgateway.Identity, desc *protobufs.AgentDescription) Refusal {
	if desc == nil {
		// Not yet reported. The first message may legitimately arrive without one, so this is the
		// caller's cue to wait rather than to refuse — see Session.
		return RefusalDescriptionMissing
	}
	attrs := identifyingAttributes(desc)

	connectorID, ok := attrs[AttrConnectorID]
	if !ok || connectorID == "" {
		return RefusalDescriptionMissing
	}
	if connectorID != id.Binding.ConnectorUID {
		return RefusalDescriptionMismatch
	}
	// Provider is checked when reported. It is a second, independent statement of the same fact, and
	// an agent that agrees about the connector but not the cloud is not the agent the binding describes.
	if provider, present := attrs[AttrCloudProvider]; present && provider != "" &&
		!strings.EqualFold(provider, id.Binding.Provider) {
		return RefusalDescriptionMismatch
	}
	return RefusalNone
}

func identifyingAttributes(desc *protobufs.AgentDescription) map[string]string {
	out := make(map[string]string, len(desc.GetIdentifyingAttributes()))
	for _, kv := range desc.GetIdentifyingAttributes() {
		if kv == nil || kv.GetKey() == "" {
			continue
		}
		out[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	return out
}

// PushReason is why a configuration was or was not sent.
type PushReason string

const (
	// PushFirstConfig: the agent has reported no remote configuration yet.
	PushFirstConfig PushReason = "first-config"
	// PushHashChanged: the agent is running something other than what Odigos wants.
	PushHashChanged PushReason = "hash-changed"
	// HoldNothingRendered: no desired configuration exists to send.
	HoldNothingRendered PushReason = "nothing-rendered"
	// HoldApplied: the agent is already running the desired configuration. The steady state, and the
	// common case — pushing here would be a write per reconcile for no change.
	HoldApplied PushReason = "already-applied"
	// HoldApplying: the agent is mid-apply of the desired configuration.
	HoldApplying PushReason = "applying"
	// HoldRejected: the agent refused this exact configuration. Re-sending it is a loop, and the
	// Supervisor has already rolled back to the last configuration that worked.
	HoldRejected PushReason = "rejected"
)

// Push is the decision about sending a configuration.
type Push struct {
	Send   bool
	Reason PushReason
	// Hash is the configuration hash the decision is about.
	Hash string
	// Error is the agent's verbatim rejection message when Reason is HoldRejected. Verbatim because it
	// comes from the Collector's own config validation, and paraphrasing a parser error loses the line
	// number that makes it actionable.
	Error string
}

// DecidePush decides whether to send the desired configuration.
//
// Steady state is zero pushes, mirroring the instrumentor's zero-write steady state: a Collector already
// running the desired configuration is left alone, so a reconcile that changes nothing sends nothing.
//
// A rejected configuration is NOT re-sent. The agent validates before applying and rolls back to its
// last known good on failure (the Supervisor's automatic_config_rollback), so re-sending the same
// document would loop it through the same validation failure for as long as the desired configuration
// stayed wrong — while the Collector, correctly, kept running the last one that worked. The rejection is
// surfaced instead, and the next genuinely different configuration is sent normally.
func DecidePush(desiredHash string, status *protobufs.RemoteConfigStatus) Push {
	if desiredHash == "" {
		return Push{Reason: HoldNothingRendered}
	}
	if status == nil || len(status.GetLastRemoteConfigHash()) == 0 {
		return Push{Send: true, Reason: PushFirstConfig, Hash: desiredHash}
	}
	if string(status.GetLastRemoteConfigHash()) != desiredHash {
		return Push{Send: true, Reason: PushHashChanged, Hash: desiredHash}
	}

	switch status.GetStatus() {
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLIED:
		return Push{Reason: HoldApplied, Hash: desiredHash}
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_APPLYING:
		return Push{Reason: HoldApplying, Hash: desiredHash}
	case protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED:
		return Push{Reason: HoldRejected, Hash: desiredHash, Error: status.GetErrorMessage()}
	default:
		// UNSET against a matching hash: the agent has the document but has not said what happened to
		// it. Sending it again is the safe reading — an agent that never applied it would otherwise wait
		// for a configuration change that may never come.
		return Push{Send: true, Reason: PushFirstConfig, Hash: desiredHash}
	}
}

// Report is what one AgentToServer message says about the Collector.
//
// EffectiveConfig and AvailableComponents are held for reporting and for the deferred
// metrics-driven scaling - deliberately NOT columns on managed_collectors: an effective
// configuration is a whole document that changes on every push, and a components list is fixed by the
// image. Storing either would make the row grow without telling a reader anything the image digest and
// the config hash do not already say.
type Report struct {
	InstanceUID string
	// Healthy is the agent's own verdict. Recorded and reported; never an input to endpoint selection,
	// which changes only on configuration.
	Healthy     bool
	AgentHealth string
	// EffectiveConfig is the configuration the Collector says it is running, flattened for reporting.
	EffectiveConfig string
	// AvailableComponents lists what the image can be configured with, used to keep a rendered
	// configuration inside what this Collector could actually run.
	AvailableComponents []string
	// RemoteConfigError is the verbatim validation failure when the agent rejected a configuration.
	RemoteConfigError  string
	Description        *protobufs.AgentDescription
	RemoteConfigStatus *protobufs.RemoteConfigStatus
}

// ExtractReport reads one message. Fields the message does not carry are left zero: OpAMP sends only
// what changed, so a message with no health is "health unchanged", not "unhealthy".
func ExtractReport(msg *protobufs.AgentToServer) Report {
	r := Report{
		InstanceUID:        instanceUID(msg.GetInstanceUid()),
		Description:        msg.GetAgentDescription(),
		RemoteConfigStatus: msg.GetRemoteConfigStatus(),
	}

	if health := msg.GetHealth(); health != nil {
		r.Healthy = health.GetHealthy()
		r.AgentHealth = health.GetStatus()
		if r.AgentHealth == "" {
			r.AgentHealth = healthLabel(health.GetHealthy())
		}
		if msg := health.GetLastError(); msg != "" {
			r.AgentHealth = healthLabel(health.GetHealthy()) + ": " + msg
		}
	}
	if status := msg.GetRemoteConfigStatus(); status != nil &&
		status.GetStatus() == protobufs.RemoteConfigStatuses_RemoteConfigStatuses_FAILED {
		r.RemoteConfigError = status.GetErrorMessage()
	}
	if eff := msg.GetEffectiveConfig(); eff != nil {
		r.EffectiveConfig = flattenConfig(eff.GetConfigMap())
	}
	for _, c := range componentNames(msg.GetAvailableComponents()) {
		r.AvailableComponents = append(r.AvailableComponents, c)
	}
	return r
}

func healthLabel(healthy bool) string {
	if healthy {
		return "Healthy"
	}
	return "Unhealthy"
}

// instanceUID renders the agent's instance id. A replaced Collector task gets a NEW one, which is why it
// is recorded but is not the Collector's identity — that is collector_id, which survives replacement.
func instanceUID(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	const hex = "0123456789abcdef"
	out := make([]byte, 0, len(raw)*2)
	for _, b := range raw {
		out = append(out, hex[b>>4], hex[b&0x0f])
	}
	return string(out)
}

// flattenConfig joins a multi-file effective configuration in a stable order, so two reports of the same
// state compare equal rather than differing by map iteration.
func flattenConfig(cfg *protobufs.AgentConfigMap) string {
	if cfg == nil || len(cfg.GetConfigMap()) == 0 {
		return ""
	}
	names := make([]string, 0, len(cfg.GetConfigMap()))
	for name := range cfg.GetConfigMap() {
		names = append(names, name)
	}
	sortStrings(names)

	var b strings.Builder
	for _, name := range names {
		if name != "" {
			b.WriteString("# " + name + "\n")
		}
		b.Write(cfg.GetConfigMap()[name].GetBody())
		b.WriteString("\n")
	}
	return b.String()
}

func componentNames(available *protobufs.AvailableComponents) []string {
	if available == nil {
		return nil
	}
	var out []string
	for kind, details := range available.GetComponents() {
		for name := range details.GetSubComponentMap() {
			out = append(out, kind+"/"+name)
		}
	}
	sortStrings(out)
	return out
}

// sortStrings is an insertion sort: these lists are tens of entries at most, and it keeps this file free
// of an import used for nothing else.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
