package observer

import (
	"os"
	"strings"
	"testing"
)

// TestExpectedStopsAreNotFaults is the classifier's most valuable answer, because everything else it
// says is about a Collector somebody already suspects. A rolling deployment, a Spot interruption and a
// task somebody stopped by hand all leave behind a task whose container reason reads like a failure —
// and reporting those would keep a healthy fleet permanently Degraded, most of all on a Spot network,
// where replacing tasks is what the opt-in buys.
func TestExpectedStopsAreNotFaults(t *testing.T) {
	for _, code := range []string{
		stopCodeServiceSchedulerInitiated, stopCodeSpotInterruption,
		stopCodeUserInitiated, stopCodeTerminationNotice,
	} {
		// With an alarming reason attached, which is the case that matters: the stop CODE has to win
		// over the reason text, or every deployment reads as a crash.
		got := ClassifyStop(code, "Essential container in task exited", "OutOfMemoryError: killed")
		if got != StopCauseExpected {
			t.Errorf("stop code %s classified as %s, want %s", code, got, StopCauseExpected)
		}
	}
}

// TestEveryDiagnosableStopHasItsOwnCause walks the reasons rows are actually detected by. The
// strings are the ones ECS emits; each is a different remedy, which is the whole reason to classify at
// all.
func TestEveryDiagnosableStopHasItsOwnCause(t *testing.T) {
	cases := map[string]struct {
		reason string
		want   StopCause
	}{
		"a registry that cannot be reached": {
			"CannotPullContainerError: ref pull has been retried 5 time(s): failed to copy: httpReadSeeker: " +
				"failed open: failed to do request: Get \"https://ghcr.io/v2/...\": dial tcp: i/o timeout",
			StopCauseImagePull,
		},
		"a digest the registry does not have": {
			"CannotPullContainerError: manifest unknown", StopCauseImagePull,
		},
		"an anonymous Docker Hub pull that was rate-limited": {
			"CannotPullContainerError: toomanyrequests: You have reached your pull rate limit",
			StopCauseImagePull,
		},
		"an execution role that cannot read the secret": {
			"ResourceInitializationError: unable to pull secrets or registry auth: execution resource " +
				"retrieval failed: unable to retrieve secret from asm: service call has been retried 1 time(s): " +
				"AccessDeniedException: User is not authorized to perform: secretsmanager:GetSecretValue",
			StopCauseSecrets,
		},
		"a subnet with no route to Secrets Manager": {
			"ResourceInitializationError: unable to pull secrets or registry auth: execution resource " +
				"retrieval failed: unable to retrieve secrets from ssm: RequestError: send request failed",
			StopCauseSecrets,
		},
		"a container killed for its memory": {
			"OutOfMemoryError: Container killed due to memory usage", StopCauseOutOfMemory,
		},
		"a task the service killed for failing health checks": {
			"Task failed ELB health checks in (target-group arn:aws:elasticloadbalancing:...)",
			StopCauseHealthCheck,
		},
		"a Collector that started and stopped": {
			"Essential container in task exited", StopCauseExited,
		},
		"something nobody has seen before": {
			"CannotStartContainerError: ResourceInitializationError: failed to configure ENI", StopCauseUnknown,
		},
		"no reason at all": {"", StopCauseUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Through the container reason, which is where ECS actually puts these — the task-level
			// reason for a failure to start is the unhelpful "Task failed to start".
			if got := ClassifyStop("TaskFailedToStart", "Task failed to start", tc.reason); got != tc.want {
				t.Errorf("classified as %s, want %s", got, tc.want)
			}
		})
	}
}

// TestSecretsOutrankTheRegistryInOneMessage. "unable to pull secrets or registry auth" names both, and
// the secret is the half a customer can act on: the registry auth in that message is only ever consulted
// for a private registry, and the Collector image is public.
func TestSecretsOutrankTheRegistryInOneMessage(t *testing.T) {
	got := ClassifyStop("TaskFailedToStart",
		"ResourceInitializationError: unable to pull secrets or registry auth: AccessDeniedException")
	if got != StopCauseSecrets {
		t.Fatalf("classified as %s, want %s", got, StopCauseSecrets)
	}
}

