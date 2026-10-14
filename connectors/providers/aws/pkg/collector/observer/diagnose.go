package observer

import (
	"fmt"
	"sort"
	"strings"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/opamp"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/deployer"
	collectorsettings "github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/collector/settings"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// DiagnosisInput is everything a verdict depends on: what AWS says, what the row already holds, and what
// the configuration asked for.
type DiagnosisInput struct {
	Network baseapi.CollectorNetwork
	// Connector is this connector's name, which every object name is derived from — so a message can
	// name the log group and the secret a reader has to go and look at.
	Connector string
	// Mode is the deployment mode. It changes what a finding MEANS rather than what was found — a
	// missing service is a recreation in automatic mode and a customer's next action in manual mode —
	// so it is read all through the diagnosis and never used to skip a read.
	Mode string
	// Settings is this network's resolved configuration.
	Settings collectorsettings.Network

	// Placement carries this network's placement conditions from the plan. Reported against the
	// Collector row because a network with no usable subnet has no other place to be reported — dropping
	// it from the fleet would turn "this VPC has no subnet a Collector could export from" into silence.
	Placement []collectorstatus.Problem

	// Record is the row as it stands: the OpAMP facts the session wrote, and the refs the converge
	// recorded. Read, never assumed — the difference between "Odigos never built this" and "what Odigos
	// built is gone" is exactly whether the row names a workload.
	Record basestore.CollectorRecord

	Observation Observation
	// Liveness is the OpAMP session's state, from the row's last-seen timestamp. Computed by
	// collectorreport so this and the customer-facing report cannot disagree about what "stale" means.
	Liveness collectorreport.Liveness

	// OpAMPEndpoint is what the installation publishes NOW, for the drift check against what the
	// Collector was deployed with.
	OpAMPEndpoint string
	// Pin is the image this connector build expects, for the drift check against what is running.
	Pin collectorimage.Pin
}

// Diagnosis is one pass's verdict.
type Diagnosis struct {
	// Status and Message are what to record, or empty when there is nothing to say — see Records. The
	// observer deliberately never records Ready: readiness is a claim about the configuration plane as
	// well, and the OpAMP session is the party that can see that one.
	Status  collectorstatus.Status
	Message string

	// Problems is every condition found, worst first.
	Problems []collectorstatus.Problem

	// Transport and EndpointSource are the endpoint workloads should be pointed at, resolved from what
	// was discovered. Populated in both modes: in manual mode this is the ONLY thing that ever publishes
	// a customer-deployed Collector's address.
	Transport      collectoringress.Transport
	EndpointSource string
}

// Records reports whether this verdict is worth writing to the row.
func (d Diagnosis) Records() bool { return d.Status != "" }

// Diagnose turns one observation into a verdict.
//
// Pure, and that is the point rather than a convenience: is a table of failures each of which has to
// produce its own sentence, and the only way to hold a table of twenty rows correct is for every row to
// be a value a test can write down. Nothing here reads a clock, a client or a config file.
//
// The order the halves are consulted in is the order a reader needs them. Placement first, because a
// network that cannot host a Collector explains everything after it. Then the workload — a Collector
// that is not running makes every data-path and configuration finding a restatement. Then the data path,
// then the configuration plane, then drift, which is the only class that applies to a Collector that is
// working perfectly.
func Diagnose(in DiagnosisInput) Diagnosis {
	var out Diagnosis
	problems := append([]collectorstatus.Problem(nil), in.Placement...)
	problems = append(problems, in.Observation.Problems...)

	out.Transport, out.EndpointSource, problems = transport(in, problems)

	problems = append(problems, workloadProblems(in)...)
	problems = append(problems, manualTaskDefinitionProblems(in)...)
	problems = append(problems, dataPathProblems(in)...)
	problems = append(problems, configPlaneProblems(in)...)
	problems = append(problems, driftProblems(in)...)

	out.Problems = worstFirst(problems)
	out.Status, out.Message = verdict(in, out.Problems)
	return out
}

// Manual infrastructure is customer-owned, so report invalid task wiring
// directly instead of waiting for a task pull or secret fetch to fail.
func manualTaskDefinitionProblems(in DiagnosisInput) []collectorstatus.Problem {
	if in.Mode != spec.ModeManual || !in.Observation.ServiceFound || in.Observation.Revision.ARN == "" {
		return nil
	}
	revision := in.Observation.Revision
	var problems []collectorstatus.Problem
	add := func(message string) {
		problems = append(problems, collectorstatus.Problem{Code: ProblemTaskDefinitionInvalid,
			Severity: collectorstatus.SeverityError, Message: message})
	}
	if revision.RepositoryCredentialsRef == "" {
		add("the customer-owned Collector task definition must set repositoryCredentials to the licence secret ARN so ECS can pull the private image")
	}
	if got := revision.Secrets[collectorimage.EnvOnPremToken]; got == "" ||
		!strings.HasPrefix(got, revision.RepositoryCredentialsRef+":password::") {
		add("the customer-owned Collector task definition must read ODIGOS_ONPREM_TOKEN from the licence secret's password JSON key")
	}
	if got := revision.Secrets[collectorimage.EnvOpAMPAuthorization]; got == "" {
		add("the customer-owned Collector task definition must read ODIGOS_OPAMP_AUTHORIZATION from the issued credential secret")
	}
	if want := deployer.LogGroupName(in.Connector); revision.LogGroup != want {
		add(fmt.Sprintf("the customer-owned Collector task definition must send logs to %s, the execution role's log-group scope", want))
	}
	return problems
}

// transport resolves the endpoint workloads are pointed at, from the load balancer that was discovered.
//
// The SAME resolution the deployer performs, over deployer.AWSPlatform, because the endpoint Odigos
// publishes and the endpoint Odigos reports having published have to be one value — two resolutions here
// would report drift on a Collector that has none.
func transport(in DiagnosisInput, problems []collectorstatus.Problem) (
	collectoringress.Transport, string, []collectorstatus.Problem) {
	source := deployer.EndpointSourceOverride
	host := ""
	if in.Settings.EndpointOverride == "" {
		source = deployer.EndpointSourceLoadBalancer
		if targets := in.Observation.LoadBalancer.ListenerTargets; targets != nil {
			for _, group := range in.Observation.TargetGroups {
				if group.Port != 4317 && group.Port != 4318 {
					continue
				}
				if targets[group.Port] != group.ARN {
					problems = append(problems, collectorstatus.Problem{
						Code: ProblemLoadBalancerShapeInvalid, Severity: collectorstatus.SeverityError,
						Message: fmt.Sprintf("Collector NLB listener %d must forward to target group %s", group.Port, group.ARN),
					})
					return collectoringress.Transport{}, source, problems
				}
			}
		}
		for _, group := range in.Observation.TargetGroups {
			if group.HealthCheckPort != "" && group.HealthCheckPort != "13133" {
				problems = append(problems, collectorstatus.Problem{
					Code: ProblemLoadBalancerShapeInvalid, Severity: collectorstatus.SeverityError,
					Message: fmt.Sprintf("Collector target group %s must health-check port 13133, got %s", group.ARN, group.HealthCheckPort),
				})
				return collectoringress.Transport{}, source, problems
			}
		}
		if lb := in.Observation.LoadBalancer; lb.ARN != "" && lb.Scheme != "" && lb.Scheme != "internal" {
			problems = append(problems, collectorstatus.Problem{
				Code: ProblemLoadBalancerShapeInvalid, Severity: collectorstatus.SeverityError,
				Message: "the Collector load balancer must use the internal scheme; correct the customer-owned NLB before routing telemetry through it",
			})
			return collectoringress.Transport{}, source, problems
		}
		host = in.Observation.LoadBalancer.DNSName
	}
	resolved, transportProblems := collectoringress.ResolveTransport(in.Settings.TLS, deployer.AWSPlatform, host)
	return resolved, source, append(problems, transportProblems...)
}

// workloadProblems judges the ECS service and its tasks.
func workloadProblems(in DiagnosisInput) []collectorstatus.Problem {
	o := in.Observation
	network := in.Network.NetworkID

	if !o.ServiceFound {
		if o.Partial {
			// The describe failed, so "not found" is not a finding. Reporting a Collector as gone
			// because a read was throttled is the one mistake that turns a healthy fleet into a page.
			return nil
		}
		return []collectorstatus.Problem{workloadMissing(in)}
	}
	if !o.Live() {
		return []collectorstatus.Problem{{
			Code:     ProblemWorkloadStopping,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the ECS service for the Collector on %s is %s, so it is being removed and "+
				"is not carrying telemetry. Nothing can be converged onto a service ECS is deleting; the name "+
				"becomes free when it finishes and a Collector is built again then",
				network, strings.ToLower(o.Status)),
		}}
	}

	var problems []collectorstatus.Problem
	if o.Rollout == rolloutFailed {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemDeploymentFailed,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("ECS gave up deploying the Collector on %s and stopped retrying: %s. The "+
				"previously running tasks are still serving, so telemetry is not lost, but the new revision "+
				"will not roll out until whatever stopped its tasks is fixed",
				network, reasonOr(o.RolloutReason, "no reason given")),
		})
	}

	// The stopped tasks, most recent first, and only the FIRST unexpected cause. A service that cannot
	// start a task has stopped dozens of them for the same reason, and reporting each would bury the
	// reason in repetition.
	if cause, task := firstFault(o.StoppedTasks); cause != StopCauseExpected {
		if problem, ok := stoppedTaskProblem(in, cause, task); ok {
			problems = append(problems, problem)
		}
	}

	if o.DesiredCount > 0 && !o.TasksRunning() && !hasCode(problems, ProblemTaskStopped) &&
		!hasAnyStopFault(problems) {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemNoTaskRunning,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the Collector on %s wants %d tasks and none is running, and no task has "+
				"stopped with a reason. That is almost always placement: Fargate capacity in the chosen "+
				"availability zones, or a subnet with no free addresses left. Its subnets are %s",
				network, o.DesiredCount, joinOr(o.Subnets, "not recorded")),
		})
	}
	return problems
}

