package deployer

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	awsingress "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/ingress"
)

// SecurityGroupClient is the slice of EC2 this package writes through. The Describe call it needs is
// declared under the MANAGEMENT capability, which every deployment also has.
type SecurityGroupClient interface {
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
	CreateSecurityGroup(ctx context.Context, in *ec2.CreateSecurityGroupInput, opts ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error)
	AuthorizeSecurityGroupIngress(ctx context.Context, in *ec2.AuthorizeSecurityGroupIngressInput, opts ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupIngressOutput, error)
	RevokeSecurityGroupIngress(ctx context.Context, in *ec2.RevokeSecurityGroupIngressInput, opts ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error)
	AuthorizeSecurityGroupEgress(ctx context.Context, in *ec2.AuthorizeSecurityGroupEgressInput, opts ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error)
}

// SecurityGroupInput is what converging one regional Collector's security group depends on.
type SecurityGroupInput struct {
	Region      string
	NetworkID   string
	VPCID       string
	Connector   string
	ConnectorID string
	Identity    collectorownership.Identity
	Mode        string
	// Rules is the COMPLETE desired ingress set from collectoringress.Resolve. Not a delta, and not a
	// set this package adds to: puts every authorization decision in that one function so a
	// provider cannot quietly widen or narrow it.
	Rules []awsingress.Rule
}

// SecurityGroup is the converged group.
type SecurityGroup struct {
	ID string
	// Authorized and Revoked are what this pass changed, for reporting. Both zero is the steady state.
	Authorized int
	Revoked    int
}

// EnsureSecurityGroup converges the Collector's own security group to exactly the rules it should have.
//
// This group is the entire security boundary in front of the Collector in v1: the OTLP ports carry no
// authentication, and anything that can write to them forwards telemetry under the customer's own
// destination credential. So the interesting behaviour is not the authorize — it is the REVOKE.
// `collectoringress.Resolve` returns the complete desired set precisely so that converging to it takes
// a widened network-CIDR fallback back down the moment a workload's security group resolves, with no
// separate cleanup pass that could be forgotten or fail silently. A rule that is not in the desired set
// is removed, including one added by hand: `ingress.additionalSecurityGroupIds` and
// `additionalCidrs` are the sanctioned way to admit another sender, and they are inputs to the desired
// set rather than exceptions to it.
//
// Egress is left open, and re-opened if somebody closed it. The Collector has to reach the destination
// and the OpAMP endpoint; a Collector that can be written to and cannot export is the worst of both.
func EnsureSecurityGroup(ctx context.Context, api SecurityGroupClient, in SecurityGroupInput) (SecurityGroup, []collectorstatus.Problem, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return SecurityGroup{}, problems, nil
	}

	name := SecurityGroupName(in.NetworkID)
	tags := baseapi.CollectorManagedTags(in.Connector, in.ConnectorID, in.Region, in.NetworkID)

	group, problems, err := findOrCreateSecurityGroup(ctx, api, in, name, tags)
	if err != nil || group == nil {
		return SecurityGroup{}, problems, err
	}

	out := SecurityGroup{ID: aws.ToString(group.GroupId)}
	desired := desiredPermissions(in.Rules, out.ID)
	actual := group.IpPermissions

	// Authorize before revoke. If the pass dies between the two, the group is momentarily wider than
	// intended rather than narrower — a Collector that admits one stale sender for a few seconds is
	// recoverable, and one that admits nobody has dropped every span on the network in the meantime.
	if add := permissionsMissing(desired, actual); len(add) > 0 {
		if _, err := api.AuthorizeSecurityGroupIngress(ctx, &ec2.AuthorizeSecurityGroupIngressInput{
			GroupId: group.GroupId, IpPermissions: add,
		}); err != nil {
			return out, problems, fmt.Errorf("authorize ingress on %s: %w", name, err)
		}
		out.Authorized = len(add)
	}
	if remove := permissionsMissing(actual, desired); len(remove) > 0 {
		if _, err := api.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
			GroupId: group.GroupId, IpPermissions: remove,
		}); err != nil {
			return out, problems, fmt.Errorf("revoke ingress on %s: %w", name, err)
		}
		out.Revoked = len(remove)
	}
	if out.Authorized > 0 || out.Revoked > 0 {
		log.Info("converged collector ingress rules",
			"network", in.NetworkID, "group", out.ID, "authorized", out.Authorized, "revoked", out.Revoked)
	}

	if len(group.IpPermissionsEgress) == 0 {
		// A group created by EC2 always starts with allow-all egress, so an empty set means somebody
		// removed it. Restored rather than reported: the Collector cannot reach the destination or the
		// OpAMP endpoint without it, and it would run, accept telemetry and export nothing.
		if _, err := api.AuthorizeSecurityGroupEgress(ctx, &ec2.AuthorizeSecurityGroupEgressInput{
			GroupId:       group.GroupId,
			IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("-1"), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}}},
		}); err != nil {
			return out, problems, fmt.Errorf("reopen egress on %s: %w", name, err)
		}
		log.Info("restored collector egress", "network", in.NetworkID, "group", out.ID)
	}
	return out, problems, nil
}

