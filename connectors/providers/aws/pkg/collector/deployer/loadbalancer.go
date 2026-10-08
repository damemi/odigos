package deployer

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// Target group attributes, and the first of them is the one this whole design rests on.
const (
	// attrPreserveClientIP makes the target see the SENDING WORKLOAD's address rather than the load
	// balancer's, which is what lets the tier-1 rules work at all.
	//
	// AWS defaults it to DISABLED for an `ip` target group on TCP or TLS — read that again, because it
	// is the trap. With it disabled, traffic reaching a Collector task arrives from the load balancer's
	// own network interfaces, so a rule admitting the WORKLOADS' security groups matches nothing: every
	// workload's telemetry is dropped by the group Odigos just wrote, and the Collector reports healthy
	// throughout. Enabling it makes the source address the workload's, so EC2 resolves it to the
	// workload's interface and the group reference matches.
	//
	// The alternative — a self-referencing rule on 4317/4318 so the load balancer's own interfaces are
	// admitted — is deliberately NOT taken: it would be an authorization rule this package invented,
	// and puts every one of those in collectoringress.Resolve so no provider can widen the
	// boundary on its own.
	attrPreserveClientIP = "preserve_client_ip.enabled"
	// attrCrossZone spreads a network's telemetry across every zone the Collector runs in. Off by
	// default on a Network Load Balancer, so a zone holding no task would blackhole the share of
	// traffic that resolved to it.
	attrCrossZone = "load_balancing.cross_zone.enabled"
)

// LoadBalancerClient is the slice of ELBv2 this package uses. The Describe calls are declared under the
// MANAGEMENT capability — manual mode reads the same objects to report an endpoint — and the writes
// under DEPLOYMENT.
type LoadBalancerClient interface {
	DescribeLoadBalancers(ctx context.Context, in *elb.DescribeLoadBalancersInput, opts ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error)
	CreateLoadBalancer(ctx context.Context, in *elb.CreateLoadBalancerInput, opts ...func(*elb.Options)) (*elb.CreateLoadBalancerOutput, error)
	SetSecurityGroups(ctx context.Context, in *elb.SetSecurityGroupsInput, opts ...func(*elb.Options)) (*elb.SetSecurityGroupsOutput, error)
	DescribeTags(ctx context.Context, in *elb.DescribeTagsInput, opts ...func(*elb.Options)) (*elb.DescribeTagsOutput, error)
	DescribeTargetGroups(ctx context.Context, in *elb.DescribeTargetGroupsInput, opts ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error)
	CreateTargetGroup(ctx context.Context, in *elb.CreateTargetGroupInput, opts ...func(*elb.Options)) (*elb.CreateTargetGroupOutput, error)
	ModifyTargetGroupAttributes(ctx context.Context, in *elb.ModifyTargetGroupAttributesInput, opts ...func(*elb.Options)) (*elb.ModifyTargetGroupAttributesOutput, error)
	DescribeListeners(ctx context.Context, in *elb.DescribeListenersInput, opts ...func(*elb.Options)) (*elb.DescribeListenersOutput, error)
	CreateListener(ctx context.Context, in *elb.CreateListenerInput, opts ...func(*elb.Options)) (*elb.CreateListenerOutput, error)
	ModifyListener(ctx context.Context, in *elb.ModifyListenerInput, opts ...func(*elb.Options)) (*elb.ModifyListenerOutput, error)
}

// LoadBalancerInput is what converging one regional Collector's internal NLB depends on.
type LoadBalancerInput struct {
	Region      string
	NetworkID   string
	VPCID       string
	Connector   string
	ConnectorID string
	Identity    collectorownership.Identity
	Mode        string
	// Subnets are the subnets placement chose. Fixed at creation: changing an NLB's subnets is
	// disruptive and is reported as drift rather than applied.
	Subnets []string
	// SecurityGroupID must be non-empty. An NLB created without a security group can NEVER have one
	// added, and this one is the entire boundary in front of an unauthenticated OTLP port.
	SecurityGroupID string
	// TLSCertificateARN is set when tls.mode is loadBalancer, and turns the data listeners from TCP
	// into TLS.
	TLSCertificateARN string
}

// LoadBalancer is the converged data plane in front of one network's Collector.
type LoadBalancer struct {
	ARN string
	// DNSName is what workloads are pointed at, unless a TLS hostname or an override replaces it.
	DNSName string
	// TargetGroupARNs are the groups the ECS service registers its tasks into, in data-port order.
	TargetGroupARNs []string
}

