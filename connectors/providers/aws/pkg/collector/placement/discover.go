package placement

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
)

var log = connlog.Subsystem("aws-collector-placement")

// EC2Reader is the read-only slice of EC2 this package needs, and the whole of it: the five calls
// declared for aws.fargate-collector under the MANAGEMENT capability, so placement runs in manual
// mode too — a customer who deploys the Collector themselves still gets told which network it serves and
// whether its subnets can reach anything.
//
// An interface rather than *ec2.Client so the assembly below is testable without a network. It is the
// route-table association fallback that earns the seam: an implicit association names no subnet, so a
// subnet's effective table is usually the VPC's main one, and reading that wrong turns a healthy account
// into "no usable subnets anywhere".
type EC2Reader interface {
	DescribeVpcs(ctx context.Context, in *ec2.DescribeVpcsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error)
	DescribeSubnets(ctx context.Context, in *ec2.DescribeSubnetsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error)
	DescribeRouteTables(ctx context.Context, in *ec2.DescribeRouteTablesInput, opts ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error)
	DescribeVpcPeeringConnections(ctx context.Context, in *ec2.DescribeVpcPeeringConnectionsInput, opts ...func(*ec2.Options)) (*ec2.DescribeVpcPeeringConnectionsOutput, error)
	DescribeSecurityGroups(ctx context.Context, in *ec2.DescribeSecurityGroupsInput, opts ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error)
}

// Discover builds the topology for a set of regions through the shared config cache.
//
// A region that fails to list is LEFT OUT of the returned topology rather than returned empty, and the
// error names it. That distinction is the whole reason Topology tracks its regions: an empty region and
// an unlistable one are indistinguishable in the values, and treating the second as the first reports
// every source there as running on a network Odigos could not find. Other regions still produce a plan —
// the networks are independent by construction, and one region's throttling must not hold the fleet
// unconverged.
//
// Narrowing regions to the ones that actually have enabled sources is the caller's call and the cheap
// default; passing every enabled region is correct and costs three describes per region per pass.
func Discover(ctx context.Context, cache awsclient.ConfigSource, regions []string) (Topology, error) {
	topo := NewTopology(nil, nil, nil)
	var errs []error
	for _, region := range regions {
		cfg, err := cache.Config(ctx, region)
		if err != nil {
			errs = append(errs, fmt.Errorf("resolve aws config for %s: %w", region, err))
			continue
		}
		regional, err := DiscoverRegion(ctx, ec2.NewFromConfig(cfg), region)
		if err != nil {
			errs = append(errs, fmt.Errorf("describe network topology in %s: %w", region, err))
			log.Error("collector placement topology scan failed", "region", region, "error", err)
			continue
		}
		topo = topo.Merge(regional)
	}
	return topo, errors.Join(errs...)
}

// DiscoverRegion describes one region's VPCs, subnets, route tables and active direct peerings, and
// classifies each subnet's egress.
//
// All five listings must succeed. A partial one would classify subnets whose route tables were not read
// as having no egress, which reads as a refusal — Odigos declining to deploy a Collector into a
// perfectly reachable subnet, with a message telling the customer to add a NAT gateway they already
// have. Failing the region instead leaves it out and says why.
func DiscoverRegion(ctx context.Context, api EC2Reader, region string) (Topology, error) {
	vpcs, err := describeVPCs(ctx, api, region)
	if err != nil {
		return Topology{}, fmt.Errorf("describe vpcs: %w", err)
	}
	tables, err := describeRouteTables(ctx, api)
	if err != nil {
		return Topology{}, fmt.Errorf("describe route tables: %w", err)
	}
	subnets, err := describeSubnets(ctx, api, region, tables)
	if err != nil {
		return Topology{}, fmt.Errorf("describe subnets: %w", err)
	}
	peerings, err := describePeerings(ctx, api, region)
	if err != nil {
		return Topology{}, fmt.Errorf("describe vpc peerings: %w", err)
	}
	securityGroups, err := describeSecurityGroups(ctx, api, region)
	if err != nil {
		return Topology{}, fmt.Errorf("describe security groups: %w", err)
	}
	log.Info("collector placement topology scanned", "region", region, "vpcs", len(vpcs), "subnets", len(subnets), "peerings", len(peerings), "security_groups", len(securityGroups))
	return NewTopologyWithSecurityGroups([]string{region}, vpcs, subnets, peerings, securityGroups), nil
}

func describeVPCs(ctx context.Context, api EC2Reader, region string) ([]VPC, error) {
	var out []VPC
	paginator := ec2.NewDescribeVpcsPaginator(api, &ec2.DescribeVpcsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range page.Vpcs {
			out = append(out, VPC{
				Region:     region,
				ID:         aws.ToString(v.VpcId),
				CIDRBlocks: vpcCIDRs(v),
				IsDefault:  aws.ToBool(v.IsDefault),
			})
		}
	}
	return out, nil
}

func describeRouteTables(ctx context.Context, api EC2Reader) ([]ec2types.RouteTable, error) {
	var out []ec2types.RouteTable
	paginator := ec2.NewDescribeRouteTablesPaginator(api, &ec2.DescribeRouteTablesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, page.RouteTables...)
	}
	return out, nil
}

