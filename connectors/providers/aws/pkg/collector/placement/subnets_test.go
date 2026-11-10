package placement

import (
	"reflect"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

func testVPC() VPC {
	return VPC{Region: "eu-west-1", ID: "vpc-1", CIDRBlocks: []string{"10.0.0.0/16"}}
}

// sn builds a subnet in the test VPC. egress and free-address count are the two facts that decide
// usability, so they are the only positional arguments.
func sn(id, zone string, egress Egress, freeIPs int32) Subnet {
	return Subnet{Region: "eu-west-1", ID: id, VPCID: "vpc-1", AvailabilityZone: zone, AvailableIPs: freeIPs, Egress: egress}
}

func codes(problems []collectorstatus.Problem) []string {
	out := make([]string, 0, len(problems))
	for _, p := range problems {
		out = append(out, p.Code)
	}
	return out
}

func hasCode(problems []collectorstatus.Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

func problemWith(t *testing.T, problems []collectorstatus.Problem, code string) collectorstatus.Problem {
	t.Helper()
	for _, p := range problems {
		if p.Code == code {
			return p
		}
	}
	t.Fatalf("no %s problem in %v", code, codes(problems))
	return collectorstatus.Problem{}
}

func TestSelectsOneSubnetPerZonePreferringPrivate(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-priv-a", "eu-west-1a", EgressNAT, 200),
		sn("subnet-priv-a2", "eu-west-1a", EgressNAT, 500), // more room in the same zone
		sn("subnet-priv-b", "eu-west-1b", EgressNAT, 200),
		sn("subnet-pub-c", "eu-west-1c", EgressPublic, 200),
	}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, false)

	// The public subnet is dropped even though taking it would add a third zone — see the trade-off
	// documented in selectAutomatic.
	want := []string{"subnet-priv-a2", "subnet-priv-b"}
	if !reflect.DeepEqual(sel.SubnetIDs, want) {
		t.Errorf("subnets = %v, want %v (one per zone, the roomier one inside a zone, private only)", sel.SubnetIDs, want)
	}
	if !reflect.DeepEqual(sel.Zones, []string{"eu-west-1a", "eu-west-1b"}) {
		t.Errorf("zones = %v", sel.Zones)
	}
	if !sel.Private {
		t.Error("selection is not marked private, but every chosen subnet has NAT egress")
	}
	if sel.Configured {
		t.Error("selection is marked configured, but Odigos chose it")
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", codes(problems))
	}
}

func TestZoneSpreadIsCapped(t *testing.T) {
	var subnets []Subnet
	for _, z := range []string{"a", "b", "c", "d", "e"} {
		subnets = append(subnets, sn("subnet-"+z, "eu-west-1"+z, EgressNAT, 100))
	}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, false)
	if len(sel.SubnetIDs) != maxAvailabilityZones {
		t.Fatalf("selected %d subnets, want the %d-zone cap: %v", len(sel.SubnetIDs), maxAvailabilityZones, sel.SubnetIDs)
	}
	// Lowest zones, so the choice is stable rather than map-order.
	if !reflect.DeepEqual(sel.Zones, []string{"eu-west-1a", "eu-west-1b", "eu-west-1c"}) {
		t.Errorf("zones = %v, want the first three", sel.Zones)
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", codes(problems))
	}
}

// TestSelectionIsStableAcrossPasses. A changed subnet set is a new task definition revision and a
// rolling replacement of the Collector, so re-reading unchanged infrastructure must not produce a
// different answer — which map iteration order would.
func TestSelectionIsStableAcrossPasses(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-b1", "eu-west-1b", EgressNAT, 100),
		sn("subnet-a1", "eu-west-1a", EgressNAT, 100),
		sn("subnet-a2", "eu-west-1a", EgressNAT, 100), // same room as a1: the id breaks the tie
		sn("subnet-c1", "eu-west-1c", EgressForwarded, 100),
		sn("subnet-d1", "eu-west-1d", EgressNAT, 100),
	}
	first, _ := SelectSubnets(testVPC(), subnets, nil, false)
	for i := 0; i < 50; i++ {
		again, _ := SelectSubnets(testVPC(), subnets, nil, false)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("selection changed between identical passes: %v then %v", first.SubnetIDs, again.SubnetIDs)
		}
	}
	if first.SubnetIDs[0] != "subnet-a1" {
		t.Errorf("tie inside a zone did not break on the id: %v", first.SubnetIDs)
	}
}

