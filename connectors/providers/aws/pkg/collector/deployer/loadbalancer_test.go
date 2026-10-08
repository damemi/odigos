package deployer

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

var testSubnets = []string{"subnet-a", "subnet-b"}

func loadBalancerInput() LoadBalancerInput {
	return LoadBalancerInput{
		Region:          testRegion,
		NetworkID:       testVPC,
		VPCID:           testVPC,
		Connector:       testConnector,
		ConnectorID:     testUID,
		Identity:        testIdentity(),
		Mode:            spec.ModeAutomatic,
		Subnets:         testSubnets,
		SecurityGroupID: "sg-collector",
	}
}

func TestEnsureLoadBalancerCreatesAnInternalNLBWithItsSecurityGroup(t *testing.T) {
	api := newFakeELB()
	in := loadBalancerInput()

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureLoadBalancer: %v / %v", err, problems)
	}
	if !lb.Complete() {
		t.Fatalf("load balancer = %+v", lb)
	}

	name := LoadBalancerName(testVPC)
	created := api.lbs[name]
	if created == nil {
		t.Fatalf("no load balancer named %s: %v", name, api.calls)
	}
	if created.Scheme != elbtypes.LoadBalancerSchemeEnumInternal {
		t.Errorf("scheme = %q, want internal: the Collector must not be reachable from the internet", created.Scheme)
	}
	if created.Type != elbtypes.LoadBalancerTypeEnumNetwork {
		t.Errorf("type = %q, want network", created.Type)
	}
	// Internal load balancers must be ipv4, and the Collector reaches nothing over IPv6 anyway.
	if created.IpAddressType != elbtypes.IpAddressTypeIpv4 {
		t.Errorf("ip address type = %q, want ipv4", created.IpAddressType)
	}
	// The one-shot decision (§7.7): a security group MUST be present in the create call, because an NLB
	// created without one can never have one added, and this group is the entire boundary in front of
	// an unauthenticated OTLP port.
	var createdWithGroup bool
	for _, call := range api.calls {
		if strings.HasPrefix(call, "CreateLoadBalancer:") && strings.Contains(call, "sgs=[sg-collector]") {
			createdWithGroup = true
		}
	}
	if !createdWithGroup {
		t.Fatalf("the load balancer was not created with its security group attached: %v", api.calls)
	}
	parsed := baseapi.ParseCollectorTags(api.tags[aws.ToString(created.LoadBalancerArn)])
	if !parsed.HasOwnershipMarker() || parsed.Region != testRegion || parsed.Network != testVPC {
		t.Errorf("the load balancer is not tagged as Odigos-created, so teardown could never remove it")
	}
}

// TestClientIPPreservationIsEnabled is the most consequential assertion in this file.
//
// AWS defaults preserve_client_ip.enabled to FALSE for an `ip` target group on TCP or TLS. With it
// false, traffic reaching a Collector task arrives from the load balancer's own interfaces — so the
// §7.7 tier-1 rules, which admit the WORKLOADS' security groups, match nothing. Every workload's
// telemetry is dropped by the group Odigos just wrote, and both the service and the target health
// report fine throughout.
func TestClientIPPreservationIsEnabled(t *testing.T) {
	api := newFakeELB()
	lb, _, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput())
	if err != nil {
		t.Fatal(err)
	}
	for _, arn := range lb.TargetGroupARNs {
		attrs := api.attributes[arn]
		if attrs[attrPreserveClientIP] != "true" {
			t.Errorf("%s = %q on %s, want \"true\": without it the workloads' security-group rules match nothing",
				attrPreserveClientIP, attrs[attrPreserveClientIP], arn)
		}
		// Off by default on an NLB, so a zone holding no task would blackhole the share of traffic that
		// resolved to it.
		if attrs[attrCrossZone] != "true" {
			t.Errorf("%s = %q on %s, want \"true\"", attrCrossZone, attrs[attrCrossZone], arn)
		}
	}
}

