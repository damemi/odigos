package observer

import (
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// Observation is everything one read-only pass over AWS found about a network's Collector.
//
// A value, with no client and no context in it, so the whole diagnosis is a pure function of what was
// read. That is what makes failure table testable: every row is an Observation a test can write
// down, and there is no way to reach a diagnosis that depends on the order the reads happened in.
//
// Absent is distinguished from zero everywhere it could matter. ServiceFound is separate from the counts
// because "no service" and "a service wanting no tasks" are different Collectors, and Partial says the
// facts are incomplete so a read failure is never mistaken for a finding.
type Observation struct {
	// ServiceFound is whether an ECS service exists under the name this network's Collector uses.
	ServiceFound bool
	// Status is the service's ECS status: ACTIVE, DRAINING or INACTIVE.
	Status string

	ServiceARN string
	ClusterARN string
	// RevisionARN is the task definition the service is running, which is not necessarily the newest in
	// the family — that difference is what revision drift is.
	RevisionARN string
	// Tags are the service's, for the ownership classification. Read through DescribeServices with
	// TAGS included rather than a second call, so a manual-mode observer needs no tagging permission.
	Tags map[string]string

	DesiredCount int
	RunningCount int
	PendingCount int

	// Rollout is the primary deployment's rollout state and reason. COMPLETED, IN_PROGRESS or FAILED —
	// and FAILED is the statement that ECS has stopped trying, which no task reason says.
	Rollout       string
	RolloutReason string

	// StoppedTasks are this service's recently stopped tasks, newest first. The single richest source of
	// diagnosis on ECS: the reason a Collector will not start is here and nowhere else.
	StoppedTasks []StoppedTask

	// Revision is the task definition's own content, which is what drift is measured against.
	Revision Revision

	// TargetGroups carry the load balancer's verdict on the tasks. Empty when the service has no load
	// balancer attachment at all, which is the `endpointOverride` shape rather than a fault.
	TargetGroups []TargetGroup
	LoadBalancer LoadBalancer

	// Subnets and SecurityGroups are the service's awsvpc configuration as deployed.
	Subnets        []string
	SecurityGroups []string

	// Partial is whether any read failed. Its problems are in Problems; the flag exists so the
	// diagnosis can decline to draw a conclusion from facts it knows are missing — reporting a Collector
	// as broken because a describe was throttled is worse than reporting nothing.
	Partial  bool
	Problems []collectorstatus.Problem
}

// StoppedTask is one task that stopped, and why.
type StoppedTask struct {
	ARN string
	// StopCode is ECS's own classification, consulted before any reason string: a task the scheduler
	// replaced during a deployment carries an alarming container reason and is not a fault.
	StopCode string
	// Reason is the task-level StoppedReason.
	Reason string
	// ContainerReasons are the per-container reasons, which is where CannotPullContainerError and
	// ResourceInitializationError actually appear — the task-level reason for both is the unhelpful
	// "Task failed to start".
	ContainerReasons []string
	// ExitCode is the container's, when it ran at all. A pointer because 0 is a real exit code and
	// means something different from "never ran".
	ExitCode  *int32
	StoppedAt *time.Time
}

// Cause classifies this stop.
func (t StoppedTask) Cause() StopCause {
	return ClassifyStop(t.StopCode, append([]string{t.Reason}, t.ContainerReasons...)...)
}

// Detail is the most specific text ECS gave, for a message that carries the reason verbatim.
//
// The container reason first, because that is where the actionable ones live; the task reason is the
// fallback and is usually a restatement.
func (t StoppedTask) Detail() string {
	for _, r := range t.ContainerReasons {
		if r != "" {
			return r
		}
	}
	return t.Reason
}

// Revision is a task definition's content, as deployed.
type Revision struct {
	ARN string
	// Image is the reference the definition names. Digest and Tag are split out of it because the
	// DIGEST is the image's identity and the tag is only what a human reads.
	Image  string
	Digest string
	Tag    string

	Architecture string
	CPU          string
	Memory       string

	// Environment is the container's plain environment. Read for one value that exists nowhere else:
	// ODIGOS_OPAMP_ENDPOINT, the endpoint this Collector was DEPLOYED against, which is the only way to
	// tell that the installation has moved since.
	Environment map[string]string
	// Secret references and log scope are checked for customer-owned task definitions.
	RepositoryCredentialsRef string
	Secrets                  map[string]string
	LogGroup                 string
}

// TargetGroup is one data port's target group and the load balancer's verdict on its targets.
type TargetGroup struct {
	ARN             string
	Name            string
	Port            int
	HealthCheckPort string
	// Targets is every registered target. An EMPTY slice on an existing group is a finding of its own —
	// the tasks were never registered — and not the same fault as a target failing its health check.
	Targets []Target
}

// Target is one registered task, as the load balancer sees it.
type Target struct {
	ID    string
	Port  int
	State string
	// Reason and Description are ELB's own explanation. Reason separates the causes that matter:
	// Target.Timeout is a health check that got no answer (almost always a security group), and
	// Target.FailedHealthChecks is one that was answered with the wrong thing.
	Reason      string
	Description string
}

// Healthy reports whether this target is serving.
func (t Target) Healthy() bool { return t.State == targetHealthy }

// Settling reports whether this target's state is one that resolves on its own — a registration in
// progress, or the initial health check. Reported as Pending rather than as a fault, because a Collector
// is in this state for the first half-minute of every deployment.
func (t Target) Settling() bool {
	return t.State == targetInitial || t.Reason == reasonRegistrationInProgress ||
		t.Reason == reasonInitialHealthChecking
}

// Target health states and reasons, as ELBv2 spells them.
const (
	targetHealthy   = "healthy"
	targetUnhealthy = "unhealthy"
	targetInitial   = "initial"
	targetDraining  = "draining"
	targetUnused    = "unused"

	reasonRegistrationInProgress = "Elb.RegistrationInProgress"
	reasonInitialHealthChecking  = "Elb.InitialHealthChecking"
	reasonTimeout                = "Target.Timeout"
	reasonFailedHealthChecks     = "Target.FailedHealthChecks"
	reasonNotRegistered          = "Target.NotRegistered"
)

// Healthy counts the targets serving in this group.
func (g TargetGroup) Healthy() int {
	n := 0
	for _, t := range g.Targets {
		if t.Healthy() {
			n++
		}
	}
	return n
}

// Failing returns the targets that are neither healthy nor still settling, which is the set worth
// reporting. A draining target is excluded: it is on its way out by design.
func (g TargetGroup) Failing() []Target {
	var out []Target
	for _, t := range g.Targets {
		if t.Healthy() || t.Settling() || t.State == targetDraining {
			continue
		}
		out = append(out, t)
	}
	return out
}

// Settling reports whether every target in this group is still coming up.
func (g TargetGroup) Settling() bool {
	if len(g.Targets) == 0 {
		return false
	}
	for _, t := range g.Targets {
		if !t.Settling() {
			return false
		}
	}
	return true
}

// LoadBalancer is the Collector's front, discovered from the service's target groups rather than looked
// up by name.
//
// Through the service on purpose: in manual mode the load balancer is the customer's and they may have
// named it anything, so a name lookup would find nothing and report a Collector with no endpoint. What
// is invariant in both modes is that the ECS service is attached to the target groups the load balancer
// forwards to.
type LoadBalancer struct {
	ARN     string
	DNSName string
	// ListenerTargets maps OTLP listener ports to their default target-group ARN.
	// Nil means the listener read was not available; an empty non-nil map means
	// no listener exists.
	ListenerTargets map[int]string
	// State is provisioning, active, active_impaired or failed.
	State  string
	Scheme string
}

// Load balancer states, as ELBv2 spells them.
const (
	lbActive         = "active"
	lbProvisioning   = "provisioning"
	lbActiveImpaired = "active_impaired"
	lbFailed         = "failed"
)

// Serving reports whether the load balancer can carry traffic. active_impaired counts: it means some
// zone is degraded, which is a report rather than an outage.
func (lb LoadBalancer) Serving() bool {
	return lb.State == lbActive || lb.State == lbActiveImpaired
}

// TasksRunning reports whether anything is actually running for this Collector.
func (o Observation) TasksRunning() bool { return o.RunningCount > 0 }

// Live reports whether the service is one that can be converged or serve traffic at all.
func (o Observation) Live() bool { return o.ServiceFound && o.Status == serviceStatusActive }

// serviceStatusActive is the only ECS service status that is neither absent nor going away.
const serviceStatusActive = "ACTIVE"
