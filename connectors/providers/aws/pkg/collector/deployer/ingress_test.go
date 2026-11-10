package deployer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	"github.com/odigos-io/odigos/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	awsingress "github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/ingress"
	"github.com/odigos-io/odigos/connectors/spec"
)

// ingressNetwork is a realistic modern VPC id — 21 characters — because the ELBv2 32-character name
// limit is what forced the shortened prefixes, and a test on a short id would not exercise it.
func ingressKey() basestore.CollectorKey {
	return basestore.CollectorKey{Region: testRegion}
}

func ingressInput() IngressInput {
	return IngressInput{
		Key: ingressKey(),
		// VPCID is the physical host network for this regional Collector.
		Mode:          spec.ModeAutomatic,
		Shared:        Shared{Partition: "aws"},
		VPCID:         testVPC,
		Subnets:       testSubnets,
		NetworkBlocks: []string{"10.0.0.0/16"},
		Sources: []awsingress.Source{
			{Name: "api", SecurityGroupIDs: []string{"sg-app"}},
		},
		Config: collectoringress.Config{AllowNetworkCIDRFallback: true},
	}
}

// ingressProvisioner is a Provisioner with only the ingress half wired; the identity half has its own
// tests.
func ingressProvisioner(clients *fakeClients) *Provisioner {
	return &Provisioner{Identity: testIdentity(), Provider: "aws", AccountID: testAccount, Clients: clients}
}

type fakeLoadBalancerRetryStore struct {
	retry basestore.CollectorLoadBalancerRetry
}

func (f *fakeLoadBalancerRetryStore) GetCollectorLoadBalancerRetry(context.Context, basestore.CollectorKey) (basestore.CollectorLoadBalancerRetry, error) {
	return f.retry, nil
}
func (f *fakeLoadBalancerRetryStore) RecordCollectorLoadBalancerRetry(_ context.Context, _ basestore.CollectorKey, retry basestore.CollectorLoadBalancerRetry) error {
	f.retry = retry
	return nil
}
func (f *fakeLoadBalancerRetryStore) ClearCollectorLoadBalancerRetry(context.Context, basestore.CollectorKey) error {
	f.retry = basestore.CollectorLoadBalancerRetry{}
	return nil
}

func TestAccountRestrictionBacksOffButDiscoversAnExistingLoadBalancer(t *testing.T) {
	clients := newFakeClients()
	clients.elb.createLBErr = &elbtypes.OperationNotPermittedException{
		Message: aws.String("This AWS account currently does not support creating load balancers. For more information, please contact AWS Support."),
	}
	retryStore := &fakeLoadBalancerRetryStore{}
	now := time.Date(2026, 10, 13, 13, 0, 0, 0, time.UTC)
	p := ingressProvisioner(clients)
	p.Retry = retryStore
	p.Now = func() time.Time { return now }

	first, err := p.EnsureIngress(context.Background(), ingressInput())
	if err != nil || !hasCode(first.Problems, ProblemLoadBalancerAccountRestricted) {
		t.Fatalf("first refusal: ingress=%+v err=%v", first, err)
	}
	if retryStore.retry.Attempts != 1 || !retryStore.retry.RetryAfter.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("retry = %+v", retryStore.retry)
	}
	creates := countCallsWithPrefix(clients.elb.calls, "CreateLoadBalancer:")
	second, err := p.EnsureIngress(context.Background(), ingressInput())
	if err != nil || !hasCode(second.Problems, ProblemLoadBalancerAccountRestricted) {
		t.Fatalf("cooldown: ingress=%+v err=%v", second, err)
	}
	if got := countCallsWithPrefix(clients.elb.calls, "CreateLoadBalancer:"); got != creates {
		t.Fatalf("CreateLoadBalancer called during cooldown: %d -> %d", creates, got)
	}
	now = now.Add(5 * time.Minute)
	_, err = p.EnsureIngress(context.Background(), ingressInput())
	if err != nil || retryStore.retry.Attempts != 2 || !retryStore.retry.RetryAfter.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("second restriction: retry=%+v err=%v", retryStore.retry, err)
	}
	if got := countCallsWithPrefix(clients.elb.calls, "CreateLoadBalancer:"); got != creates+1 {
		t.Fatalf("CreateLoadBalancer after cooldown = %d, want %d", got, creates+1)
	}

	// Another actor (or an AWS response lost after creation) may have provided
	// the NLB. Discovery must win over the persisted cooldown.
	clients.elb.withLoadBalancer(LoadBalancerName(testVPC), ourTags(testVPC), []string{first.SecurityGroupID}, testSubnets)
	third, err := p.EnsureIngress(context.Background(), ingressInput())
	if err != nil || !third.Ready() {
		t.Fatalf("existing load balancer: ingress=%+v err=%v", third, err)
	}
	if retryStore.retry.Attempts != 0 {
		t.Fatalf("restriction was not cleared: %+v", retryStore.retry)
	}
}