// workloadMissing is the "no service" finding, whose meaning is entirely a function of the mode and of
// whether Odigos had recorded one.
//
// Three sentences from one code, because three different people have to act:
//
//   - manual mode: the customer has not deployed it yet. Pending, which the report turns into
//     AwaitingCustomer — a state that clears on the connection itself.
//   - automatic mode with nothing recorded: Odigos has not built it yet. Pending, because reporting
//     Degraded here would call every new Collector broken for the minute it takes to create one.
//   - automatic mode with a recorded workload: something outside Odigos removed a running Collector.
//     Degraded, because telemetry is being dropped right now, and it names the recreation so nobody
//     goes looking for a control that has already been triggered.
func workloadMissing(in DiagnosisInput) collectorstatus.Problem {
	network := in.Network.NetworkID
	if in.Mode == spec.ModeManual {
		return collectorstatus.Problem{
			Code:     ProblemWorkloadMissing,
			Severity: collectorstatus.SeverityPending,
			Message: fmt.Sprintf("no ECS service named %q exists in %s, so the Collector for %s has not been "+
				"deployed yet. Odigos does not create it in manual mode — apply the published deployment "+
				"artifacts for this network, and Odigos configures and reports the result once it connects",
				deployer.ServiceName(in.Network.Region), in.Network.Region, network),
		}
	}
	if in.Record.WorkloadRef == "" {
		return collectorstatus.Problem{
			Code:     ProblemWorkloadMissing,
			Severity: collectorstatus.SeverityPending,
			Message:  fmt.Sprintf("no Collector service exists for %s yet; Odigos is building it", network),
		}
	}
	return collectorstatus.Problem{
		Code:     ProblemWorkloadMissing,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("the ECS service Odigos created for the Collector on %s is gone — something "+
			"outside Odigos removed it. Telemetry from every instrumented workload on this network is being "+
			"dropped until it is running again. Odigos recreates it on the next converge pass; nothing needs "+
			"to be done unless it keeps disappearing", network),
	}
}

