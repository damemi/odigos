package collectorreport

import (
	"fmt"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Input is everything one Collector's report depends on: its managed_collectors row, the problems this
// pass found, and the clock.
type Input struct {
	// Region is the Collector's identity. NetworkID is the host VPC/VNet metadata, not part of the
	// report identity. The report's name is built from region.
	Region    string
	NetworkID string

	// Mode and Origin are independent and both belong in the message, because together they are what
	// tell a reader whether Odigos will remove this Collector at teardown — the one thing a
	// customer cannot infer from the infrastructure itself.
	Mode    string
	Origin  string
	Adopted bool

	// Status is what the reconcile pass last recorded. Problems can only make it worse.
	Status collectorstatus.Status
	// RecordedMessage is managed_collectors.message: the sentence whoever last judged this Collector
	// wrote beside that status.
	//
	// Carried into the report because it is the ONLY channel for a diagnosis the reader cannot
	// reconstruct from the row's other columns — the Collector's verbatim rejection of a configuration,
	// or the ECS reason a task will not start. Composed problems cover the standing conditions this
	// package can re-derive; this covers the ones only the writer saw.
	RecordedMessage string

	// OpAMPLastSeenAt drives liveness. OpAMPInstanceUID is carried for a caller that wants it and is
	// deliberately absent from the name and the message: a replaced task gets a new one, and a
	// report that churned on it would announce a routine task replacement as a change.
	OpAMPLastSeenAt   *time.Time
	OpAMPInstanceUID  string
	AgentHealth       string
	HeartbeatInterval time.Duration

	// Topology, for the message.
	EndpointGRPC string
	EndpointHTTP string
	TLSMode      collectoringress.TLSMode
	IngressTier  collectoringress.Tier
	AdmittedFrom int
	DesiredCount int
	RunningCount int

	ConfigHash     string
	ConfigPushedAt *time.Time

	// Problems found on THIS pass. Re-derived every time rather than read from a one-time event,
	// because a standing posture reported once — at the moment it was applied — is invisible to
	// everyone who looks afterwards, which is everyone who matters.
	Problems []collectorstatus.Problem

	Now time.Time
}

// Report is one Collector's entry.
type Report struct {
	// Name is the region and is the Collector's stable identity. Never the host network or OpAMP instance
	// uid: that changes every time a task is replaced, and a report keyed on it would show one
	// Collector as a stream of new ones.
	Name string
	// Status is the identifier to branch on. Message is the reader-facing sentence and is free to be
	// reworded.
	Status  collectorstatus.Status
	Healthy bool
	Message string

	// Liveness and InstanceUID are why, for a caller that needs more than a boolean.
	Liveness    Liveness
	InstanceUID string
}

// Build renders one Collector's report.
func Build(in Input) Report {
	liveness := LivenessOf(in.OpAMPLastSeenAt, in.HeartbeatInterval, in.Now)
	status := statusFor(in, liveness)

	return Report{
		Name:        Name(in.Region),
		Status:      status,
		Healthy:     Healthy(status, liveness),
		Message:     message(in, liveness),
		Liveness:    liveness,
		InstanceUID: in.OpAMPInstanceUID,
	}
}

// Name is a Collector's identity in the report.
//
// Region alone is unique, and remains stable across host-network changes, task replacement, credential
// rotation and connector recreation.
func Name(region string) string { return region }

// statusFor is the state machine: the recorded status, escalated by this pass's problems, then adjusted
// for what the OpAMP session says.
//
// The table is exhaustive on purpose — every (recorded status, liveness) pair has one defined answer, so
// a new state cannot fall through to whatever the last branch happened to be.
func statusFor(in Input, liveness Liveness) collectorstatus.Status {
	base := in.Status
	if base == "" {
		base = collectorstatus.StatusPending
	}

	// Problems first. An Error here is terminal for this pass: nothing liveness says is worse than a
	// refused configuration or a broken credential.
	escalated := collectorstatus.StatusFor(in.Problems, base)
	if escalated == collectorstatus.StatusError {
		return collectorstatus.StatusError
	}

	switch liveness {
	case LivenessNeverConnected:
		if in.Mode == spec.ModeManual {
			// The direct analogue of the workload WaitingForCustomer issue: a non-error transient state
			// that clears on its own when the Collector connects. Not Pending, because Pending
			// says "Odigos is working on it" and here Odigos is not — only the customer can act.
			return collectorstatus.StatusAwaitingCustomer
		}
		// Automatic mode: Odigos is still building it, so whatever the reconciler said stands. Reporting
		// Degraded here would call every Collector broken for the minute it takes to create one.
		return escalated

	case LivenessStale:
		// It connected once and went quiet. The Collector is very likely still running and exporting —
		// the data path does not depend on OpAMP — but Odigos can no longer configure it. Degraded is
		// exactly that: working, in a posture the customer must be told about.
		if escalated == collectorstatus.StatusDeploying || escalated == collectorstatus.StatusPending {
			// Except mid-deploy, where silence is expected: the tasks are being replaced.
			return escalated
		}
		return collectorstatus.StatusDegraded

	default:
		// Live. An AwaitingCustomer recorded earlier is cleared by the connection itself, which is the
		// whole point of that state — the customer deployed the Collector and it called home.
		if escalated == collectorstatus.StatusAwaitingCustomer {
			return collectorstatus.StatusReady
		}
		return escalated
	}
}

// Healthy is the boolean the report carries, and it is a table rather than an expression so every
// combination has a stated answer.
//
// Ready AND live, and nothing else. A Collector that is Ready but silent is not healthy: Odigos
// cannot change its configuration, and calling that healthy would hide the failure this whole reporting
// path exists to surface.
func Healthy(status collectorstatus.Status, liveness Liveness) bool {
	return status == collectorstatus.StatusReady && liveness == LivenessLive
}

// message renders the reader-facing sentence. Composed from parts in a fixed order, so two reads of
// unchanged state produce the same string.
func message(in Input, liveness Liveness) string {
	var parts []string

	mode := in.Mode
	if mode == "" {
		mode = "unknown mode"
	}
	parts = append(parts, mode)

	// Origin, and the teardown consequence spelled out. four classes reduce to one question a
	// customer actually asks: will Odigos remove this?
	if note := ownershipNote(in); note != "" {
		parts = append(parts, note)
	}

	if liveness == LivenessNeverConnected && in.Mode == spec.ModeManual {
		// Manual and never connected: the deployment is the customer's next action, and saying anything
		// about tasks or endpoints Odigos has not seen would be inventing state.
		parts = append(parts,
			"waiting for you to deploy the Collector",
			"see the deployment instructions")
		return strings.Join(parts, " · ")
	}

	if in.DesiredCount > 0 || in.RunningCount > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d tasks", in.RunningCount, in.DesiredCount))
	}
	if endpoint := endpointNote(in); endpoint != "" {
		parts = append(parts, endpoint)
	}
	if ingress := ingressNote(in); ingress != "" {
		parts = append(parts, ingress)
	}
	if in.ConfigPushedAt != nil {
		parts = append(parts, "config pushed "+since(in.ConfigPushedAt, in.Now))
	}
	parts = append(parts, liveness.Describe(in.OpAMPLastSeenAt, in.HeartbeatInterval, in.Now))

	if in.AgentHealth != "" {
		parts = append(parts, in.AgentHealth)
	}
	// The recorded sentence, unless it only repeats what the agent already said. Two writers reach this
	// column — the OpAMP session for the configuration plane and a provider observer for the
	// infrastructure — and neither can reconstruct the other's finding, so whichever looked last is
	// what a reader gets.
	if in.RecordedMessage != "" && in.RecordedMessage != in.AgentHealth {
		parts = append(parts, in.RecordedMessage)
	}
	// The problems last, verbatim. This is how a Degraded reason is visible at all — the plaintext
	// default rides here and never in the status, and the network-CIDR fallback says so on every pass
	// for as long as it applies.
	for _, p := range in.Problems {
		parts = append(parts, p.Message)
	}
	return strings.Join(parts, " · ")
}

