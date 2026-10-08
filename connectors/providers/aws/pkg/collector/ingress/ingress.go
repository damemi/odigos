// Package ingress resolves the AWS security-group policy for a managed Collector.
//
// A managed Collector holds the destination credential, so anything that can write to its OTLP ports
// forwards under that credential. The ingress rules are the only gate in front of it in v1 — which is
// why the network-CIDR fallback is constrained here rather than left to each deployer.
//
// Resolve returns the complete desired rule set, never a delta. Converging to it revokes a widened
// fallback the moment security groups resolve.
package ingress

import (
	"fmt"
	"net/netip"
	"sort"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

const (
	PortOTLPGRPC         = collectoringress.PortOTLPGRPC
	PortOTLPHTTP         = collectoringress.PortOTLPHTTP
	PortHealth           = collectoringress.PortHealth
	PortSupervisorHealth = collectoringress.PortSupervisorHealth
)

// DataPorts are the ports a workload sends telemetry to — the only ports a source rule may name.
func DataPorts() []int { return collectoringress.DataPorts() }

// FallbackAfterCycles is how many consecutive discovery cycles must resolve zero security groups before
// the VPC-CIDR fallback is considered. Empty groups mean AWS discovery is incomplete, so wait rather
// than widen.
const FallbackAfterCycles = 3

// MinFallbackPrefixLength is the default floor on automatic fallback width (/16, the widest AWS VPC
// block). Shorter prefixes are refused; customers may configure a stricter floor per region.
const MinFallbackPrefixLength = 16

type Tier = collectoringress.Tier

const (
	// TierNone: no workload ingress is currently required. A configured Collector can still be
	// provisioned ready for sources to be enabled later; no OTLP sender is admitted while the tier is
	// none. Provider-owned health-check authorization is outside this decision.
	TierNone = collectoringress.TierNone
	// TierNetworkIdentities: source rules for provider-native workload identities. The only steady state.
	TierNetworkIdentities = collectoringress.TierNetworkIdentities
	// TierConfiguredCIDRs: ranges the customer named explicitly.
	TierConfiguredCIDRs = collectoringress.TierConfiguredCIDRs
	// TierNetworkCIDR: the network's own blocks — constrained fallback. Reported degraded while in effect.
	TierNetworkCIDR = collectoringress.TierNetworkCIDR
)

// Source is one enabled workload routed to this Collector.
type Source struct {
	// Name identifies the workload in problem messages.
	Name string
	// SecurityGroupIDs are the groups traffic originates from. Empty means unresolved.
	SecurityGroupIDs []string
}

type Config = collectoringress.Config

// Input is everything Resolve needs.
type Input struct {
	// Sources are the enabled workloads routed here. None means nothing needs to reach the Collector.
	Sources []Source
	Config  Config
	// NetworkBlocks are the network's own CIDR associations. Fallback may only name these — never peered
	// ranges, gateway routes, or synthesised blocks.
	NetworkBlocks []string
	// UnresolvedCycles is consecutive discovery cycles with zero security groups.
	// Fallback is not considered below FallbackAfterCycles.
	UnresolvedCycles int
}

// Rule is one AWS security-group ingress permission. Exactly one source field is set.
type Rule struct {
	Port            int
	SecurityGroupID string
	CIDR            string
	Self            bool
}

// Severity and Problem are aliases of collectorstatus so ingress, transport, and credential lifecycle
// share one vocabulary. Callers keep writing collectoringress.SeverityError.
type (
	Severity = collectorstatus.Severity
	Problem  = collectorstatus.Problem
)

const (
	SeverityInfo     = collectorstatus.SeverityInfo
	SeverityPending  = collectorstatus.SeverityPending
	SeverityDegraded = collectorstatus.SeverityDegraded
	SeverityError    = collectorstatus.SeverityError
)

// Decision is the full outcome: desired rules, whether to deploy, and what to report.
type Decision struct {
	Tier Tier
	// Rules is the complete desired ingress set. Converge to it; do not diff against it.
	Rules []Rule
	// Deploy is whether the Collector should be created or kept converged. False: write no rules
	// (leave an existing Collector's rules alone; do not create a new one).
	Deploy   bool
	Problems []Problem
}

// Resolve decides the ingress rules for a regional Collector in its selected host network.
func Resolve(in Input) Decision {
	d := Decision{Tier: TierNone}

	groups, unresolved := resolveGroups(in.Sources, in.Config.AdditionalNetworkIdentityIDs)
	cidrs, cidrProblems := configuredCIDRs(in.Config.AdditionalCIDRs)
	d.Problems = append(d.Problems, cidrProblems...)
	// A rejected additionalCidrs entry refuses the whole decision rather than falling through to the
	// network-wide fallback — that field exists to avoid widening, so a typo must not produce one.
	if collectorstatus.HasError(cidrProblems) {
		return d
	}

	switch {
	case len(groups) > 0:
		// Any resolved security group wins over fallback. Partial resolution admits what resolved and reports
		// the rest — widening to cover an unresolved workload would admit the whole network.
		d.Tier = TierNetworkIdentities
		d.Rules = append(d.Rules, rulesForGroups(groups)...)
		d.Rules = append(d.Rules, rulesForCIDRs(cidrs)...)
		d.Deploy = true
		if len(unresolved) > 0 {
			d.Problems = append(d.Problems, unresolvedProblem(unresolved, SeverityDegraded))
		}
	case len(cidrs) > 0:
		// Customer's explicit ranges; fallback never reached.
		d.Tier = TierConfiguredCIDRs
		d.Rules = rulesForCIDRs(cidrs)
		d.Deploy = true
		if len(unresolved) > 0 {
			d.Problems = append(d.Problems, unresolvedProblem(unresolved, SeverityDegraded))
		}
	case len(in.Sources) == 0:
		// Collector lifecycle intent is configuration-driven, not source-driven. Provision the endpoint
		// now so it can become a managed destination before any source is marked, but admit no OTLP
		// sender yet. Provider-owned health-check authorization is separate; workload identity rules are
		// converged on a later pass after instrumentation is enabled.
		d.Deploy = true
		d.Problems = append(d.Problems, Problem{
			Code:     "NoRoutedWorkloads",
			Severity: SeverityInfo,
			Message: "no enabled workload in this region is routed to this Collector; the endpoint is " +
				"provisioned without OTLP ingress and source rules will be added after instrumentation is enabled. " +
				"For external senders, set collector.regions.<region>.ingress.additionalNetworkIdentityIds or additionalCidrs",
		})
	default:
		fb, problems := fallback(in, unresolved)
		d.Problems = append(d.Problems, problems...)
		if len(fb) == 0 {
			return d
		}
		d.Tier = TierNetworkCIDR
		d.Rules = rulesForCIDRs(fb)
		d.Deploy = true
	}

	// AWS's NLB and Collector tasks share the Collector security group. This provider-owned rule is
	// deliberately absent from the shared ingress contract.
	d.Rules = append(d.Rules, Rule{Port: PortHealth, Self: true})
	return d
}

// resolveGroups collects distinct security groups to admit and workloads whose groups are unresolved.
func resolveGroups(sources []Source, configured []string) (groups, unresolved []string) {
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			groups = append(groups, id)
		}
	}
	for _, s := range sources {
		if len(s.SecurityGroupIDs) == 0 {
			unresolved = append(unresolved, s.Name)
			continue
		}
		for _, id := range s.SecurityGroupIDs {
			add(id)
		}
	}
	for _, id := range configured {
		add(id)
	}
	sort.Strings(groups)
	sort.Strings(unresolved)
	return groups, unresolved
}

