package placement

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

func route(dest string, target func(*ec2types.Route)) ec2types.Route {
	r := ec2types.Route{DestinationCidrBlock: aws.String(dest), State: ec2types.RouteStateActive}
	if target != nil {
		target(&r)
	}
	return r
}

func toIGW(r *ec2types.Route) { r.GatewayId = aws.String("igw-0123") }
func toNAT(r *ec2types.Route) { r.NatGatewayId = aws.String("nat-0123") }
func toTGW(r *ec2types.Route) { r.TransitGatewayId = aws.String("tgw-0123") }
func toVGW(r *ec2types.Route) { r.GatewayId = aws.String("vgw-0123") }
func toPCX(r *ec2types.Route) { r.VpcPeeringConnectionId = aws.String("pcx-0123") }
func toENI(r *ec2types.Route) { r.NetworkInterfaceId = aws.String("eni-0123") }

func table(id, vpcID string, routes []ec2types.Route, assocs ...ec2types.RouteTableAssociation) ec2types.RouteTable {
	return ec2types.RouteTable{
		RouteTableId: aws.String(id),
		VpcId:        aws.String(vpcID),
		Routes:       routes,
		Associations: assocs,
	}
}

func assocSubnet(subnetID string) ec2types.RouteTableAssociation {
	return ec2types.RouteTableAssociation{SubnetId: aws.String(subnetID)}
}

func assocMain() ec2types.RouteTableAssociation {
	return ec2types.RouteTableAssociation{Main: aws.Bool(true)}
}

func withState(a ec2types.RouteTableAssociation, code ec2types.RouteTableAssociationStateCode) ec2types.RouteTableAssociation {
	a.AssociationState = &ec2types.RouteTableAssociationState{State: code}
	return a
}

// TestSubnetEgressFallsBackToTheMainRouteTable is the single most consequential case in this file. Most
// subnets in most accounts are never explicitly associated with a route table, and an implicit
// association returns NO SubnetId — so a lookup that only searches for the subnet finds nothing and
// reads a perfectly reachable private subnet as having no way out. That reads as a refusal, and the
// refusal message tells the customer to add a NAT gateway they already have.
func TestSubnetEgressFallsBackToTheMainRouteTable(t *testing.T) {
	tables := []ec2types.RouteTable{
		table("rtb-main", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toNAT)}, assocMain()),
	}
	if got := subnetEgress("subnet-unassociated", "vpc-1", tables); got != EgressNAT {
		t.Fatalf("unassociated subnet egress = %q, want %q (main route table not consulted)", got, EgressNAT)
	}
}

// TestExplicitAssociationWinsOverMain: a subnet with its own table must be judged by that table, or an
// isolated subnet in a VPC whose main table has a NAT route would be deployed into.
func TestExplicitAssociationWinsOverMain(t *testing.T) {
	tables := []ec2types.RouteTable{
		table("rtb-main", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toNAT)}, assocMain()),
		table("rtb-isolated", "vpc-1", nil, assocSubnet("subnet-isolated")),
	}
	if got := subnetEgress("subnet-isolated", "vpc-1", tables); got != EgressNone {
		t.Fatalf("explicitly associated isolated subnet = %q, want %q", got, EgressNone)
	}
}

// TestBlackholedDefaultRouteIsNoEgress is the deleted-NAT-gateway case, and it is the reason this file
// matches on route STATE and not only on the destination. The route stays in the table after the gateway
// is gone, so a destination-only match reads it as private egress and places a Collector that comes up
// healthy, runs, and exports nothing.
func TestBlackholedDefaultRouteIsNoEgress(t *testing.T) {
	dead := route(defaultRouteIPv4, toNAT)
	dead.State = ec2types.RouteStateBlackhole
	tables := []ec2types.RouteTable{table("rtb-1", "vpc-1", []ec2types.Route{dead}, assocSubnet("subnet-1"))}
	if got := subnetEgress("subnet-1", "vpc-1", tables); got != EgressNone {
		t.Fatalf("blackholed default route = %q, want %q", got, EgressNone)
	}
}