// Complete reports whether the whole data plane exists.
func (l LoadBalancer) Complete() bool {
	return l.ARN != "" && l.DNSName != "" && len(l.TargetGroupARNs) == len(collectoringress.DataPorts())
}

// EnsureLoadBalancer converges the internal NLB, its target groups and its listeners.
//
// Resumable at every step, because it has to be: four objects are created in sequence and a pass can
// die between any two of them. Each is found by name (the load balancer) or through its parent (the
// target groups and listeners), never by an id this package recorded — so a run that created the load
// balancer and died leaves the next run completing it rather than creating a second one. Objects are
// tagged in their create call, not after, for the same reason: one created and not tagged would fail
// its own ownership check for ever.
//
// The one step that cannot be repaired is the security group attachment. An NLB created without one can
// never have one added, and recreating it changes its DNS name, which re-points every workload on the
// network — a fleet-wide rollout. So a load balancer found without a security group is REFUSED with
// that explanation rather than silently recreated or silently used.
func EnsureLoadBalancer(ctx context.Context, api LoadBalancerClient, in LoadBalancerInput) (LoadBalancer, []collectorstatus.Problem, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return LoadBalancer{}, problems, nil
	}
	if in.SecurityGroupID == "" {
		return LoadBalancer{}, []collectorstatus.Problem{{
			Code:     ProblemLoadBalancerUnprotected,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no load balancer is created for region %s: its security group is not ready, "+
				"and an NLB created without one can never have one added. The OTLP ports carry no "+
				"authentication, so the group is the whole boundary — creating the load balancer first and "+
				"protecting it later is not a thing that can be done", in.Region),
		}}, nil
	}
	if problems := CheckDataPlaneNames(in.NetworkID, in.Region); len(problems) > 0 {
		return LoadBalancer{}, problems, nil
	}

	tags := baseapi.CollectorManagedTags(in.Connector, in.ConnectorID, in.Region, in.NetworkID)

	lb, problems, err := findOrCreateLoadBalancer(ctx, api, in, tags)
	if err != nil || lb == nil {
		return LoadBalancer{}, problems, err
	}
	out := LoadBalancer{ARN: aws.ToString(lb.LoadBalancerArn), DNSName: aws.ToString(lb.DNSName)}

	groups, groupProblems, err := ensureTargetGroups(ctx, api, in, tags)
	problems = append(problems, groupProblems...)
	if err != nil || len(groupProblems) > 0 {
		return out, problems, err
	}
	out.TargetGroupARNs = groups

	if err := ensureListeners(ctx, api, in, out.ARN, groups, tags); err != nil {
		return out, problems, err
	}
	return out, problems, nil
}

// findOrCreateLoadBalancer resolves the NLB by name, creating it if absent.
func findOrCreateLoadBalancer(ctx context.Context, api LoadBalancerClient, in LoadBalancerInput, tags map[string]string) (*elbtypes.LoadBalancer, []collectorstatus.Problem, error) {
	name := LoadBalancerName(in.NetworkID)

	found, err := describeLoadBalancer(ctx, api, name)
	if err != nil {
		return nil, nil, err
	}
	if found != nil {
		object, err := taggedObject(ctx, api, aws.ToString(found.LoadBalancerArn))
		if err != nil {
			return nil, nil, err
		}
		if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
			return nil, []collectorstatus.Problem{notOurs("load balancer", name, object, in.Identity)}, nil
		}
		problems, err := reconcileLoadBalancer(ctx, api, in, found)
		if err != nil || len(problems) > 0 {
			return nil, problems, err
		}
		return found, nil, nil
	}

	created, err := api.CreateLoadBalancer(ctx, &elb.CreateLoadBalancerInput{
		Name:   aws.String(name),
		Type:   elbtypes.LoadBalancerTypeEnumNetwork,
		Scheme: elbtypes.LoadBalancerSchemeEnumInternal,
		// Internal load balancers must be ipv4, and the Collector reaches nothing over IPv6 anyway.
		IpAddressType: elbtypes.IpAddressTypeIpv4,
		Subnets:       in.Subnets,
		// Attached HERE and nowhere else. This is the one-shot decision that describes.
		SecurityGroups: []string{in.SecurityGroupID},
		Tags:           elbTags(tags),
	})
	if err == nil {
		log.Info("created collector load balancer", "network", in.NetworkID, "name", name)
		if len(created.LoadBalancers) == 0 {
			return nil, nil, fmt.Errorf("create load balancer %s returned no load balancer", name)
		}
		return &created.LoadBalancers[0], nil, nil
	}
	if !isDuplicateLoadBalancerName(err) {
		return nil, nil, fmt.Errorf("create load balancer %s: %w", name, err)
	}
	// Lost a race, or the name is a customer's. Re-read: only one of those may be used.
	found, err = describeLoadBalancer(ctx, api, name)
	if err != nil {
		return nil, nil, err
	}
	if found == nil {
		return nil, nil, fmt.Errorf("load balancer %s reported as a duplicate name but cannot be read back", name)
	}
	object, err := taggedObject(ctx, api, aws.ToString(found.LoadBalancerArn))
	if err != nil {
		return nil, nil, err
	}
	if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
		return nil, []collectorstatus.Problem{notOurs("load balancer", name, object, in.Identity)}, nil
	}
	problems, err := reconcileLoadBalancer(ctx, api, in, found)
	if err != nil || len(problems) > 0 {
		return nil, problems, err
	}
	return found, nil, nil
}