// findOrCreateSecurityGroup resolves the group by name within the VPC, creating it if absent.
//
// By NAME rather than by a recorded id, and that is what makes a half-finished pass resumable: a group
// created by a run that died before it could record the id is found by the next run instead of being
// created twice. The tags go on in the CreateSecurityGroup call itself for the same reason — a group
// created and then not tagged would fail its own ownership check on every later pass, and be refused
// for ever.
func findOrCreateSecurityGroup(ctx context.Context, api SecurityGroupClient, in SecurityGroupInput, name string, tags map[string]string) (*ec2types.SecurityGroup, []collectorstatus.Problem, error) {
	found, err := describeSecurityGroup(ctx, api, name, in.VPCID)
	if err != nil {
		return nil, nil, err
	}
	if found != nil {
		object := collectorownership.Object{
			Ref:  aws.ToString(found.GroupId),
			Tags: baseapi.ParseCollectorTags(ec2TagMap(found.Tags)),
		}
		if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
			return nil, []collectorstatus.Problem{notOurs("security group", name, object, in.Identity)}, nil
		}
		return found, nil, nil
	}

	created, err := api.CreateSecurityGroup(ctx, &ec2.CreateSecurityGroupInput{
		GroupName:   aws.String(name),
		VpcId:       aws.String(in.VPCID),
		Description: aws.String("Odigos managed Collector - telemetry ingress for " + in.NetworkID),
		TagSpecifications: []ec2types.TagSpecification{{
			ResourceType: ec2types.ResourceTypeSecurityGroup,
			Tags:         ec2Tags(tags),
		}},
	})
	if err == nil {
		log.Info("created collector security group", "network", in.NetworkID, "group", aws.ToString(created.GroupId))
		// Re-describe rather than synthesising the group from the create response: the egress rule EC2
		// adds by default is not in it, and the converge below decides what to do from the actual state.
		return describeRequiredSecurityGroup(ctx, api, name, in.VPCID)
	}
	if !isDuplicateSecurityGroup(err) {
		return nil, nil, fmt.Errorf("create security group %s in %s: %w", name, in.VPCID, err)
	}
	// Lost a race with another replica, or with a customer who happens to use this name. Re-read: the
	// two are indistinguishable from here, and only one of them may be written to.
	found, err = describeSecurityGroup(ctx, api, name, in.VPCID)
	if err != nil {
		return nil, nil, err
	}
	if found == nil {
		return nil, nil, fmt.Errorf("security group %s in %s reported as duplicate but cannot be read back", name, in.VPCID)
	}
	object := collectorownership.Object{Ref: aws.ToString(found.GroupId), Tags: baseapi.ParseCollectorTags(ec2TagMap(found.Tags))}
	if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
		return nil, []collectorstatus.Problem{notOurs("security group", name, object, in.Identity)}, nil
	}
	return found, nil, nil
}

