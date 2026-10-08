package placement

import (
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// defaultRouteIPv4 is the destination a general egress route carries. Only the IPv4 default is
// considered: everything a managed Collector must reach on startup — the ECR image, the credential in
// Secrets Manager, the log stream — is reached over IPv4 in every configuration Odigos builds, so an
// IPv6-only default route (`::/0` to an egress-only internet gateway) does not make a subnet usable.
const defaultRouteIPv4 = "0.0.0.0/0"

// routeStateBlackhole is the route state EC2 reports when the route's target is gone — a deleted NAT
// gateway, a detached gateway, a terminated NAT instance.
//
// Skipping blackholed routes is the single most valuable line in this file. A private subnet whose NAT
// gateway was deleted still has a 0.0.0.0/0 route in its table; matching on the destination alone would
// read it as private egress and place a Collector that comes up healthy and exports nothing.
const routeStateBlackhole = "blackhole"

// subnetEgress classifies one subnet's way out from its effective route table.
//
// tables are the region's route tables. The effective table is the one explicitly associated with the
// subnet, and the VPC's main table when there is none — an implicit association returns no SubnetId, so
// it cannot be found by looking for the subnet and has to be resolved through Main instead. Getting that
// fallback wrong is the difference between "this account has no usable subnets" and a correct plan, and
// it is the common case: most subnets are never explicitly associated with anything.
func subnetEgress(subnetID, vpcID string, tables []ec2types.RouteTable) Egress {
	var main *ec2types.RouteTable
	for i := range tables {
		t := &tables[i]
		if aws.ToString(t.VpcId) != vpcID {
			continue
		}
		for _, a := range t.Associations {
			if !associated(a) {
				continue
			}
			if aws.ToString(a.SubnetId) == subnetID {
				return routesEgress(t.Routes)
			}
			if aws.ToBool(a.Main) {
				main = t
			}
		}
	}
	if main != nil {
		return routesEgress(main.Routes)
	}
	// No table at all, explicit or main. Either the listing was incomplete or Odigos cannot read the
	// VPC's tables; both mean the same thing here — nothing asserts a way out, so nothing is deployed on
	// the strength of a guess.
	return EgressNone
}

// associated reports whether an association is in force.
//
// A failed or disassociating association is still listed, and taking one at face value would judge a
// subnet by a route table it is not actually using — refusing a healthy network if that table happens to
// have no default route. A nil state is not a problem: EC2 omits it for long-standing associations.
func associated(a ec2types.RouteTableAssociation) bool {
	return a.AssociationState == nil || a.AssociationState.State == ec2types.RouteTableAssociationStateCodeAssociated
}

// routesEgress classifies a route table's IPv4 default route.
//
// A route table can hold only one route per destination, so there is at most one 0.0.0.0/0 route and no
// precedence to resolve. Routes to a specific prefix (including the prefix lists a gateway VPC endpoint
// installs for S3 and DynamoDB) are deliberately ignored: they reach one service, not the destination
// and the OpAMP endpoint.
func routesEgress(routes []ec2types.Route) Egress {
	for _, r := range routes {
		if aws.ToString(r.DestinationCidrBlock) != defaultRouteIPv4 {
			continue
		}
		if string(r.State) == routeStateBlackhole {
			continue
		}
		return targetEgress(r)
	}
	return EgressNone
}

// targetEgress maps a default route's target to what taking it costs.
func targetEgress(r ec2types.Route) Egress {
	switch {
	case aws.ToString(r.NatGatewayId) != "":
		return EgressNAT
	// A NAT instance is an ENI or instance target. Indistinguishable from any other instance-forwarded
	// route without inspecting the instance, so it is classified as NAT: an instance holding the
	// default route is forwarding for the subnet, which is what matters, and the task needs no public
	// address either way.
	case aws.ToString(r.InstanceId) != "", aws.ToString(r.NetworkInterfaceId) != "":
		return EgressNAT
	case aws.ToString(r.TransitGatewayId) != "",
		aws.ToString(r.VpcPeeringConnectionId) != "",
		aws.ToString(r.CoreNetworkArn) != "",
		aws.ToString(r.CarrierGatewayId) != "",
		aws.ToString(r.LocalGatewayId) != "":
		return EgressForwarded
	}
	// GatewayId carries an internet gateway, a virtual private gateway, or the literal "local". "local"
	// cannot appear on a default route (local routes are the VPC's own CIDR), but it is checked rather
	// than assumed away.
	gw := aws.ToString(r.GatewayId)
	switch {
	case gw == "" || gw == "local":
		return EgressNone
	case strings.HasPrefix(gw, "igw-"):
		return EgressPublic
	case strings.HasPrefix(gw, "vgw-"):
		return EgressForwarded
	}
	// An unrecognized gateway id. Forwarded rather than none: it is a target the customer attached, and
	// a new AWS gateway kind should read as "the customer built a path Odigos cannot see", which is
	// exactly EgressForwarded, rather than refusing a network over a string this code has not been
	// taught yet.
	return EgressForwarded
}