// reconcileLoadBalancer checks what an existing NLB can and cannot be brought into line on.
//
// Its security groups can be changed (as long as it has at least one) and its subnets cannot — so the
// first is applied and the second is reported. Reporting rather than recreating is the whole point: the
// DNS name is what every workload on the network points at, and replacing the load balancer to fix a
// subnet list would re-point all of them.
func reconcileLoadBalancer(ctx context.Context, api LoadBalancerClient, in LoadBalancerInput, lb *elbtypes.LoadBalancer) ([]collectorstatus.Problem, error) {
	name := aws.ToString(lb.LoadBalancerName)
	if len(lb.SecurityGroups) == 0 {
		return []collectorstatus.Problem{{
			Code:     ProblemLoadBalancerUnprotected,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("the load balancer %q in front of the Collector on %s has no security group, "+
				"and an NLB created without one can never have one added — so its unauthenticated OTLP ports "+
				"are reachable from the whole network and Odigos cannot fix it in place. Nothing is converged "+
				"here. Deleting it lets Odigos recreate it correctly, which changes its DNS name and therefore "+
				"re-points every workload on the network, so it is your call to make",
				name, in.NetworkID),
		}}, nil
	}
	if !containsStringValue(lb.SecurityGroups, in.SecurityGroupID) {
		groups := append([]string{in.SecurityGroupID}, lb.SecurityGroups...)
		if _, err := api.SetSecurityGroups(ctx, &elb.SetSecurityGroupsInput{
			LoadBalancerArn: lb.LoadBalancerArn,
			SecurityGroups:  groups,
		}); err != nil {
			return nil, fmt.Errorf("attach the collector security group to %s: %w", name, err)
		}
		log.Info("attached the collector security group to an existing load balancer", "network", in.NetworkID, "name", name)
	}

	if drift := subnetsMissing(lb, in.Subnets); len(drift) > 0 {
		return []collectorstatus.Problem{{
			Code:     ProblemLoadBalancerSubnetDrift,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the load balancer %q is not in %v, which placement chose for the Collector in region "+
				"%s. Odigos does not change an existing load balancer's subnets: the zones it answers in are "+
				"part of what its DNS name resolves to, and moving them is disruptive. It keeps working in the "+
				"zones it has; delete it to have Odigos rebuild it, or name the subnets it already uses in "+
				"collector.regions.%s.subnetIds", name, drift, in.Region, in.Region),
		}}, nil
	}
	return nil, nil
}

func describeLoadBalancer(ctx context.Context, api LoadBalancerClient, name string) (*elbtypes.LoadBalancer, error) {
	out, err := api.DescribeLoadBalancers(ctx, &elb.DescribeLoadBalancersInput{Names: []string{name}})
	if err != nil {
		var notFound *elbtypes.LoadBalancerNotFoundException
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("describe load balancer %s: %w", name, err)
	}
	for i := range out.LoadBalancers {
		if aws.ToString(out.LoadBalancers[i].LoadBalancerName) == name {
			return &out.LoadBalancers[i], nil
		}
	}
	return nil, nil
}

