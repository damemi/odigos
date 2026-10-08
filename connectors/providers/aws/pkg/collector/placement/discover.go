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

// EC2Reader is the read-only slice of EC2 this package needs, and the whole of it: the three calls
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
func Discover(ctx context.Context, cache *awsclient.Cache, regions []string) (Topology, error) {
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

// DiscoverRegion describes one region's VPCs, subnets and route tables and classifies each subnet's
// egress.
//
// All three listings must succeed. A partial one would classify subnets whose route tables were not read
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
	log.Info("collector placement topology scanned", "region", region, "vpcs", len(vpcs), "subnets", len(subnets))
	return NewTopology([]string{region}, vpcs, subnets), nil
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
				AvailabilityZone: aws.ToString(s.AvailabilityZone),
				AvailableIPs:     aws.ToInt32(s.AvailableIpAddressCount),
				Egress:           subnetEgress(id, vpcID, tables),
			})
		}
	}
	return out, nil
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
