package collectorgateway

import (
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

const prodEndpoint = "wss://central.odigos.io/v1/opamp"

func collectorType() []spec.Type { return []spec.Type{{Name: "aws.fargate-collector"}} }

// TestCheckEndpointStaysQuietWithoutACollector: an installation that does not use managed Collectors
// has no use for the endpoint, and a fault reported on every connector in it is how an operator learns
// to ignore the report that matters.
func TestCheckEndpointStaysQuietWithoutACollector(t *testing.T) {
	for _, configured := range []string{"", "nonsense", "ws://central.odigos.io/v1/opamp", prodEndpoint} {
		if problems := CheckEndpoint(configured, nil); len(problems) != 0 {
			t.Errorf("CheckEndpoint(%q, no collector types) = %v, want nothing", configured, problems)
		}
	}
}

// TestCheckEndpointAcceptsAConfiguredInstallation covers both environments the feature is configured
// per: they are different Central installations and therefore different endpoints.
func TestCheckEndpointAcceptsAConfiguredInstallation(t *testing.T) {
	for _, configured := range []string{
		prodEndpoint,
		"wss://central.staging.odigos.io/v1/opamp",
		"wss://odigos.internal.example.com:8443/v1/opamp",
		// Host only: the path is OpAMP's default and the gateway serves it.
		"wss://central.odigos.io",
	} {
		if problems := CheckEndpoint(configured, collectorType()); len(problems) != 0 {
			t.Errorf("CheckEndpoint(%q) = %v, want nothing", configured, problems)
		}
	}
}

// TestCheckEndpointSeparatesUnsetFromWrong: nothing is broken when the endpoint was never set — the
// feature is off. A value somebody did set and got wrong is a different message and a different fix.
func TestCheckEndpointSeparatesUnsetFromWrong(t *testing.T) {
	cases := []struct {
		name, configured, wantCode, wantIn string
	}{
		{
			name: "never set", configured: "",
			wantCode: "OpAMPEndpointNotConfigured", wantIn: "ODIGOS_OPAMP_ENDPOINT",
		},
		{
			name: "set to something that is not a URL", configured: "central.odigos.io",
			wantCode: "OpAMPEndpointInvalid", wantIn: "no scheme",
		},
		{
			name: "set to a plaintext endpoint", configured: "ws://central.odigos.io/v1/opamp",
			wantCode: "OpAMPEndpointInvalid", wantIn: "destination's own credential",
		},
		{
			name: "set with credentials in the URL", configured: "wss://u:p@central.odigos.io/v1/opamp",
			wantCode: "OpAMPEndpointInvalid", wantIn: "must not contain credentials",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			problems := CheckEndpoint(tc.configured, collectorType())
			if len(problems) != 1 {
				t.Fatalf("problems = %v, want exactly one", problems)
			}
			if problems[0].Code != tc.wantCode {
				t.Errorf("Code = %q, want %q", problems[0].Code, tc.wantCode)
			}
			// An Error, not a warning: it is a configuration fault somebody has to fix, and until they
			// do no Collector is deployed at all.
			if problems[0].Severity != collectorstatus.SeverityError {
				t.Errorf("Severity = %q, want %q", problems[0].Severity, collectorstatus.SeverityError)
			}
			if !strings.Contains(problems[0].Message, tc.wantIn) {
				t.Errorf("message does not mention %q: %s", tc.wantIn, problems[0].Message)
			}
			// Actionable means naming the thing to change, not just what is wrong.
			if !strings.Contains(problems[0].Message, "no Collector") && !strings.Contains(problems[0].Message, "deploy none") {
				t.Errorf("message does not say what the consequence is: %s", problems[0].Message)
			}
		})
	}
}

// TestCheckEndpointDriftIgnoresRespellings: an endpoint edit that does not change where Collectors
// connect must not report drift, because acting on it would redeploy the whole fleet.
func TestCheckEndpointDriftIgnoresRespellings(t *testing.T) {
	for _, configured := range []string{
		prodEndpoint,
		"WSS://CENTRAL.ODIGOS.IO/v1/opamp",
		"  " + prodEndpoint + "  ",
	} {
		if problems := CheckEndpointDrift(prodEndpoint, configured, spec.ModeAutomatic); len(problems) != 0 {
			t.Errorf("CheckEndpointDrift(%q, %q) = %v, want nothing", prodEndpoint, configured, problems)
		}
	}

	// Nothing deployed yet is not drift.
	if problems := CheckEndpointDrift("", prodEndpoint, spec.ModeAutomatic); len(problems) != 0 {
		t.Errorf("a Collector that does not exist reported endpoint drift: %v", problems)
	}
	// Neither is a broken new value — refusing to act on it is what keeps a typo in the chart from
	// redeploying every Collector.
	if problems := CheckEndpointDrift(prodEndpoint, "ws://central.odigos.io/v1/opamp", spec.ModeAutomatic); len(problems) != 0 {
		t.Errorf("an unusable new endpoint reported drift: %v", problems)
	}
}

// TestCheckEndpointDriftDependsOnMode: the two modes cure a moved endpoint differently, and the
// difference has a visible cost — Odigos replaces the tasks in automatic mode and cannot touch the
// customer's infrastructure in manual mode.
func TestCheckEndpointDriftDependsOnMode(t *testing.T) {
	const moved = "wss://central.staging.odigos.io/v1/opamp"

	auto := CheckEndpointDrift(prodEndpoint, moved, spec.ModeAutomatic)
	if len(auto) != 1 || auto[0].Code != "OpAMPEndpointMovedRedeployPending" {
		t.Fatalf("automatic mode: %v", auto)
	}
	if !strings.Contains(auto[0].Message, "Odigos is replacing its tasks") {
		t.Errorf("automatic mode does not say Odigos will fix it: %s", auto[0].Message)
	}

	manual := CheckEndpointDrift(prodEndpoint, moved, spec.ModeManual)
	if len(manual) != 1 || manual[0].Code != "OpAMPEndpointMovedAwaitingCustomer" {
		t.Fatalf("manual mode: %v", manual)
	}
	if !strings.Contains(manual[0].Message, "redeploy it") {
		t.Errorf("manual mode does not say what the customer must do: %s", manual[0].Message)
	}

	// Both are Degraded: the Collector keeps exporting under its last configuration, but Odigos can no
	// longer change it — that is a real, visible loss in either mode, not a pending nicety.
	for _, problems := range [][]collectorstatus.Problem{auto, manual} {
		if problems[0].Severity != collectorstatus.SeverityDegraded {
			t.Errorf("Severity = %q, want %q", problems[0].Severity, collectorstatus.SeverityDegraded)
		}
		// Both messages must name both endpoints, or nobody can tell which way the move went.
		if !strings.Contains(problems[0].Message, prodEndpoint) || !strings.Contains(problems[0].Message, moved) {
			t.Errorf("message does not name both endpoints: %s", problems[0].Message)
		}
	}
}
