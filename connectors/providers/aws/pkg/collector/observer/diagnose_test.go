package observer

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorreport"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/deployer"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/collector/placement"
	"github.com/odigos-io/odigos/connectors/spec"
)

// healthy is a Collector with nothing wrong with it: the service running its recorded revision on the
// pinned image, both target groups serving, and an OpAMP session that was alive a moment ago.
func healthy(mutate func(*DiagnosisInput)) DiagnosisInput {
	in := DiagnosisInput{
		Network:   testNetwork(),
		Connector: testConnector,
		Mode:      spec.ModeAutomatic,
		Settings:  testSettings().Region(testRegion).Network,
		Record: basestore.CollectorRecord{
			Key:             testKey(),
			NetworkID:       testVPC,
			WorkloadRef:     serviceARN(),
			RevisionRef:     revisionARN(1),
			CredentialRef:   "arn:aws:secretsmanager:eu-west-1:123456789012:secret:odigos/collector/prod/eu-west-1/credential-AbCdEf",
			OpAMPLastSeenAt: ago(10 * time.Second),
			AgentHealth:     "Healthy",
			Status:          collectorstatus.StatusReady,
		},
		Observation:   healthyObservation(),
		Liveness:      collectorreport.LivenessLive,
		OpAMPEndpoint: testEndpoint,
		Pin:           testPin(),
	}
	if mutate != nil {
		mutate(&in)
	}
	return in
}

func healthyObservation() Observation {
	return Observation{
		ServiceFound: true,
		Status:       "ACTIVE",
		ServiceARN:   serviceARN(),
		ClusterARN:   "arn:aws:ecs:eu-west-1:123456789012:cluster/odigos-collector",
		RevisionARN:  revisionARN(1),
		Tags:         ourTags(),
		DesiredCount: 2,
		RunningCount: 2,
		Rollout:      "COMPLETED",
		Revision: Revision{
			ARN:                      revisionARN(1),
			Image:                    "ghcr.io/odigos-io/odigos-enterprise-collector@" + testDigest,
			Digest:                   testDigest,
			Architecture:             "X86_64",
			CPU:                      "1024",
			Memory:                   "2048",
			Environment:              map[string]string{"ODIGOS_OPAMP_ENDPOINT": testEndpoint},
			RepositoryCredentialsRef: "arn:aws:secretsmanager:eu-west-1:123456789012:secret:odigos/collector/prod/eu-west-1/license-AbCdEf",
			Secrets: map[string]string{
				"ODIGOS_ONPREM_TOKEN":        "arn:aws:secretsmanager:eu-west-1:123456789012:secret:odigos/collector/prod/eu-west-1/license-AbCdEf:password::",
				"ODIGOS_OPAMP_AUTHORIZATION": "arn:aws:secretsmanager:eu-west-1:123456789012:secret:odigos/collector/prod/eu-west-1/credential-AbCdEf",
			},
			LogGroup: deployer.LogGroupName(testConnector),
		},
		TargetGroups: []TargetGroup{
			{ARN: targetGroupARN(4317), Port: 4317, Targets: []Target{{State: targetHealthy}, {State: targetHealthy}}},
			{ARN: targetGroupARN(4318), Port: 4318, Targets: []Target{{State: targetHealthy}, {State: targetHealthy}}},
		},
		LoadBalancer: LoadBalancer{ARN: loadBalancerARN, DNSName: testLBDNS, State: lbActive, Scheme: "internal"},
		Subnets:      []string{"subnet-a", "subnet-b"},
	}
}

// TestAWorkingCollectorIsDiagnosedWithNothing. The steady state, and the assertion that matters most:
// a diagnosis that found something wrong with a healthy Collector would put a standing fault on every
// row in the fleet, and nobody would read the next one.
func TestAWorkingCollectorIsDiagnosedWithNothing(t *testing.T) {
	got := Diagnose(healthy(nil))
	if fault := faults(got.Problems); len(fault) != 0 {
		t.Fatalf("faults on a working Collector: %v", codes(fault))
	}
	// The plaintext posture IS reported, at Info, on every pass for as long as it applies — is
	// explicit that a standing posture stated once is invisible to everyone who looks later.
	if !hasProblem(got.Problems, "PlaintextTelemetry") {
		t.Errorf("the plaintext default was not reported: %v", codes(got.Problems))
	}
	// And nothing is recorded, because the observer never claims Ready — that is the OpAMP session's to
	// say, since readiness is a claim about the configuration plane too.
	if got.Records() {
		t.Errorf("recorded %q / %q over a working Collector", got.Status, got.Message)
	}
	// The endpoint is still resolved, because that is what workloads are pointed at.
	if got.Transport.GRPCEndpoint == "" || !strings.Contains(got.Transport.GRPCEndpoint, testLBDNS) {
		t.Errorf("endpoint = %q", got.Transport.GRPCEndpoint)
	}
}

