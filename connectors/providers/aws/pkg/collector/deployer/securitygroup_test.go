package deployer

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	awsingress "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/ingress"
	"github.com/odigos-io/odigos/connectors/spec"
)

// workloadRules is the AWS steady-state tier, including the self-referencing NLB health rule.
func workloadRules(groups ...string) []awsingress.Rule {
	var out []awsingress.Rule
	for _, port := range collectoringress.DataPorts() {
		for _, g := range groups {
			out = append(out, awsingress.Rule{Port: port, SecurityGroupID: g})
		}
	}
	return append(out, awsingress.Rule{Port: collectoringress.PortHealth, Self: true})
}

func cidrRules(cidrs ...string) []awsingress.Rule {
	var out []awsingress.Rule
	for _, port := range collectoringress.DataPorts() {
		for _, c := range cidrs {
			out = append(out, awsingress.Rule{Port: port, CIDR: c})
		}
	}
	return append(out, awsingress.Rule{Port: collectoringress.PortHealth, Self: true})
}

func securityGroupInput(rules []awsingress.Rule) SecurityGroupInput {
	return SecurityGroupInput{
		Region:      testRegion,
		NetworkID:   testVPC,
		VPCID:       testVPC,
		Connector:   testConnector,
		ConnectorID: testUID,
		Identity:    testIdentity(),
		Mode:        spec.ModeAutomatic,
		Rules:       rules,
	}
}

func TestEnsureSecurityGroupCreatesTaggedAndScopedToTheVPC(t *testing.T) {
	api := newFakeEC2SG()
	in := securityGroupInput(workloadRules("sg-app"))

	group, problems, err := EnsureSecurityGroup(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecurityGroup: %v / %v", err, problems)
	}
	if group.ID == "" {
		t.Fatal("no group id returned")
	}

	created := api.groups[testVPC+"/"+SecurityGroupName(testVPC)]
	if created == nil {
		t.Fatalf("no group created under %s: %v", SecurityGroupName(testVPC), api.calls)
	}
	if len(api.descriptions) != 1 || api.descriptions[0] != "Odigos managed Collector - telemetry ingress for "+testVPC {
		t.Fatalf("security group descriptions = %q", api.descriptions)
	}
	for _, r := range api.descriptions[0] {
		if r < 0x20 || r > 0x7e {
			t.Fatalf("security group description contains an AWS-invalid character %U", r)
		}
	}
	// Tagged in the CreateSecurityGroup call itself. A group created and then tagged separately would
	// fail its own ownership check on every later pass if the pass died in between, and be refused for
	// ever.
	parsed := baseapi.ParseCollectorTags(ec2TagMap(created.Tags))
	if !parsed.IsCollector() || !parsed.HasOwnershipMarker() || parsed.Region != testRegion || parsed.Network != testVPC {
		t.Errorf("group tags = %v", ec2TagMap(created.Tags))
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "CreateTags") {
			t.Errorf("tags were written in a separate call: %v", api.calls)
		}
	}
}

// TestTheLookupIsScopedToTheVPC. A security group NAME is unique only within its VPC, so a filter on
// the name alone returns this connector's groups from every VPC in the region — and converging the
// first would write another network's rules onto it.
func TestTheLookupIsScopedToTheVPC(t *testing.T) {
	api := newFakeEC2SG()
	// The same name in a DIFFERENT VPC, tagged as ours, so only the scoping can tell them apart.
	other := "vpc-other"
	api.groups[other+"/"+SecurityGroupName(testVPC)] = &ec2types.SecurityGroup{
		GroupId:   aws.String("sg-elsewhere"),
		GroupName: aws.String(SecurityGroupName(testVPC)),
		VpcId:     aws.String(other),
		Tags:      ec2Tags(ourTags(testVPC)),
	}

	group, problems, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app")))
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecurityGroup: %v / %v", err, problems)
	}
	if group.ID == "sg-elsewhere" {
		t.Fatal("converged the group from another VPC")
	}
	var scoped bool
	for _, call := range api.calls {
		if call == "DescribeSecurityGroups:"+testVPC+"/"+SecurityGroupName(testVPC) {
			scoped = true
		}
	}
	if !scoped {
		t.Errorf("the lookup was not filtered on the VPC: %v", api.calls)
	}
}