// configuredCIDRs validates customer-named ranges.
//
// 0.0.0.0/0 and ::/0 are refused even when asked for — an unauthenticated OTLP port open to the world
// is an open relay under the destination credential. Use endpointOverride for traffic from outside.
// Public (non-private) ranges are allowed and reported as degraded.
func configuredCIDRs(configured []string) (out []string, problems []Problem) {
	for _, c := range configured {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			problems = append(problems, Problem{
				Code:     "InvalidIngressCIDR",
				Severity: SeverityError,
				Message:  fmt.Sprintf("collector.regions.<region>.ingress.additionalCidrs entry %q is not a valid CIDR: %v", c, err),
			})
			continue
		}
		if p.Bits() == 0 {
			problems = append(problems, Problem{
				Code:     "OpenIngressRefused",
				Severity: SeverityError,
				Message: fmt.Sprintf("collector.regions.<region>.ingress.additionalCidrs entry %q would open the OTLP ports "+
					"every address; the ports carry no authentication and the Collector forwards under your "+
					"destination credential, so this is refused. Name the sending ranges, or put your own "+
					"authenticated endpoint in front and set collector.regions.<region>.endpointOverride", c),
			})
			continue
		}
		if !p.Addr().IsPrivate() && !p.Addr().IsLoopback() && !p.Addr().IsLinkLocalUnicast() {
			problems = append(problems, Problem{
				Code:     "PublicIngressCIDR",
				Severity: SeverityDegraded,
				Message: fmt.Sprintf("collector.regions.<region>.ingress.additionalCidrs entry %q is a public "+
					"range, so hosts outside your private network can write telemetry to this Collector. "+
					"Intentional for a datacentre sending over a gateway; remove it otherwise", c),
			})
		}
		out = append(out, p.Masked().String())
	}
	sort.Strings(out)
	return out, problems
}