// stoppedTaskProblem turns one classified stop into its own sentence.
func stoppedTaskProblem(in DiagnosisInput, cause StopCause, task StoppedTask) (collectorstatus.Problem, bool) {
	network := in.Network.NetworkID
	detail := task.Detail()

	switch cause {
	case StopCauseImagePull:
		registry := imageRegistry(in.Observation.Revision.Image)
		note := ""
		if registry == dockerHubHost || registry == "" {
			// reason for preferring GHCR: anonymous Docker Hub pulls are rate-limited per source
			// address, and every task behind one NAT gateway shares that address — so a scale-out is
			// exactly when the limit is reached.
			note = " Anonymous Docker Hub pulls are rate-limited per source address, and every task in " +
				"this VPC shares one NAT address, so a scale-out is when the limit is reached."
		}
		return collectorstatus.Problem{
			Code:     ProblemImagePullFailed,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("the Collector on %s cannot start because its image could not be pulled from "+
				"%s: %s. The subnets a Collector runs in need egress to the registry — the same requirement "+
				"the agents image already has.%s The image is %s",
				network, registryOr(registry), detail, note, in.Observation.Revision.Image),
		}, true

	case StopCauseSecrets:
		return collectorstatus.Problem{
			Code:     ProblemSecretFetchFailed,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("the Collector on %s cannot start because its task execution role could not "+
				"read the secrets holding its OpAMP credential and its licence: %s. Either the role's policy "+
				"does not reach those secrets, or the Collector's subnets have no route to Secrets Manager. "+
				"Both secrets are under %q", network, detail, secretPrefixFor(in)),
		}, true

	case StopCauseOutOfMemory:
		return collectorstatus.Problem{
			Code:     ProblemTaskOutOfMemory,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("a Collector task on %s was killed for exceeding its %s MiB memory "+
				"allocation: %s. Raise collector.regions.%s.memory — memory_limiter is derived from the "+
				"allocation, so the change is a configuration push as well as a new revision, and the next "+
				"size up is the documented step",
				network, memoryOr(in.Settings.Memory), detail, in.Network.Region),
		}, true

	case StopCauseHealthCheck:
		// Deliberately no problem of its own: the load balancer's own verdict is reported by
		// dataPathProblems with the target-health reason, which says WHY the check failed. A second
		// problem here would report the consequence beside the cause.
		return collectorstatus.Problem{}, false

	case StopCauseExited:
		return collectorstatus.Problem{
			Code:     ProblemTaskStopped,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("a Collector task on %s started and then exited%s: %s. ECS cannot say why — "+
				"the reason is in the Collector's own output, in the log group %s",
				network, exitCodeNote(task.ExitCode), detail, logGroupFor(in)),
		}, true
	}

	return collectorstatus.Problem{
		Code:     ProblemTaskStopped,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("a Collector task on %s stopped and Odigos does not recognise the reason, so "+
			"here it is exactly as ECS gave it: %s%s", network, reasonOr(detail, "no reason given"),
			stopCodeNote(task.StopCode)),
	}, true
}