// taggedObject reads an ELBv2 object's tags for the ownership decision.
//
// A separate call, because neither DescribeLoadBalancers nor DescribeTargetGroups returns tags — which
// is the whole reason elasticloadbalancing:DescribeTags is in the permission set.
func taggedObject(ctx context.Context, api LoadBalancerClient, arn string) (collectorownership.Object, error) {
	out, err := api.DescribeTags(ctx, &elb.DescribeTagsInput{ResourceArns: []string{arn}})
	if err != nil {
		return collectorownership.Object{}, fmt.Errorf("describe tags of %s: %w", arn, err)
	}
	tags := map[string]string{}
	for _, d := range out.TagDescriptions {
		for _, t := range d.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	return collectorownership.Object{Ref: arn, Tags: baseapi.ParseCollectorTags(tags)}, nil
}

// ensureTargetGroups converges one target group per data port, in data-port order.
//
// Found by NAME, not through the load balancer, and that is a resumability requirement rather than a
// preference: DescribeTargetGroups against a load balancer returns only the groups a LISTENER
// forwards to, so a pass that created a target group and died before creating its listener would leave
// the next pass unable to see it — and CreateTargetGroup would then fail with a duplicate name, for
// ever. Looking it up by name finds exactly what the previous pass left behind.
//
// The cost of name lookup is that a target group with this name might be a customer's, so its tags are
// checked the same way the load balancer's are. Pointing our listener at their group would register the
// Collector's tasks as targets of their load balancer.
func ensureTargetGroups(ctx context.Context, api LoadBalancerClient, in LoadBalancerInput, tags map[string]string) ([]string, []collectorstatus.Problem, error) {
	var out []string

	for _, port := range collectoringress.DataPorts() {
		name := TargetGroupName(in.NetworkID, port)

		tg, err := describeTargetGroup(ctx, api, name)
		if err != nil {
			return nil, nil, err
		}
		if tg == nil {
			created, err := api.CreateTargetGroup(ctx, &elb.CreateTargetGroupInput{
				Name:     aws.String(name),
				Protocol: elbtypes.ProtocolEnumTcp,
				Port:     aws.Int32(int32(port)),
				VpcId:    aws.String(in.VPCID),
				// `ip` rather than `instance`: a Fargate task has no instance to register, only an ENI.
				TargetType: elbtypes.TargetTypeEnumIp,
				// TCP against 13133, the health_check extension — NOT traffic-port, which is the default
				// and would health-check the OTLP port instead. An OTLP receiver accepts a bare TCP
				// connection whether or not the pipeline behind it works, so traffic-port would report a
				// Collector healthy while its exporter was failing.
				HealthCheckEnabled:  aws.Bool(true),
				HealthCheckProtocol: elbtypes.ProtocolEnumTcp,
				HealthCheckPort:     aws.String(fmt.Sprint(collectoringress.PortHealth)),
				Tags:                elbTags(tags),
			})
			if err != nil {
				return nil, nil, fmt.Errorf("create target group %s: %w", name, err)
			}
			if len(created.TargetGroups) == 0 {
				return nil, nil, fmt.Errorf("create target group %s returned no target group", name)
			}
			tg = &created.TargetGroups[0]
			log.Info("created collector target group", "network", in.NetworkID, "name", name, "port", port)
		} else {
			object, err := taggedObject(ctx, api, aws.ToString(tg.TargetGroupArn))
			if err != nil {
				return nil, nil, err
			}
			if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
				return nil, []collectorstatus.Problem{notOurs("target group", name, object, in.Identity)}, nil
			}
		}
		arn := aws.ToString(tg.TargetGroupArn)

		// Written every pass rather than only at creation. Both attributes default the wrong way for
		// this design, so a group created by an older version of this code — or edited by hand — is
		// repaired instead of quietly degrading a whole network's telemetry.
		if _, err := api.ModifyTargetGroupAttributes(ctx, &elb.ModifyTargetGroupAttributesInput{
			TargetGroupArn: aws.String(arn),
			Attributes: []elbtypes.TargetGroupAttribute{
				{Key: aws.String(attrPreserveClientIP), Value: aws.String("true")},
				{Key: aws.String(attrCrossZone), Value: aws.String("true")},
			},
		}); err != nil {
			return nil, nil, fmt.Errorf("set attributes on target group %s: %w", name, err)
		}
		out = append(out, arn)
	}
	return out, nil, nil
}

// describeTargetGroup finds a target group by name, or nil.
func describeTargetGroup(ctx context.Context, api LoadBalancerClient, name string) (*elbtypes.TargetGroup, error) {
	found, err := api.DescribeTargetGroups(ctx, &elb.DescribeTargetGroupsInput{Names: []string{name}})
	if err != nil {
		var notFound *elbtypes.TargetGroupNotFoundException
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("describe target group %s: %w", name, err)
	}
	for i := range found.TargetGroups {
		if aws.ToString(found.TargetGroups[i].TargetGroupName) == name {
			return &found.TargetGroups[i], nil
		}
	}
	return nil, nil
}