// TestAMissingWorkloadMeansThreeDifferentThings — the "recreation of missing automatic resources"
// criterion, and the reason one code carries three sentences. Three different people have to act, and a
// single "no Collector found" would send all three to the wrong place.
func TestAMissingWorkloadMeansThreeDifferentThings(t *testing.T) {
	cases := map[string]struct {
		mutate       func(*DiagnosisInput)
		wantSeverity collectorstatus.Severity
		wantStatus   collectorstatus.Status
		wantSaid     string
	}{
		"automatic, never built": {
			mutate: func(in *DiagnosisInput) {
				in.Observation = Observation{}
				in.Record.WorkloadRef = ""
				in.Liveness = collectorreport.LivenessNeverConnected
			},
			// Pending, not Degraded: reporting Degraded here would call every new Collector broken for
			// the minute it takes to create one.
			wantSeverity: collectorstatus.SeverityPending,
			wantStatus:   collectorstatus.StatusDeploying,
			wantSaid:     "Odigos is building it",
		},
		"automatic, deleted from under Odigos": {
			mutate: func(in *DiagnosisInput) {
				in.Observation = Observation{}
				in.Liveness = collectorreport.LivenessStale
			},
			// Degraded: telemetry is being dropped right now, and the recreation is named so nobody goes
			// looking for a control that has already been triggered.
			wantSeverity: collectorstatus.SeverityDegraded,
			wantStatus:   collectorstatus.StatusDegraded,
			wantSaid:     "recreates it on the next converge pass",
		},
		"manual, not deployed yet": {
			mutate: func(in *DiagnosisInput) {
				in.Mode = spec.ModeManual
				in.Observation = Observation{}
				in.Record.WorkloadRef = ""
				in.Liveness = collectorreport.LivenessNeverConnected
			},
			wantSeverity: collectorstatus.SeverityPending,
			wantStatus:   collectorstatus.StatusPending,
			wantSaid:     "apply the published deployment artifacts",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Diagnose(healthy(tc.mutate))
			problem := problemWith(t, got.Problems, ProblemWorkloadMissing)
			if problem.Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", problem.Severity, tc.wantSeverity)
			}
			if !strings.Contains(problem.Message, tc.wantSaid) {
				t.Errorf("message does not say %q: %q", tc.wantSaid, problem.Message)
			}
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tc.wantStatus)
			}
		})
	}
}

// TestAReadFailureIsNeverReportedAsAMissingCollector. The one mistake that turns a healthy fleet into a
// page: a throttled DescribeServices would otherwise read as "the Collector is gone" and, in automatic
// mode, as a Collector that needs recreating.
func TestAReadFailureIsNeverReportedAsAMissingCollector(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation = Observation{
			Partial: true,
			Problems: []collectorstatus.Problem{{
				Code: ProblemObservationFailed, Severity: collectorstatus.SeverityDegraded,
				Message: "Odigos could not read the state of the Collector",
			}},
		}
	}))
	if hasProblem(got.Problems, ProblemWorkloadMissing) {
		t.Fatalf("a failed read was reported as a missing Collector: %v", codes(got.Problems))
	}
	problemWith(t, got.Problems, ProblemObservationFailed)
	// And no verdict about the Collector itself: Deploying would say Odigos is building one, which it
	// is not.
	if got.Status == collectorstatus.StatusDeploying {
		t.Errorf("status = %q over a failed read", got.Status)
	}
}

// TestEveryStoppedTaskCauseHasItsOwnRemedy walks task rows. Each assertion is on the REMEDY in the
// message, not on the code: a customer reading "the Collector is unhealthy" learns nothing, and the
// codes exist so a caller can branch, not so a reader can.
func TestEveryStoppedTaskCauseHasItsOwnRemedy(t *testing.T) {
	cases := map[string]struct {
		reason   string
		image    string
		wantCode string
		wantSaid []string
	}{
		"an image that cannot be pulled": {
			reason:   "CannotPullContainerError: dial tcp: i/o timeout",
			wantCode: ProblemImagePullFailed,
			// The registry, the digest and the egress requirement — all three, because the fix is one of
			// them and the message cannot tell which.
			wantSaid: []string{"ghcr.io", "egress to the registry", testDigest},
		},
		"an anonymous Docker Hub pull": {
			reason:   "CannotPullContainerError: toomanyrequests: pull rate limit",
			image:    "keyval/odigos-enterprise-collector@" + testDigest,
			wantCode: ProblemImagePullFailed,
			// The NAT-address rate limit, which is why prefers GHCR and is invisible otherwise: a
			// scale-out is exactly when the limit is reached.
			wantSaid: []string{"rate-limited", "NAT address"},
		},
		"a secret the execution role cannot read": {
			reason:   "ResourceInitializationError: unable to pull secrets or registry auth: AccessDeniedException",
			wantCode: ProblemSecretFetchFailed,
			wantSaid: []string{"execution role", "route to Secrets Manager", "odigos/collector/prod"},
		},
		"a container killed for its memory": {
			reason:   "OutOfMemoryError: Container killed due to memory usage",
			wantCode: ProblemTaskOutOfMemory,
			wantSaid: []string{"collector.regions." + testRegion + ".memory", "configuration push"},
		},
		"a Collector that started and exited": {
			reason:   "Essential container in task exited",
			wantCode: ProblemTaskStopped,
			wantSaid: []string{"/odigos/collector/prod", "exit code 1"},
		},
		"a reason nobody recognises": {
			reason:   "CannotStartContainerError: failed to configure ENI",
			wantCode: ProblemTaskStopped,
			wantSaid: []string{"failed to configure ENI", "exactly as ECS gave it"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Diagnose(healthy(func(in *DiagnosisInput) {
				in.Observation.RunningCount = 0
				if tc.image != "" {
					in.Observation.Revision.Image = tc.image
				}
				in.Observation.StoppedTasks = []StoppedTask{{
					StopCode:         "TaskFailedToStart",
					Reason:           "Task failed to start",
					ContainerReasons: []string{tc.reason},
					ExitCode:         aws.Int32(1),
				}}
			}))
			problem := problemWith(t, got.Problems, tc.wantCode)
			for _, want := range tc.wantSaid {
				if !strings.Contains(problem.Message, want) {
					t.Errorf("message does not name %q: %q", want, problem.Message)
				}
			}
			// And the generic "nothing is running" is NOT added beside the specific reason nothing is
			// running: two problems for one fault is how a message stops being read.
			if hasProblem(got.Problems, ProblemNoTaskRunning) {
				t.Errorf("the generic no-task problem was reported beside %s: %v", tc.wantCode, codes(got.Problems))
			}
		})
	}
}