// dataPathProblems judges what the load balancer says about the tasks.
//
// Skipped entirely when the service has no load balancer attachment, which is the `endpointOverride`
// shape: Odigos created no load balancer, so there is no target health to read and the customer's
// own front reaches the tasks directly.
func dataPathProblems(in DiagnosisInput) []collectorstatus.Problem {
	o := in.Observation
	network := in.Network.NetworkID
	if !o.Live() || len(o.TargetGroups) == 0 {
		return nil
	}

	var problems []collectorstatus.Problem
	if lb := o.LoadBalancer; lb.ARN != "" && !lb.Serving() {
		severity := collectorstatus.SeverityDegraded
		detail := fmt.Sprintf("its state is %q", lb.State)
		if lb.State == lbProvisioning {
			// Still being created. Nothing to fix and it clears on its own, which is what Pending means.
			severity = collectorstatus.SeverityPending
			detail = "it is still being provisioned"
		}
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemLoadBalancerUnavailable,
			Severity: severity,
			Message: fmt.Sprintf("the load balancer in front of the Collector on %s is not carrying traffic: "+
				"%s. Until it is, its DNS name resolves to nothing and every workload pointed at this "+
				"Collector fails to export", network, detail),
		})
	}

	for _, group := range o.TargetGroups {
		switch {
		case len(group.Targets) == 0:
			if !o.TasksRunning() {
				// No tasks to register. Whatever stopped them is already reported and this would only
				// restate it.
				continue
			}
			problems = append(problems, collectorstatus.Problem{
				Code:     ProblemNoTargetsRegistered,
				Severity: collectorstatus.SeverityDegraded,
				Message: fmt.Sprintf("the Collector on %s has %d task(s) running but none is registered with "+
					"the load balancer target group for port %d, so nothing reaches it. That is the service's "+
					"load balancer attachment rather than the network rules — the container name and port in "+
					"the attachment have to match the task definition's",
					network, o.RunningCount, group.Port),
			})
		case group.Settling():
			problems = append(problems, collectorstatus.Problem{
				Code:     ProblemTargetsUnhealthy,
				Severity: collectorstatus.SeverityPending,
				Message: fmt.Sprintf("the Collector's targets on port %d for %s are still coming up; the load "+
					"balancer has not finished its first health check", group.Port, network),
			})
		case group.Healthy() == 0:
			problems = append(problems, targetsUnhealthy(in, group, collectorstatus.SeverityError))
		case len(group.Failing()) > 0:
			problems = append(problems, targetsUnhealthy(in, group, collectorstatus.SeverityDegraded))
		}
	}
	return problems
}