// TestHealthCheckIsAdmittedFromTheCollectorsOwnGroup — the first acceptance criterion. The load
// balancer shares this group, so a self-referencing rule on 13133 is how its health check reaches the
// task; nothing else may reach that port, and 13134 must be reachable from nothing at all.
func TestHealthCheckIsAdmittedFromTheCollectorsOwnGroup(t *testing.T) {
	api := newFakeEC2SG()
	group, _, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app")))
	if err != nil {
		t.Fatal(err)
	}

	keys := api.ingressKeys(group.ID)
	self := "tcp/13133-13133/sg:" + group.ID
	if !containsString(keys, self) {
		t.Fatalf("no self-referencing rule on %d: %v", collectoringress.PortHealth, keys)
	}
	// Only from itself. A workload's group on 13133 would let any sender read the Collector's health.
	for _, k := range keys {
		if strings.HasPrefix(k, "tcp/13133-13133/") && k != self {
			t.Errorf("port %d is admitted from something other than the Collector's own group: %s", collectoringress.PortHealth, k)
		}
		if strings.Contains(k, "13134") {
			t.Errorf("port %d is admitted at all; it reports on the process holding the OpAMP credential: %s", collectoringress.PortSupervisorHealth, k)
		}
	}
}

func TestConvergenceRetainsBothNLBBackendAndWorkloadRules(t *testing.T) {
	api := newFakeEC2SG()
	decision := awsingress.Resolve(awsingress.Input{Sources: []awsingress.Source{{Name: "api", SecurityGroupIDs: []string{"sg-app"}}}})
	for pass := 0; pass < 2; pass++ {
		group, problems, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(decision.Rules))
		if err != nil || len(problems) != 0 {
			t.Fatalf("pass %d: %v / %v", pass, err, problems)
		}
		for _, port := range []string{"4317", "4318"} {
			for _, source := range []string{"sg-app", group.ID} {
				key := "tcp/" + port + "-" + port + "/sg:" + source
				if !containsString(api.ingressKeys(group.ID), key) {
					t.Fatalf("pass %d removed required ingress %s", pass, key)
				}
			}
		}
	}
}

// TestNoUnauthorizedIngressReachesTheTask — the third acceptance criterion. The group must contain
// exactly the desired set and nothing else, because the OTLP ports carry no authentication and anything
// that can write to them forwards under the customer's destination credential.
func TestNoUnauthorizedIngressReachesTheTask(t *testing.T) {
	api := newFakeEC2SG()
	rules := workloadRules("sg-app")
	group, _, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(rules))
	if err != nil {
		t.Fatal(err)
	}

	want := []string{
		"tcp/13133-13133/sg:" + group.ID,
		"tcp/4317-4317/sg:sg-app",
		"tcp/4318-4318/sg:sg-app",
	}
	if got := api.ingressKeys(group.ID); !reflect.DeepEqual(got, want) {
		t.Fatalf("ingress = %v, want exactly %v", got, want)
	}
}

// TestAWidenedFallbackIsNarrowedWhenAGroupResolves is the behaviour collectoringress.Resolve returns a
// COMPLETE set for. The network-CIDR fallback admits every host on the network; the moment a workload's
// own security group resolves, that rule has to go — and with no separate cleanup step that could be
// forgotten or fail on its own.
func TestAWidenedFallbackIsNarrowedWhenAGroupResolves(t *testing.T) {
	api := newFakeEC2SG()
	in := securityGroupInput(cidrRules("10.0.0.0/16"))

	group, _, err := EnsureSecurityGroup(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(api.ingressKeys(group.ID), "tcp/4317-4317/cidr:10.0.0.0/16") {
		t.Fatalf("the fallback rule was not written: %v", api.ingressKeys(group.ID))
	}

	// Discovery resolves the workload's group on the next pass.
	api.calls = nil
	in.Rules = workloadRules("sg-app")
	group, _, err = EnsureSecurityGroup(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}

	keys := api.ingressKeys(group.ID)
	for _, k := range keys {
		if strings.Contains(k, "cidr:10.0.0.0/16") {
			t.Errorf("the network-wide rule survived: %v", keys)
		}
	}
	if !containsString(keys, "tcp/4317-4317/sg:sg-app") {
		t.Errorf("the narrow rule was not written: %v", keys)
	}
	// Authorize before revoke: if the pass dies between them the group is briefly too wide, not too
	// narrow — a Collector admitting one stale sender for a few seconds is recoverable, one admitting
	// nobody has dropped every span on the network.
	var authorizeAt, revokeAt = -1, -1
	for i, call := range api.calls {
		if strings.HasPrefix(call, "AuthorizeIngress") && authorizeAt < 0 {
			authorizeAt = i
		}
		if strings.HasPrefix(call, "RevokeIngress") && revokeAt < 0 {
			revokeAt = i
		}
	}
	if authorizeAt < 0 || revokeAt < 0 || authorizeAt > revokeAt {
		t.Errorf("calls = %v, want the authorize before the revoke", api.calls)
	}
}

// TestARuleAddedByHandIsRevoked. The desired set is complete, so `additionalNetworkIdentityIds` and
// `additionalCidrs` are the sanctioned way to admit another sender — inputs to the set rather than
// exceptions to it. A rule nobody can account for on the port that forwards under the customer's
// destination credential is exactly what must not survive a converge.
func TestARuleAddedByHandIsRevoked(t *testing.T) {
	name := SecurityGroupName(testVPC)
	api := newFakeEC2SG().withGroup(name, ourTags(testVPC), []ec2types.IpPermission{
		{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(4317), ToPort: aws.Int32(4317),
			IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		},
		{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22),
			IpRanges: []ec2types.IpRange{{CidrIp: aws.String("10.0.0.0/8")}},
		},
	})

	group, _, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app")))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range api.ingressKeys(group.ID) {
		if strings.Contains(k, "0.0.0.0/0") {
			t.Error("an open-to-the-world rule survived the converge")
		}
		if strings.Contains(k, "/22-22/") {
			t.Error("a rule on a port Odigos does not manage survived; the desired set is complete")
		}
	}
}