// fallback admits the network's own blocks when discovery has failed and policy allows it.
// Gates: AllowNetworkCIDRFallback, FallbackAfterCycles, known NetworkBlocks, MinFallbackPrefixLength.
func fallback(in Input, unresolved []string) ([]string, []Problem) {
	var problems []Problem
	if !in.Config.AllowNetworkCIDRFallback {
		return nil, append(problems, Problem{
			Code:     "IngressUnresolved",
			Severity: SeverityError,
			Message: fmt.Sprintf("no security group resolved for %s and the VPC-CIDR fallback is "+
				"disabled, so no ingress rule is written and this Collector is not deployed. Set "+
				"collector.regions.<region>.ingress.additionalNetworkIdentityIds to the identities your workloads "+
				"send from, or additionalCidrs to their ranges", describe(unresolved)),
		})
	}
	if in.UnresolvedCycles < FallbackAfterCycles {
		return nil, append(problems, Problem{
			Code:     "IngressPendingDiscovery",
			Severity: SeverityPending,
			Message: fmt.Sprintf("waiting for AWS discovery to resolve the security groups of %s before writing "+
				"any ingress rule (%d of %d cycles); this normally clears "+
				"on its own", describe(unresolved), in.UnresolvedCycles, FallbackAfterCycles),
		})
	}
	if len(in.NetworkBlocks) == 0 {
		return nil, append(problems, Problem{
			Code:     "NetworkBlocksUnknown",
			Severity: SeverityError,
			Message: "the network's own address blocks are not known, so the ingress fallback has nothing " +
				"it is allowed to name; set collector.regions.<region>.ingress.additionalCidrs explicitly",
		})
	}

	floor := in.Config.MinFallbackPrefixLength
	if floor <= 0 {
		floor = MinFallbackPrefixLength
	}
	var out []string
	for _, b := range in.NetworkBlocks {
		p, err := netip.ParsePrefix(b)
		if err != nil {
			problems = append(problems, Problem{
				Code:     "InvalidNetworkBlock",
				Severity: SeverityError,
				Message:  fmt.Sprintf("network block %q is not a valid CIDR, so it is not admitted: %v", b, err),
			})
			continue
		}
		if p.Bits() < floor {
			problems = append(problems, Problem{
				Code:     "FallbackBlockTooWide",
				Severity: SeverityError,
				Message: fmt.Sprintf("network block %s is wider than the /%d floor, so the ingress fallback "+
					"refuses it: admitting it would let every host in that range write telemetry through "+
					"this Collector. Set collector.regions.<region>.ingress.additionalNetworkIdentityIds or "+
					"additionalCidrs, or raise minFallbackPrefixLength deliberately", b, floor),
			})
			continue
		}
		out = append(out, p.Masked().String())
	}
	if len(out) == 0 {
		return nil, problems
	}
	sort.Strings(out)
	problems = append(problems, Problem{
		Code:     "IngressWidenedToNetwork",
		Severity: SeverityDegraded,
		Message: fmt.Sprintf("no security group resolved for %s, so the OTLP ports admit the whole VPC "+
			"(%v). Every host in that range can write telemetry through this Collector, which forwards under "+
			"your destination credential. Set collector.regions.<region>.ingress.additionalNetworkIdentityIds to "+
			"narrow it; Odigos replaces this rule automatically as soon as a security group resolves",
			describe(unresolved), out),
	})
	return out, problems
}

func rulesForGroups(groups []string) []Rule {
	var out []Rule
	for _, port := range DataPorts() {
		for _, id := range groups {
			out = append(out, Rule{Port: port, SecurityGroupID: id})
		}
	}
	return out
}

func rulesForCIDRs(cidrs []string) []Rule {
	var out []Rule
	for _, port := range DataPorts() {
		for _, c := range cidrs {
			out = append(out, Rule{Port: port, CIDR: c})
		}
	}
	return out
}

func unresolvedProblem(names []string, sev Severity) Problem {
	return Problem{
		Code:     "WorkloadSecurityGroupsUnresolved",
		Severity: sev,
		Message: fmt.Sprintf("the security groups of %s are not resolved, so those workloads may not be able "+
			"to reach this Collector. Set collector.regions.<region>.ingress.additionalNetworkIdentityIds to AWS security group IDs if the "+
			"gap persists", describe(names)),
	}
}

// HasError reports whether any problem is SeverityError. Callers branch on this and Deploy, not messages.
func (d Decision) HasError() bool { return collectorstatus.HasError(d.Problems) }

func describe(names []string) string {
	switch len(names) {
	case 0:
		return "this network's workloads"
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	default:
		return fmt.Sprintf("%s and %d other workloads", names[0], len(names)-1)
	}
}