// targetsUnhealthy is "load balancer created, targets never healthy" row.
//
// The security group and the health-check port are named because it is almost always one of the two, and
// the ELB reason is carried because it separates them: Target.Timeout is a check that got no answer at
// all — a security group that does not admit the load balancer — while Target.FailedHealthChecks is one
// that was answered with the wrong thing, which is the Collector or the port.
func targetsUnhealthy(in DiagnosisInput, group TargetGroup, severity collectorstatus.Severity) collectorstatus.Problem {
	network := in.Network.NetworkID
	failing := group.Failing()
	reason, description := "", ""
	if len(failing) > 0 {
		reason, description = failing[0].Reason, failing[0].Description
	}

	hint := fmt.Sprintf("Check that the Collector's security group admits the health-check port %d from the "+
		"load balancer, and that the Collector is answering on it", collectoringress.PortHealth)
	if reason == reasonTimeout {
		hint = fmt.Sprintf("The check got no answer at all, which is the security group rather than the "+
			"Collector: port %d has to be reachable from the load balancer's own group",
			collectoringress.PortHealth)
	} else if reason == reasonFailedHealthChecks {
		hint = fmt.Sprintf("The check was answered with the wrong thing, so the port is reachable and the "+
			"Collector is not serving health on %d yet — a Collector still validating its configuration "+
			"answers late, which is what the service's health-check grace period covers",
			collectoringress.PortHealth)
	}

	return collectorstatus.Problem{
		Code:     ProblemTargetsUnhealthy,
		Severity: severity,
		Message: fmt.Sprintf("%d of %d Collector targets on port %d for %s are healthy%s. %s",
			group.Healthy(), len(group.Targets), group.Port, network,
			reasonSuffix(reason, description), hint),
	}
}

