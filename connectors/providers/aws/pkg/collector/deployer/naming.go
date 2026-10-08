// Package deployer provisions what a managed Collector needs in the customer's AWS account before its
// ECS service can exist: the region's log group, the two secrets that carry its enterprise licence and
// its OpAMP credential, and the credential itself. Its ECS cluster and task execution role are
// customer-owned prerequisites.
//
// Everything here is idempotent and re-entrant. It runs on every reconcile pass, so the interesting
// property is not "can it create these" but "does the second pass do nothing": a pass that re-minted a
// credential would rotate it, a rotation is a redeploy, and a redeploy every pass is a fleet
// permanently rolling.
//
// The one thing this package handles that nothing else does is a credential VALUE. It exists for the
// few statements between collectorcredential.Mint and the Secrets Manager write, never leaves
// collectorcredential.Secret, and is never an argument to a log call — deployer_secrecy_test.go pins
// every place Reveal is allowed to appear.
package deployer

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// Object names. Each is derived from the connector name, so a customer reading their log groups or
// secret list can tell which connector owns what without looking anything up — and so a second
// pass finds what the first created instead of making another.
const (
	// logGroupPrefix is the region's shared log group. One per region rather than one per network: the
	// stream name carries the task, so a per-network group would multiply groups without separating
	// anything a reader cannot already filter.
	logGroupPrefix = "/odigos/collector/"

	// secretPrefix is the root of every secret this connector creates. The execution role's
	// GetSecretValue is scoped to it, which is what keeps a Collector's identity from reaching the
	// customer's own secrets.
	secretPrefix = "odigos/collector/"
	// licenseSecretLeaf names the licence secret within a region. Not a network id, and it cannot
	// collide with one: every VPC id begins "vpc-".
	licenseSecretLeaf = "license"
)

// LogGroupName is the region's shared log group for this connector's Collectors.
func LogGroupName(connector string) string { return logGroupPrefix + connector }

// CredentialSecretName is the regional Collector secret holding its OpAMP credential.
//
// Per Collector, because revocation is per Collector: whoever holds one can read that
// Collector's desired configuration, which carries the destination's own credential. The region is in
// the name even though the secret already lives in one — it is the spelling, and it keeps the name
// readable next to the credential row, which is keyed on region. The host VPC is metadata and never
// enters this identity name, so moving a regional Collector does not create a second credential secret.
func CredentialSecretName(connector, region string) string {
	return secretPrefix + connector + "/" + region + "/credential"
}

// LicenseSecretName is the region's licence secret, holding the value of the connector's own
// ODIGOS_ONPREM_TOKEN.
//
// Per region rather than per Collector, because a Secrets Manager secret is regional and the licence is
// the same value for every Collector in the install. Separate from the credential secret for the reason
// that gives: different issuers, lifetimes and revocation authorities, so one secret holding both would
// make every credential rotation rewrite the licence.
func LicenseSecretName(connector, region string) string {
	return secretPrefix + connector + "/" + region + "/" + licenseSecretLeaf
}

// Partition returns the ARN partition a region belongs to.
//
// Not hardcoded to "aws" even though the connector only reaches the commercial partition today
// (EnabledRegions calls ec2:DescribeRegions in us-east-1). A hardcoded partition does not fail — it
// produces a syntactically valid policy whose resource ARNs match nothing, so the role is created, the
// task starts, and the secret fetch is denied with a message about the secret rather than the policy.
func Partition(region string) string {
	switch {
	case strings.HasPrefix(region, "us-gov-"):
		return "aws-us-gov"
	case strings.HasPrefix(region, "cn-"):
		return "aws-cn"
	case strings.HasPrefix(region, "us-iso-"), strings.HasPrefix(region, "us-isob-"):
		// The ISO partitions are air-gapped and Odigos does not reach them; named so a policy built for
		// one is wrong loudly rather than wrong as an "aws" ARN that silently matches nothing.
		return "aws-iso"
	default:
		return "aws"
	}
}

