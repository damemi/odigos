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
	"strings"
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