func TestRouteTargetsClassify(t *testing.T) {
	cases := []struct {
		name   string
		target func(*ec2types.Route)
		want   Egress
	}{
		{"internet gateway", toIGW, EgressPublic},
		{"nat gateway", toNAT, EgressNAT},
		{"nat instance via eni", toENI, EgressNAT},
		{"nat instance via instance id", func(r *ec2types.Route) { r.InstanceId = aws.String("i-0123") }, EgressNAT},
		{"transit gateway", toTGW, EgressForwarded},
		{"virtual private gateway", toVGW, EgressForwarded},
		{"vpc peering", toPCX, EgressForwarded},
		{"carrier gateway", func(r *ec2types.Route) { r.CarrierGatewayId = aws.String("cagw-0123") }, EgressForwarded},
		{"core network", func(r *ec2types.Route) { r.CoreNetworkArn = aws.String("arn:aws:networkmanager::1:core-network/x") }, EgressForwarded},
		// A gateway kind this code has not been taught. Forwarded rather than refused: the customer
		// attached it, and refusing a whole network over an unrecognized string is the worse failure.
		{"unrecognized gateway", func(r *ec2types.Route) { r.GatewayId = aws.String("zzz-0123") }, EgressForwarded},
		{"literal local", func(r *ec2types.Route) { r.GatewayId = aws.String("local") }, EgressNone},
		{"no target at all", nil, EgressNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := routesEgress([]ec2types.Route{route(defaultRouteIPv4, tc.target)})
			if got != tc.want {
				t.Fatalf("egress = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOnlyTheIPv4DefaultRouteCounts. Everything a managed Collector needs at startup — the ECR image,
// the credential in Secrets Manager, the log stream — is fetched over IPv4, so an IPv6-only default
// route does not make a subnet usable. A prefix-list route (what a gateway VPC endpoint installs for S3
// or DynamoDB) reaches one service and not the destination.
func TestOnlyTheIPv4DefaultRouteCounts(t *testing.T) {
	ipv6Only := ec2types.Route{
		DestinationIpv6CidrBlock:    aws.String("::/0"),
		EgressOnlyInternetGatewayId: aws.String("eigw-0123"),
		State:                       ec2types.RouteStateActive,
	}
	if got := routesEgress([]ec2types.Route{ipv6Only}); got != EgressNone {
		t.Errorf("ipv6-only default route = %q, want %q", got, EgressNone)
	}

	prefixList := ec2types.Route{
		DestinationPrefixListId: aws.String("pl-0123"),
		GatewayId:               aws.String("vpce-0123"),
		State:                   ec2types.RouteStateActive,
	}
	if got := routesEgress([]ec2types.Route{prefixList}); got != EgressNone {
		t.Errorf("prefix-list route = %q, want %q", got, EgressNone)
	}

	local := route("10.0.0.0/16", func(r *ec2types.Route) { r.GatewayId = aws.String("local") })
	if got := routesEgress([]ec2types.Route{local, prefixList}); got != EgressNone {
		t.Errorf("local + prefix list = %q, want %q", got, EgressNone)
	}
}

// TestRouteTablesFromAnotherVPCAreIgnored. Route tables are described account-wide in one call, so every
// VPC's tables arrive together — and another VPC's main table must never answer for this VPC's subnets.
func TestRouteTablesFromAnotherVPCAreIgnored(t *testing.T) {
	tables := []ec2types.RouteTable{
		table("rtb-other-main", "vpc-2", []ec2types.Route{route(defaultRouteIPv4, toNAT)}, assocMain()),
	}
	if got := subnetEgress("subnet-1", "vpc-1", tables); got != EgressNone {
		t.Fatalf("egress = %q, want %q: vpc-2's main table answered for a vpc-1 subnet", got, EgressNone)
	}
}

// TestNoRouteTableAtAllIsNoEgress: an incomplete listing, or a VPC Odigos cannot read the tables of.
// Both mean nothing asserts a way out, and nothing is deployed on the strength of a guess.
func TestNoRouteTableAtAllIsNoEgress(t *testing.T) {
	if got := subnetEgress("subnet-1", "vpc-1", nil); got != EgressNone {
		t.Fatalf("egress with no tables = %q, want %q", got, EgressNone)
	}
}

// TestAFailedAssociationDoesNotJudgeTheSubnet. A failed or disassociating association is still listed,
// and taking one at face value judges a subnet by a table it is not using — here, refusing a subnet that
// is really on the VPC's main table and has a NAT route.
func TestAFailedAssociationDoesNotJudgeTheSubnet(t *testing.T) {
	tables := []ec2types.RouteTable{
		table("rtb-main", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toNAT)}, assocMain()),
		table("rtb-isolated", "vpc-1", nil,
			withState(assocSubnet("subnet-1"), ec2types.RouteTableAssociationStateCodeFailed)),
	}
	if got := subnetEgress("subnet-1", "vpc-1", tables); got != EgressNAT {
		t.Fatalf("egress = %q, want %q: a failed association overrode the main table", got, EgressNAT)
	}

	// An association in force still wins, state reported or not.
	inForce := []ec2types.RouteTable{
		table("rtb-main", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toNAT)}, assocMain()),
		table("rtb-isolated", "vpc-1", nil,
			withState(assocSubnet("subnet-1"), ec2types.RouteTableAssociationStateCodeAssociated)),
	}
	if got := subnetEgress("subnet-1", "vpc-1", inForce); got != EgressNone {
		t.Fatalf("egress = %q, want %q", got, EgressNone)
	}
}

// TestADisassociatedMainTableIsNotUsed guards the same rule on the fallback half.
func TestADisassociatedMainTableIsNotUsed(t *testing.T) {
	tables := []ec2types.RouteTable{
		table("rtb-main", "vpc-1", []ec2types.Route{route(defaultRouteIPv4, toNAT)},
			withState(assocMain(), ec2types.RouteTableAssociationStateCodeDisassociated)),
	}
	if got := subnetEgress("subnet-1", "vpc-1", tables); got != EgressNone {
		t.Fatalf("egress = %q, want %q", got, EgressNone)
	}
}