// describeSecurityGroup finds a group by name within a VPC, or nil.
//
// Filtered on both, because a security group NAME is unique only within its VPC: a filter on the name
// alone would return this connector's groups from every VPC in the region, and acting on the first
// would converge the wrong network's rules.
func describeSecurityGroup(ctx context.Context, api SecurityGroupClient, name, vpcID string) (*ec2types.SecurityGroup, error) {
	out, err := api.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []ec2types.Filter{
			{Name: aws.String("group-name"), Values: []string{name}},
			{Name: aws.String("vpc-id"), Values: []string{vpcID}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("describe security group %s in %s: %w", name, vpcID, err)
	}
	for i := range out.SecurityGroups {
		if aws.ToString(out.SecurityGroups[i].GroupName) == name {
			return &out.SecurityGroups[i], nil
		}
	}
	return nil, nil
}

func describeRequiredSecurityGroup(ctx context.Context, api SecurityGroupClient, name, vpcID string) (*ec2types.SecurityGroup, []collectorstatus.Problem, error) {
	found, err := describeSecurityGroup(ctx, api, name, vpcID)
	if err != nil {
		return nil, nil, err
	}
	if found == nil {
		return nil, nil, fmt.Errorf("security group %s in %s was created but cannot be read back", name, vpcID)
	}
	return found, nil, nil
}

// desiredPermissions turns the domain's rules into EC2 permissions, one per rule.
//
// One permission per rule rather than grouping several sources under one port: EC2 accepts both, and
// the ungrouped form is what makes the comparison below a set comparison over comparable values
// instead of a merge. selfID is the Collector's own group, which a Self rule names — the load balancer
// shares this group, so that is how the health check on 13133 is admitted and the only rule
// this package resolves rather than passes through.
func desiredPermissions(rules []awsingress.Rule, selfID string) []ec2types.IpPermission {
	out := make([]ec2types.IpPermission, 0, len(rules))
	for _, r := range rules {
		p := ec2types.IpPermission{
			IpProtocol: aws.String("tcp"),
			FromPort:   aws.Int32(int32(r.Port)),
			ToPort:     aws.Int32(int32(r.Port)),
		}
		switch {
		case r.Self:
			p.UserIdGroupPairs = []ec2types.UserIdGroupPair{{GroupId: aws.String(selfID)}}
		case r.SecurityGroupID != "":
			p.UserIdGroupPairs = []ec2types.UserIdGroupPair{{GroupId: aws.String(r.SecurityGroupID)}}
		case r.CIDR != "":
			p.IpRanges = []ec2types.IpRange{{CidrIp: aws.String(r.CIDR)}}
		default:
			continue
		}
		out = append(out, p)
	}
	return out
}

// permissionSource is one flattened permission: a port and exactly ONE source, with a comparable key.
//
// Flattening is what makes the comparison correct against reality rather than against how a rule was
// written. EC2 returns the rules it stored, grouped however it chose, so comparing grouped shapes would
// see differences that are not differences — authorizing a rule that already exists (harmless) and, far
// worse, revoking one that should stay. Carrying the rebuilt single-source permission alongside its key
// also means a grouped rule read back from EC2 can be revoked one source at a time.
type permissionSource struct {
	key        string
	permission ec2types.IpPermission
}

func flattenPermission(p ec2types.IpPermission) []permissionSource {
	prefix := fmt.Sprintf("%s/%d-%d/", aws.ToString(p.IpProtocol), aws.ToInt32(p.FromPort), aws.ToInt32(p.ToPort))
	base := func() ec2types.IpPermission {
		return ec2types.IpPermission{IpProtocol: p.IpProtocol, FromPort: p.FromPort, ToPort: p.ToPort}
	}
	var out []permissionSource
	for _, g := range p.UserIdGroupPairs {
		one := base()
		one.UserIdGroupPairs = []ec2types.UserIdGroupPair{{GroupId: g.GroupId}}
		out = append(out, permissionSource{prefix + "sg:" + aws.ToString(g.GroupId), one})
	}
	for _, r := range p.IpRanges {
		one := base()
		one.IpRanges = []ec2types.IpRange{{CidrIp: r.CidrIp}}
		out = append(out, permissionSource{prefix + "cidr:" + aws.ToString(r.CidrIp), one})
	}
	for _, r := range p.Ipv6Ranges {
		one := base()
		one.Ipv6Ranges = []ec2types.Ipv6Range{{CidrIpv6: r.CidrIpv6}}
		out = append(out, permissionSource{prefix + "cidr6:" + aws.ToString(r.CidrIpv6), one})
	}
	for _, l := range p.PrefixListIds {
		one := base()
		one.PrefixListIds = []ec2types.PrefixListId{{PrefixListId: l.PrefixListId}}
		out = append(out, permissionSource{prefix + "pl:" + aws.ToString(l.PrefixListId), one})
	}
	if len(out) == 0 {
		// A permission with a port and no source at all. Keyed so it is still comparable, and therefore
		// revocable: EC2 will not have stored one, but a rule whose only source was deleted can read
		// back this way and would otherwise be invisible to the converge.
		out = append(out, permissionSource{prefix + "none", base()})
	}
	return out
}

// permissionsMissing returns the sources of want that is does not already cover, one permission each,
// in a stable order.
func permissionsMissing(want, is []ec2types.IpPermission) []ec2types.IpPermission {
	have := map[string]bool{}
	for _, p := range is {
		for _, src := range flattenPermission(p) {
			have[src.key] = true
		}
	}
	seen := map[string]bool{}
	var keys []string
	byKey := map[string]ec2types.IpPermission{}
	for _, p := range want {
		for _, src := range flattenPermission(p) {
			if have[src.key] || seen[src.key] {
				continue
			}
			seen[src.key] = true
			byKey[src.key] = src.permission
			keys = append(keys, src.key)
		}
	}
	sort.Strings(keys)
	out := make([]ec2types.IpPermission, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

func ec2TagMap(tags []ec2types.Tag) map[string]string {
	out := make(map[string]string, len(tags))
	for _, t := range tags {
		out[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return out
}

func ec2Tags(tags map[string]string) []ec2types.Tag {
	out := make([]ec2types.Tag, 0, len(tags))
	for _, k := range sortedKeys(tags) {
		out = append(out, ec2types.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

// isDuplicateSecurityGroup matches EC2's duplicate-name error, which has no typed form in the SDK —
// EC2 reports errors as codes on a generic APIError rather than as distinct types the way IAM and
// Secrets Manager do.
func isDuplicateSecurityGroup(err error) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == "InvalidGroup.Duplicate"
}
