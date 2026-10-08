package ingress

import (
	"strings"
	"testing"
)

func src(name string, groups ...string) Source {
	return Source{Name: name, SecurityGroupIDs: groups}
}

// allowFallback is the default config: the fallback is permitted, and enough cycles have passed that it
// would be reached if nothing else resolved.
func allowFallback() Config { return Config{AllowNetworkCIDRFallback: true} }

func cidrsIn(d Decision) []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range d.Rules {
		if r.CIDR != "" && !seen[r.CIDR] {
			seen[r.CIDR] = true
			out = append(out, r.CIDR)
		}
	}
	return out
}

func codes(d Decision) []string {
	var out []string
	for _, p := range d.Problems {
		out = append(out, p.Code)
	}
	return out
}

func hasCode(d Decision, code string) bool {
	for _, p := range d.Problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

// TestResolvePrefersNetworkIdentities: any resolved identity wins. The partial case is the one that matters —
// widening the rule to cover the workload Odigos cannot see would hand every other host in the network
// the same access as a side effect, so the gap is reported instead.
func TestResolvePrefersNetworkIdentities(t *testing.T) {
	d := Resolve(Input{
		Sources:          []Source{src("api", "sg-1"), src("worker")},
		Config:           allowFallback(),
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: 99,
	})
	if d.Tier != TierNetworkIdentities || !d.Deploy {
		t.Fatalf("tier = %q deploy = %v, want network identities and a deploy", d.Tier, d.Deploy)
	}
	if got := cidrsIn(d); len(got) != 0 {
		t.Errorf("a partial resolution widened to %v; it must admit only what resolved", got)
	}
	if !hasCode(d, "WorkloadSecurityGroupsUnresolved") {
		t.Errorf("problems = %v, want the unresolved workload reported", codes(d))
	}
	for _, p := range d.Problems {
		if p.Severity == SeverityError {
			t.Errorf("a partial resolution refused the deploy: %s", p.Message)
		}
		if !strings.Contains(p.Message, "worker") {
			t.Errorf("problem does not name the unresolved workload: %s", p.Message)
		}
	}
}

// TestConfiguredNetworkIdentitiesAvoidTheFallback: supplying an identity is what a customer does INSTEAD of
// accepting a network-wide rule, so it must count as a resolution.
func TestConfiguredNetworkIdentitiesAvoidTheFallback(t *testing.T) {
	d := Resolve(Input{
		Sources:          []Source{src("api")},
		Config:           Config{AllowNetworkCIDRFallback: true, AdditionalNetworkIdentityIDs: []string{"identity-mine"}},
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: 99,
	})
	if d.Tier != TierNetworkIdentities {
		t.Fatalf("tier = %q, want network identities", d.Tier)
	}
	if got := cidrsIn(d); len(got) != 0 {
		t.Errorf("fell back to %v despite a configured network identity", got)
	}
}

// TestFallbackWaitsForDiscovery: zero resolved groups means discovery has not finished, because every
// routed workload has a group on every platform this deploys to. The rule must not widen on cycle one.
func TestFallbackWaitsForDiscovery(t *testing.T) {
	for cycles := 0; cycles < FallbackAfterCycles; cycles++ {
		d := Resolve(Input{
			Sources:          []Source{src("api")},
			Config:           allowFallback(),
			NetworkBlocks:    []string{"10.0.0.0/16"},
			UnresolvedCycles: cycles,
		})
		if d.Deploy || d.Tier != TierNone {
			t.Errorf("cycle %d: tier = %q deploy = %v, want no rule yet", cycles, d.Tier, d.Deploy)
		}
		if !hasCode(d, "IngressPendingDiscovery") {
			t.Errorf("cycle %d: problems = %v, want the wait reported", cycles, codes(d))
		}
	}
	d := Resolve(Input{
		Sources:          []Source{src("api")},
		Config:           allowFallback(),
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: FallbackAfterCycles,
	})
	if d.Tier != TierNetworkCIDR || !d.Deploy {
		t.Fatalf("tier = %q deploy = %v, want the fallback after %d cycles", d.Tier, d.Deploy, FallbackAfterCycles)
	}
	if !hasCode(d, "IngressWidenedToNetwork") {
		t.Errorf("problems = %v, want the widening reported", codes(d))
	}
}

// TestFallbackIsReportedForAsLongAsItApplies: the widening is a standing posture, not an event, so the
// degraded problem must come back on every pass rather than only on the one that applied it.
func TestFallbackIsReportedForAsLongAsItApplies(t *testing.T) {
	in := Input{
		Sources:          []Source{src("api")},
		Config:           allowFallback(),
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: 50,
	}
	for pass := 0; pass < 3; pass++ {
		d := Resolve(in)
		var degraded bool
		for _, p := range d.Problems {
			if p.Code == "IngressWidenedToNetwork" && p.Severity == SeverityDegraded {
				degraded = true
			}
		}
		if !degraded {
			t.Fatalf("pass %d: problems = %v, want the widening still reported as degraded", pass, codes(d))
		}
	}
}

// TestFallbackConstraints walks every gate that stops the network rule from being written.
func TestFallbackConstraints(t *testing.T) {
	cases := []struct {
		name     string
		cfg      Config
		blocks   []string
		wantTier Tier
		wantCode string
	}{
		{
			name:     "disabled by policy",
			cfg:      Config{AllowNetworkCIDRFallback: false},
			blocks:   []string{"10.0.0.0/16"},
			wantTier: TierNone, wantCode: "IngressUnresolved",
		},
		{
			name:     "block wider than the floor",
			cfg:      allowFallback(),
			blocks:   []string{"10.0.0.0/8"},
			wantTier: TierNone, wantCode: "FallbackBlockTooWide",
		},
		{
			name:     "floor raised by configuration",
			cfg:      Config{AllowNetworkCIDRFallback: true, MinFallbackPrefixLength: 24},
			blocks:   []string{"10.0.0.0/16"},
			wantTier: TierNone, wantCode: "FallbackBlockTooWide",
		},
		{
			name:     "no blocks known",
			cfg:      allowFallback(),
			blocks:   nil,
			wantTier: TierNone, wantCode: "NetworkBlocksUnknown",
		},
		{
			name:     "malformed block",
			cfg:      allowFallback(),
			blocks:   []string{"not-a-cidr"},
			wantTier: TierNone, wantCode: "InvalidNetworkBlock",
		},
		{
			name:     "a /16 is admitted",
			cfg:      allowFallback(),
			blocks:   []string{"10.0.0.0/16"},
			wantTier: TierNetworkCIDR, wantCode: "IngressWidenedToNetwork",
		},
	}
	for _, tc := range cases {
		d := Resolve(Input{
			Sources:          []Source{src("api")},
			Config:           tc.cfg,
			NetworkBlocks:    tc.blocks,
			UnresolvedCycles: 99,
		})
		if d.Tier != tc.wantTier {
			t.Errorf("%s: tier = %q, want %q", tc.name, d.Tier, tc.wantTier)
		}
		if !hasCode(d, tc.wantCode) {
			t.Errorf("%s: problems = %v, want %s", tc.name, codes(d), tc.wantCode)
		}
		if d.Tier == TierNone && d.Deploy {
			t.Errorf("%s: deploy = true with no usable rule", tc.name)
		}
	}
}

// TestOpenIngressIsRefusedEvenWhenAskedFor: an unauthenticated OTLP port open to every address makes the
// Collector an open relay into the customer's backend, forwarding under their own destination
// credential. The refusal stands against an explicit request, and the message points at the sanctioned
// alternative.
func TestOpenIngressIsRefusedEvenWhenAskedFor(t *testing.T) {
	for _, open := range []string{"0.0.0.0/0", "::/0"} {
		d := Resolve(Input{
			Sources: []Source{src("api", "sg-1")},
			Config:  Config{AllowNetworkCIDRFallback: true, AdditionalCIDRs: []string{open}},
		})
		if d.Deploy || d.Tier != TierNone {
			t.Fatalf("%s: tier = %q deploy = %v, want a refusal", open, d.Tier, d.Deploy)
		}
		if !hasCode(d, "OpenIngressRefused") {
			t.Errorf("%s: problems = %v, want OpenIngressRefused", open, codes(d))
		}
		for _, p := range d.Problems {
			if p.Code == "OpenIngressRefused" && !strings.Contains(p.Message, "endpointOverride") {
				t.Errorf("%s: refusal does not name the alternative: %s", open, p.Message)
			}
		}
	}
}

// TestNoRuleEverOpensTheWorld is the invariant behind the refusal above, asserted over the outputs rather
// than the inputs: whatever is configured, no emitted rule may name a default route.
func TestNoRuleEverOpensTheWorld(t *testing.T) {
	inputs := []Input{
		{Sources: []Source{src("api", "sg-1")}, Config: allowFallback(), NetworkBlocks: []string{"0.0.0.0/0"}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: allowFallback(), NetworkBlocks: []string{"0.0.0.0/0"}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: Config{AllowNetworkCIDRFallback: true, AdditionalCIDRs: []string{"0.0.0.0/0"}}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: Config{AllowNetworkCIDRFallback: true, MinFallbackPrefixLength: 0}, NetworkBlocks: []string{"::/0"}, UnresolvedCycles: 99},
	}
	for i, in := range inputs {
		for _, c := range cidrsIn(Resolve(in)) {
			if strings.HasSuffix(c, "/0") {
				t.Errorf("input %d emitted %s", i, c)
			}
		}
	}
}

// TestBadConfiguredCIDRDoesNotWiden: a typo in the field a customer sets to AVOID a network-wide rule
// must not be what produces one.
func TestBadConfiguredCIDRDoesNotWiden(t *testing.T) {
	d := Resolve(Input{
		Sources:          []Source{src("api")},
		Config:           Config{AllowNetworkCIDRFallback: true, AdditionalCIDRs: []string{"10.1.0.0/33"}},
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: 99,
	})
	if d.Deploy || d.Tier != TierNone {
		t.Fatalf("tier = %q deploy = %v, want a refusal rather than a fallback", d.Tier, d.Deploy)
	}
	if !hasCode(d, "InvalidIngressCIDR") {
		t.Errorf("problems = %v, want the invalid range reported", codes(d))
	}
}

// TestConfiguredCIDRsUsedInsteadOfTheFallback: a customer's explicit ranges are narrower than the whole
// network, so they are used and the fallback is never reached. A public range is allowed — a datacentre
// sending over a gateway has a real reason for one — and reported.
func TestConfiguredCIDRsUsedInsteadOfTheFallback(t *testing.T) {
	d := Resolve(Input{
		Sources:          []Source{src("api")},
		Config:           Config{AllowNetworkCIDRFallback: true, AdditionalCIDRs: []string{"10.9.0.0/24", "203.0.113.0/24"}},
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: 99,
	})
	if d.Tier != TierConfiguredCIDRs || !d.Deploy {
		t.Fatalf("tier = %q deploy = %v, want the configured ranges", d.Tier, d.Deploy)
	}
	if got := cidrsIn(d); len(got) != 2 {
		t.Errorf("cidrs = %v, want both configured ranges and nothing else", got)
	}
	if !hasCode(d, "PublicIngressCIDR") {
		t.Errorf("problems = %v, want the public range reported", codes(d))
	}
	if hasCode(d, "IngressWidenedToNetwork") {
		t.Error("the fallback ran despite explicit ranges being configured")
	}
}

// TestNarrowingIsStructural: Resolve returns the COMPLETE desired set, so a deployer that converges to it
// revokes the widened rule the moment groups resolve. There is no separate cleanup step to forget.
func TestNarrowingIsStructural(t *testing.T) {
	widened := Resolve(Input{
		Sources:          []Source{src("api")},
		Config:           allowFallback(),
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: 99,
	})
	if len(cidrsIn(widened)) == 0 {
		t.Fatal("setup: expected the fallback to have widened")
	}
	resolved := Resolve(Input{
		Sources:          []Source{src("api", "sg-1")},
		Config:           allowFallback(),
		NetworkBlocks:    []string{"10.0.0.0/16"},
		UnresolvedCycles: 0,
	})
	if got := cidrsIn(resolved); len(got) != 0 {
		t.Errorf("the narrowed set still carries %v; converging to it would leave the network rule in place", got)
	}
	if resolved.Tier != TierNetworkIdentities {
		t.Errorf("tier = %q, want network identities once one resolved", resolved.Tier)
	}
}

// TestNoRoutedWorkloadsProvisionAnEndpointWithoutOTLPIngress pins the create-Collector-first flow. The
// load balancer needs its self-referencing health check, but no sender is admitted until a source is
// enabled (or the customer explicitly configures an additional sender).
func TestNoRoutedWorkloadsProvisionAnEndpointWithoutOTLPIngress(t *testing.T) {
	d := Resolve(Input{Config: allowFallback(), NetworkBlocks: []string{"10.0.0.0/16"}, UnresolvedCycles: 99})
	if !d.Deploy {
		t.Fatal("source-free Collector was not deployed")
	}
	if d.Tier != TierNone {
		t.Fatalf("tier = %q, want none", d.Tier)
	}
	if len(d.Rules) != 1 || !d.Rules[0].Self || d.Rules[0].Port != PortHealth {
		t.Fatalf("rules = %+v, want only the self-referencing health check", d.Rules)
	}
	if !hasCode(d, "NoRoutedWorkloads") {
		t.Errorf("problems = %v, want NoRoutedWorkloads", codes(d))
	}
	for _, p := range d.Problems {
		if p.Severity == SeverityError {
			t.Errorf("problems = %+v, source-free provisioning must not be an error", d.Problems)
		}
	}
}

func TestNoRoutedWorkloadsHonorExplicitIngress(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		tier Tier
	}{
		{
			name: "network identity",
			cfg:  Config{AdditionalNetworkIdentityIDs: []string{"identity-external"}},
			tier: TierNetworkIdentities,
		},
		{
			name: "cidr",
			cfg:  Config{AdditionalCIDRs: []string{"10.20.0.0/16"}},
			tier: TierConfiguredCIDRs,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Resolve(Input{Config: tt.cfg})
			if !d.Deploy || d.Tier != tt.tier {
				t.Fatalf("deploy = %v tier = %q, want true/%q", d.Deploy, d.Tier, tt.tier)
			}
			if hasCode(d, "NoRoutedWorkloads") {
				t.Fatalf("explicit ingress reported no routed workloads: %+v", d.Problems)
			}
		})
	}
}

