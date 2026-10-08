package deployer

import (
	"context"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	elb "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/smithy-go"
)

const testVPC = "vpc-0abc123def456789a"

// --- EC2 security groups ---

// duplicateGroupError is EC2's untyped duplicate-name error. EC2 reports errors as codes on a generic
// APIError rather than as distinct types, so the fake has to produce the same shape.
type duplicateGroupError struct{}

func (duplicateGroupError) Error() string                 { return "InvalidGroup.Duplicate" }
func (duplicateGroupError) ErrorCode() string             { return "InvalidGroup.Duplicate" }
func (duplicateGroupError) ErrorMessage() string          { return "duplicate group name" }
func (duplicateGroupError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

type fakeEC2SG struct {
	groups       map[string]*ec2types.SecurityGroup // key: vpc + "/" + name
	calls        []string
	descriptions []string
	// raceWinner, when set, is registered as an existing group by the first CreateSecurityGroup, which
	// then reports a duplicate name — what a competing writer looks like from here.
	raceWinner map[string]string
	raced      bool
	createErr  error
}

func newFakeEC2SG() *fakeEC2SG {
	return &fakeEC2SG{groups: map[string]*ec2types.SecurityGroup{}}
}

// withGroup seeds an existing group. openEgress mirrors what EC2 does on create.
func (f *fakeEC2SG) withGroup(name string, tags map[string]string, ingress []ec2types.IpPermission) *fakeEC2SG {
	f.groups[testVPC+"/"+name] = &ec2types.SecurityGroup{
		GroupId:             aws.String("sg-" + fmt.Sprint(len(f.groups)+1)),
		GroupName:           aws.String(name),
		VpcId:               aws.String(testVPC),
		Tags:                ec2Tags(tags),
		IpPermissions:       ingress,
		IpPermissionsEgress: []ec2types.IpPermission{{IpProtocol: aws.String("-1"), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}}},
	}
	return f
}

func (f *fakeEC2SG) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	var name, vpc string
	for _, filter := range in.Filters {
		switch aws.ToString(filter.Name) {
		case "group-name":
			if len(filter.Values) > 0 {
				name = filter.Values[0]
			}
		case "vpc-id":
			if len(filter.Values) > 0 {
				vpc = filter.Values[0]
			}
		}
	}
	f.calls = append(f.calls, "DescribeSecurityGroups:"+vpc+"/"+name)
	// Both filters are honoured, so a test can prove the lookup is scoped to the VPC: a group name is
	// unique only within one.
	if g, ok := f.groups[vpc+"/"+name]; ok {
		return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: []ec2types.SecurityGroup{*g}}, nil
	}
	return &ec2.DescribeSecurityGroupsOutput{}, nil
}