func countCallsWithPrefix(calls []string, prefix string) int {
	n := 0
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func TestOtherOperationNotPermittedDoesNotEnterAccountRestrictionRetry(t *testing.T) {
	clients := newFakeClients()
	clients.elb.createLBErr = &elbtypes.OperationNotPermittedException{Message: aws.String("Security group not supported")}
	p := ingressProvisioner(clients)
	p.Retry = &fakeLoadBalancerRetryStore{}
	_, err := p.EnsureIngress(context.Background(), ingressInput())
	if err == nil {
		t.Fatal("unrelated OperationNotPermitted was swallowed")
	}
}

func TestEnsureIngressBuildsTheWholeDataPlane(t *testing.T) {
	clients := newFakeClients()
	p := ingressProvisioner(clients)

	got, err := p.EnsureIngress(context.Background(), ingressInput())
	if err != nil {
		t.Fatalf("EnsureIngress: %v", err)
	}
	if !got.Ready() {
		t.Fatalf("not ready: %+v", got.Problems)
	}
	if got.Tier != collectoringress.TierNetworkIdentities {
		t.Errorf("tier = %q, want the steady state", got.Tier)
	}
	if got.SecurityGroupID == "" || got.LoadBalancerARN == "" || len(got.TargetGroupARNs) != 2 {
		t.Fatalf("data plane = %+v", got)
	}
	if got.EndpointSource != EndpointSourceLoadBalancer {
		t.Errorf("endpoint source = %q, want %q", got.EndpointSource, EndpointSourceLoadBalancer)
	}
	// The published endpoint is the load balancer's DNS name on the OTLP gRPC port.
	if !strings.HasSuffix(got.Transport.GRPCEndpoint, ":4317") || !strings.Contains(got.Transport.GRPCEndpoint, LoadBalancerName(testVPC)) {
		t.Errorf("grpc endpoint = %q", got.Transport.GRPCEndpoint)
	}
	// No TLS mode set, so the hop is plaintext and the network rules are the whole boundary — reported
	// as Info, which is what it asks for rather than a fault.
	if !got.Transport.Insecure {
		t.Error("plaintext was not reported as insecure")
	}
	if !hasCode(got.Problems, "PlaintextTelemetry") {
		t.Errorf("problems = %v, want the plaintext posture reported", codes(got.Problems))
	}
}

func TestIngressFailureReturnsTheSecurityGroupForCheckpointing(t *testing.T) {
	clients := newFakeClients()
	clients.elb.createLBErr = errors.New("load balancer creation denied")
	got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), ingressInput())
	if err == nil || got.SecurityGroupID == "" || got.LoadBalancerARN != "" {
		t.Fatalf("partial ingress lost its created security group: %+v, %v", got, err)
	}
}