// TestPortsAdmitted pins the AWS port set. The NLB health port is admitted from the Collector's own
// security group; the Supervisor health port must never appear.
func TestPortsAdmitted(t *testing.T) {
	d := Resolve(Input{Sources: []Source{src("api", "sg-1")}, Config: allowFallback()})
	var health int
	for _, r := range d.Rules {
		switch r.Port {
		case PortOTLPGRPC, PortOTLPHTTP:
			if r.Self {
				t.Errorf("data port %d admitted from the Collector's own group", r.Port)
			}
		case PortHealth:
			health++
			if !r.Self {
				t.Errorf("health port admitted from %q/%q rather than the Collector's own group", r.SecurityGroupID, r.CIDR)
			}
		default:
			t.Errorf("unexpected port %d in an ingress rule", r.Port)
		}
	}
	if health != 1 {
		t.Errorf("health rules = %d, want exactly one self-referencing rule", health)
	}
	for _, r := range d.Rules {
		if r.Port == PortSupervisorHealth {
			t.Error("the Supervisor health port is admitted; it must never appear in an ingress rule")
		}
	}
}

// TestRefusalWritesNoRules: every path that refuses must emit an empty set, because a caller that
// converges to a partial set on a refusal narrows to nothing and takes the Collector offline.
func TestRefusalWritesNoRules(t *testing.T) {
	refusals := []Input{
		{Sources: []Source{src("api")}, Config: Config{}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: allowFallback(), NetworkBlocks: []string{"10.0.0.0/8"}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: Config{AllowNetworkCIDRFallback: true, AdditionalCIDRs: []string{"bad"}}, UnresolvedCycles: 99},
	}
	for i, in := range refusals {
		d := Resolve(in)
		if d.Deploy {
			t.Errorf("refusal %d: deploy = true", i)
		}
		if len(d.Rules) != 0 {
			t.Errorf("refusal %d: emitted %d rules; a refusal must write nothing", i, len(d.Rules))
		}
		if !d.HasError() && !hasCode(d, "IngressPendingDiscovery") {
			t.Errorf("refusal %d: problems = %v, want an error or an explicit wait", i, codes(d))
		}
	}
}