// configPlaneProblems judges the OpAMP session and what the Collector says about itself.
func configPlaneProblems(in DiagnosisInput) []collectorstatus.Problem {
	var problems []collectorstatus.Problem
	network := in.Network.NetworkID

	// What the Collector reported about itself, classified. The licence case is first for the reason
	// gives: an expired licence and a rejected configuration are indistinguishable from outside and send
	// a reader to opposite ends of the product.
	switch ClassifyAgentHealth(in.Record.AgentHealth) {
	case AgentCauseLicenseExpired:
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemLicenseExpired,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("the Collector on %s reports that its Odigos enterprise licence is missing or "+
				"expired, so its Collector process will not run: %s. This is a LICENCE failure and not a "+
				"rejected configuration — the two look the same from outside, and the Collector's own "+
				"configuration rollback cannot rescue this one because nothing is wrong with the "+
				"configuration. Renew the licence, then redeploy", network, in.Record.AgentHealth),
		})
	case AgentCauseExporter:
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemExporterFailing,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the Collector on %s is running and cannot deliver to its destination: %s. "+
				"This is NOT a deployment failure — the service, the load balancer and the configuration are "+
				"all in force — so the fix is at the destination or on the Collector's route to it, and "+
				"redeploying the Collector will not change it", network, in.Record.AgentHealth),
		})
	case AgentCauseUnhealthy:
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemAgentUnhealthy,
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the Collector on %s reports itself unhealthy for a reason Odigos does not "+
				"recognise, so here it is exactly as the Collector gave it: %s",
				network, in.Record.AgentHealth),
		})
	}

	// The endpoint this Collector was DEPLOYED against, which lives in its own task definition and
	// nowhere else — no column holds it, so this is the only place the check can be made.
	problems = append(problems, collectorgateway.CheckEndpointDrift(
		in.Observation.Revision.Environment[opamp.EnvEndpoint], in.OpAMPEndpoint, in.Mode)...)

	// "Deployed but never connected". Only once everything Odigos can see is in place: while a
	// task will not start or a target is unhealthy, the silence is explained and this would be noise.
	if in.Liveness == collectorreport.LivenessNeverConnected && in.Observation.TasksRunning() &&
		serving(in.Observation) {
		problems = append(problems, neverConnected(in))
	}
	return problems
}

// neverConnected is the diagnosis for a Collector that runs and has never called home.
//
// Three causes, all invisible from the connector's side, so all three are named. That is the whole value
// of the message: the connector cannot tell a refused credential from a blocked egress from a wrong
// endpoint — the session it would have learned that from is the one that never happened — and a reader
// given "not connected" with no list has nowhere to start.
func neverConnected(in DiagnosisInput) collectorstatus.Problem {
	endpoint := in.Observation.Revision.Environment[opamp.EnvEndpoint]
	credential := "its OpAMP credential secret has never been written, so the Supervisor started with an " +
		"empty Authorization header"
	if in.Record.CredentialRef != "" {
		credential = fmt.Sprintf("the credential in %s is not the one Odigos expects — a secret version "+
			"written after the task started is not picked up until the task is replaced",
			in.Record.CredentialRef)
	}
	return collectorstatus.Problem{
		Code:     ProblemNeverConnected,
		Severity: collectorstatus.SeverityDegraded,
		Message: fmt.Sprintf("the Collector on %s is running and has never connected to Odigos over OpAMP, so "+
			"it is serving the inert bootstrap configuration and exporting nothing. Odigos cannot see which of "+
			"three causes it is, because the session that would have said is the one that did not happen: "+
			"(1) egress — the Collector's subnets need a route to %s; (2) the credential — %s; (3) the "+
			"endpoint — the Supervisor was started with %q",
			in.Network.NetworkID, endpointOr(endpoint, "the OpAMP endpoint"), credential,
			endpointOr(endpoint, "")),
	}
}

