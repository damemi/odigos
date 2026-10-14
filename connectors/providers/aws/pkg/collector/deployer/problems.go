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
	// AWS has rejected NLB creation for this account. The connector retains a retry
	// deadline and keeps checking for an NLB that was created in the meantime.
	ProblemLoadBalancerAccountRestricted = "CollectorLoadBalancerAccountRestricted"
	// ProblemLoadBalancerSubnetDrift: the load balancer is not in the subnets placement chose. Degraded:
	// it works in the zones it has, and moving it would re-point every workload on the network.
	ProblemLoadBalancerSubnetDrift = "CollectorLoadBalancerSubnetDrift"
	// ProblemClusterRequired: AWS Collector deployment requires a customer-provisioned ECS cluster.
	ProblemClusterRequired = "CollectorClusterRequired"
	// ProblemClusterMissing: `clusterArn` names a cluster Odigos cannot find. Refused — Odigos never
	// creates a cluster.
	ProblemClusterMissing = "CollectorClusterMissing"
	// ProblemSpotUnavailable: useFargateSpot is set but the cluster has no FARGATE_SPOT association.
	// Degraded: the Collector runs on on-demand capacity, so what is lost is the saving.
	ProblemSpotUnavailable = "CollectorFargateSpotUnavailable"
	// ProblemImageUnavailable: no image can be resolved for this network — no pin in the build, or an
	// architecture the pinned manifest list does not carry. Refused before any write.
	ProblemImageUnavailable = "CollectorImageUnavailable"
	// ProblemTaskSizeInvalid: the cpu/memory pair is not one Fargate offers. Refused, because
	// RegisterTaskDefinition would fail every pass with an error that names no field.
	ProblemTaskSizeInvalid = "CollectorTaskSizeInvalid"
	// ProblemServiceNotReady: the service has no cluster or no revision to run. Refused — a service is a
	// permanent object and one created without either describes a Collector that never existed.
	ProblemServiceNotReady = "CollectorServiceNotReady"
	// ProblemServiceNameConflict: a service Odigos may not write to holds the name this network needs.
	// Refused, and reported by object: the name is unique per cluster, so there is nowhere else to go.
	ProblemServiceNameConflict = "CollectorServiceNameConflict"
	// ProblemServiceAdopted: Odigos brought an existing service under convergence. Info — nothing is
	// wrong — but never silent, because what it means at teardown depends on which kind of adoption it was.
	ProblemServiceAdopted = "CollectorServiceAdopted"
	// ProblemScalingModeUnknown: `scaling.mode` is neither "manual" nor "auto". A refusal, and the one
	// place refusing is safer than guessing: "automatic" is the obvious typo for "auto", and treating an
	// unknown mode as manual would deregister the scalable target the customer had working.
	ProblemScalingModeUnknown = "CollectorScalingModeUnknown"
	// ProblemScalingBoundsCorrected: a configured replica bound or utilisation target could not be used
	// and a default was applied. Degraded, and reported on every pass for as long as it applies — the
	// Collector scales, but not the way its own configuration reads.
	ProblemScalingBoundsCorrected = "CollectorScalingBoundsCorrected"
	// ProblemScalingSingleTask: the policy in force allows the Collector to run on one task, so every
	// deployment, task replacement and Spot interruption is a gap in a data path with no local queue.
	// Info: a legitimate choice on a network whose telemetry is not precious.
	ProblemScalingSingleTask = "CollectorScalingSingleTask"
	// ProblemScalingTargetNotOurs: an Application Auto Scaling target on this service was not registered
	// by Odigos. Degraded, and the consequence differs by scaling mode — in `auto` the configured bounds
	// are not in force, in `manual` something other than Odigos is still moving the replica count.
	ProblemScalingTargetNotOurs = "CollectorScalingTargetNotOwnedByOdigos"
	// ProblemScalingSuspended: scaling is suspended on the target, so the policies are configured and
	// inert. Degraded rather than repaired: a suspension is a deliberate act, usually mid-incident.
	ProblemScalingSuspended = "CollectorScalingSuspended"
	// ProblemScalingPolicyForeign: a scaling policy Odigos did not write shares this Collector's
	// scalable target, or holds one of Odigos's two policy names as a type that cannot be converged.
	// Degraded: two controls on one replica count, and only the customer can remove theirs.
	ProblemScalingPolicyForeign = "CollectorScalingPolicyForeign"
	// ProblemBootstrapUnavailable: the Collector's configuration could not be rendered for its cold-start
	// secret. Degraded, never a refusal: the Collector deploys and is configured over OpAMP as usual, and
	// what it loses is the mitigation for a restart during a control-plane outage.
	ProblemBootstrapUnavailable = "CollectorBootstrapConfigUnavailable"
	// ProblemConvergenceFailed: a provider or persistence call failed before its ensure step could
	// produce a more specific standing condition. Error: the requested infrastructure was not fully
	// converged, and the original provider error is retained as the actionable message.
	ProblemConvergenceFailed = "CollectorConvergenceFailed"
	// ProblemTeardownRetained: teardown left an object standing because the deletion gate refused it.
	// Severity depends on WHY (see keepReason): Info for an object Odigos never created, Degraded for one
	// that looks like Odigos's and whose two records disagree — the state that leaks.
	ProblemTeardownRetained = "CollectorTeardownObjectRetained"
	// ProblemTeardownSharedRetained: a shared object — the region's cluster or log group, the connector's
	// roles, the region's licence secret — is still needed by another network. Info: it is removed with
	// the last Collector that uses it, which is the steady state of tearing one network down.
	ProblemTeardownSharedRetained = "CollectorTeardownSharedObjectRetained"
	// ProblemTeardownUnrecorded: teardown was asked for a network this connector records nothing about,
	// so nothing passes the deletion gate. Degraded, because anything running on that network is now
	// infrastructure Odigos cannot remove.
	ProblemTeardownUnrecorded = "CollectorTeardownNoRecord"
	// ProblemTeardownManualMode: teardown ran on a manual-mode Collector and deleted nothing. Info —
	// that is the contract, not a fault.
	ProblemTeardownManualMode = "CollectorTeardownManualMode"
	// ProblemTeardownCapabilityRemoved: the credentials were revoked and the Collector deliberately left
	// running. Degraded: it is still exporting under a configuration nothing updates.
	ProblemTeardownCapabilityRemoved = "CollectorTeardownCapabilityRemoved"
	// ProblemTeardownIncomplete: a delete call failed, so the record is kept. Pending when AWS is
	// releasing dependencies and automatic retries continue; Error for other failures.
	ProblemTeardownIncomplete = "CollectorTeardownIncomplete"
	// ProblemTeardownRevocationFailed: the credentials could not be revoked, so NOTHING was deleted. The
	// ordering is the safety property and this is what it looks like when it holds.
	ProblemTeardownRevocationFailed = "CollectorTeardownRevocationFailed"
	// ProblemRetentionParked: an idle network's Collector was scaled to zero by the retention policy.
	// Info, and reported for as long as it applies — a Collector at zero tasks that nobody
	// explained reads as a broken deployment.
	ProblemRetentionParked = "CollectorScaledToZeroWhileIdle"
	// ProblemRetentionPolicy: a retention value could not be used and the Collector is kept instead.
	// Degraded: the configuration reads as though something happens when it does not.
	ProblemRetentionPolicy = "CollectorRetentionPolicyCorrected"

	// ProblemManualModeProvisioning: provisioning was asked for on a manual-mode Collector. A guard,
	// not a customer-facing condition — the coordinator wires no deployer for a manual-mode type at all
	// (api.CollectorDeployer), so reaching this means something bypassed that.
	ProblemManualModeProvisioning = "CollectorManualModeProvisioning"
)