// TestTheSecurityGroupIsAttachedToTheLoadBalancerItProtects. The group has to exist first, because an
// NLB created without one can never have one added — so this ordering is forced, not chosen.
func TestTheSecurityGroupIsAttachedToTheLoadBalancerItProtects(t *testing.T) {
	clients := newFakeClients()
	p := ingressProvisioner(clients)

	got, err := p.EnsureIngress(context.Background(), ingressInput())
	if err != nil {
		t.Fatal(err)
	}
	lb := clients.elb.lbs[LoadBalancerName(testVPC)]
	if lb == nil {
		t.Fatal("no load balancer")
	}
	if !containsStringValue(lb.SecurityGroups, got.SecurityGroupID) {
		t.Fatalf("the load balancer's groups %v do not include the Collector's %q", lb.SecurityGroups, got.SecurityGroupID)
	}
	// And the group admits the health check from itself, which is how the load balancer — a member of
	// the same group — reaches 13133 on the task.
	if !containsString(clients.ec2.ingressKeys(got.SecurityGroupID), "tcp/13133-13133/sg:"+got.SecurityGroupID) {
		t.Errorf("ingress = %v, want the self-referencing health-check rule", clients.ec2.ingressKeys(got.SecurityGroupID))
	}
}

// TestEndpointOverrideBuildsNoLoadBalancer — the second acceptance criterion. The customer's own front
// replaces the NLB entirely, but the tasks still need a security group: their front sends to the tasks,
// so the rules are still the boundary.
func TestEndpointOverrideBuildsNoLoadBalancer(t *testing.T) {
	clients := newFakeClients()
	p := ingressProvisioner(clients)
	in := ingressInput()
	in.Transport.EndpointOverride = "https://collector.internal.example.com"

	got, err := p.EnsureIngress(context.Background(), in)
	if err != nil {
		t.Fatalf("EnsureIngress: %v", err)
	}
	if !got.Ready() {
		t.Fatalf("not ready: %+v", got.Problems)
	}
	if len(clients.elb.calls) != 0 {
		t.Fatalf("Elastic Load Balancing was called with an endpointOverride set: %v", clients.elb.calls)
	}
	if got.LoadBalancerARN != "" || len(got.TargetGroupARNs) != 0 {
		t.Errorf("reported load balancer objects that were not created: %+v", got)
	}
	if got.EndpointSource != EndpointSourceOverride {
		t.Errorf("endpoint source = %q, want %q", got.EndpointSource, EndpointSourceOverride)
	}
	if got.Transport.GRPCEndpoint != "collector.internal.example.com" {
		t.Errorf("grpc endpoint = %q, want the override's host", got.Transport.GRPCEndpoint)
	}
	// The security group is still built and still carries the rules: the override moves the front, not
	// the boundary.
	if got.SecurityGroupID == "" {
		t.Fatal("no security group with an endpointOverride; the customer's front still sends to the tasks")
	}
	if !containsString(clients.ec2.ingressKeys(got.SecurityGroupID), "tcp/4317-4317/sg:sg-app") {
		t.Errorf("ingress = %v", clients.ec2.ingressKeys(got.SecurityGroupID))
	}
}

// TestATLSModeThatContradictsTheOverrideWritesNothing. A TLS listener needs a load balancer to put the
// certificate on, and an endpointOverride means Odigos creates none — so this is a configuration error,
// caught before anything is created rather than after a load balancer exists with nothing pointing at it.
func TestATLSModeThatContradictsTheOverrideWritesNothing(t *testing.T) {
	clients := newFakeClients()
	p := ingressProvisioner(clients)
	in := ingressInput()
	in.Transport = collectoringress.TransportConfig{
		Mode:             collectoringress.TLSLoadBalancer,
		Hostname:         "collector.example.com",
		CertificateRef:   "arn:aws:acm:eu-west-1:123456789012:certificate/abc",
		EndpointOverride: "https://collector.example.com",
	}

	got, err := p.EnsureIngress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready() {
		t.Fatal("reported ready on a contradictory tls tree")
	}
	if !collectorstatus.HasError(got.Problems) {
		t.Fatalf("problems = %v, want an error", codes(got.Problems))
	}
	if len(clients.ec2.calls) != 0 || len(clients.elb.calls) != 0 {
		t.Errorf("wrote something for a configuration that cannot work: %v / %v", clients.ec2.calls, clients.elb.calls)
	}
}