// TestARoutineReplacementDoesNotHideTheFaultBehindIt. A crash-looping Collector's most recent stopped
// task is often the scheduler's own replacement, and stopping at the first entry would report a
// deployment where there is a failure.
func TestARoutineReplacementDoesNotHideTheFaultBehindIt(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.RunningCount = 0
		in.Observation.StoppedTasks = []StoppedTask{
			{StopCode: stopCodeServiceSchedulerInitiated, Reason: "Scaling activity initiated by deployment"},
			{StopCode: "TaskFailedToStart", ContainerReasons: []string{"CannotPullContainerError: manifest unknown"}},
		}
	}))
	problemWith(t, got.Problems, ProblemImagePullFailed)
}

// TestASpotInterruptionIsNotAFault. opt-in replaces tasks as a matter of course, so a Spot
// network would otherwise sit permanently Degraded — and the report nobody trusts is worse than none.
func TestASpotInterruptionIsNotAFault(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.StoppedTasks = []StoppedTask{{
			StopCode: stopCodeSpotInterruption,
			Reason:   "Your Spot Task was interrupted",
		}}
	}))
	if fault := faults(got.Problems); len(fault) != 0 {
		t.Fatalf("a Spot interruption was reported as a fault: %v", codes(fault))
	}
}

// TestUnhealthyTargetsNameTheTwoThingsItAlwaysIs, and separate them by the reason ELB gave: a check that
// got no answer is the security group, and one answered with the wrong thing is the port or the
// Collector. says it is almost always one of the two; the reason is what tells them apart.
func TestUnhealthyTargetsNameTheTwoThingsItAlwaysIs(t *testing.T) {
	cases := map[string]struct {
		reason   string
		wantSaid string
	}{
		"a check that got no answer": {
			reasonTimeout, "security group rather than the",
		},
		"a check answered with the wrong thing": {
			reasonFailedHealthChecks, "not serving health on 13133",
		},
		"a reason ELB did not give": {
			"", "security group admits the health-check port 13133",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := Diagnose(healthy(func(in *DiagnosisInput) {
				in.Observation.TargetGroups[0].Targets = []Target{
					{State: targetUnhealthy, Reason: tc.reason, Description: "Health checks failed"},
				}
			}))
			problem := problemWith(t, got.Problems, ProblemTargetsUnhealthy)
			if !strings.Contains(problem.Message, tc.wantSaid) {
				t.Errorf("message does not say %q: %q", tc.wantSaid, problem.Message)
			}
			if !strings.Contains(problem.Message, "0 of 1") {
				t.Errorf("message does not count the targets: %q", problem.Message)
			}
			// No healthy target in a group is an Error: nothing reaches the Collector through that port.
			if problem.Severity != collectorstatus.SeverityError {
				t.Errorf("severity = %q, want Error", problem.Severity)
			}
		})
	}
}

// TestSomeTargetsHealthyIsDegradedRatherThanBroken: the Collector is serving on the healthy targets, so
// telemetry is flowing and the fault is capacity rather than reachability.
func TestSomeTargetsHealthyIsDegradedRatherThanBroken(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.TargetGroups[0].Targets = []Target{
			{State: targetHealthy},
			{State: targetUnhealthy, Reason: reasonTimeout},
		}
	}))
	problem := problemWith(t, got.Problems, ProblemTargetsUnhealthy)
	if problem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", problem.Severity)
	}
}

// TestTargetsStillComingUpAreNotAFault. Every deployment passes through this state, and a Collector
// reported broken for the first half-minute of its life is a report that gets ignored.
func TestTargetsStillComingUpAreNotAFault(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.TargetGroups[0].Targets = []Target{
			{State: targetInitial, Reason: reasonInitialHealthChecking},
		}
	}))
	problem := problemWith(t, got.Problems, ProblemTargetsUnhealthy)
	if problem.Severity != collectorstatus.SeverityPending {
		t.Errorf("severity = %q, want Pending", problem.Severity)
	}
	if got.Records() && got.Status == collectorstatus.StatusDegraded {
		t.Errorf("a Collector still coming up was recorded as %q", got.Status)
	}
}