// driftProblems compares what is running with what this connector expects.
func driftProblems(in DiagnosisInput) []collectorstatus.Problem {
	o := in.Observation
	if !o.Live() {
		return nil
	}
	network := in.Network.NetworkID
	var problems []collectorstatus.Problem

	// Revision drift, in AUTOMATIC mode only. In manual mode the recorded revision is whatever this
	// observer last wrote down, so comparing the two would be comparing a value with itself.
	if in.Mode == spec.ModeAutomatic && in.Record.RevisionRef != "" && o.RevisionARN != "" &&
		in.Record.RevisionRef != o.RevisionARN {
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemRevisionDrift,
			Severity: collectorstatus.SeverityPending,
			Message: fmt.Sprintf("the Collector on %s is running %s and Odigos recorded %s; the next converge "+
				"pass sets it forward. Replacing its tasks costs the telemetry queued in them, which is why "+
				"this is not done twice", network, shortARN(o.RevisionARN), shortARN(in.Record.RevisionRef)),
		})
	}

	// Image drift, in BOTH modes, on the DIGEST. A drift check against a tag can only ever report that
	// the tag still matches, and in manual mode this is the check that catches a customer who
	// applied a previous release's artifact — nothing converges a manual deployment, so nothing else
	// would.
	if in.Pin.Digest != "" && o.Revision.Digest != "" && o.Revision.Digest != in.Pin.Digest {
		severity, remedy := collectorstatus.SeverityPending,
			"the next converge pass registers a revision on the expected build"
		if in.Mode == spec.ModeManual {
			severity = collectorstatus.SeverityDegraded
			remedy = "nothing converges a manual deployment, so re-apply the published artifacts for this " +
				"network — they carry the digest verbatim for exactly this reason"
		}
		problems = append(problems, collectorstatus.Problem{
			Code:     ProblemImageDrift,
			Severity: severity,
			Message: fmt.Sprintf("the Collector on %s is running image digest %s and this connector expects "+
				"%s (%s); %s", network, shortDigest(o.Revision.Digest), shortDigest(in.Pin.Digest),
				collectorimage.Describe(in.Pin, collectorimage.CloudAWS), remedy),
		})
	}
	return problems
}

// verdict reduces the problems to what gets recorded on the row.
//
// The observer never records Ready, and that is a deliberate division rather than an omission. Readiness
// is a claim about the configuration plane as well as the infrastructure, and the OpAMP session is the
// only party that can see whether the Collector accepted its configuration — so it is the party that
// clears a status. This one escalates.
func verdict(in DiagnosisInput, problems []collectorstatus.Problem) (collectorstatus.Status, string) {
	base := collectorstatus.Status("")
	switch {
	case !in.Observation.ServiceFound && !in.Observation.Partial:
		// Nothing is deployed. Deploying in automatic mode says Odigos is working on it; Pending in
		// manual mode becomes AwaitingCustomer in the report, once liveness is folded in.
		base = collectorstatus.StatusPending
		if in.Mode == spec.ModeAutomatic {
			base = collectorstatus.StatusDeploying
		}
	case in.Observation.Live() && in.Observation.RunningCount < in.Observation.DesiredCount:
		base = collectorstatus.StatusDeploying
	}

	status := collectorstatus.StatusFor(problems, base)
	if status == "" {
		return "", ""
	}
	return status, describe(problems)
}

