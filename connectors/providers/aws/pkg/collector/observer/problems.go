package observer

// Problem codes reported by this package. Callers branch on these, never on message text.
const (
	// No ECS service for this network. Transient in automatic mode; AwaitingCustomer in manual.
	ProblemWorkloadMissing = "CollectorWorkloadMissing"
	// Service exists but is DRAINING/INACTIVE; recreate must wait for the name to free.
	ProblemWorkloadStopping = "CollectorWorkloadStopping"
	// Service wants tasks but none are running and no stopped task names a reason (usually placement).
	ProblemNoTaskRunning = "CollectorNoTaskRunning"
	// Task stopped: could not pull the Collector image (egress or registry rate limit).
	ProblemImagePullFailed = "CollectorImagePullFailed"
	// Task stopped before start: execution role could not read OpAMP credential or licence.
	ProblemSecretFetchFailed = "CollectorSecretFetchFailed"
	// Container killed for exceeding its memory allocation.
	ProblemTaskOutOfMemory = "CollectorTaskOutOfMemory"
	// Task stopped for an unrecognized reason; message carries the reason verbatim.
	ProblemTaskStopped = "CollectorTaskStopped"
	// ECS gave up on a deployment; old tasks stay, new revision will not come.
	ProblemDeploymentFailed = "CollectorDeploymentFailed"

	// Tasks running but the load balancer will not send to them (SG / health-check).
	ProblemTargetsUnhealthy = "CollectorTargetsUnhealthy"
	// Target group has no targets; tasks were never registered.
	ProblemNoTargetsRegistered = "CollectorNoTargetsRegistered"
	// Load balancer not active (provisioning or failed).
	ProblemLoadBalancerUnavailable = "CollectorLoadBalancerUnavailable"
	// The discovered load balancer is public or otherwise violates the internal front contract.
	ProblemLoadBalancerShapeInvalid = "CollectorLoadBalancerShapeInvalid"

	// Infra looks fine but the Collector has never opened an OpAMP session.
	ProblemNeverConnected = "CollectorNeverConnected"
	// Enterprise licence expired; not a rejected configuration.
	ProblemLicenseExpired = "CollectorLicenseExpired"
	// Collector running but cannot deliver to the destination.
	ProblemExporterFailing = "CollectorExporterFailing"
	// Collector reports unhealthy for an unclassified reason; message is verbatim.
	ProblemAgentUnhealthy = "CollectorAgentUnhealthy"

	// Running task-definition revision differs from connector state.
	ProblemRevisionDrift = "CollectorRevisionDrift"
	// Running image digest differs from the expected build.
	ProblemImageDrift            = "CollectorImageDrift"
	ProblemTaskDefinitionInvalid = "CollectorTaskDefinitionInvalid"
	ProblemSecurityGroupInvalid  = "CollectorSecurityGroupInvalid"

	// An AWS read failed; report is partial. Not an Error — distinguish "could not look" from "broken".
	ProblemObservationFailed = "CollectorObservationFailed"
)