// TestTasksRunningButNotRegisteredIsItsOwnFault. Not the same as an unhealthy target: the tasks were
// never attached, which is the service's load balancer attachment rather than the network rules.
func TestTasksRunningButNotRegisteredIsItsOwnFault(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.TargetGroups[0].Targets = nil
	}))
	problem := problemWith(t, got.Problems, ProblemNoTargetsRegistered)
	if !strings.Contains(problem.Message, "container name and port") {
		t.Errorf("message does not name the attachment: %q", problem.Message)
	}
	// And no unhealthy-targets problem for the same group, which would be the same fault twice.
	if hasProblem(got.Problems, ProblemTargetsUnhealthy) {
		t.Errorf("both problems reported for one group: %v", codes(got.Problems))
	}
}

// TestAnUnregisteredGroupIsNotReportedWhenNothingIsRunning: with no task, there is nothing to register,
// and whatever stopped the tasks already says why.
func TestAnUnregisteredGroupIsNotReportedWhenNothingIsRunning(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.RunningCount = 0
		in.Observation.TargetGroups[0].Targets = nil
		in.Observation.TargetGroups[1].Targets = nil
	}))
	if hasProblem(got.Problems, ProblemNoTargetsRegistered) {
		t.Fatalf("registration reported with no tasks: %v", codes(got.Problems))
	}
}

// TestAnEndpointOverrideMeansNoTargetHealthToJudge: Odigos created no load balancer, so target
// health is not a fault it can have.
func TestAnEndpointOverrideMeansNoTargetHealthToJudge(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Settings.EndpointOverride = "collector.internal.example.com:4317"
		in.Settings.TLS.EndpointOverride = in.Settings.EndpointOverride
		in.Observation.TargetGroups = nil
		in.Observation.LoadBalancer = LoadBalancer{}
	}))
	if hasProblem(got.Problems, ProblemTargetsUnhealthy) || hasProblem(got.Problems, ProblemNoTargetsRegistered) {
		t.Fatalf("target health judged with no load balancer: %v", codes(got.Problems))
	}
	if got.EndpointSource != "override" {
		t.Errorf("endpoint source = %q, want override", got.EndpointSource)
	}
	if !strings.Contains(got.Transport.GRPCEndpoint, "collector.internal.example.com") {
		t.Errorf("endpoint = %q, want the customer's own front", got.Transport.GRPCEndpoint)
	}
}

// TestALoadBalancerStillProvisioningIsPending, and one that FAILED is not: the first clears on its own
// and the second does not.
func TestALoadBalancerStillProvisioningIsPending(t *testing.T) {
	for state, want := range map[string]collectorstatus.Severity{
		lbProvisioning: collectorstatus.SeverityPending,
		lbFailed:       collectorstatus.SeverityDegraded,
	} {
		got := Diagnose(healthy(func(in *DiagnosisInput) {
			in.Observation.LoadBalancer.State = state
		}))
		problem := problemWith(t, got.Problems, ProblemLoadBalancerUnavailable)
		if problem.Severity != want {
			t.Errorf("%s: severity = %q, want %q", state, problem.Severity, want)
		}
	}
	// active_impaired still carries traffic, so it is not reported at all.
	if got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.LoadBalancer.State = lbActiveImpaired
	})); hasProblem(got.Problems, ProblemLoadBalancerUnavailable) {
		t.Error("an impaired but serving load balancer was reported as unavailable")
	}
}

func TestPublicLoadBalancerIsNotPublishedAsCollectorEndpoint(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.LoadBalancer.Scheme = "internet-facing"
	}))
	if !hasProblem(got.Problems, ProblemLoadBalancerShapeInvalid) {
		t.Fatalf("public load balancer was accepted: %v", codes(got.Problems))
	}
	if got.Transport.GRPCEndpoint != "" {
		t.Fatalf("public endpoint was published: %+v", got.Transport)
	}
}

func TestWrongTargetHealthPortIsNotPublishedAsCollectorEndpoint(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.TargetGroups[0].HealthCheckPort = "traffic-port"
	}))
	if !hasProblem(got.Problems, ProblemLoadBalancerShapeInvalid) || got.Transport.GRPCEndpoint != "" {
		t.Fatalf("wrong target health port was accepted: %+v", got)
	}
}

func TestWrongListenerTargetIsNotPublishedAsCollectorEndpoint(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.LoadBalancer.ListenerTargets = map[int]string{4317: targetGroupARN(4317), 4318: targetGroupARN(4317)}
	}))
	if !hasProblem(got.Problems, ProblemLoadBalancerShapeInvalid) || got.Transport.GRPCEndpoint != "" {
		t.Fatalf("wrong listener target was accepted: %+v", got)
	}
}

func TestManualTaskDefinitionRequiresPrivateImageAndSecretWiring(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Mode = spec.ModeManual
		in.Observation.Revision.RepositoryCredentialsRef = ""
		in.Observation.Revision.Secrets = nil
		in.Observation.Revision.LogGroup = "/wrong"
	}))
	count := 0
	for _, problem := range got.Problems {
		if problem.Code == ProblemTaskDefinitionInvalid {
			count++
		}
	}
	if count != 4 {
		t.Fatalf("got %d task definition problems, want 4: %v", count, got.Problems)
	}
}

