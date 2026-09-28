// Package collectoringress defines the provider-neutral managed Collector data-plane contract.
//
// It deliberately does not model firewall rules. A provider owns the meaning of a network identity,
// how identities are discovered, which fallback is safe for its network, and how its ingress resource
// is converged. The shared layer retains only the Collector protocol, persisted posture vocabulary,
// and customer intent that is meaningful across providers.
package collectoringress

const (
	PortOTLPGRPC         = 4317
	PortOTLPHTTP         = 4318
	PortHealth           = 13133
	PortSupervisorHealth = 13134
)

func DataPorts() []int { return []int{PortOTLPGRPC, PortOTLPHTTP} }

// Tier describes the portable security posture, not a provider firewall primitive.
type Tier string

const (
	TierNone              Tier = "none"
	TierNetworkIdentities Tier = "network-identities"
	TierConfiguredCIDRs   Tier = "configured-cidrs"
	TierNetworkCIDR       Tier = "network-cidr"
)

// Config is provider-neutral ingress intent. Network identity values are opaque to the shared layer;
// each provider documents and validates its own identity format.
type Config struct {
	AdditionalNetworkIdentityIDs []string
	AdditionalCIDRs              []string
	AllowNetworkCIDRFallback     bool
	MinFallbackPrefixLength      int
}

type Severity string

type Problem struct {
	Code     string
	Severity Severity
	Message  string
}

const (
	SeverityInfo     Severity = "Info"
	SeverityPending  Severity = "Pending"
	SeverityDegraded Severity = "Degraded"
	SeverityError    Severity = "Error"
)