func describeSubnets(ctx context.Context, api EC2Reader, region string, tables []ec2types.RouteTable) ([]Subnet, error) {
	var out []Subnet
	paginator := ec2.NewDescribeSubnetsPaginator(api, &ec2.DescribeSubnetsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, s := range page.Subnets {
			id, vpcID := aws.ToString(s.SubnetId), aws.ToString(s.VpcId)
			out = append(out, Subnet{
				Region:           region,
				ID:               id,
				VPCID:            vpcID,
				CIDRBlock:        aws.ToString(s.CidrBlock),
				AvailabilityZone: aws.ToString(s.AvailabilityZone),
				AvailableIPs:     aws.ToInt32(s.AvailableIpAddressCount),
				Egress:           subnetEgress(id, vpcID, tables),
				Routes:           subnetRoutes(id, vpcID, tables),
			})
		}
	}
	return out, nil
}

func describePeerings(ctx context.Context, api EC2Reader, region string) ([]Peering, error) {
	var out []Peering
	paginator := ec2.NewDescribeVpcPeeringConnectionsPaginator(api, &ec2.DescribeVpcPeeringConnectionsInput{
		Filters: []ec2types.Filter{{
			Name:   aws.String("status-code"),
			Values: []string{string(ec2types.VpcPeeringConnectionStateReasonCodeActive)},
		}},
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, p := range page.VpcPeeringConnections {
			if p.Status == nil || p.Status.Code != ec2types.VpcPeeringConnectionStateReasonCodeActive {
				continue
			}
			requester, accepter := p.RequesterVpcInfo, p.AccepterVpcInfo
			if requester == nil || accepter == nil {
				continue
			}
			vpc1, vpc2 := aws.ToString(requester.VpcId), aws.ToString(accepter.VpcId)
			if vpc1 == "" || vpc2 == "" {
				continue
			}
			region1, region2 := aws.ToString(requester.Region), aws.ToString(accepter.Region)
			if region1 == "" {
				region1 = region
			}
			if region2 == "" {
				region2 = region
			}
			// The regional API can expose inter-region peerings from either side. They are not eligible
			// for this direct same-region proof, so retain only peerings wholly in the scanned region.
			if region1 != region || region2 != region {
				continue
			}
			if id := aws.ToString(p.VpcPeeringConnectionId); id != "" {
				out = append(out, Peering{ID: id, VPC1: vpc1, VPC2: vpc2, Region1: region1, Region2: region2})
			}
		}
	}
	return out, nil
}

func describeSecurityGroups(ctx context.Context, api EC2Reader, region string) ([]SecurityGroup, error) {
	var out []SecurityGroup
	paginator := ec2.NewDescribeSecurityGroupsPaginator(api, &ec2.DescribeSecurityGroupsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, group := range page.SecurityGroups {
			id, vpcID := aws.ToString(group.GroupId), aws.ToString(group.VpcId)
			if id == "" || vpcID == "" {
				continue
			}
			out = append(out, SecurityGroup{Region: region, ID: id, VPCID: vpcID, Ingress: securityGroupIngress(group.IpPermissions)})
		}
	}
	return out, nil
}

func securityGroupIngress(permissions []ec2types.IpPermission) []SecurityGroupRule {
	out := make([]SecurityGroupRule, 0, len(permissions))
	for _, permission := range permissions {
		rule := SecurityGroupRule{
			Protocol:   aws.ToString(permission.IpProtocol),
			FromPort:   aws.ToInt32(permission.FromPort),
			ToPort:     aws.ToInt32(permission.ToPort),
			PortsKnown: permission.FromPort != nil && permission.ToPort != nil,
		}
		if rule.Protocol == "-1" {
			rule.PortsKnown = true
		}
		for _, pair := range permission.UserIdGroupPairs {
			if id := aws.ToString(pair.GroupId); id != "" {
				rule.SourceSecurityGroupIDs = append(rule.SourceSecurityGroupIDs, id)
			} else {
				rule.Incomplete = true
			}
		}
		for _, cidr := range permission.IpRanges {
			if value := aws.ToString(cidr.CidrIp); value != "" {
				rule.SourceCIDRs = append(rule.SourceCIDRs, value)
			} else {
				rule.Incomplete = true
			}
		}
		for _, cidr := range permission.Ipv6Ranges {
			if value := aws.ToString(cidr.CidrIpv6); value != "" {
				rule.SourceCIDRs = append(rule.SourceCIDRs, value)
			} else {
				rule.Incomplete = true
			}
		}
		for _, prefix := range permission.PrefixListIds {
			if aws.ToString(prefix.PrefixListId) == "" {
				rule.Incomplete = true
			}
		}
		if rule.Protocol == "" || (!rule.PortsKnown && rule.Protocol != "-1") {
			rule.Incomplete = true
		}
		out = append(out, rule)
	}
	return out
}

// vpcCIDRs returns a VPC's IPv4 blocks, the primary first.
//
// CidrBlockAssociationSet holds every block including the primary, but only in an associated state that
// has to be filtered — a disassociated block is still listed, and authorizing ingress from a CIDR the
// VPC no longer owns would open the Collector to whoever holds it next. CidrBlock is the primary
// and is used as the fallback for an account where the association set comes back empty.
func vpcCIDRs(v ec2types.Vpc) []string {
	var out []string
	for _, a := range v.CidrBlockAssociationSet {
		if a.CidrBlockState != nil && a.CidrBlockState.State != ec2types.VpcCidrBlockStateCodeAssociated {
			continue
		}
		if c := aws.ToString(a.CidrBlock); c != "" {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		if c := aws.ToString(v.CidrBlock); c != "" {
			out = append(out, c)
		}
	}
	return out
}