func TestManualPublicECRTaskStillRequiresRuntimeLicenseButNotRegistryCredentials(t *testing.T) {
	in := healthy(func(in *DiagnosisInput) {
		in.Mode = spec.ModeManual
		in.Observation.Revision.Image = "public.ecr.aws/example/collector:v2"
		in.Observation.Revision.RepositoryCredentialsRef = ""
	})
	if got := manualTaskDefinitionProblems(in); len(got) != 0 {
		t.Fatalf("anonymous public ECR pull rejected: %+v", got)
	}
	delete(in.Observation.Revision.Secrets, collectorimage.EnvOnPremToken)
	if got := manualTaskDefinitionProblems(in); len(got) != 1 || !strings.Contains(got[0].Message, "ODIGOS_ONPREM_TOKEN") {
		t.Fatalf("missing runtime license not reported: %+v", got)
	}
}

func TestManualSecurityGroupRequiresSelfHealthIngress(t *testing.T) {
	observer := &Observer{Mode: spec.ModeManual, plan: &placement.Plan{Topology: placement.NewTopologyWithSecurityGroups(
		[]string{testRegion}, []placement.VPC{{Region: testRegion, ID: testVPC}}, nil, nil,
		[]placement.SecurityGroup{{Region: testRegion, ID: "sg-collector", VPCID: testVPC}},
	)}}
	observation := healthyObservation()
	observation.SecurityGroups = []string{"sg-collector"}
	problems := observer.manualSecurityGroupProblems(testNetwork(), observation)
	if len(problems) != 1 || problems[0].Code != ProblemSecurityGroupInvalid {
		t.Fatalf("missing manual security-group issue: %+v", problems)
	}
}

// TestDeployedButNeverConnectedNamesAllThreeCauses, and the message is the whole value: the
// connector cannot tell a refused credential from blocked egress from a wrong endpoint, because the
// session it would have learned that from is the one that never happened.
func TestDeployedButNeverConnectedNamesAllThreeCauses(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Liveness = collectorreport.LivenessNeverConnected
		in.Record.OpAMPLastSeenAt = nil
	}))
	problem := problemWith(t, got.Problems, ProblemNeverConnected)
	for _, want := range []string{"egress", "credential", "endpoint", testEndpoint} {
		if !strings.Contains(problem.Message, want) {
			t.Errorf("message does not name %q: %q", want, problem.Message)
		}
	}
	if !strings.Contains(problem.Message, "inert bootstrap configuration") {
		t.Errorf("message does not say what the Collector is doing meanwhile: %q", problem.Message)
	}
}

// TestSilenceIsNotReportedWhenItIsAlreadyExplained. A Collector whose tasks will not start has not
// connected for an obvious reason, and adding "it has never connected" to "it cannot pull its image"
// buries the one that matters.
func TestSilenceIsNotReportedWhenItIsAlreadyExplained(t *testing.T) {
	for name, mutate := range map[string]func(*DiagnosisInput){
		"nothing is running": func(in *DiagnosisInput) {
			in.Observation.RunningCount = 0
		},
		"nothing is reachable": func(in *DiagnosisInput) {
			in.Observation.TargetGroups[0].Targets = []Target{{State: targetUnhealthy, Reason: reasonTimeout}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := Diagnose(healthy(func(in *DiagnosisInput) {
				in.Liveness = collectorreport.LivenessNeverConnected
				in.Record.OpAMPLastSeenAt = nil
				mutate(in)
			}))
			if hasProblem(got.Problems, ProblemNeverConnected) {
				t.Fatalf("silence reported over an explained failure: %v", codes(got.Problems))
			}
		})
	}
}

// TestTheLicenceIsDiagnosedAsALicence, never as a configuration. is explicit, and this is the
// assertion that holds it: the message has to say so in words, because the two failures look identical
// and a reader who guesses wrong edits a pipeline for an hour.
func TestTheLicenceIsDiagnosedAsALicence(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Record.AgentHealth = "Unhealthy: odigos enterprise on-prem token expired"
	}))
	problem := problemWith(t, got.Problems, ProblemLicenseExpired)
	if problem.Severity != collectorstatus.SeverityError {
		t.Errorf("severity = %q, want Error", problem.Severity)
	}
	for _, want := range []string{"LICENCE failure and not a", "Renew the licence"} {
		if !strings.Contains(problem.Message, want) {
			t.Errorf("message does not say %q: %q", want, problem.Message)
		}
	}
	if got.Status != collectorstatus.StatusError {
		t.Errorf("status = %q, want Error", got.Status)
	}
}

// TestAnExporterFailureIsNotADeploymentFailure again, in words, because the whole point of the row
// is that redeploying the Collector will not change it.
func TestAnExporterFailureIsNotADeploymentFailure(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Record.AgentHealth = "Unhealthy: exporter otlp: permanent error: rpc error: code = Unauthenticated"
	}))
	problem := problemWith(t, got.Problems, ProblemExporterFailing)
	if problem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("severity = %q, want Degraded", problem.Severity)
	}
	for _, want := range []string{"NOT a deployment failure", "redeploying the Collector will not change it"} {
		if !strings.Contains(problem.Message, want) {
			t.Errorf("message does not say %q: %q", want, problem.Message)
		}
	}
	// And the verbatim reason, because "cannot reach the destination" without the code is unactionable.
	if !strings.Contains(problem.Message, "Unauthenticated") {
		t.Errorf("message drops the agent's own reason: %q", problem.Message)
	}
}