// TestFullSubnetsAreNotChosen: zero free addresses means ECS cannot attach an awsvpc ENI, so the
// service would be created and never place a task.
func TestFullSubnetsAreNotChosen(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-full-a", "eu-west-1a", EgressNAT, 0),
		sn("subnet-ok-a", "eu-west-1a", EgressNAT, 8),
		sn("subnet-ok-b", "eu-west-1b", EgressNAT, 8),
		// The only subnet in its zone, so a full subnet that was not excluded would show up as a third
		// zone rather than being quietly outranked inside a zone it shares.
		sn("subnet-full-c", "eu-west-1c", EgressNAT, 0),
	}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, false)
	if !reflect.DeepEqual(sel.SubnetIDs, []string{"subnet-ok-a", "subnet-ok-b"}) {
		t.Fatalf("subnets = %v, want only the two with free addresses", sel.SubnetIDs)
	}
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", codes(problems))
	}
}

// TestPublicOnlyWithNoPublicIPIsRefused is the trap rule 3 exists for, and the most likely shape of
// "a wrong default produces a Collector nothing can reach": a default VPC has only public subnets,
// assignPublicIp defaults to false, and an awsvpc ENI with no public address gets nothing routed for it
// by the internet gateway. Deploying would look completely healthy from ECS.
func TestPublicOnlyWithNoPublicIPIsRefused(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-pub-a", "eu-west-1a", EgressPublic, 100),
		sn("subnet-pub-b", "eu-west-1b", EgressPublic, 100),
	}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, false)
	if !sel.Empty() {
		t.Fatalf("selected %v, want a refusal", sel.SubnetIDs)
	}
	p := problemWith(t, problems, ProblemNoUsableSubnet)
	if p.Severity != collectorstatus.SeverityError {
		t.Errorf("severity = %q, want Error", p.Severity)
	}
	// The message has to name the fix, because the two fixes are unrelated and a customer cannot guess
	// which applies.
	for _, want := range []string{"assignPublicIp", "internet gateway", "subnet-pub-a"} {
		if !strings.Contains(p.Message, want) {
			t.Errorf("message does not mention %q: %s", want, p.Message)
		}
	}
}

func TestPublicOnlyWithAPublicIPIsUsedAndReported(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-pub-a", "eu-west-1a", EgressPublic, 100),
		sn("subnet-pub-b", "eu-west-1b", EgressPublic, 100),
	}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, true)
	if !reflect.DeepEqual(sel.SubnetIDs, []string{"subnet-pub-a", "subnet-pub-b"}) {
		t.Fatalf("subnets = %v", sel.SubnetIDs)
	}
	if sel.Private {
		t.Error("a public-subnet selection is marked private")
	}
	p := problemWith(t, problems, ProblemSubnetPublic)
	if p.Severity != collectorstatus.SeverityInfo {
		t.Errorf("severity = %q, want Info: public placement is an allowed customer network choice", p.Severity)
	}
	// The message must list what was actually selected, not every public subnet considered.
	if strings.Contains(p.Message, "subnet-pub-c") {
		t.Errorf("message names a subnet that was not selected: %s", p.Message)
	}
}

func TestNoDefaultRouteAnywhereIsRefused(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-iso-a", "eu-west-1a", EgressNone, 100),
		sn("subnet-iso-b", "eu-west-1b", EgressNone, 100),
	}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, true)
	if !sel.Empty() {
		t.Fatalf("selected %v, want a refusal", sel.SubnetIDs)
	}
	p := problemWith(t, problems, ProblemNoUsableSubnet)
	for _, want := range []string{"default route", "NAT gateway", "subnetIds"} {
		if !strings.Contains(p.Message, want) {
			t.Errorf("message does not mention %q: %s", want, p.Message)
		}
	}
}

func TestEveryEgressButFullIsRefusedForCapacity(t *testing.T) {
	subnets := []Subnet{sn("subnet-a", "eu-west-1a", EgressNAT, 0)}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, false)
	if !sel.Empty() {
		t.Fatalf("selected %v, want a refusal", sel.SubnetIDs)
	}
	if !strings.Contains(problemWith(t, problems, ProblemNoUsableSubnet).Message, "free IPv4") {
		t.Errorf("capacity refusal does not name the cause: %s", problems[0].Message)
	}
}

func TestNoSubnetsAtAllIsRefused(t *testing.T) {
	sel, problems := SelectSubnets(testVPC(), nil, nil, false)
	if !sel.Empty() {
		t.Fatal("selected something from an empty VPC")
	}
	if !strings.Contains(problemWith(t, problems, ProblemNoUsableSubnet).Message, "no subnets") {
		t.Errorf("message = %s", problems[0].Message)
	}
}

func TestSingleZoneIsReportedNotRefused(t *testing.T) {
	subnets := []Subnet{sn("subnet-a", "eu-west-1a", EgressNAT, 100)}
	sel, problems := SelectSubnets(testVPC(), subnets, nil, false)
	if sel.Empty() {
		t.Fatal("a single-zone VPC was refused; one zone costs resilience, not reachability")
	}
	p := problemWith(t, problems, ProblemSingleAvailabilityZone)
	if p.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", p.Severity)
	}
}