// ownershipNote turns mode × origin into the teardown answer.
func ownershipNote(in Input) string {
	switch {
	case in.Mode == spec.ModeAutomatic && in.Adopted:
		return "adopted · Odigos converges this Collector and will not delete it"
	case in.Mode == spec.ModeAutomatic && in.Origin == "customer":
		return "customer-created · Odigos converges this Collector and will not delete it"
	case in.Mode == spec.ModeAutomatic && in.Origin == "odigos":
		return "Odigos-created · removed at teardown"
	case in.Mode == spec.ModeManual && in.Origin == "odigos":
		return "handed over · Odigos configures it and will not change or remove its infrastructure"
	case in.Mode == spec.ModeManual:
		return "yours · Odigos configures it and never touches its infrastructure"
	}
	return ""
}

// endpointNote names what workloads are pointed at and what the hop costs. The plaintext default is
// stated, never hidden, and never a status of its own.
func endpointNote(in Input) string {
	endpoint := in.EndpointGRPC
	if endpoint == "" {
		endpoint = in.EndpointHTTP
	}
	if endpoint == "" {
		return ""
	}
	switch in.TLSMode {
	case collectoringress.TLSOff, "":
		return endpoint + " (plaintext)"
	case collectoringress.TLSCollector:
		return endpoint + " (TLS, terminated by the Collector)"
	case collectoringress.TLSLoadBalancer:
		return endpoint + " (TLS, terminated by the load balancer)"
	case collectoringress.TLSPlatform:
		return endpoint + " (TLS, terminated by the platform)"
	default:
		return endpoint + " (TLS mode " + string(in.TLSMode) + ")"
	}
}

// ingressNote says who may reach the Collector. The fallback tier is the one worth reading twice.
func ingressNote(in Input) string {
	switch in.IngressTier {
	case collectoringress.TierNetworkIdentities:
		if in.AdmittedFrom > 0 {
			return fmt.Sprintf("admitted from %d network identities", in.AdmittedFrom)
		}
		return "admitted from the routed workloads' network identities"
	case collectoringress.TierConfiguredCIDRs:
		return "admitted from the address ranges you configured"
	case collectoringress.TierNetworkCIDR:
		return "admitted from the whole network's address range (fallback)"
	default:
		return ""
	}
}

// BuildAll renders a whole fleet, in a stable order so two reads of unchanged state produce the same
// list rather than a reshuffle.
func BuildAll(inputs []Input) []Report {
	out := make([]Report, 0, len(inputs))
	for _, in := range inputs {
		out = append(out, Build(in))
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