// ensureListeners converges one listener per data port, forwarding to that port's target group.
//
// TCP, or TLS carrying the customer's ACM certificate when `tls.mode` is loadBalancer. A listener whose
// protocol or certificate no longer matches is MODIFIED rather than replaced: deleting and recreating
// one leaves the port refusing connections in between, and a TLS mode change is a deliberate
// configuration change, not a reason to drop telemetry.
func ensureListeners(ctx context.Context, api LoadBalancerClient, in LoadBalancerInput, lbARN string, targetGroups []string, tags map[string]string) error {
	current, err := api.DescribeListeners(ctx, &elb.DescribeListenersInput{LoadBalancerArn: aws.String(lbARN)})
	if err != nil {
		var notFound *elbtypes.ListenerNotFoundException
		if !errors.As(err, &notFound) {
			return fmt.Errorf("describe the listeners of %s: %w", lbARN, err)
		}
	}
	existing := map[int32]elbtypes.Listener{}
	if current != nil {
		for _, l := range current.Listeners {
			existing[aws.ToInt32(l.Port)] = l
		}
	}

	protocol := elbtypes.ProtocolEnumTcp
	var certificates []elbtypes.Certificate
	if in.TLSCertificateARN != "" {
		protocol = elbtypes.ProtocolEnumTls
		certificates = []elbtypes.Certificate{{CertificateArn: aws.String(in.TLSCertificateARN)}}
	}

	for i, port := range collectoringress.DataPorts() {
		actions := []elbtypes.Action{{
			Type:           elbtypes.ActionTypeEnumForward,
			TargetGroupArn: aws.String(targetGroups[i]),
		}}
		listener, ok := existing[int32(port)]
		if !ok {
			if _, err := api.CreateListener(ctx, &elb.CreateListenerInput{
				LoadBalancerArn: aws.String(lbARN),
				Port:            aws.Int32(int32(port)),
				Protocol:        protocol,
				Certificates:    certificates,
				DefaultActions:  actions,
				Tags:            elbTags(tags),
			}); err != nil {
				return fmt.Errorf("create listener on port %d of %s: %w", port, lbARN, err)
			}
			log.Info("created collector listener", "network", in.NetworkID, "port", port, "protocol", protocol)
			continue
		}
		if listenerMatches(listener, protocol, in.TLSCertificateARN, targetGroups[i]) {
			continue
		}
		if _, err := api.ModifyListener(ctx, &elb.ModifyListenerInput{
			ListenerArn:    listener.ListenerArn,
			Port:           aws.Int32(int32(port)),
			Protocol:       protocol,
			Certificates:   certificates,
			DefaultActions: actions,
		}); err != nil {
			return fmt.Errorf("modify listener on port %d of %s: %w", port, lbARN, err)
		}
		log.Info("converged collector listener", "network", in.NetworkID, "port", port, "protocol", protocol)
	}
	return nil
}

// listenerMatches reports whether a listener already does what it should, so an unchanged pass issues
// no write. Compared rather than written unconditionally because a ModifyListener is a real change to a
// live listener, unlike a target-group attribute or a role policy.
func listenerMatches(l elbtypes.Listener, protocol elbtypes.ProtocolEnum, certificateARN, targetGroupARN string) bool {
	if l.Protocol != protocol {
		return false
	}
	if certificateARN != "" {
		var found bool
		for _, c := range l.Certificates {
			if aws.ToString(c.CertificateArn) == certificateARN {
				found = true
			}
		}
		if !found {
			return false
		}
	} else if len(l.Certificates) > 0 {
		return false
	}
	for _, a := range l.DefaultActions {
		if a.Type == elbtypes.ActionTypeEnumForward && aws.ToString(a.TargetGroupArn) == targetGroupARN {
			return true
		}
	}
	return false
}

// subnetsMissing lists the subnets placement chose that the load balancer is not in.
//
// Only the missing direction is reported. An NLB in EXTRA subnets is not drift worth a message: a
// customer who added a zone by hand made the Collector more available, and placement caps its own
// choice at three zones anyway.
func subnetsMissing(lb *elbtypes.LoadBalancer, want []string) []string {
	have := map[string]bool{}
	for _, az := range lb.AvailabilityZones {
		have[aws.ToString(az.SubnetId)] = true
	}
	var out []string
	for _, s := range want {
		if !have[s] {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func elbTags(tags map[string]string) []elbtypes.Tag {
	out := make([]elbtypes.Tag, 0, len(tags))
	for _, k := range sortedKeys(tags) {
		out = append(out, elbtypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

func containsStringValue(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func isDuplicateLoadBalancerName(err error) bool {
	var e *elbtypes.DuplicateLoadBalancerNameException
	return errors.As(err, &e)
}
