package deployer

// Problem codes this package reports. Callers branch on these, never on a message.
//
// Every one of them except ProblemLicenseTokenMissing is a REFUSAL: nothing is written and nothing is
// deployed. That is the posture and both settle on — an object Odigos is not sure it owns is
// left exactly as it is, and a Collector that would run without an identity is not created.
const (
	// ProblemNameTooLong: a derived object name cannot be used, so nothing is provisioned.
	ProblemNameTooLong = "CollectorObjectNameUnusable"
	// ProblemObjectNotOurs: an object with the name Odigos needs already exists and does not belong to
	// this connector. Refused rather than adopted or renamed.
	ProblemObjectNotOurs = "CollectorObjectNotOwnedByOdigos"
	// ProblemExecutionRoleRequired: executionRoleArn is missing or is not an IAM role in this account.
	ProblemExecutionRoleRequired = "CollectorExecutionRoleRequired"
	// ProblemLicenseTokenMissing: the connector holds no ODIGOS_ONPREM_TOKEN, so there is nothing to
	// copy into the customer's licence secret.
	ProblemLicenseTokenMissing = "CollectorLicenseTokenMissing"
	// ProblemCredentialIssueFailed: the credential could not be issued or could not be installed.
	ProblemCredentialIssueFailed = "CollectorCredentialIssueFailed"
	// ProblemLogRetentionUnset: the log group exists but its retention policy could not be applied.
	// Degraded, not an error: logs are kept under the group's existing policy, which costs storage
	// rather than telemetry.
	ProblemLogRetentionUnset = "CollectorLogRetentionUnset"
	// ProblemLoadBalancerUnprotected: the Collector's load balancer has no security group, and one
	// cannot be added to an existing NLB. Refused — the OTLP ports carry no authentication.
	ProblemLoadBalancerUnprotected = "CollectorLoadBalancerUnprotected"
	// ProblemLoadBalancerSubnetDrift: the load balancer is not in the subnets placement chose. Degraded:
	// it works in the zones it has, and moving it would re-point every workload on the network.
	ProblemLoadBalancerSubnetDrift = "CollectorLoadBalancerSubnetDrift"
	// ProblemManualModeProvisioning: provisioning was asked for on a manual-mode Collector. A guard,
	// not a customer-facing condition — the coordinator wires no deployer for a manual-mode type at all
	// (api.CollectorDeployer), so reaching this means something bypassed that.
	ProblemManualModeProvisioning = "CollectorManualModeProvisioning"
)