// The data-plane object names identify physical AWS resources in a host VPC. The logical
// Collector is regional, but an NLB, target group, and security group cannot span VPCs, so their names
// remain host-network-derived and their tags carry both region and host network metadata.
const (
	securityGroupPrefix = "odigos-collector-"
	// loadBalancerPrefix is the internal NLB. "col" rather than "collector" to fit the 32-character
	// budget alongside a 17-hex network id.
	loadBalancerPrefix = "odigos-col-"
	// targetGroupPrefix is followed by the port, then the network id: `odigos-4317-<netid>`.
	targetGroupPrefix = "odigos-"
	// vpcIDPrefix is stripped from a network id before it goes into an ELBv2 name.
	vpcIDPrefix = "vpc-"
)

// elbNameMaxLen is the ELBv2 limit on a load balancer or target group name.
const elbNameMaxLen = 32

// SecurityGroupName is the Collector's own security group in the host VPC.
func SecurityGroupName(networkID string) string { return securityGroupPrefix + networkID }

// LoadBalancerName is the internal NLB in the host VPC.
func LoadBalancerName(networkID string) string {
	return loadBalancerPrefix + strings.TrimPrefix(networkID, vpcIDPrefix)
}

// TargetGroupName is the target group for one data port in the host VPC.
func TargetGroupName(networkID string, port int) string {
	return fmt.Sprintf("%s%d-%s", targetGroupPrefix, port, strings.TrimPrefix(networkID, vpcIDPrefix))
}

// CheckDataPlaneNames reports whether one host VPC's ELBv2 names fit, before anything is created.
//
// Checked up front rather than discovered at the CreateLoadBalancer call, because the two objects are
// created in sequence: a load balancer whose name fits followed by a target group whose name does not
// leaves a load balancer with no listeners in the customer's account, which is a chargeable object
// serving nothing. AWS VPC ids always fit; a provider whose network ids are customer-chosen NAMES —
// an Azure VNet, a GCP VPC network — will not always, which is why this is a check and not an assertion.
func CheckDataPlaneNames(networkID, region string) []collectorstatus.Problem {
	var problems []collectorstatus.Problem
	names := []string{LoadBalancerName(networkID)}
	for _, port := range collectoringress.DataPorts() {
		names = append(names, TargetGroupName(networkID, port))
	}
	for _, name := range names {
		if len(name) > elbNameMaxLen {
			problems = append(problems, collectorstatus.Problem{
				Code:     ProblemNameTooLong,
				Severity: collectorstatus.SeverityError,
				Message: fmt.Sprintf("the load balancer object name %q is %d characters, over the %d that "+
					"Elastic Load Balancing allows, so no Collector is deployed for host network %s. Its name is "+
					"derived from the host network id and cannot be shortened here; set "+
					"collector.regions.%s.endpointOverride to put your own front in front of the Collector instead",
					name, len(name), elbNameMaxLen, networkID, region),
			})
		}
	}
	return problems
}

// The workload object names. The task definition family and the shared regional cluster.
const (
	// clusterName is the ECS cluster every Collector in a region runs in. One per region rather than one
	// per network: a cluster is a namespace and a capacity-provider association, not an isolation
	// boundary, so a cluster per VPC would multiply objects without separating anything.
	clusterName = "odigos-collector"
	// taskDefinitionPrefix names the family. A family holds the revisions of one regional Collector, so
	// it is per region. Host VPCs are placement metadata and must not create additional regional identity.
	taskDefinitionPrefix = "odigos-collector-"
)

// ClusterName is the shared regional cluster's name.
func ClusterName() string { return clusterName }

// TaskDefinitionFamily is the family holding one region's Collector revisions.
func TaskDefinitionFamily(region string) string { return taskDefinitionPrefix + region }

// CollectorContainerName is the single container in the task definition. Named rather than inlined
// because the ECS service's load balancer attachment has to name the same container, and the two
// disagreeing produces a service that will not start with an error about a container that "does not
// exist" in a definition where it plainly does.
const CollectorContainerName = "collector"