// TestTheHealthCheckTargetsTheHealthPort, not traffic-port, which is the AWS default. An OTLP receiver
// accepts a bare TCP connection whether or not the pipeline behind it works, so health-checking the
// data port would report a Collector healthy while its exporter was failing.
func TestTheHealthCheckTargetsTheHealthPort(t *testing.T) {
	api := newFakeELB()
	if _, _, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput()); err != nil {
		t.Fatal(err)
	}
	for _, port := range collectoringress.DataPorts() {
		tg := api.targetGroups[TargetGroupName(testVPC, port)]
		if tg == nil {
			t.Fatalf("no target group for port %d", port)
		}
		if aws.ToString(tg.HealthCheckPort) != "13133" {
			t.Errorf("health check port = %q, want 13133 (the health_check extension)", aws.ToString(tg.HealthCheckPort))
		}
		if tg.HealthCheckProtocol != elbtypes.ProtocolEnumTcp {
			t.Errorf("health check protocol = %q, want TCP", tg.HealthCheckProtocol)
		}
		// `ip`, because a Fargate task has no instance to register — only an ENI.
		if tg.TargetType != elbtypes.TargetTypeEnumIp {
			t.Errorf("target type = %q, want ip", tg.TargetType)
		}
		if aws.ToInt32(tg.Port) != int32(port) {
			t.Errorf("target group port = %d, want %d", aws.ToInt32(tg.Port), port)
		}
	}
}

func TestListenersForwardEachDataPortToItsOwnTargetGroup(t *testing.T) {
	api := newFakeELB()
	lb, _, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput())
	if err != nil {
		t.Fatal(err)
	}
	name := LoadBalancerName(testVPC)
	for i, port := range collectoringress.DataPorts() {
		l := api.listenerOn(name, int32(port))
		if l.ListenerArn == nil {
			t.Fatalf("no listener on port %d: %v", port, api.calls)
		}
		if l.Protocol != elbtypes.ProtocolEnumTcp {
			t.Errorf("listener %d protocol = %q, want TCP with no TLS mode set", port, l.Protocol)
		}
		if len(l.Certificates) > 0 {
			t.Errorf("listener %d carries a certificate with no TLS mode set", port)
		}
		if len(l.DefaultActions) != 1 || aws.ToString(l.DefaultActions[0].TargetGroupArn) != lb.TargetGroupARNs[i] {
			t.Errorf("listener %d forwards to %v, want %s", port, l.DefaultActions, lb.TargetGroupARNs[i])
		}
	}
}

// TestATLSModeChangeModifiesRatherThanReplaces. Deleting and recreating a listener leaves the port
// refusing connections in between, and switching to a customer certificate is a deliberate
// configuration change, not a reason to drop telemetry.
func TestATLSModeChangeModifiesRatherThanReplaces(t *testing.T) {
	api := newFakeELB()
	in := loadBalancerInput()
	if _, _, err := EnsureLoadBalancer(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}

	api.calls = nil
	in.TLSCertificateARN = "arn:aws:acm:" + testRegion + ":" + testAccount + ":certificate/abc"
	if _, _, err := EnsureLoadBalancer(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}

	for _, call := range api.calls {
		if strings.HasPrefix(call, "DeleteListener") || strings.HasPrefix(call, "CreateListener") {
			t.Errorf("the listener was replaced rather than modified: %v", api.calls)
		}
	}
	name := LoadBalancerName(testVPC)
	for _, port := range collectoringress.DataPorts() {
		l := api.listenerOn(name, int32(port))
		if l.Protocol != elbtypes.ProtocolEnumTls {
			t.Errorf("listener %d protocol = %q, want TLS", port, l.Protocol)
		}
		if len(l.Certificates) != 1 || aws.ToString(l.Certificates[0].CertificateArn) != in.TLSCertificateARN {
			t.Errorf("listener %d certificates = %v", port, l.Certificates)
		}
	}

	// And an unchanged pass touches nothing.
	api.calls = nil
	if _, _, err := EnsureLoadBalancer(context.Background(), api, in); err != nil {
		t.Fatal(err)
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "ModifyListener") || strings.HasPrefix(call, "CreateListener") {
			t.Errorf("a steady-state pass wrote a listener: %v", api.calls)
		}
	}
}