// TestATLSModeReachesTheListeners: the certificate the customer named ends up on the data listeners,
// and only for the mode that puts it there.
func TestATLSModeReachesTheListeners(t *testing.T) {
	const certificate = "arn:aws:acm:eu-west-1:123456789012:certificate/abc"

	t.Run("loadBalancer mode", func(t *testing.T) {
		clients := newFakeClients()
		in := ingressInput()
		in.Transport = collectoringress.TransportConfig{
			Mode: collectoringress.TLSLoadBalancer, Hostname: "collector.example.com", CertificateRef: certificate,
		}
		got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), in)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Ready() {
			t.Fatalf("not ready: %v", codes(got.Problems))
		}
		l := clients.elb.listenerOn(LoadBalancerName(testVPC), 4317)
		if len(l.Certificates) != 1 || aws.ToString(l.Certificates[0].CertificateArn) != certificate {
			t.Errorf("listener certificates = %v", l.Certificates)
		}
		// The published endpoint is the hostname the certificate is for, not the load balancer's own
		// generated name — nothing can issue a certificate for that.
		if !strings.HasPrefix(got.Transport.GRPCEndpoint, "collector.example.com:") {
			t.Errorf("grpc endpoint = %q", got.Transport.GRPCEndpoint)
		}
	})

	// `collector` mode terminates TLS inside the Collector, so the listeners must stay TCP and pass the
	// encrypted stream through. A TLS listener there would terminate the customer's session at the load
	// balancer and re-open a plaintext one behind it.
	t.Run("collector mode leaves the listeners TCP", func(t *testing.T) {
		clients := newFakeClients()
		in := ingressInput()
		in.Transport = collectoringress.TransportConfig{
			Mode: collectoringress.TLSCollector, Hostname: "collector.example.com",
			CertificateRef: "odigos/collector/prod/eu-west-1/vpc-0abc/tls",
		}
		if _, err := ingressProvisioner(clients).EnsureIngress(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		l := clients.elb.listenerOn(LoadBalancerName(testVPC), 4317)
		if len(l.Certificates) > 0 {
			t.Errorf("the load balancer terminates TLS in collector mode: %v", l.Certificates)
		}
	})
}

// TestARefusedIngressDecisionWritesNothingAndLeavesAnExistingGroupAlone. the refusals exist so a
// network whose senders Odigos cannot identify gets NO Collector rather than one the whole network can
// write to — and narrowing an existing group to nothing on a refusal would take a working Collector
// offline, which is the opposite of what the refusal protects against.
func TestARefusedIngressDecisionWritesNothingAndLeavesAnExistingGroupAlone(t *testing.T) {
	clients := newFakeClients()
	existing := []ec2types.IpPermission{{
		IpProtocol: aws.String("tcp"), FromPort: aws.Int32(4317), ToPort: aws.Int32(4317),
		UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-app")}},
	}}
	clients.ec2.withGroup(SecurityGroupName(testVPC), ourTags(testVPC), existing)

	in := ingressInput()
	// No group resolved, and the fallback is switched off — the "policy forbids widening" case.
	in.Sources = []awsingress.Source{{Name: "api"}}
	in.Config.AllowNetworkCIDRFallback = false

	got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready() {
		t.Fatal("reported ready on a refused ingress decision")
	}
	if !collectorstatus.HasError(got.Problems) {
		t.Fatalf("problems = %v, want an error", codes(got.Problems))
	}
	for _, call := range append(clients.ec2.calls, clients.elb.calls...) {
		for _, forbidden := range []string{"Create", "Authorize", "Revoke"} {
			if strings.HasPrefix(call, forbidden) {
				t.Errorf("a refused decision called %s", call)
			}
		}
	}
	if len(clients.ec2.ingressKeys("sg-1")) != 1 {
		t.Errorf("the existing group's rules were changed: %v", clients.ec2.ingressKeys("sg-1"))
	}
}