// --- configured subnets: the customer asserted, Odigos validates and reports ---

func TestConfiguredSubnetsAreUsedAsGiven(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-a", "eu-west-1a", EgressNAT, 100),
		sn("subnet-b", "eu-west-1b", EgressNAT, 100),
		sn("subnet-c", "eu-west-1c", EgressNAT, 100),
	}
	// Duplicated and unsorted, as a hand-written config often is. Four entries, three subnets.
	sel, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-c", "subnet-a", "subnet-c", ""}, false)
	if !reflect.DeepEqual(sel.SubnetIDs, []string{"subnet-a", "subnet-c"}) {
		t.Fatalf("subnets = %v, want the two named, deduplicated and sorted", sel.SubnetIDs)
	}
	if !sel.Configured {
		t.Error("selection is not marked configured")
	}
	// Not capped, and subnet-b is not added to improve the spread: the customer chose.
	if len(problems) != 0 {
		t.Errorf("unexpected problems: %v", codes(problems))
	}
}

func TestConfiguredUnknownSubnetRefusesTheNetwork(t *testing.T) {
	subnets := []Subnet{sn("subnet-a", "eu-west-1a", EgressNAT, 100)}
	sel, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-a", "subnet-typo"}, false)
	if !sel.Empty() {
		t.Fatalf("selected %v; deploying into the subset the customer did not fully name is worse than refusing", sel.SubnetIDs)
	}
	p := problemWith(t, problems, ProblemSubnetUnknown)
	if p.Severity != collectorstatus.SeverityError {
		t.Errorf("severity = %q, want Error", p.Severity)
	}
	if !strings.Contains(p.Message, "subnet-typo") {
		t.Errorf("message does not name the offending id: %s", p.Message)
	}
}

func TestConfiguredForeignSubnetRefusesTheNetwork(t *testing.T) {
	other := sn("subnet-elsewhere", "eu-west-1a", EgressNAT, 100)
	other.VPCID = "vpc-2"
	subnets := []Subnet{sn("subnet-a", "eu-west-1a", EgressNAT, 100), other}
	sel, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-a", "subnet-elsewhere"}, false)
	if !sel.Empty() {
		t.Fatalf("selected %v, want a refusal: an ECS service cannot span VPCs", sel.SubnetIDs)
	}
	if problemWith(t, problems, ProblemSubnetNotInNetwork).Severity != collectorstatus.SeverityError {
		t.Error("a cross-VPC subnet list is not an Error")
	}
}

func TestConfiguredFullSubnetIsSkippedNotRefused(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-full", "eu-west-1a", EgressNAT, 0),
		sn("subnet-ok", "eu-west-1b", EgressNAT, 100),
	}
	sel, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-full", "subnet-ok"}, false)
	if !reflect.DeepEqual(sel.SubnetIDs, []string{"subnet-ok"}) {
		t.Fatalf("subnets = %v, want the full one skipped", sel.SubnetIDs)
	}
	if problemWith(t, problems, ProblemSubnetFull).Severity != collectorstatus.SeverityDegraded {
		t.Error("a skipped full subnet is not Degraded")
	}
	if hasCode(problems, ProblemNoUsableSubnet) {
		t.Error("refused the network even though a named subnet was usable")
	}
}

func TestConfiguredAllFullRefusesTheNetwork(t *testing.T) {
	subnets := []Subnet{sn("subnet-full", "eu-west-1a", EgressNAT, 0)}
	sel, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-full"}, false)
	if !sel.Empty() {
		t.Fatalf("selected %v, want a refusal", sel.SubnetIDs)
	}
	if !hasCode(problems, ProblemSubnetFull) || !hasCode(problems, ProblemNoUsableSubnet) {
		t.Errorf("problems = %v, want both the cause and the refusal", codes(problems))
	}
}

// TestConfiguredSubnetWithNoRouteIsReportedNotRefused is the deliberate asymmetry between the two paths,
// and the reason it exists: Odigos cannot see interface VPC endpoints — DescribeVpcEndpoints is not in
// the managed-Collector permission set — so a subnet reaching the destination through one looks
// identical to an isolated subnet. Where the customer named it, that ambiguity resolves in favour of
// their assertion, exactly as the provider-neutral placement contract does.
func TestConfiguredSubnetWithNoRouteIsReportedNotRefused(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-endpoints-only", "eu-west-1a", EgressNone, 100),
		sn("subnet-endpoints-only-b", "eu-west-1b", EgressNone, 100),
	}
	sel, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-endpoints-only", "subnet-endpoints-only-b"}, false)
	if sel.Empty() {
		t.Fatal("refused subnets the customer named; a VPC-endpoint-only topology is legitimate and unseeable")
	}
	p := problemWith(t, problems, ProblemSubnetNoEgress)
	if p.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", p.Severity)
	}
	if !strings.Contains(p.Message, "VPC endpoint") {
		t.Errorf("message does not say what Odigos is unsure about: %s", p.Message)
	}
}