func (f *fakeEC2SG) CreateSecurityGroup(_ context.Context, in *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	name, vpc := aws.ToString(in.GroupName), aws.ToString(in.VpcId)
	f.calls = append(f.calls, "CreateSecurityGroup:"+vpc+"/"+name)
	f.descriptions = append(f.descriptions, aws.ToString(in.Description))
	if f.raceWinner != nil && !f.raced {
		f.raced = true
		f.withGroup(name, f.raceWinner, nil)
		return nil, duplicateGroupError{}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	tags := map[string]string{}
	for _, spec := range in.TagSpecifications {
		for _, t := range spec.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
	}
	f.withGroup(name, tags, nil)
	return &ec2.CreateSecurityGroupOutput{GroupId: f.groups[vpc+"/"+name].GroupId}, nil
}

func (f *fakeEC2SG) byID(id string) *ec2types.SecurityGroup {
	for _, g := range f.groups {
		if aws.ToString(g.GroupId) == id {
			return g
		}
	}
	return nil
}

func (f *fakeEC2SG) AuthorizeSecurityGroupIngress(_ context.Context, in *ec2.AuthorizeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error) {
	f.calls = append(f.calls, "AuthorizeIngress:"+describePermissions(in.IpPermissions))
	g := f.byID(aws.ToString(in.GroupId))
	if g == nil {
		return nil, fmt.Errorf("no such group %s", aws.ToString(in.GroupId))
	}
	g.IpPermissions = append(g.IpPermissions, in.IpPermissions...)
	return &ec2.AuthorizeSecurityGroupIngressOutput{}, nil
}

func (f *fakeEC2SG) RevokeSecurityGroupIngress(_ context.Context, in *ec2.RevokeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	f.calls = append(f.calls, "RevokeIngress:"+describePermissions(in.IpPermissions))
	g := f.byID(aws.ToString(in.GroupId))
	if g == nil {
		return nil, fmt.Errorf("no such group %s", aws.ToString(in.GroupId))
	}
	remove := map[string]bool{}
	for _, p := range in.IpPermissions {
		for _, src := range flattenPermission(p) {
			remove[src.key] = true
		}
	}
	var kept []ec2types.IpPermission
	for _, p := range g.IpPermissions {
		for _, src := range flattenPermission(p) {
			if !remove[src.key] {
				kept = append(kept, src.permission)
			}
		}
	}
	g.IpPermissions = kept
	return &ec2.RevokeSecurityGroupIngressOutput{}, nil
}

func (f *fakeEC2SG) AuthorizeSecurityGroupEgress(_ context.Context, in *ec2.AuthorizeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	f.calls = append(f.calls, "AuthorizeEgress:"+aws.ToString(in.GroupId))
	if g := f.byID(aws.ToString(in.GroupId)); g != nil {
		g.IpPermissionsEgress = append(g.IpPermissionsEgress, in.IpPermissions...)
	}
	return &ec2.AuthorizeSecurityGroupEgressOutput{}, nil
}

// ingressKeys renders a group's current ingress as sorted comparable keys.
func (f *fakeEC2SG) ingressKeys(id string) []string {
	g := f.byID(id)
	if g == nil {
		return nil
	}
	var out []string
	for _, p := range g.IpPermissions {
		for _, src := range flattenPermission(p) {
			out = append(out, src.key)
		}
	}
	sort.Strings(out)
	return out
}

func describePermissions(ps []ec2types.IpPermission) string {
	var keys []string
	for _, p := range ps {
		for _, src := range flattenPermission(p) {
			keys = append(keys, src.key)
		}
	}
	sort.Strings(keys)
	return fmt.Sprint(keys)
}

// --- ELBv2 ---

type fakeELB struct {
	lbs          map[string]*elbtypes.LoadBalancer // by name
	targetGroups map[string]*elbtypes.TargetGroup  // by name
	listeners    map[string][]elbtypes.Listener    // by load balancer ARN
	tags         map[string]map[string]string      // by ARN
	attributes   map[string]map[string]string      // target group ARN -> attributes
	calls        []string
	createLBErr  error
	nextID       int
}

func newFakeELB() *fakeELB {
	return &fakeELB{
		lbs:          map[string]*elbtypes.LoadBalancer{},
		targetGroups: map[string]*elbtypes.TargetGroup{},
		listeners:    map[string][]elbtypes.Listener{},
		tags:         map[string]map[string]string{},
		attributes:   map[string]map[string]string{},
	}
}

func (f *fakeELB) arn(kind, name string) string {
	f.nextID++
	return fmt.Sprintf("arn:aws:elasticloadbalancing:%s:%s:%s/%s/%d", testRegion, testAccount, kind, name, f.nextID)
}

// withLoadBalancer seeds an existing NLB. Empty securityGroups is the unrepairable case: an NLB created
// without one can never have one added.
func (f *fakeELB) withLoadBalancer(name string, tags map[string]string, securityGroups, subnets []string) *fakeELB {
	arn := f.arn("loadbalancer/net", name)
	zones := make([]elbtypes.AvailabilityZone, 0, len(subnets))
	for _, s := range subnets {
		zones = append(zones, elbtypes.AvailabilityZone{SubnetId: aws.String(s)})
	}
	f.lbs[name] = &elbtypes.LoadBalancer{
		LoadBalancerArn:   aws.String(arn),
		LoadBalancerName:  aws.String(name),
		DNSName:           aws.String(name + ".elb." + testRegion + ".amazonaws.com"),
		Type:              elbtypes.LoadBalancerTypeEnumNetwork,
		Scheme:            elbtypes.LoadBalancerSchemeEnumInternal,
		SecurityGroups:    securityGroups,
		AvailabilityZones: zones,
	}
	f.tags[arn] = tags
	return f
}

func (f *fakeELB) withTargetGroup(name string, tags map[string]string) *fakeELB {
	arn := f.arn("targetgroup", name)
	f.targetGroups[name] = &elbtypes.TargetGroup{
		TargetGroupArn:  aws.String(arn),
		TargetGroupName: aws.String(name),
	}
	f.tags[arn] = tags
	return f
}

func (f *fakeELB) DescribeLoadBalancers(_ context.Context, in *elb.DescribeLoadBalancersInput, _ ...func(*elb.Options)) (*elb.DescribeLoadBalancersOutput, error) {
	f.calls = append(f.calls, "DescribeLoadBalancers:"+fmt.Sprint(in.Names))
	var out []elbtypes.LoadBalancer
	for _, name := range in.Names {
		if lb, ok := f.lbs[name]; ok {
			out = append(out, *lb)
		}
	}
	if len(out) == 0 && len(in.Names) > 0 {
		return nil, &elbtypes.LoadBalancerNotFoundException{}
	}
	return &elb.DescribeLoadBalancersOutput{LoadBalancers: out}, nil
}

func (f *fakeELB) CreateLoadBalancer(_ context.Context, in *elb.CreateLoadBalancerInput, _ ...func(*elb.Options)) (*elb.CreateLoadBalancerOutput, error) {
	name := aws.ToString(in.Name)
	f.calls = append(f.calls, fmt.Sprintf("CreateLoadBalancer:%s/sgs=%v/subnets=%v/%s/%s", name, in.SecurityGroups, in.Subnets, in.Scheme, in.Type))
	if f.createLBErr != nil {
		return nil, f.createLBErr
	}
	if _, exists := f.lbs[name]; exists {
		return nil, &elbtypes.DuplicateLoadBalancerNameException{}
	}
	tags := map[string]string{}
	for _, t := range in.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	f.withLoadBalancer(name, tags, in.SecurityGroups, in.Subnets)
	// Recorded from the input rather than assumed, so a test can assert on what was actually asked for.
	// A fake that hardcoded "internal" would pass a scheme assertion whatever the code sent.
	f.lbs[name].Scheme = in.Scheme
	f.lbs[name].Type = in.Type
	f.lbs[name].IpAddressType = in.IpAddressType
	return &elb.CreateLoadBalancerOutput{LoadBalancers: []elbtypes.LoadBalancer{*f.lbs[name]}}, nil
}

func (f *fakeELB) SetSecurityGroups(_ context.Context, in *elb.SetSecurityGroupsInput, _ ...func(*elb.Options)) (*elb.SetSecurityGroupsOutput, error) {
	f.calls = append(f.calls, "SetSecurityGroups:"+fmt.Sprint(in.SecurityGroups))
	for _, lb := range f.lbs {
		if aws.ToString(lb.LoadBalancerArn) == aws.ToString(in.LoadBalancerArn) {
			lb.SecurityGroups = in.SecurityGroups
		}
	}
	return &elb.SetSecurityGroupsOutput{}, nil
}

func (f *fakeELB) DescribeTags(_ context.Context, in *elb.DescribeTagsInput, _ ...func(*elb.Options)) (*elb.DescribeTagsOutput, error) {
	f.calls = append(f.calls, "DescribeTags:"+fmt.Sprint(len(in.ResourceArns)))
	var out []elbtypes.TagDescription
	for _, arn := range in.ResourceArns {
		var tags []elbtypes.Tag
		for k, v := range f.tags[arn] {
			tags = append(tags, elbtypes.Tag{Key: aws.String(k), Value: aws.String(v)})
		}
		out = append(out, elbtypes.TagDescription{ResourceArn: aws.String(arn), Tags: tags})
	}
	return &elb.DescribeTagsOutput{TagDescriptions: out}, nil
}

func (f *fakeELB) DescribeTargetGroups(_ context.Context, in *elb.DescribeTargetGroupsInput, _ ...func(*elb.Options)) (*elb.DescribeTargetGroupsOutput, error) {
	f.calls = append(f.calls, "DescribeTargetGroups:"+fmt.Sprint(in.Names))
	if len(in.Names) > 0 {
		var out []elbtypes.TargetGroup
		for _, name := range in.Names {
			if tg, ok := f.targetGroups[name]; ok {
				out = append(out, *tg)
			}
		}
		if len(out) == 0 {
			return nil, &elbtypes.TargetGroupNotFoundException{}
		}
		return &elb.DescribeTargetGroupsOutput{TargetGroups: out}, nil
	}
	// By load balancer: only the groups a LISTENER forwards to, which is what the real API returns and
	// the reason this package looks them up by name instead.
	var out []elbtypes.TargetGroup
	for _, l := range f.listeners[aws.ToString(in.LoadBalancerArn)] {
		for _, a := range l.DefaultActions {
			for _, tg := range f.targetGroups {
				if aws.ToString(tg.TargetGroupArn) == aws.ToString(a.TargetGroupArn) {
					out = append(out, *tg)
				}
			}
		}
	}
	return &elb.DescribeTargetGroupsOutput{TargetGroups: out}, nil
}

func (f *fakeELB) CreateTargetGroup(_ context.Context, in *elb.CreateTargetGroupInput, _ ...func(*elb.Options)) (*elb.CreateTargetGroupOutput, error) {
	name := aws.ToString(in.Name)
	f.calls = append(f.calls, fmt.Sprintf("CreateTargetGroup:%s/port=%d/type=%s/hc=%s:%s",
		name, aws.ToInt32(in.Port), in.TargetType, in.HealthCheckProtocol, aws.ToString(in.HealthCheckPort)))
	if _, exists := f.targetGroups[name]; exists {
		return nil, &elbtypes.DuplicateTargetGroupNameException{}
	}
	tags := map[string]string{}
	for _, t := range in.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	f.withTargetGroup(name, tags)
	tg := f.targetGroups[name]
	tg.Port, tg.Protocol, tg.TargetType = in.Port, in.Protocol, in.TargetType
	tg.HealthCheckProtocol, tg.HealthCheckPort = in.HealthCheckProtocol, in.HealthCheckPort
	tg.VpcId = in.VpcId
	return &elb.CreateTargetGroupOutput{TargetGroups: []elbtypes.TargetGroup{*tg}}, nil
}

func (f *fakeELB) ModifyTargetGroupAttributes(_ context.Context, in *elb.ModifyTargetGroupAttributesInput, _ ...func(*elb.Options)) (*elb.ModifyTargetGroupAttributesOutput, error) {
	arn := aws.ToString(in.TargetGroupArn)
	f.calls = append(f.calls, "ModifyTargetGroupAttributes:"+arn)
	if f.attributes[arn] == nil {
		f.attributes[arn] = map[string]string{}
	}
	for _, a := range in.Attributes {
		f.attributes[arn][aws.ToString(a.Key)] = aws.ToString(a.Value)
	}
	return &elb.ModifyTargetGroupAttributesOutput{}, nil
}

func (f *fakeELB) DescribeListeners(_ context.Context, in *elb.DescribeListenersInput, _ ...func(*elb.Options)) (*elb.DescribeListenersOutput, error) {
	arn := aws.ToString(in.LoadBalancerArn)
	f.calls = append(f.calls, "DescribeListeners:"+arn)
	return &elb.DescribeListenersOutput{Listeners: f.listeners[arn]}, nil
}

func (f *fakeELB) CreateListener(_ context.Context, in *elb.CreateListenerInput, _ ...func(*elb.Options)) (*elb.CreateListenerOutput, error) {
	lbARN := aws.ToString(in.LoadBalancerArn)
	f.calls = append(f.calls, fmt.Sprintf("CreateListener:%d/%s", aws.ToInt32(in.Port), in.Protocol))
	listener := elbtypes.Listener{
		ListenerArn:     aws.String(f.arn("listener/net", fmt.Sprint(aws.ToInt32(in.Port)))),
		LoadBalancerArn: in.LoadBalancerArn,
		Port:            in.Port,
		Protocol:        in.Protocol,
		Certificates:    in.Certificates,
		DefaultActions:  in.DefaultActions,
	}
	f.listeners[lbARN] = append(f.listeners[lbARN], listener)
	return &elb.CreateListenerOutput{Listeners: []elbtypes.Listener{listener}}, nil
}

func (f *fakeELB) ModifyListener(_ context.Context, in *elb.ModifyListenerInput, _ ...func(*elb.Options)) (*elb.ModifyListenerOutput, error) {
	f.calls = append(f.calls, fmt.Sprintf("ModifyListener:%d/%s", aws.ToInt32(in.Port), in.Protocol))
	for lbARN, listeners := range f.listeners {
		for i := range listeners {
			if aws.ToString(listeners[i].ListenerArn) == aws.ToString(in.ListenerArn) {
				listeners[i].Protocol = in.Protocol
				listeners[i].Certificates = in.Certificates
				listeners[i].DefaultActions = in.DefaultActions
				f.listeners[lbARN] = listeners
			}
		}
	}
	return &elb.ModifyListenerOutput{}, nil
}

// listenerOn returns the listener on a port of the one load balancer, or the zero value.
func (f *fakeELB) listenerOn(lbName string, port int32) elbtypes.Listener {
	lb, ok := f.lbs[lbName]
	if !ok {
		return elbtypes.Listener{}
	}
	for _, l := range f.listeners[aws.ToString(lb.LoadBalancerArn)] {
		if aws.ToInt32(l.Port) == port {
			return l
		}
	}
	return elbtypes.Listener{}
}