// TestAPartialCreateIsResumed — the fourth acceptance criterion, and the case that is easy to get
// wrong. DescribeTargetGroups against a load balancer returns only the groups a LISTENER forwards to,
// so a pass that created the target group and died before its listener leaves a group the next pass
// cannot see through the load balancer — and CreateTargetGroup then fails with a duplicate name, for
// ever. Looking it up by name finds it.
func TestAPartialCreateIsResumed(t *testing.T) {
	api := newFakeELB()
	in := loadBalancerInput()
	name := LoadBalancerName(testVPC)

	// What a pass that died after creating the load balancer and one target group leaves behind: no
	// listeners, so the group is invisible through the load balancer.
	api.withLoadBalancer(name, ourTags(testVPC), []string{"sg-collector"}, testSubnets)
	firstPort := collectoringress.DataPorts()[0]
	api.withTargetGroup(TargetGroupName(testVPC, firstPort), ourTags(testVPC))
	orphanARN := aws.ToString(api.targetGroups[TargetGroupName(testVPC, firstPort)].TargetGroupArn)

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureLoadBalancer: %v / %v", err, problems)
	}
	if !lb.Complete() {
		t.Fatalf("the pass did not complete: %+v", lb)
	}
	if lb.TargetGroupARNs[0] != orphanARN {
		t.Errorf("the target group left by the previous pass was not reused: %v", lb.TargetGroupARNs)
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "CreateLoadBalancer") {
			t.Errorf("recreated the load balancer: %v", api.calls)
		}
	}
	// Both listeners now exist, and the second target group was created.
	for _, port := range collectoringress.DataPorts() {
		if api.listenerOn(name, int32(port)).ListenerArn == nil {
			t.Errorf("no listener on port %d after resuming", port)
		}
	}
}

// TestALoadBalancerWithNoSecurityGroupIsRefused. It cannot be repaired: an NLB created without a
// security group can never have one added, and recreating it changes the DNS name every workload on
// the network points at.
func TestALoadBalancerWithNoSecurityGroupIsRefused(t *testing.T) {
	api := newFakeELB()
	name := LoadBalancerName(testVPC)
	api.withLoadBalancer(name, ourTags(testVPC), nil, testSubnets)

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput())
	if err != nil {
		t.Fatal(err)
	}
	if lb.ARN != "" {
		t.Error("returned an unprotected load balancer as usable")
	}
	if !collectorstatus.HasError(problems) || problems[0].Code != ProblemLoadBalancerUnprotected {
		t.Fatalf("problems = %v, want the unprotected refusal", problems)
	}
	// The message has to say why Odigos will not fix it and what the customer's option costs.
	for _, want := range []string{"can never have one added", "DNS name"} {
		if !strings.Contains(problems[0].Message, want) {
			t.Errorf("message does not mention %q: %s", want, problems[0].Message)
		}
	}
	for _, call := range api.calls {
		if strings.HasPrefix(call, "CreateTargetGroup") || strings.HasPrefix(call, "CreateListener") {
			t.Errorf("built a data plane behind an unprotected load balancer: %v", api.calls)
		}
	}
}

// TestNoSecurityGroupYetRefusesBeforeCreating, so a Collector's load balancer is never created in the
// window before its group exists — there is no second chance to attach one.
func TestNoSecurityGroupYetRefusesBeforeCreating(t *testing.T) {
	api := newFakeELB()
	in := loadBalancerInput()
	in.SecurityGroupID = ""

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if lb.ARN != "" || len(api.calls) != 0 {
		t.Errorf("created something with no security group: %+v / %v", lb, api.calls)
	}
	if !collectorstatus.HasError(problems) || problems[0].Code != ProblemLoadBalancerUnprotected {
		t.Fatalf("problems = %v", problems)
	}
}