// TestARejectedConfigurationIsNotInventedHere. The OpAMP session sees the agent's RemoteConfigStatus and
// records the rejection with the validation error verbatim; this package sees neither, and a config class
// guessed from a health string would be a guess over the top of a writer who knows.
func TestARejectedConfigurationIsNotInventedHere(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		// The row as the session left it after a rejection: Degraded, with the agent still reporting
		// healthy because its own rollback restored the last good configuration.
		in.Record.Status = collectorstatus.StatusDegraded
		in.Record.Message = "the Collector rejected this configuration and is still running the last one " +
			"that applied cleanly. It reported: unknown type: \"batchh\""
	}))
	if fault := faults(got.Problems); len(fault) != 0 {
		t.Fatalf("faults invented over a config rejection: %v", codes(fault))
	}
	// Nothing to record, so the session's sentence survives — writesStatus is what enforces that, and
	// this is the input it needs.
	if got.Records() {
		t.Fatalf("recorded %q over the session's own verdict", got.Status)
	}
}

// TestEndpointDriftIsCheckedAgainstTheDeployedValue. The endpoint a Collector was deployed against lives
// in its own task definition and in no column, so this is the only place the check can be made — the
// base package's own attempt compared the data endpoint with a config override and could never fire.
func TestEndpointDriftIsCheckedAgainstTheDeployedValue(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.Revision.Environment["ODIGOS_OPAMP_ENDPOINT"] = "wss://old-central.example.com/v1/opamp"
	}))
	if !hasProblem(got.Problems, "OpAMPEndpointMovedRedeployPending") {
		t.Fatalf("endpoint drift not reported: %v", codes(got.Problems))
	}
	// Manual mode says the customer has to redeploy, because Odigos will not.
	manual := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Mode = spec.ModeManual
		in.Observation.Revision.Environment["ODIGOS_OPAMP_ENDPOINT"] = "wss://old-central.example.com/v1/opamp"
	}))
	if !hasProblem(manual.Problems, "OpAMPEndpointMovedAwaitingCustomer") {
		t.Fatalf("manual-mode endpoint drift not reported: %v", codes(manual.Problems))
	}
	// And an unchanged endpoint is not drift.
	if hasProblem(Diagnose(healthy(nil)).Problems, "OpAMPEndpointMovedRedeployPending") {
		t.Error("drift reported on a matching endpoint")
	}
}

// TestImageDriftIsMatchedOnTheDigestInBothModes. A drift check against a tag can only ever report that
// the tag still matches — and in manual mode this is the check that catches a customer who
// applied a previous release's artifact, because nothing converges a manual deployment.
func TestImageDriftIsMatchedOnTheDigestInBothModes(t *testing.T) {
	const other = "sha256:2222222222222222222222222222222222222222222222222222222222222222"

	auto := Diagnose(healthy(func(in *DiagnosisInput) { in.Observation.Revision.Digest = other }))
	autoProblem := problemWith(t, auto.Problems, ProblemImageDrift)
	if autoProblem.Severity != collectorstatus.SeverityPending {
		t.Errorf("automatic severity = %q, want Pending: the converge fixes it", autoProblem.Severity)
	}

	manual := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Mode = spec.ModeManual
		in.Observation.Revision.Digest = other
	}))
	manualProblem := problemWith(t, manual.Problems, ProblemImageDrift)
	if manualProblem.Severity != collectorstatus.SeverityDegraded {
		t.Errorf("manual severity = %q, want Degraded: nothing converges it", manualProblem.Severity)
	}
	if !strings.Contains(manualProblem.Message, "re-apply the published artifacts") {
		t.Errorf("manual message does not name the remedy: %q", manualProblem.Message)
	}

	// The SAME digest under a different tag is not drift, which is the half a tag comparison would get
	// backwards.
	if got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.Revision.Tag = "v0.0.1-old"
	})); hasProblem(got.Problems, ProblemImageDrift) {
		t.Error("drift reported on a matching digest with a different tag")
	}
	// A tag-only installation is valid, but an older digest-only task needs redeployment.
	if got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Pin.Digest = ""
	})); !hasProblem(got.Problems, ProblemImageDrift) {
		t.Error("switching from digest to tag did not report image drift")
	}
}

func TestRuntimeImageDriftInBothModes(t *testing.T) {
	for _, mode := range []string{spec.ModeAutomatic, spec.ModeManual} {
		t.Run(string(mode), func(t *testing.T) {
			in := healthy(func(in *DiagnosisInput) {
				in.Mode = mode
				in.Settings.Image = collectorimage.Selection{"registry": "mirror.example.com", "tag": "v2.0.0"}
			})
			got := problemWith(t, Diagnose(in).Problems, ProblemImageDrift)
			wantSeverity := collectorstatus.SeverityPending
			if mode == spec.ModeManual {
				wantSeverity = collectorstatus.SeverityDegraded
			}
			if got.Severity != wantSeverity || !strings.Contains(got.Message, "mirror.example.com/odigos-io/odigos-enterprise-collector:v2.0.0") {
				t.Fatalf("runtime image drift = %+v", got)
			}
			in.Observation.Revision.Image = "mirror.example.com/odigos-io/odigos-enterprise-collector:v2.0.0"
			in.Observation.Revision.Tag = "v2.0.0"
			if hasProblem(Diagnose(in).Problems, ProblemImageDrift) {
				t.Fatal("matching runtime image still reported as drifted")
			}
		})
	}
}