// TestSourceFreeCollectorGetsAnEndpointButNoOTLPIngress covers the user-visible lifecycle order:
// create Collector, publish its managed destination, then mark sources. Before that final step the
// load balancer must be healthy, but neither OTLP port may admit a sender.
func TestSourceFreeCollectorGetsAnEndpointButNoOTLPIngress(t *testing.T) {
	clients := newFakeClients()
	in := ingressInput()
	in.Sources = nil

	got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ready() {
		t.Fatalf("source-free ingress not ready: %+v", got.Problems)
	}
	if got.Tier != collectoringress.TierNone {
		t.Errorf("tier = %q, want none", got.Tier)
	}
	keys := clients.ec2.ingressKeys(got.SecurityGroupID)
	if len(keys) != 1 || !strings.HasPrefix(keys[0], "tcp/13133-13133/sg:") {
		t.Fatalf("ingress = %v, want only the load balancer health-check rule", keys)
	}
}

// TestTheWidenedFallbackIsReportedAndDeployed. A Collector no workload can reach drops every span
// silently, which is worse than a rule that is too wide and says so.
func TestTheWidenedFallbackIsReportedAndDeployed(t *testing.T) {
	clients := newFakeClients()
	in := ingressInput()
	in.Sources = []awsingress.Source{{Name: "api"}}
	in.UnresolvedCycles = awsingress.FallbackAfterCycles

	got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Ready() {
		t.Fatalf("not deployed: %v", codes(got.Problems))
	}
	if got.Tier != collectoringress.TierNetworkCIDR {
		t.Errorf("tier = %q, want the network-CIDR fallback", got.Tier)
	}
	if !hasCode(got.Problems, "IngressWidenedToNetwork") {
		t.Errorf("problems = %v, want the widening reported", codes(got.Problems))
	}
	if !containsString(clients.ec2.ingressKeys(got.SecurityGroupID), "tcp/4317-4317/cidr:10.0.0.0/16") {
		t.Errorf("ingress = %v, want the network block", clients.ec2.ingressKeys(got.SecurityGroupID))
	}
}

// TestDiscoveryIsWaitedForRatherThanWidenedAround. Every routed workload on these platforms HAS a
// security group, so zero resolved means discovery has not finished — and the answer to an incomplete
// discovery is to wait, not to admit the whole network.
func TestDiscoveryIsWaitedForRatherThanWidenedAround(t *testing.T) {
	clients := newFakeClients()
	in := ingressInput()
	in.Sources = []awsingress.Source{{Name: "api"}}
	in.UnresolvedCycles = awsingress.FallbackAfterCycles - 1

	got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.SecurityGroupID != "" || len(clients.ec2.calls) != 0 {
		t.Errorf("wrote a rule while discovery was still incomplete: %v", clients.ec2.calls)
	}
	if !hasCode(got.Problems, "IngressPendingDiscovery") {
		t.Errorf("problems = %v, want the pending report", codes(got.Problems))
	}
	// Pending, not an error: nothing is broken and it normally clears on its own.
	if collectorstatus.HasError(got.Problems) {
		t.Errorf("an incomplete discovery was reported as a fault: %v", got.Problems)
	}
}

// TestASecondIngressPassWritesNothing — resumable AND idempotent. Every object is found by name, so the
// second pass completes what the first left and changes nothing that is already right.
func TestASecondIngressPassWritesNothing(t *testing.T) {
	clients := newFakeClients()
	p := ingressProvisioner(clients)
	first, err := p.EnsureIngress(context.Background(), ingressInput())
	if err != nil {
		t.Fatal(err)
	}

	for pass := 0; pass < 4; pass++ {
		clients.ec2.calls, clients.elb.calls = nil, nil
		got, err := p.EnsureIngress(context.Background(), ingressInput())
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if got.SecurityGroupID != first.SecurityGroupID || got.LoadBalancerARN != first.LoadBalancerARN {
			t.Fatalf("pass %d produced different objects: %+v", pass, got)
		}
		if got.Authorized != 0 || got.Revoked != 0 {
			t.Errorf("pass %d authorized %d and revoked %d", pass, got.Authorized, got.Revoked)
		}
		for _, call := range append(clients.ec2.calls, clients.elb.calls...) {
			for _, forbidden := range []string{"Create", "Authorize", "Revoke", "SetSecurityGroups", "ModifyListener"} {
				if strings.HasPrefix(call, forbidden) {
					t.Errorf("pass %d called %s", pass, call)
				}
			}
		}
	}
}

