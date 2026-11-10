package observer

import (
	"strings"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorimage"
)

// The two classifiers in this file are pure and are the whole of the "specific diagnosis" contract:
// ECS and the Collector both hand back a free-text reason, and turning each into one of a small set of
// named causes is what lets table produce a different sentence per row instead of one sentence
// per fault.
//
// Both are deliberately substring matchers over text AWS and the Collector are free to reword. That is
// a real risk and it is handled the only way it can be: the fallback class carries the reason VERBATIM,
// so a reworded message degrades to "here is exactly what it said" rather than to silence.

// StopCause is why a Collector task stopped.
type StopCause string

const (
	// StopCauseUnknown: a reason this package does not recognise, reported verbatim.
	StopCauseUnknown StopCause = "unknown"
	// StopCauseImagePull: the image could not be pulled.
	StopCauseImagePull StopCause = "image-pull"
	// StopCauseSecrets: the execution role could not fetch a secret, or could not reach Secrets Manager.
	StopCauseSecrets StopCause = "secrets"
	// StopCauseOutOfMemory: the container exceeded its memory allocation.
	StopCauseOutOfMemory StopCause = "out-of-memory"
	// StopCauseHealthCheck: the service killed the task for failing its load balancer health checks. The
	// task itself was fine as far as ECS could tell, which is why it is not a task fault.
	StopCauseHealthCheck StopCause = "health-check"
	// StopCauseExited: the container ran and exited. The reason is inside the Collector, so this points
	// at the log group rather than pretending to know.
	StopCauseExited StopCause = "exited"
	// StopCauseExpected: a stop nobody needs to act on — a deployment replacing tasks, a Spot
	// interruption, or somebody stopping a task by hand.
	//
	// Classified rather than ignored because the alternative is worse in both directions: a converge
	// that reported every routine task replacement as a fault would keep a healthy fleet permanently
	// Degraded, and a Spot network replaces tasks as a matter of course.
	StopCauseExpected StopCause = "expected"
)

// ECS stop codes, as strings. Typed constants exist in the SDK but the values are what this compares
// and spelling them out keeps the table readable next to the reasons.
const (
	stopCodeUserInitiated             = "UserInitiated"
	stopCodeServiceSchedulerInitiated = "ServiceSchedulerInitiated"
	stopCodeSpotInterruption          = "SpotInterruption"
	stopCodeTerminationNotice         = "TerminationNotice"
)

// ClassifyStop decides why a task stopped, from the task's stop code and every reason string ECS
// attached to it — the task's own and each container's.
//
// The stop CODE is consulted first and it is not a tie-break: a task the scheduler replaced during a
// deployment carries a perfectly alarming-looking container reason, and reading that instead of the code
// turns every rolling update into a reported failure.
func ClassifyStop(stopCode string, reasons ...string) StopCause {
	switch stopCode {
	case stopCodeUserInitiated, stopCodeServiceSchedulerInitiated,
		stopCodeSpotInterruption, stopCodeTerminationNotice:
		return StopCauseExpected
	}

	joined := strings.ToLower(strings.Join(reasons, " | "))
	switch {
	case joined == "":
		return StopCauseUnknown
	case strings.Contains(joined, "cannotpullcontainer"),
		strings.Contains(joined, "imagepull"),
		strings.Contains(joined, "pull image"),
		strings.Contains(joined, "manifest unknown"),
		strings.Contains(joined, "toomanyrequests"):
		return StopCauseImagePull
	case strings.Contains(joined, "unable to pull secrets"),
		strings.Contains(joined, "secretsmanager"),
		strings.Contains(joined, "asm secret"),
		strings.Contains(joined, "accessdenied"),
		strings.Contains(joined, "not authorized to perform"):
		// Ordered after the image cases on purpose: "unable to pull secrets or registry auth" mentions
		// both, and the secret is the half a customer can act on — the registry auth in that message is
		// only ever consulted for a private registry, which the Collector image is not.
		return StopCauseSecrets
	case strings.Contains(joined, "outofmemory"), strings.Contains(joined, "memory usage"):
		return StopCauseOutOfMemory
	case strings.Contains(joined, "elb health check"), strings.Contains(joined, "elb health checks"):
		return StopCauseHealthCheck
	case strings.Contains(joined, "essential container in task exited"):
		return StopCauseExited
	}
	return StopCauseUnknown
}

// AgentCause is what a Collector's own reported health says is wrong with it.
type AgentCause string

const (
	// AgentCauseNone: nothing reported, or reported healthy.
	AgentCauseNone AgentCause = "none"
	// AgentCauseLicenseExpired: the enterprise licence is missing, unverifiable or expired, so the
	// Collector process will not run.
	AgentCauseLicenseExpired AgentCause = "license"
	// AgentCauseExporter: the Collector is running and cannot deliver to the destination.
	AgentCauseExporter AgentCause = "exporter"
	// AgentCauseUnhealthy: unhealthy for a reason this package cannot classify, reported verbatim.
	AgentCauseUnhealthy AgentCause = "unhealthy"
)

// licenseExpiredError is the text the Collector's own licence gate reports through OpAMP when a token
// that was valid at startup later expires.
//
// Copied rather than imported: it lives in collector/extension/odigosenterpriseauthextension, which is a
// different module built into the Collector image, and taking a dependency on the agent's internals from
// the connector to match one string would be worse than a copy a test pins. classify_test.go reads that
// file and fails if the phrase is gone — a rewording there would silently turn every expired licence
// into "the agent is unhealthy", which is exactly the confusion says must not happen.
const licenseExpiredError = "on-prem token expired"

// ClassifyAgentHealth decides what a Collector's reported health means.
//
// The licence is checked FIRST and that ordering is the point: an expired licence and a rejected
// configuration look identical from outside — a Collector that will not serve — and they send a reader
// to opposite ends of the product. The licence gate reports a fatal error, `automatic_config_rollback`
// cannot rescue it because nothing is wrong with the configuration, and the remedy is a renewed token
// rather than an edited pipeline.
//
// A rejected configuration is deliberately NOT a class here. The OpAMP session sees the agent's
// RemoteConfigStatus and records the rejection with the agent's own validation error verbatim; this
// package sees neither, and inventing a config class from a health string would mean guessing over the
// top of a writer who knows.
func ClassifyAgentHealth(health string) AgentCause {
	text := strings.ToLower(strings.TrimSpace(health))
	if text == "" {
		return AgentCauseNone
	}
	switch {
	case strings.Contains(text, licenseExpiredError),
		strings.Contains(text, strings.ToLower(collectorimage.EnvOnPremToken)),
		strings.Contains(text, "on-prem token"):
		return AgentCauseLicenseExpired
	case strings.Contains(text, "exporter"),
		strings.Contains(text, "sending queue"),
		strings.Contains(text, "connection refused"),
		strings.Contains(text, "no such host"),
		strings.Contains(text, "context deadline exceeded"),
		strings.Contains(text, "permanent error"):
		return AgentCauseExporter
	case healthyLabel(text):
		return AgentCauseNone
	}
	return AgentCauseUnhealthy
}

// healthyLabel reports whether a health string is one of the agent's own "all fine" spellings.
//
// The agent sends a label rather than a boolean over the OpAMP session (base/opamp.ExtractReport), so
// "Healthy" and "StatusOK" arrive as text and would otherwise be reported as an unclassifiable fault.
// A healthy label with an error appended is NOT healthy, which is why this matches the whole string.
func healthyLabel(lowered string) bool {
	switch lowered {
	case "healthy", "ok", "statusok", "status_ok", "running", "starting":
		return true
	}
	return false
}
