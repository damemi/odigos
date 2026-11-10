package placement

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// fakeEC2 serves canned pages. Each slice is one page, so a multi-page listing is expressed by giving
// more than one — which is what proves the paginators are actually being drained rather than the first
// page being taken for the whole account.
type fakeEC2 struct {
	vpcPages           [][]ec2types.Vpc
	subnetPages        [][]ec2types.Subnet
	tablePages         [][]ec2types.RouteTable
	peeringPages       [][]ec2types.VpcPeeringConnection
	securityGroupPages [][]ec2types.SecurityGroup

	vpcErr, subnetErr, tableErr, peeringErr, securityGroupErr error

	vpcCalls, subnetCalls, tableCalls, peeringCalls, securityGroupCalls int
}

func page[T any](pages [][]T, token *string) ([]T, *string) {
	i := 0
	if token != nil {
		i = int((*token)[0] - '0')
	}
	if i >= len(pages) {
		return nil, nil
	}
	var next *string
	if i+1 < len(pages) {
		next = aws.String(string(rune('0' + i + 1)))
	}
	return pages[i], next
}

func (f *fakeEC2) DescribeVpcs(_ context.Context, in *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	f.vpcCalls++
	if f.vpcErr != nil {
		return nil, f.vpcErr
	}
	vpcs, next := page(f.vpcPages, in.NextToken)
	return &ec2.DescribeVpcsOutput{Vpcs: vpcs, NextToken: next}, nil
}

func (f *fakeEC2) DescribeSubnets(_ context.Context, in *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	f.subnetCalls++
	if f.subnetErr != nil {
		return nil, f.subnetErr
	}
	subnets, next := page(f.subnetPages, in.NextToken)
	return &ec2.DescribeSubnetsOutput{Subnets: subnets, NextToken: next}, nil
}

func (f *fakeEC2) DescribeRouteTables(_ context.Context, in *ec2.DescribeRouteTablesInput, _ ...func(*ec2.Options)) (*ec2.DescribeRouteTablesOutput, error) {
	f.tableCalls++
	if f.tableErr != nil {
		return nil, f.tableErr
	}
	tables, next := page(f.tablePages, in.NextToken)
	return &ec2.DescribeRouteTablesOutput{RouteTables: tables, NextToken: next}, nil
}

func (f *fakeEC2) DescribeVpcPeeringConnections(_ context.Context, in *ec2.DescribeVpcPeeringConnectionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcPeeringConnectionsOutput, error) {
	f.peeringCalls++
	if f.peeringErr != nil {
		return nil, f.peeringErr
	}
	peerings, next := page(f.peeringPages, in.NextToken)
	return &ec2.DescribeVpcPeeringConnectionsOutput{VpcPeeringConnections: peerings, NextToken: next}, nil
}

func (f *fakeEC2) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	f.securityGroupCalls++
	if f.securityGroupErr != nil {
		return nil, f.securityGroupErr
	}
	groups, next := page(f.securityGroupPages, in.NextToken)
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: groups, NextToken: next}, nil
}

func awsSubnet(id, vpcID, zone string, freeIPs int32) ec2types.Subnet {
	return ec2types.Subnet{
		SubnetId:                aws.String(id),
		VpcId:                   aws.String(vpcID),
		AvailabilityZone:        aws.String(zone),
		AvailableIpAddressCount: aws.Int32(freeIPs),
	}
}

func awsPeering(id, vpc1, vpc2, region1, region2 string, state ec2types.VpcPeeringConnectionStateReasonCode) ec2types.VpcPeeringConnection {
	return ec2types.VpcPeeringConnection{
		VpcPeeringConnectionId: aws.String(id),
		RequesterVpcInfo:       &ec2types.VpcPeeringConnectionVpcInfo{VpcId: aws.String(vpc1), Region: aws.String(region1)},
		AccepterVpcInfo:        &ec2types.VpcPeeringConnectionVpcInfo{VpcId: aws.String(vpc2), Region: aws.String(region2)},
		Status:                 &ec2types.VpcPeeringConnectionStateReason{Code: state},
	}
}

