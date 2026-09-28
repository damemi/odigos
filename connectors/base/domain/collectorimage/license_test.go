package collectorimage

import (
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

var now = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func codes(ps []collectorstatus.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

// TestSecretsNeverTravelAsPlainEnvironment is the "secret handling is safe" criterion for the delivery
// path: the two variables that carry secret material must be absent from a rendered container
// environment, whatever their value. On ECS anyone with `ecs:DescribeTaskDefinition` can read a plain
// value, and that is a far wider set of people than those who can read the secret.
func TestSecretsNeverTravelAsPlainEnvironment(t *testing.T) {
	for _, name := range SecretEnvVars() {
		for _, value := range []string{"eyJhbGciOi.real.token", "Bearer abc", "", "<REDACTED>"} {
			ps := EnvProblems(map[string]string{
				"ODIGOS_OPAMP_ENDPOINT": "wss://central.example.com/v1/opamp",
				name:                    value,
			})
			if !collectorstatus.HasError(ps) {
				t.Errorf("%s=%q accepted in a plain environment", name, value)
				continue
			}
			if !strings.Contains(ps[0].Message, name) {
				t.Errorf("%s: the refusal does not name the variable: %s", name, ps[0].Message)
			}
			if strings.Contains(ps[0].Message, value) && value != "" {
				t.Errorf("%s: the refusal echoed the value: %s", name, ps[0].Message)
			}
		}
	}

	// The non-secret variables the deployment contract requires must pass untouched, or the check is
	// useless: it has to be a rule about two names, not a guess at what looks secret.
	clean := EnvProblems(map[string]string{
		"ODIGOS_OPAMP_ENDPOINT":     "wss://central.example.com/v1/opamp",
		"ODIGOS_CLOUD_CONNECTOR_ID": "uid-1",
		"ODIGOS_CLOUD_PROVIDER":     "aws",
	})
	if len(clean) != 0 {
		t.Errorf("the deployment contract's non-secret variables were refused: %v", codes(clean))
	}
	if len(EnvProblems(nil)) != 0 {
		t.Error("an empty environment was refused")
	}
}

// TestDeliveryHasNowhereToPutAValue: the type is the enforcement. A Delivery that could carry a value
// would eventually render one into a definition, so the check is that the fields are references and
// there is no third field.
func TestDeliveryHasNowhereToPutAValue(t *testing.T) {
	d := Delivery{LicenseSecretRef: "arn:secret:license", CredentialSecretRef: "arn:secret:opamp"}
	if ps := DeliveryProblems(d); len(ps) != 0 {
		t.Errorf("a well-formed delivery was refused: %v", codes(ps))
	}
	// Both refs are required, and each missing one has its own consequence worth stating.
	if ps := DeliveryProblems(Delivery{CredentialSecretRef: "arn:secret:opamp"}); !collectorstatus.HasError(ps) {
		t.Error("a delivery with no licence secret was accepted; the Collector would crash-loop")
	}
	if ps := DeliveryProblems(Delivery{LicenseSecretRef: "arn:secret:license"}); !collectorstatus.HasError(ps) {
		t.Error("a delivery with no credential secret was accepted; the Collector would serve the fallback for ever")
	}
}

// TestLicenceAndCredentialMustNotShareASecret: they have different issuers, lifetimes and revocation
// authorities. Sharing one secret means every credential rotation rewrites the licence and every licence
// renewal rewrites the credential — two independent clocks fighting over one value.
func TestLicenceAndCredentialMustNotShareASecret(t *testing.T) {
	ps := DeliveryProblems(Delivery{LicenseSecretRef: "arn:secret:both", CredentialSecretRef: "arn:secret:both"})
	if !collectorstatus.HasError(ps) {
		t.Fatalf("a shared secret was accepted: %v", codes(ps))
	}
	var found bool
	for _, p := range ps {
		if p.Code == "LicenseAndCredentialShareASecret" {
			found = true
			if !strings.Contains(p.Message, "rotation") {
				t.Errorf("the refusal does not explain the collision: %s", p.Message)
			}
		}
	}
	if !found {
		t.Errorf("problems = %v, want LicenseAndCredentialShareASecret", codes(ps))
	}
}

// TestLicenceExpiryIsFatalAndNotAConfigFailure: on expiry the enterprise auth extension reports a fatal
// error and the Collector shuts down, which on a container platform is a crash loop. The Supervisor's
// config rollback cannot rescue it, so the message must not send someone looking at their pipeline.
func TestLicenceExpiryIsFatalAndNotAConfigFailure(t *testing.T) {
	ps := CheckLicense(now.Add(-time.Hour), now, true)
	if !collectorstatus.HasError(ps) {
		t.Fatalf("an expired licence reported %v, want an error", codes(ps))
	}
	msg := ps[0].Message
	for _, want := range []string{"shuts itself down", "not a configuration failure", "renew"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message does not say %q: %s", want, msg)
		}
	}
	// JWT exp is exclusive — at the exact instant it must already be expired, not "renewal due".
	if ps := CheckLicense(now, now, true); !collectorstatus.HasError(ps) || ps[0].Code != "LicenseExpired" {
		t.Errorf("at exact expiry: %v, want LicenseExpired", codes(ps))
	}
}

// TestLicenceRenewalWarnsWithLeadTime: the warning has to arrive far enough ahead that a licence renewal
// — which happens outside this system — can actually be obtained.
func TestLicenceRenewalWarnsWithLeadTime(t *testing.T) {
	if ps := CheckLicense(now.Add(90*24*time.Hour), now, true); len(ps) != 0 {
		t.Errorf("a licence 90 days out reported %v", codes(ps))
	}

	due := CheckLicense(now.Add(LicenseRenewBefore-time.Hour), now, true)
	if len(due) != 1 || due[0].Severity != collectorstatus.SeverityDegraded {
		t.Fatalf("problems = %v, want one degraded warning", codes(due))
	}

	// The remediation differs by mode; the severity must not. An expired licence is equally fatal either
	// way, and Odigos redeploying does not help if the licence itself was never renewed.
	auto := CheckLicense(now.Add(time.Hour), now, true)
	manual := CheckLicense(now.Add(time.Hour), now, false)
	if len(auto) != 1 || len(manual) != 1 || auto[0].Severity != manual[0].Severity {
		t.Fatalf("severity differs by mode: %v vs %v", auto, manual)
	}
	if !strings.Contains(auto[0].Message, "Odigos redeploys") {
		t.Errorf("automatic mode does not say Odigos handles the redeploy: %s", auto[0].Message)
	}
	if !strings.Contains(manual[0].Message, "redeploy the Collector") {
		t.Errorf("manual mode does not say who redeploys: %s", manual[0].Message)
	}
	if strings.Contains(manual[0].Message, "Odigos redeploys") {
		t.Errorf("manual mode claims Odigos redeploys: %s", manual[0].Message)
	}
}

// TestUnknownLicenceExpiryIsPendingNotAFault: before the token has been read there is nothing to warn
// about and nothing misconfigured. Reporting it as an error sends someone looking for a bug.
func TestUnknownLicenceExpiryIsPendingNotAFault(t *testing.T) {
	ps := CheckLicense(time.Time{}, now, true)
	if collectorstatus.HasError(ps) {
		t.Fatalf("an unknown expiry reported %v, want pending", codes(ps))
	}
	if len(ps) != 1 || ps[0].Severity != collectorstatus.SeverityPending {
		t.Errorf("problems = %v, want one pending note", codes(ps))
	}
}

// TestEveryProblemIsActionable: each condition here has a fix, and a message that describes a problem
// without naming its fix is the failure mode a customer actually hits.
func TestEveryProblemIsActionable(t *testing.T) {
	all := [][]collectorstatus.Problem{
		EnvProblems(map[string]string{EnvOnPremToken: "x"}),
		EnvProblems(map[string]string{EnvOpAMPAuthorization: "x"}),
		DeliveryProblems(Delivery{}),
		DeliveryProblems(Delivery{LicenseSecretRef: "s", CredentialSecretRef: "s"}),
		CheckLicense(now.Add(-time.Hour), now, true),
		CheckLicense(now.Add(time.Hour), now, false),
		CheckLicense(time.Time{}, now, true),
	}
	for i, ps := range all {
		if len(ps) == 0 {
			t.Errorf("case %d produced no problem", i)
		}
		for _, p := range ps {
			if p.Code == "" || p.Message == "" {
				t.Errorf("case %d: %+v is not reportable", i, p)
			}
			if len(p.Message) < 60 {
				t.Errorf("case %d: %s is too terse to act on: %s", i, p.Code, p.Message)
			}
		}
	}
}

// TestLicenceSourceIsTheCustomersOwnToken: documented as a constant because it is the fact that keeps
// this from being a cross-tenant leak. The token installed into a customer's cloud account is the one
// already in their own cluster, not Odigos-the-vendor's.
func TestLicenceSourceIsTheCustomersOwnToken(t *testing.T) {
	if !strings.Contains(LicenseSource, EnvOnPremToken) {
		t.Errorf("LicenseSource does not name the variable it comes from: %s", LicenseSource)
	}
	if !strings.Contains(LicenseSource, "own") {
		t.Errorf("LicenseSource does not say whose token it is: %s", LicenseSource)
	}
}