// TestOurGroupIsAttachedToALoadBalancerThatHasOthers. It can be added when the load balancer already
// has at least one, so this is the repairable half of the same rule.
func TestOurGroupIsAttachedToALoadBalancerThatHasOthers(t *testing.T) {
	api := newFakeELB()
	name := LoadBalancerName(testVPC)
	api.withLoadBalancer(name, ourTags(testVPC), []string{"sg-someone-else"}, testSubnets)

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput())
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureLoadBalancer: %v / %v", err, problems)
	}
	if !lb.Complete() {
		t.Fatalf("load balancer = %+v", lb)
	}
	groups := api.lbs[name].SecurityGroups
	if !containsStringValue(groups, "sg-collector") {
		t.Errorf("security groups = %v, want the Collector's group attached", groups)
	}
	// The customer's group is kept: removing it might break something Odigos cannot see.
	if !containsStringValue(groups, "sg-someone-else") {
		t.Errorf("security groups = %v, want the pre-existing group kept", groups)
	}
}

// TestSubnetDriftIsReportedNotFixed. The zones a load balancer answers in are part of what its DNS name
// resolves to, so moving them is disruptive — and replacing it re-points every workload on the network.
func TestSubnetDriftIsReportedNotFixed(t *testing.T) {
	api := newFakeELB()
	name := LoadBalancerName(testVPC)
	api.withLoadBalancer(name, ourTags(testVPC), []string{"sg-collector"}, []string{"subnet-a"})

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput())
	if err != nil {
		t.Fatal(err)
	}
	if lb.ARN != "" {
		t.Error("converged over a load balancer whose subnets drifted")
	}
	if len(problems) != 1 || problems[0].Code != ProblemLoadBalancerSubnetDrift {
		t.Fatalf("problems = %v, want the drift report", problems)
	}
	if problems[0].Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded: it keeps working in the zones it has", problems[0].Severity)
	}
	if !strings.Contains(problems[0].Message, "subnet-b") {
		t.Errorf("message does not name the missing subnet: %s", problems[0].Message)
	}

	// EXTRA subnets are not drift: a customer who added a zone made the Collector more available.
	api = newFakeELB()
	api.withLoadBalancer(name, ourTags(testVPC), []string{"sg-collector"}, []string{"subnet-a", "subnet-b", "subnet-c"})
	if _, problems, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput()); err != nil || len(problems) > 0 {
		t.Fatalf("extra subnets were reported as drift: %v / %v", err, problems)
	}
}

func TestObjectsThatAreNotOursAreRefused(t *testing.T) {
	name := LoadBalancerName(testVPC)

	t.Run("load balancer", func(t *testing.T) {
		api := newFakeELB()
		api.withLoadBalancer(name, map[string]string{"team": "platform"}, []string{"sg-theirs"}, testSubnets)

		lb, problems, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput())
		if err != nil {
			t.Fatal(err)
		}
		if lb.ARN != "" {
			t.Error("adopted a load balancer Odigos does not own")
		}
		if !collectorstatus.HasError(problems) || problems[0].Code != ProblemObjectNotOurs {
			t.Fatalf("problems = %v", problems)
		}
		for _, call := range api.calls {
			if strings.HasPrefix(call, "SetSecurityGroups") || strings.HasPrefix(call, "CreateListener") {
				t.Errorf("wrote to a load balancer Odigos does not own: %v", api.calls)
			}
		}
	})

	// A target group with our name that is somebody else's: pointing our listener at it would register
	// the Collector's tasks as targets of their load balancer.
	t.Run("target group", func(t *testing.T) {
		api := newFakeELB()
		api.withLoadBalancer(name, ourTags(testVPC), []string{"sg-collector"}, testSubnets)
		api.withTargetGroup(TargetGroupName(testVPC, collectoringress.DataPorts()[0]), map[string]string{"team": "platform"})

		lb, problems, err := EnsureLoadBalancer(context.Background(), api, loadBalancerInput())
		if err != nil {
			t.Fatal(err)
		}
		if len(lb.TargetGroupARNs) != 0 {
			t.Errorf("adopted a target group Odigos does not own: %v", lb.TargetGroupARNs)
		}
		if !collectorstatus.HasError(problems) || problems[0].Code != ProblemObjectNotOurs {
			t.Fatalf("problems = %v", problems)
		}
		for _, call := range api.calls {
			if strings.HasPrefix(call, "ModifyTargetGroupAttributes") || strings.HasPrefix(call, "CreateListener") {
				t.Errorf("wrote to a target group Odigos does not own: %v", api.calls)
			}
		}
	})
}