// TestIngressResumesFromAnyPartialState walks the sequence one object at a time and asserts that a pass
// starting from each intermediate state completes — which is what "resumable creation" has to mean when
// four objects are created in order and the process can die between any two.
func TestIngressResumesFromAnyPartialState(t *testing.T) {
	seed := []struct {
		name string
		set  func(*fakeClients)
	}{
		{"nothing", func(*fakeClients) {}},
		{"security group only", func(c *fakeClients) {
			c.ec2.withGroup(SecurityGroupName(testVPC), ourTags(testVPC), nil)
		}},
		{"group and load balancer", func(c *fakeClients) {
			c.ec2.withGroup(SecurityGroupName(testVPC), ourTags(testVPC), nil)
			c.elb.withLoadBalancer(LoadBalancerName(testVPC), ourTags(testVPC), []string{"sg-1"}, testSubnets)
		}},
		{"one target group, no listeners", func(c *fakeClients) {
			c.ec2.withGroup(SecurityGroupName(testVPC), ourTags(testVPC), nil)
			c.elb.withLoadBalancer(LoadBalancerName(testVPC), ourTags(testVPC), []string{"sg-1"}, testSubnets)
			c.elb.withTargetGroup(TargetGroupName(testVPC, 4317), ourTags(testVPC))
		}},
		{"both target groups, one listener", func(c *fakeClients) {
			c.ec2.withGroup(SecurityGroupName(testVPC), ourTags(testVPC), nil)
			c.elb.withLoadBalancer(LoadBalancerName(testVPC), ourTags(testVPC), []string{"sg-1"}, testSubnets)
			c.elb.withTargetGroup(TargetGroupName(testVPC, 4317), ourTags(testVPC))
			c.elb.withTargetGroup(TargetGroupName(testVPC, 4318), ourTags(testVPC))
			lb := c.elb.lbs[LoadBalancerName(testVPC)]
			tg := c.elb.targetGroups[TargetGroupName(testVPC, 4317)]
			c.elb.listeners[aws.ToString(lb.LoadBalancerArn)] = []elbtypes.Listener{{
				ListenerArn:     aws.String("arn:listener/4317"),
				LoadBalancerArn: lb.LoadBalancerArn,
				Port:            aws.Int32(4317),
				Protocol:        elbtypes.ProtocolEnumTcp,
				DefaultActions: []elbtypes.Action{{
					Type: elbtypes.ActionTypeEnumForward, TargetGroupArn: tg.TargetGroupArn,
				}},
			}}
		}},
	}
	for _, tc := range seed {
		t.Run(tc.name, func(t *testing.T) {
			clients := newFakeClients()
			tc.set(clients)

			got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), ingressInput())
			if err != nil {
				t.Fatalf("EnsureIngress: %v", err)
			}
			if !got.Ready() {
				t.Fatalf("did not resume to a working data plane: %v", codes(got.Problems))
			}
			if len(got.TargetGroupARNs) != 2 {
				t.Errorf("target groups = %v", got.TargetGroupARNs)
			}
			for _, port := range collectoringress.DataPorts() {
				if clients.elb.listenerOn(LoadBalancerName(testVPC), int32(port)).ListenerArn == nil {
					t.Errorf("no listener on port %d", port)
				}
			}
			// And it does not build a second of anything.
			if len(clients.elb.lbs) != 1 {
				t.Errorf("load balancers = %d", len(clients.elb.lbs))
			}
			if len(clients.elb.targetGroups) != 2 {
				t.Errorf("target groups = %d", len(clients.elb.targetGroups))
			}
		})
	}
}

func TestManualModeBuildsNoIngress(t *testing.T) {
	clients := newFakeClients()
	in := ingressInput()
	in.Mode = spec.ModeManual

	got, err := ingressProvisioner(clients).EnsureIngress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ready() {
		t.Fatal("reported ready in manual mode")
	}
	if len(clients.ec2.calls) != 0 || len(clients.elb.calls) != 0 {
		t.Errorf("touched AWS in manual mode: %v / %v", clients.ec2.calls, clients.elb.calls)
	}
	if len(got.Problems) != 1 || got.Problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", got.Problems)
	}
}