// TestAnExpiredLicenceIsNeverAConfigurationProblem is explicit demand. An expired licence and a
// rejected configuration look identical from outside — a Collector that will not serve — and they send a
// reader to opposite ends of the product: one is a renewal from Odigos, the other is a pipeline to edit.
func TestAnExpiredLicenceIsNeverAConfigurationProblem(t *testing.T) {
	for _, health := range []string{
		"Unhealthy: odigos enterprise on-prem token expired",
		"Unhealthy: failed to verify Odigos on-prem token: token is expired",
		"Unhealthy: ODIGOS_ONPREM_TOKEN is not set: the enterprise collector requires an Odigos on-prem token",
	} {
		if got := ClassifyAgentHealth(health); got != AgentCauseLicenseExpired {
			t.Errorf("%q classified as %s, want %s", health, got, AgentCauseLicenseExpired)
		}
	}
}

// TestExporterFailuresAreTheirOwnClass, and says why: a Collector that cannot reach its destination
// is not a deployment failure, so it must not be reported as one. Everything about the deployment is
// correct and redeploying changes nothing.
func TestExporterFailuresAreTheirOwnClass(t *testing.T) {
	for _, health := range []string{
		"Unhealthy: exporter otlp: permanent error: rpc error: code = Unauthenticated",
		`Unhealthy: exporter/otlphttp: dial tcp: lookup ingest.example.com: no such host`,
		"Unhealthy: sending queue is full",
		"Unhealthy: context deadline exceeded",
		"Unhealthy: connection refused",
	} {
		if got := ClassifyAgentHealth(health); got != AgentCauseExporter {
			t.Errorf("%q classified as %s, want %s", health, got, AgentCauseExporter)
		}
	}
}

// TestAHealthyLabelIsNotAFault. The agent sends a LABEL rather than a boolean over OpAMP, so "Healthy"
// arrives as text — and classifying it as an unrecognised fault would put a standing Degraded on every
// working Collector in the fleet.
func TestAHealthyLabelIsNotAFault(t *testing.T) {
	for _, health := range []string{"", "Healthy", "healthy", "StatusOK", "  Healthy  ", "Starting"} {
		if got := ClassifyAgentHealth(health); got != AgentCauseNone {
			t.Errorf("%q classified as %s, want %s", health, got, AgentCauseNone)
		}
	}
	// A healthy label with an error appended is NOT healthy: the agent composes exactly that when it has
	// a last error, and matching a prefix rather than the whole string would swallow it.
	if got := ClassifyAgentHealth("Healthy: something is wrong"); got != AgentCauseUnhealthy {
		t.Errorf("a healthy label with an error classified as %s", got)
	}
}

// TestAnUnclassifiableHealthStringIsStillReportable: the fallback is what makes an unrecognised failure
// specific. A reason nobody has seen before is more useful printed than classified into "unhealthy".
func TestAnUnclassifiableHealthStringIsStillReportable(t *testing.T) {
	if got := ClassifyAgentHealth("Unhealthy: extension odigosopamp: unexpected state"); got != AgentCauseUnhealthy {
		t.Fatalf("classified as %s, want %s", got, AgentCauseUnhealthy)
	}
}

// TestTheLicenceErrorPhraseStillExistsInTheCollector is a cross-module guard, and it guards the one
// misclassification explicitly forbids: if the licence gate is reworded, every expired licence
// silently becomes "the agent is unhealthy for a reason Odigos does not recognise" and the customer is
// sent to read logs instead of renewing a token.
//
// Skipped rather than failed when the file cannot be read, because the path crosses a module boundary
// and a repository re-layout must not fail a test about a string. A skip says the guard did not run; a
// failure here would say the wrong thing.
func TestTheLicenceErrorPhraseStillExistsInTheCollector(t *testing.T) {
	const path = "../../../../../../collector/extension/odigosenterpriseauthextension/extension.go"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("cannot read %s, so the licence phrase is unguarded from here: %v", path, err)
	}
	if !strings.Contains(string(src), licenseExpiredError) {
		t.Fatalf("the Collector's licence gate no longer says %q, so ClassifyAgentHealth will report an "+
			"expired licence as an unrecognised fault. Update licenseExpiredError to match %s",
			licenseExpiredError, path)
	}
}