// TestRevisionDriftIsAutomaticModeOnly. In manual mode the recorded revision is whatever this observer
// last wrote down, so comparing the two would be comparing a value with itself — a report that says
// nothing, on every pass, for ever.
func TestRevisionDriftIsAutomaticModeOnly(t *testing.T) {
	auto := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.RevisionARN = revisionARN(4)
	}))
	problem := problemWith(t, auto.Problems, ProblemRevisionDrift)
	if !strings.Contains(problem.Message, "4") || !strings.Contains(problem.Message, "1") {
		t.Errorf("message does not name both revisions: %q", problem.Message)
	}

	manual := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Mode = spec.ModeManual
		in.Observation.RevisionARN = revisionARN(4)
	}))
	if hasProblem(manual.Problems, ProblemRevisionDrift) {
		t.Errorf("revision drift reported in manual mode: %v", codes(manual.Problems))
	}
}

// TestPlacementRefusalsAreReportedAgainstTheCollector, and the first row. A network with no usable subnet
// has no other place to be reported, and dropping it from the fleet would turn a refusal into silence.
func TestPlacementRefusalsAreReportedAgainstTheCollector(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation = Observation{}
		in.Record.WorkloadRef = ""
		in.Placement = []collectorstatus.Problem{{
			Code:     "CollectorNoUsableSubnet",
			Severity: collectorstatus.SeverityError,
			Message:  "no subnet in vpc-0abc can reach the internet",
		}}
	}))
	if !hasProblem(got.Problems, "CollectorNoUsableSubnet") {
		t.Fatalf("the placement refusal is missing: %v", codes(got.Problems))
	}
	if got.Status != collectorstatus.StatusError {
		t.Errorf("status = %q, want Error", got.Status)
	}
	// Worst first, and the placement refusal is the reason for everything after it.
	if got.Problems[0].Code != "CollectorNoUsableSubnet" {
		t.Errorf("the refusal is not first: %v", codes(got.Problems))
	}
	if !strings.HasPrefix(got.Message, "no subnet in vpc-0abc") {
		t.Errorf("the recorded message does not lead with the refusal: %q", got.Message)
	}
}

// TestTheRecordedMessageIsASentenceRatherThanALog. A Collector with eight findings has one that matters
// and seven that follow from it; every problem still reaches the caller, and the row carries the two a
// reader will actually read.
func TestTheRecordedMessageIsASentenceRatherThanALog(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.RunningCount = 0
		in.Observation.LoadBalancer.State = lbFailed
		in.Observation.TargetGroups[0].Targets = []Target{{State: targetUnhealthy, Reason: reasonTimeout}}
		in.Observation.TargetGroups[1].Targets = []Target{{State: targetUnhealthy, Reason: reasonTimeout}}
		in.Record.AgentHealth = "Unhealthy: exporter otlp: connection refused"
		in.Observation.StoppedTasks = []StoppedTask{{
			StopCode: "TaskFailedToStart", ContainerReasons: []string{"CannotPullContainerError: i/o timeout"},
		}}
	}))
	if len(got.Problems) < 4 {
		t.Fatalf("expected several findings, got %v", codes(got.Problems))
	}
	if strings.Count(got.Message, " · ") > 1 {
		t.Errorf("the recorded message carries more than two findings: %q", got.Message)
	}
	// The Error leads, because that is what a reader has to act on.
	if !strings.Contains(got.Message, "could not be pulled") {
		t.Errorf("the message does not lead with the fatal finding: %q", got.Message)
	}
	if got.Status != collectorstatus.StatusError {
		t.Errorf("status = %q, want Error", got.Status)
	}
}

// TestDiagnosisIsDeterministic. Two passes over unchanged state have to produce the same sentence, or
// every reconcile writes a "new" status and the row's updated_at churns for ever.
func TestDiagnosisIsDeterministic(t *testing.T) {
	in := healthy(func(in *DiagnosisInput) {
		in.Observation.RunningCount = 0
		in.Observation.TargetGroups[0].Targets = []Target{{State: targetUnhealthy, Reason: reasonTimeout}}
		in.Observation.StoppedTasks = []StoppedTask{{
			StopCode: "TaskFailedToStart", ContainerReasons: []string{"OutOfMemoryError: memory usage"},
		}}
	})
	first, second := Diagnose(in), Diagnose(in)
	if first.Message != second.Message || first.Status != second.Status {
		t.Fatalf("two passes differ:\n%q\n%q", first.Message, second.Message)
	}
	if strings.Join(codes(first.Problems), ",") != strings.Join(codes(second.Problems), ",") {
		t.Errorf("problem order differs: %v vs %v", codes(first.Problems), codes(second.Problems))
	}
}