func TestDiscoverRegionRetainsOnlyActiveSameRegionPeeringsAndEffectiveRoutes(t *testing.T) {
	api := &fakeEC2{
		vpcPages:    [][]ec2types.Vpc{{{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16")}, {VpcId: aws.String("vpc-2"), CidrBlock: aws.String("10.1.0.0/16")}}},
		subnetPages: [][]ec2types.Subnet{{func() ec2types.Subnet {
			subnet := awsSubnet("subnet-1", "vpc-1", "eu-west-1a", 10)
			subnet.CidrBlock = aws.String("10.0.1.0/24")
			return subnet
		}()}},
		tablePages:  [][]ec2types.RouteTable{{table("rtb-1", "vpc-1", []ec2types.Route{route("10.1.0.0/16", func(r *ec2types.Route) { r.VpcPeeringConnectionId = aws.String("pcx-active") })}, assocMain())}},
		peeringPages: [][]ec2types.VpcPeeringConnection{
			{awsPeering("pcx-active", "vpc-1", "vpc-2", "eu-west-1", "eu-west-1", ec2types.VpcPeeringConnectionStateReasonCodeActive), awsPeering("pcx-pending", "vpc-1", "vpc-2", "eu-west-1", "eu-west-1", ec2types.VpcPeeringConnectionStateReasonCodePendingAcceptance)},
			{awsPeering("pcx-cross-region", "vpc-1", "vpc-2", "eu-west-1", "us-east-1", ec2types.VpcPeeringConnectionStateReasonCodeActive)},
		},
		securityGroupPages: [][]ec2types.SecurityGroup{{{
			GroupId: aws.String("sg-collector"), VpcId: aws.String("vpc-1"),
			IpPermissions: []ec2types.IpPermission{{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(4317), ToPort: aws.Int32(4318), UserIdGroupPairs: []ec2types.UserIdGroupPair{{GroupId: aws.String("sg-app")}}}},
		}}},
	}
	topo, err := DiscoverRegion(context.Background(), api, "eu-west-1")
	if err != nil {
		t.Fatalf("DiscoverRegion: %v", err)
	}
	peerings := topo.Peerings()
	if len(peerings) != 1 || peerings[0].ID != "pcx-active" {
		t.Fatalf("peerings = %+v, want only active same-region peering", peerings)
	}
	subnet, ok := topo.Subnet("subnet-1")
	if !ok || len(subnet.Routes) != 1 || subnet.Routes[0].PeeringID != "pcx-active" || subnet.CIDRBlock != "10.0.1.0/24" {
		t.Fatalf("effective subnet routes = %+v, want the main table's peering route", subnet.Routes)
	}
	group, ok := topo.SecurityGroup("sg-collector")
	if !ok || len(group.Ingress) != 1 || len(group.Ingress[0].SourceSecurityGroupIDs) != 1 || group.Ingress[0].SourceSecurityGroupIDs[0] != "sg-app" {
		t.Fatalf("security group ingress = %+v, want retained OTLP SG permission", group)
	}
}

func TestDiscoverRegionClassifiesEverySubnet(t *testing.T) {
	api := &fakeEC2{
		vpcPages: [][]ec2types.Vpc{
			{{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16"), IsDefault: aws.Bool(true)}},
			{{VpcId: aws.String("vpc-2"), CidrBlock: aws.String("10.1.0.0/16")}},
		},
		subnetPages: [][]ec2types.Subnet{
			{awsSubnet("subnet-priv", "vpc-1", "eu-west-1a", 200), awsSubnet("subnet-pub", "vpc-1", "eu-west-1b", 8)},
			{awsSubnet("subnet-other", "vpc-2", "eu-west-1a", 1)},
		},
		tablePages: [][]ec2types.RouteTable{
			{table("rtb-priv", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toNAT)}, assocSubnet("subnet-priv"))},
			{
				table("rtb-1-main", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toIGW)}, assocMain()),
				table("rtb-2-main", "vpc-2", nil, assocMain()),
			},
		},
	}

	topo, err := DiscoverRegion(context.Background(), api, "eu-west-1")
	if err != nil {
		t.Fatalf("DiscoverRegion: %v", err)
	}
	if !topo.Scanned("eu-west-1") || topo.Scanned("us-east-1") {
		t.Errorf("scanned regions = %v", topo.Regions())
	}
	if len(topo.VPCs()) != 2 {
		t.Fatalf("vpcs = %v, want both pages drained", topo.VPCs())
	}

	cases := map[string]struct {
		egress  Egress
		freeIPs int32
		vpc     string
	}{
		// Explicitly associated with the NAT table.
		"subnet-priv": {EgressNAT, 200, "vpc-1"},
		// No explicit association, so vpc-1's main table (internet gateway) applies.
		"subnet-pub": {EgressPublic, 8, "vpc-1"},
		// vpc-2's main table has no default route.
		"subnet-other": {EgressNone, 1, "vpc-2"},
	}
	for id, want := range cases {
		got, ok := topo.Subnet(id)
		if !ok {
			t.Errorf("%s missing from the topology", id)
			continue
		}
		if got.Egress != want.egress || got.AvailableIPs != want.freeIPs || got.VPCID != want.vpc {
			t.Errorf("%s = %+v, want egress %q, %d free, %s", id, got, want.egress, want.freeIPs, want.vpc)
		}
		if got.Region != "eu-west-1" {
			t.Errorf("%s carries region %q", id, got.Region)
		}
	}
}

// TestDiscoverRegionFailsWholeRatherThanPartial. A partial listing would classify subnets whose route
// tables were not read as having no egress — which reads as a refusal, and the refusal tells the
// customer to add a NAT gateway they already have.
func TestDiscoverRegionFailsWholeRatherThanPartial(t *testing.T) {
	boom := errors.New("throttled")
	for _, tc := range []struct {
		name string
		api  *fakeEC2
		want string
	}{
		{"vpcs", &fakeEC2{vpcErr: boom}, "describe vpcs"},
		{"route tables", &fakeEC2{tableErr: boom}, "describe route tables"},
		{"subnets", &fakeEC2{subnetErr: boom}, "describe subnets"},
		{"vpc peerings", &fakeEC2{peeringErr: boom}, "describe vpc peerings"},
		{"security groups", &fakeEC2{securityGroupErr: boom}, "describe security groups"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			topo, err := DiscoverRegion(context.Background(), tc.api, "eu-west-1")
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.want) || !errors.Is(err, boom) {
				t.Errorf("error = %v, want it to name %q and wrap the cause", err, tc.want)
			}
			// Nothing usable comes back, and above all the region is NOT marked scanned: a scanned
			// region with no VPCs is a valid account shape, and this must not look like one.
			if topo.Scanned("eu-west-1") {
				t.Error("a failed region is marked scanned; every workload in it would report as being on an unknown network")
			}
		})
	}
}