// TestEveryProblemNamesAnAction: an unspecific message is the failure a customer actually hits, so every
// problem this package emits must name the configuration key or the thing to look at.
func TestEveryProblemNamesAnAction(t *testing.T) {
	inputs := []Input{
		{Config: allowFallback()},
		{Sources: []Source{src("api")}, Config: Config{}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: allowFallback(), NetworkBlocks: []string{"10.0.0.0/8"}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: allowFallback(), NetworkBlocks: []string{"10.0.0.0/16"}, UnresolvedCycles: 99},
		{Sources: []Source{src("api")}, Config: allowFallback(), NetworkBlocks: []string{"10.0.0.0/16"}, UnresolvedCycles: 1},
		{Sources: []Source{src("api", "sg-1"), src("worker")}, Config: allowFallback()},
		{Sources: []Source{src("api")}, Config: Config{AllowNetworkCIDRFallback: true, AdditionalCIDRs: []string{"203.0.113.0/24"}}},
		{Sources: []Source{src("api")}, Config: Config{AllowNetworkCIDRFallback: true, AdditionalCIDRs: []string{"0.0.0.0/0"}}},
	}
	for _, in := range inputs {
		for _, p := range Resolve(in).Problems {
			if p.Code == "" {
				t.Errorf("problem with no code: %s", p.Message)
			}
			// A pending problem is a wait, not an instruction: telling someone to change a setting that is
			// already correct sends them looking for a bug. Everything else must name the key.
			if p.Severity != SeverityPending && !strings.Contains(p.Message, "collector.regions") {
				t.Errorf("%s does not name a configuration key: %s", p.Code, p.Message)
			}
		}
	}
}