// TestGroupedRulesCompareWithoutChurn. EC2 returns the rules it stored, grouped however it chose, so a
// comparison over grouped shapes would see differences that are not differences — and revoke a rule
// that should stay.
func TestGroupedRulesCompareWithoutChurn(t *testing.T) {
	name := SecurityGroupName(testVPC)
	// One permission carrying BOTH workload groups, which is how EC2 may hand back two rules written
	// separately.
	api := newFakeEC2SG().withGroup(name, ourTags(testVPC), []ec2types.IpPermission{
		{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(4317), ToPort: aws.Int32(4317),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String("sg-a")}, {GroupId: aws.String("sg-b")},
			},
		},
		{
			IpProtocol: aws.String("tcp"), FromPort: aws.Int32(4318), ToPort: aws.Int32(4318),
			UserIdGroupPairs: []ec2types.UserIdGroupPair{
				{GroupId: aws.String("sg-a")}, {GroupId: aws.String("sg-b")},
			},
		},
	})
	group := api.groups[testVPC+"/"+name]
	group.IpPermissions = append(group.IpPermissions, ec2types.IpPermission{
		IpProtocol: aws.String("tcp"), FromPort: aws.Int32(13133), ToPort: aws.Int32(13133),
		UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: group.GroupId}},
	})

	got, _, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-a", "sg-b")))
	if err != nil {
		t.Fatal(err)
	}
	if got.Authorized != 0 || got.Revoked != 0 {
		t.Fatalf("authorized %d and revoked %d against an already-correct group: %v", got.Authorized, got.Revoked, api.calls)
	}
}

// TestOneSourceIsRevokedWithoutTouchingTheOthers. Revoking a grouped rule wholesale would remove the
// sources that should stay, so each is rebuilt as a single-source permission.
func TestOneSourceIsRevokedWithoutTouchingTheOthers(t *testing.T) {
	name := SecurityGroupName(testVPC)
	api := newFakeEC2SG().withGroup(name, ourTags(testVPC), []ec2types.IpPermission{{
		IpProtocol: aws.String("tcp"), FromPort: aws.Int32(4317), ToPort: aws.Int32(4317),
		UserIdGroupPairs: []ec2types.UserIdGroupPair{
			{GroupId: aws.String("sg-stays")}, {GroupId: aws.String("sg-goes")},
		},
	}})

	group, _, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-stays")))
	if err != nil {
		t.Fatal(err)
	}
	keys := api.ingressKeys(group.ID)
	if !containsString(keys, "tcp/4317-4317/sg:sg-stays") {
		t.Errorf("the surviving source was revoked along with the other: %v", keys)
	}
	if containsString(keys, "tcp/4317-4317/sg:sg-goes") {
		t.Errorf("the stale source survived: %v", keys)
	}
}