// TestDiscoverSkipsAFailedRegionAndKeepsTheRest. The networks are independent by construction, so one
// region's throttling must not hold the fleet unconverged — and the error still names the region.
func TestDiscoverSkipsAFailedRegionAndKeepsTheRest(t *testing.T) {
	ok := &fakeEC2{
		vpcPages:    [][]ec2types.Vpc{{{VpcId: aws.String("vpc-1"), CidrBlock: aws.String("10.0.0.0/16")}}},
		subnetPages: [][]ec2types.Subnet{{awsSubnet("subnet-a", "vpc-1", "eu-west-1a", 10)}},
		tablePages:  [][]ec2types.RouteTable{{table("rtb", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toNAT)}, assocMain())}},
	}
	good, err := DiscoverRegion(context.Background(), ok, "eu-west-1")
	if err != nil {
		t.Fatalf("DiscoverRegion: %v", err)
	}
	bad, err := DiscoverRegion(context.Background(), &fakeEC2{vpcErr: errors.New("denied")}, "us-east-1")
	if err == nil {
		t.Fatal("expected the second region to fail")
	}

	merged := good.Merge(bad)
	if !merged.Scanned("eu-west-1") {
		t.Error("the successful region is not scanned")
	}
	if merged.Scanned("us-east-1") {
		t.Error("the failed region is scanned")
	}
	if _, ok := merged.Subnet("subnet-a"); !ok {
		t.Error("the successful region's subnets were lost")
	}
}

// TestVPCCIDRsDropDisassociatedBlocks. Ingress fallback authorizes senders by the network's
// CIDR, so authorizing one the VPC no longer owns would open the Collector to whoever holds it next.
func TestVPCCIDRsDropDisassociatedBlocks(t *testing.T) {
	v := ec2types.Vpc{
		VpcId:     aws.String("vpc-1"),
		CidrBlock: aws.String("10.0.0.0/16"),
		CidrBlockAssociationSet: []ec2types.VpcCidrBlockAssociation{
			{CidrBlock: aws.String("10.0.0.0/16"), CidrBlockState: &ec2types.VpcCidrBlockState{State: ec2types.VpcCidrBlockStateCodeAssociated}},
			{CidrBlock: aws.String("10.9.0.0/16"), CidrBlockState: &ec2types.VpcCidrBlockState{State: ec2types.VpcCidrBlockStateCodeDisassociated}},
			{CidrBlock: aws.String("10.8.0.0/16")}, // no state reported: kept
		},
	}
	if got, want := vpcCIDRs(v), []string{"10.0.0.0/16", "10.8.0.0/16"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cidrs = %v, want %v", got, want)
	}

	// An account whose association set comes back empty still has a primary block.
	bare := ec2types.Vpc{VpcId: aws.String("vpc-2"), CidrBlock: aws.String("172.31.0.0/16")}
	if got, want := vpcCIDRs(bare), []string{"172.31.0.0/16"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cidrs = %v, want the primary block as the fallback", got)
	}
}