// describe renders the problems into the single sentence the row carries, worst first and capped.
//
// Capped because managed_collectors.message is read as a sentence, not a log: a Collector with eight
// findings has one that matters and seven that follow from it, and the first two are the ones anybody
// reads. Every problem still reaches the caller in Diagnosis.Problems.
func describe(problems []collectorstatus.Problem) string {
	const most = 2
	parts := make([]string, 0, most)
	for _, p := range problems {
		if p.Severity == collectorstatus.SeverityInfo {
			continue
		}
		parts = append(parts, p.Message)
		if len(parts) == most {
			break
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " · ")
}

// worstFirst orders problems by severity, stably.
//
// Stable because the authoring order inside a severity is already meaningful — placement before the
// workload before the data path before the configuration plane — and a sort that reshuffled equals would
// make the recorded message change between two identical passes.
func worstFirst(problems []collectorstatus.Problem) []collectorstatus.Problem {
	out := append([]collectorstatus.Problem(nil), problems...)
	sort.SliceStable(out, func(i, j int) bool {
		return severityRank(out[i].Severity) < severityRank(out[j].Severity)
	})
	return out
}

func severityRank(s collectorstatus.Severity) int {
	switch s {
	case collectorstatus.SeverityError:
		return 0
	case collectorstatus.SeverityDegraded:
		return 1
	case collectorstatus.SeverityPending:
		return 2
	default:
		return 3
	}
}

// rolloutFailed is the ECS deployment rollout state that means the scheduler has stopped trying.
const rolloutFailed = "FAILED"

// dockerHubHost is the registry whose anonymous pulls are rate-limited per source address.
const dockerHubHost = "docker.io"

// firstFault returns the most recent stopped task whose cause is worth reporting.
//
// The list is newest first, and only the first unexpected stop is returned: a service that cannot start
// a task has stopped dozens for one reason, and an expected stop in front of them — a deployment
// replacing tasks, a Spot interruption — must not hide the fault behind it.
func firstFault(tasks []StoppedTask) (StopCause, StoppedTask) {
	for _, task := range tasks {
		if cause := task.Cause(); cause != StopCauseExpected {
			return cause, task
		}
	}
	return StopCauseExpected, StoppedTask{}
}

// serving reports whether every target group that exists has a healthy target, so a silence over OpAMP
// is not explained by an unreachable Collector.
func serving(o Observation) bool {
	for _, group := range o.TargetGroups {
		if group.Healthy() == 0 {
			return false
		}
	}
	return true
}

func hasCode(problems []collectorstatus.Problem, code string) bool {
	for _, p := range problems {
		if p.Code == code {
			return true
		}
	}
	return false
}

// hasAnyStopFault reports whether a task-level fault is already reported, so the generic "nothing is
// running" is not added beside the specific reason nothing is running.
func hasAnyStopFault(problems []collectorstatus.Problem) bool {
	for _, code := range []string{
		ProblemImagePullFailed, ProblemSecretFetchFailed, ProblemTaskOutOfMemory, ProblemTaskStopped,
	} {
		if hasCode(problems, code) {
			return true
		}
	}
	return false
}

func reasonOr(reason, fallback string) string {
	if strings.TrimSpace(reason) == "" {
		return fallback
	}
	return reason
}

func reasonSuffix(reason, description string) string {
	switch {
	case reason == "" && description == "":
		return ""
	case description == "":
		return fmt.Sprintf(" (%s)", reason)
	case reason == "":
		return fmt.Sprintf(" (%s)", description)
	}
	return fmt.Sprintf(" (%s: %s)", reason, description)
}

func stopCodeNote(code string) string {
	if code == "" {
		return ""
	}
	return fmt.Sprintf(" [stop code %s]", code)
}

func exitCodeNote(code *int32) string {
	if code == nil {
		return ""
	}
	return fmt.Sprintf(" with exit code %d", *code)
}

func joinOr(values []string, fallback string) string {
	if len(values) == 0 {
		return fallback
	}
	return strings.Join(values, ", ")
}

func endpointOr(endpoint, fallback string) string {
	if strings.TrimSpace(endpoint) == "" {
		return fallback
	}
	return endpoint
}

func memoryOr(memory string) string {
	if memory == "" {
		return "configured"
	}
	return memory
}

func registryOr(registry string) string {
	if registry == "" {
		return "its registry"
	}
	return registry
}

// imageRegistry is the host an image reference pulls from, or empty for an unqualified Docker Hub
// reference — which is itself the answer, since that is Docker Hub.
func imageRegistry(image string) string {
	first, _, found := strings.Cut(image, "/")
	if !found || !strings.Contains(first, ".") {
		return dockerHubHost
	}
	return first
}

func secretPrefixFor(in DiagnosisInput) string {
	return deployer.CredentialSecretName(in.Connector, in.Network.Region)
}

func logGroupFor(in DiagnosisInput) string {
	return deployer.LogGroupName(in.Connector)
}

// shortARN keeps the tail of an ARN, which is the part that differs between two revisions of one family.
func shortARN(arn string) string {
	if i := strings.LastIndex(arn, "/"); i >= 0 && i+1 < len(arn) {
		return arn[i+1:]
	}
	return arn
}

// shortDigest keeps enough of a digest to tell two builds apart without filling the sentence.
func shortDigest(digest string) string {
	if len(digest) > 19 {
		return digest[:19]
	}
	return digest
}