func TestASecondPassWritesNoRules(t *testing.T) {
	api := newFakeEC2SG()
	in := securityGroupInput(workloadRules("sg-app"))
	if _, _, err := EnsureSecurityGroup(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}

	api.calls = nil
	group, problems, err := EnsureSecurityGroup(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecurityGroup: %v / %v", err, problems)
	}
	if group.Authorized != 0 || group.Revoked != 0 {
		t.Errorf("a steady-state pass authorized %d and revoked %d", group.Authorized, group.Revoked)
	}
	for _, call := range api.calls {
		for _, forbidden := range []string{"Create", "Authorize", "Revoke"} {
			if strings.HasPrefix(call, forbidden) {
				t.Errorf("a steady-state pass called %s", call)
			}
		}
	}
}

// TestClosedEgressIsReopened. EC2 gives a new group allow-all egress, so an empty set means somebody
// removed it — and a Collector that can be written to and cannot export is the worst of both: it
// accepts telemetry and drops it.
func TestClosedEgressIsReopened(t *testing.T) {
	name := SecurityGroupName(testVPC)
	api := newFakeEC2SG().withGroup(name, ourTags(testVPC), nil)
	api.groups[testVPC+"/"+name].IpPermissionsEgress = nil

	group, _, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app")))
	if err != nil {
		t.Fatal(err)
	}
	if len(api.byID(group.ID).IpPermissionsEgress) == 0 {
		t.Fatalf("egress was not reopened: %v", api.calls)
	}

	// And an intact egress rule is left alone.
	api.calls = nil
	if _, _, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app"))); err != nil {
		t.Fatal(err)
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "AuthorizeEgress") {
			t.Errorf("rewrote an intact egress rule: %v", api.calls)
		}
	}
}

// TestAGroupThatIsNotOursIsNeverWritten. Its rules are a customer's security posture, and this
// function's whole job is to make a group's rules equal a set Odigos computed.
func TestAGroupThatIsNotOursIsNeverWritten(t *testing.T) {
	name := SecurityGroupName(testVPC)
	api := newFakeEC2SG().withGroup(name, map[string]string{"team": "platform"}, []ec2types.IpPermission{{
		IpProtocol: aws.String("tcp"), FromPort: aws.Int32(443), ToPort: aws.Int32(443),
		IpRanges: []ec2types.IpRange{{CidrIp: aws.String("10.0.0.0/8")}},
	}})

	group, problems, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app")))
	if err != nil {
		t.Fatal(err)
	}
	if group.ID != "" {
		t.Error("returned the id of a group Odigos does not own")
	}
	if !collectorstatus.HasError(problems) || problems[0].Code != ProblemObjectNotOurs {
		t.Fatalf("problems = %v, want the not-ours refusal", problems)
	}
	for _, call := range api.calls {
		for _, forbidden := range []string{"Authorize", "Revoke"} {
			if strings.HasPrefix(call, forbidden) {
				t.Errorf("wrote to a group Odigos does not own: %v", api.calls)
			}
		}
	}
	if len(api.ingressKeys("sg-1")) != 1 {
		t.Errorf("the customer's rules were changed: %v", api.ingressKeys("sg-1"))
	}
}

func TestALostCreateRaceOnTheGroupIsResolvedByRereading(t *testing.T) {
	t.Run("won by another replica", func(t *testing.T) {
		api := newFakeEC2SG()
		api.raceWinner = ourTags(testVPC)

		group, problems, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app")))
		if err != nil || len(problems) > 0 {
			t.Fatalf("EnsureSecurityGroup: %v / %v", err, problems)
		}
		if group.ID == "" {
			t.Fatal("no group after losing the create race to our own replica")
		}
	})

	t.Run("won by a stranger", func(t *testing.T) {
		api := newFakeEC2SG()
		api.raceWinner = map[string]string{"team": "platform"}

		group, problems, err := EnsureSecurityGroup(context.Background(), api, securityGroupInput(workloadRules("sg-app")))
		if err != nil {
			t.Fatal(err)
		}
		if group.ID != "" {
			t.Error("adopted a group a stranger created by winning the race")
		}
		if !collectorstatus.HasError(problems) {
			t.Fatalf("problems = %v, want the not-ours refusal", problems)
		}
	})
}

func TestManualModeWritesNoSecurityGroup(t *testing.T) {
	api := newFakeEC2SG()
	in := securityGroupInput(workloadRules("sg-app"))
	in.Mode = spec.ModeManual

	group, problems, err := EnsureSecurityGroup(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if group.ID != "" || len(api.calls) != 0 {
		t.Errorf("touched EC2 in manual mode: %+v / %v", group, api.calls)
	}
	if len(problems) != 1 || problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", problems)
	}
}