func TestConfiguredPublicSubnetWithoutAPublicIPIsReported(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-pub-a", "eu-west-1a", EgressPublic, 100),
		sn("subnet-pub-b", "eu-west-1b", EgressPublic, 100),
	}
	named := []string{"subnet-pub-a", "subnet-pub-b"}

	sel, problems := SelectSubnets(testVPC(), subnets, named, false)
	if sel.Empty() {
		t.Fatal("refused subnets the customer named")
	}
	if problemWith(t, problems, ProblemSubnetNeedsPublicIP).Severity != collectorstatus.SeverityDegraded {
		t.Error("severity is not Degraded")
	}

	// With a public address there is nothing to report.
	_, problems = SelectSubnets(testVPC(), subnets, named, true)
	if hasCode(problems, ProblemSubnetNeedsPublicIP) {
		t.Errorf("reported a missing public address when one is assigned: %v", codes(problems))
	}
}

func TestConfiguredSingleZoneIsReportedAgainstTheConfiguration(t *testing.T) {
	subnets := []Subnet{
		sn("subnet-a1", "eu-west-1a", EgressNAT, 100),
		sn("subnet-a2", "eu-west-1a", EgressNAT, 100),
		sn("subnet-b1", "eu-west-1b", EgressNAT, 100),
	}
	_, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-a1", "subnet-a2"}, false)
	p := problemWith(t, problems, ProblemSingleAvailabilityZone)
	// The message must point at the configuration, not at the VPC: another zone IS available here, and
	// telling the customer none is would be false.
	if !strings.Contains(p.Message, "subnetIds") {
		t.Errorf("message blames the VPC rather than the configuration: %s", p.Message)
	}
}

// TestEveryProblemIsReportedAtOnce. A customer who listed four subnets and got one complaint per pass
// would fix them one deploy at a time.
func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	foreign := sn("subnet-foreign", "eu-west-1c", EgressNAT, 100)
	foreign.VPCID = "vpc-2"
	subnets := []Subnet{
		sn("subnet-full", "eu-west-1a", EgressNAT, 0),
		foreign,
	}
	_, problems := SelectSubnets(testVPC(), subnets, []string{"subnet-unknown", "subnet-foreign", "subnet-full"}, false)
	want := []string{ProblemSubnetUnknown, ProblemSubnetNotInNetwork, ProblemSubnetFull}
	if !reflect.DeepEqual(codes(problems), want) {
		t.Fatalf("problems = %v, want %v in that fixed order", codes(problems), want)
	}
}

// TestARefusalIsAlwaysAnEmptySelection pins the invariant Placement.Deployable relies on: an error and a
// usable selection can never be returned together, so no caller can deploy from a refused plan by
// reading only one of the two.
func TestARefusalIsAlwaysAnEmptySelection(t *testing.T) {
	foreign := sn("subnet-foreign", "eu-west-1c", EgressNAT, 100)
	foreign.VPCID = "vpc-2"
	all := []Subnet{
		sn("subnet-priv-a", "eu-west-1a", EgressNAT, 100),
		sn("subnet-pub-b", "eu-west-1b", EgressPublic, 100),
		sn("subnet-iso-c", "eu-west-1c", EgressNone, 100),
		sn("subnet-full-d", "eu-west-1d", EgressNAT, 0),
		foreign,
	}
	configs := [][]string{
		nil, {}, {"subnet-priv-a"}, {"subnet-unknown"}, {"subnet-foreign"}, {"subnet-full-d"},
		{"subnet-iso-c"}, {"subnet-pub-b"}, {"subnet-priv-a", "subnet-unknown"},
	}
	for _, publicIP := range []bool{false, true} {
		for _, cfg := range configs {
			for _, subnets := range [][]Subnet{nil, all, {all[1]}, {all[2]}, {all[3]}} {
				sel, problems := SelectSubnets(testVPC(), subnets, cfg, publicIP)
				if collectorstatus.HasError(problems) != sel.Empty() {
					t.Fatalf("cfg=%v publicIP=%v subnets=%d: error=%v but selection=%v",
						cfg, publicIP, len(subnets), collectorstatus.HasError(problems), sel.SubnetIDs)
				}
			}
		}
	}
}