// TestAZeroInputDoesNotPanicOrLie: the row for a network nobody has configured, on the first pass, with
// nothing observed.
func TestAZeroInputDoesNotPanicOrLie(t *testing.T) {
	got := Diagnose(DiagnosisInput{})
	if got.Status == collectorstatus.StatusReady {
		t.Error("an empty observation was reported as Ready")
	}
	if hasProblem(got.Problems, ProblemNeverConnected) {
		t.Error("a Collector that does not exist was reported as never connected")
	}
	// The transport still resolves: with no host it reports Pending rather than publishing nothing
	// silently.
	if got.Transport.GRPCEndpoint != "" {
		t.Errorf("an endpoint was published with no load balancer: %q", got.Transport.GRPCEndpoint)
	}
}

// TestManualModeReadsEverythingAutomaticModeDoes. Odigos owns a manual-mode Collector's configuration,
// so it has to diagnose it as thoroughly — the mode changes what a finding means, never whether it is
// looked for.
func TestManualModeReadsEverythingAutomaticModeDoes(t *testing.T) {
	mutate := func(in *DiagnosisInput) {
		in.Observation.RunningCount = 0
		in.Observation.StoppedTasks = []StoppedTask{{
			StopCode: "TaskFailedToStart", ContainerReasons: []string{"CannotPullContainerError: i/o timeout"},
		}}
		in.Observation.TargetGroups[0].Targets = []Target{{State: targetUnhealthy, Reason: reasonTimeout}}
	}
	auto := Diagnose(healthy(mutate))
	manual := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Mode = spec.ModeManual
		mutate(in)
	}))
	for _, code := range []string{ProblemImagePullFailed, ProblemTargetsUnhealthy} {
		if !hasProblem(manual.Problems, code) {
			t.Errorf("manual mode did not report %s, which automatic mode did (%v)", code, codes(auto.Problems))
		}
	}
}

// TestSplitImageRefKeepsTheDigestAndTheTagApart. Both are read, and neither is derived from the other:
// the digest is the identity a drift check runs on and the tag is what a human reads. The
// registry-port case is the one that goes wrong quietly — a private registry's `host:5000/repo` would
// otherwise report every deployment as running a tag called "5000/repo".
func TestSplitImageRefKeepsTheDigestAndTheTagApart(t *testing.T) {
	cases := map[string]struct{ digest, tag string }{
		"ghcr.io/odigos-io/collector@" + testDigest:                {testDigest, ""},
		"ghcr.io/odigos-io/collector:v1.2.3":                       {"", "v1.2.3"},
		"ghcr.io/odigos-io/collector:v1.2.3@" + testDigest:         {testDigest, "v1.2.3"},
		"registry.example.com:5000/odigos/collector":               {"", ""},
		"registry.example.com:5000/odigos/collector@" + testDigest: {testDigest, ""},
		"registry.example.com:5000/odigos/collector:v1":            {"", "v1"},
		"collector": {"", ""},
	}
	for image, want := range cases {
		digest, tag := splitImageRef(image)
		if digest != want.digest || tag != want.tag {
			t.Errorf("%s -> digest %q tag %q, want %q / %q", image, digest, tag, want.digest, want.tag)
		}
	}
}

// TestTheMostRecentFaultIsTheOneReported. A crash-looping Collector accumulates stopped tasks, and the
// oldest fault is usually the one somebody already fixed — reporting it would send them back to a
// remedy that worked.
func TestTheMostRecentFaultIsTheOneReported(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		in.Observation.RunningCount = 0
		// Newest first, which is the order describeStoppedTasks establishes.
		in.Observation.StoppedTasks = []StoppedTask{
			{StopCode: "TaskFailedToStart", StoppedAt: ago(1 * time.Minute),
				ContainerReasons: []string{"CannotPullContainerError: i/o timeout"}},
			{StopCode: "EssentialContainerExited", StoppedAt: ago(30 * time.Minute),
				ContainerReasons: []string{"OutOfMemoryError: Container killed due to memory usage"}},
		}
	}))
	if !hasProblem(got.Problems, ProblemImagePullFailed) {
		t.Fatalf("the most recent fault was not reported: %v", codes(got.Problems))
	}
	if hasProblem(got.Problems, ProblemTaskOutOfMemory) {
		t.Errorf("an older, superseded fault was reported too: %v", codes(got.Problems))
	}
}

// TestTheWorstFindingLeadsWhereverItWasFound. The problems are authored in the order a reader needs
// them — placement, then the workload, then the data path — and a fatal finding from a later half still
// has to lead, because the recorded message carries only the first two.
func TestTheWorstFindingLeadsWhereverItWasFound(t *testing.T) {
	got := Diagnose(healthy(func(in *DiagnosisInput) {
		// A Degraded authored FIRST (the observation's own), and an Error authored after it.
		in.Observation.Problems = []collectorstatus.Problem{{
			Code: ProblemObservationFailed, Severity: collectorstatus.SeverityDegraded,
			Message: "Odigos could not read its recently stopped tasks",
		}}
		in.Observation.RunningCount = 0
		in.Observation.StoppedTasks = []StoppedTask{{
			StopCode: "TaskFailedToStart", ContainerReasons: []string{"CannotPullContainerError: i/o timeout"},
		}}
	}))
	if got.Problems[0].Severity != collectorstatus.SeverityError {
		t.Fatalf("the worst finding is not first: %v", codes(got.Problems))
	}
	if !strings.HasPrefix(got.Message, "the Collector on "+testVPC+" cannot start") {
		t.Errorf("the recorded message does not lead with it: %q", got.Message)
	}
}