// TestAnUnusableNameRefusesBeforeAnythingIsCreated. The two objects are created in sequence, so a load
// balancer whose name fits followed by a target group whose name does not would leave a chargeable
// object serving nothing.
func TestAnUnusableNameRefusesBeforeAnythingIsCreated(t *testing.T) {
	api := newFakeELB()
	in := loadBalancerInput()
	in.NetworkID = strings.Repeat("n", 40)

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if lb.ARN != "" || len(api.calls) != 0 {
		t.Errorf("created something for an unusable name: %+v / %v", lb, api.calls)
	}
	if !collectorstatus.HasError(problems) || problems[0].Code != ProblemNameTooLong {
		t.Fatalf("problems = %v", problems)
	}
	if !strings.Contains(problems[0].Message, "endpointOverride") {
		t.Errorf("message does not offer the way out: %s", problems[0].Message)
	}
	if !strings.Contains(problems[0].Message, "collector.regions."+testRegion) {
		t.Errorf("message uses a legacy network settings path: %s", problems[0].Message)
	}
}

// TestDataPlaneNamesFitRealAWSNetworkIDs. The ELBv2 limit is 32 characters and a modern VPC id is 21 of
// them, which is why these names carry shortened prefixes and strip the constant `vpc-`.
func TestDataPlaneNamesFitRealAWSNetworkIDs(t *testing.T) {
	for _, id := range []string{"vpc-0abc123def456789a", "vpc-0abc1234", testVPC} {
		if problems := CheckDataPlaneNames(id, testRegion); len(problems) > 0 {
			t.Errorf("network %s: %v", id, problems)
		}
		for _, name := range []string{LoadBalancerName(id), TargetGroupName(id, 4317), TargetGroupName(id, 4318)} {
			if len(name) > elbNameMaxLen {
				t.Errorf("%q is %d characters", name, len(name))
			}
			if strings.HasPrefix(name, "-") || strings.HasSuffix(name, "-") {
				t.Errorf("%q begins or ends with a hyphen, which ELBv2 refuses", name)
			}
			if strings.HasPrefix(name, "internal-") {
				t.Errorf("%q begins with \"internal-\", which a load balancer name may not", name)
			}
		}
	}
	// The two target groups must differ, or one listener would forward to the other's group.
	if TargetGroupName(testVPC, 4317) == TargetGroupName(testVPC, 4318) {
		t.Error("both data ports resolve to one target group name")
	}
}

func TestManualModeBuildsNoLoadBalancer(t *testing.T) {
	api := newFakeELB()
	in := loadBalancerInput()
	in.Mode = spec.ModeManual

	lb, problems, err := EnsureLoadBalancer(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}
	if lb.ARN != "" || len(api.calls) != 0 {
		t.Errorf("built a data plane in manual mode: %+v / %v", lb, api.calls)
	}
	if len(problems) != 1 || problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", problems)
	}
}

func TestASecondPassBuildsNothing(t *testing.T) {
	api := newFakeELB()
	in := loadBalancerInput()
	first, _, err := EnsureLoadBalancer(context.Background(), api, in)
	if err != nil {
		t.Fatal(err)
	}

	api.calls = nil
	second, problems, err := EnsureLoadBalancer(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureLoadBalancer: %v / %v", err, problems)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("the second pass produced a different data plane:\n  %+v\n  %+v", first, second)
	}
	// ModifyTargetGroupAttributes is deliberately absent from this list: the two attributes both default
	// the wrong way for this design, so they are written every pass rather than compared — a group
	// edited by hand is repaired instead of quietly degrading a whole network's telemetry.
	for _, call := range api.calls {
		for _, forbidden := range []string{"Create", "SetSecurityGroups", "ModifyListener"} {
			if strings.HasPrefix(call, forbidden) {
				t.Errorf("a steady-state pass called %s", call)
			}
		}
	}
}
