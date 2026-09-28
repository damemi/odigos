package collectorimage

import (
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// Environment variables that carry secret material into a managed Collector. Both must arrive as a
// secret-store reference, never a plain environment value.
const (
	// EnvOnPremToken is the enterprise licence token. odigosenterpriseauthextension refuses to start
	// without a valid one and shuts the Collector down if it later expires.
	EnvOnPremToken = "ODIGOS_ONPREM_TOKEN"
	// EnvOpAMPAuthorization is the complete OpAMP Authorization header value.
	EnvOpAMPAuthorization = "ODIGOS_OPAMP_AUTHORIZATION"
)

// SecretEnvVars are the variables that may only be populated from the secret store. Deployer and
// artifact guard both check against this list so they cannot disagree.
func SecretEnvVars() []string { return []string{EnvOnPremToken, EnvOpAMPAuthorization} }

// LicenseRenewBefore is how far ahead of licence expiry a redeploy must be scheduled.
// Longer than the OpAMP credential lead time: Odigos can mint a replacement OpAMP credential on
// demand, but the licence is issued outside this system — if it is not renewed, the connector can
// only warn early and repeatedly.
const LicenseRenewBefore = 14 * 24 * time.Hour

// Delivery is how licence and credential material reach a Collector. Refs only — no field for a
// value, so nothing can render one into a container definition. Values travel as
// collectorcredential.Secret from the moment they are read.
type Delivery struct {
	// LicenseSecretRef holds ODIGOS_ONPREM_TOKEN.
	LicenseSecretRef string
	// CredentialSecretRef holds ODIGOS_OPAMP_AUTHORIZATION.
	// Separate from the licence on purpose: different issuers, lifetimes, and revocation authorities.
	// One shared secret would mean every credential rotation rewrites the licence and every licence
	// renewal rewrites the credential.
	CredentialSecretRef string
}

// DeliveryProblems reports a delivery that cannot work.
func DeliveryProblems(d Delivery) []collectorstatus.Problem {
	var problems []collectorstatus.Problem
	if d.LicenseSecretRef == "" {
		problems = append(problems, collectorstatus.Problem{
			Code:     "LicenseSecretMissing",
			Severity: collectorstatus.SeverityError,
			Message: "no secret holds the Odigos licence token for this Collector. The enterprise Collector " +
				"refuses to start without " + EnvOnPremToken + ", so it would crash-loop rather than run",
		})
	}
	if d.CredentialSecretRef == "" {
		problems = append(problems, collectorstatus.Problem{
			Code:     "CredentialSecretMissing",
			Severity: collectorstatus.SeverityError,
			Message: "no secret holds the OpAMP credential for this Collector, so it could start but never " +
				"receive a configuration — it would serve the inert fallback for ever",
		})
	}
	if d.LicenseSecretRef != "" && d.LicenseSecretRef == d.CredentialSecretRef {
		problems = append(problems, collectorstatus.Problem{
			Code:     "LicenseAndCredentialShareASecret",
			Severity: collectorstatus.SeverityError,
			Message: "the licence token and the OpAMP credential are stored in one secret. They have " +
				"different lifetimes and different revocation authorities, so sharing one means every " +
				"credential rotation rewrites the licence and every licence renewal rewrites the credential",
		})
	}
	return problems
}

// EnvProblems reports a rendered container environment that carries secret material as a plain value.
// Deployer and artifact guard both run this: on ECS anyone with ecs:DescribeTaskDefinition
// can read a plain env value — a wider set than secret readers. Checks two specific names are absent;
// it does not try to find secrets by shape.
func EnvProblems(env map[string]string) []collectorstatus.Problem {
	var problems []collectorstatus.Problem
	for _, name := range SecretEnvVars() {
		if _, present := env[name]; !present {
			continue
		}
		problems = append(problems, collectorstatus.Problem{
			Code:     "SecretInPlainEnvironment",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("%s is set as a plain environment value; it must come from the secret store "+
				"as a `secrets` entry. Anyone who can read this workload's definition can read a plain value, "+
				"and that is a much wider set of people than those who can read the secret", name),
		})
	}
	return problems
}

// CheckLicense reports a Collector's licence standing.
//
// Unlike the OpAMP credential, the licence is an RSA-signed JWT with an exp issued outside this
// system. On expiry odigosenterpriseauthextension fatals and the Collector shuts down (crash loop on
// a container platform). Supervisor config rollback cannot help — the config is fine.
//
// canRedeploy (automatic mode) changes only the remediation wording, never severity: an expired
// licence is fatal either way, and redeploying does not help if the licence itself was not renewed.
func CheckLicense(expiresAt time.Time, now time.Time, canRedeploy bool) []collectorstatus.Problem {
	if expiresAt.IsZero() {
		return []collectorstatus.Problem{{
			Code:     "LicenseExpiryUnknown",
			Severity: collectorstatus.SeverityPending,
			Message: "the expiry of this Collector's licence token is not known yet, so Odigos cannot warn " +
				"before it stops the Collector. This clears once the token has been read",
		}}
	}
	// JWT exp is exclusive: the token must not be processed on or after the claim (RFC 7519).
	if !now.Before(expiresAt) {
		return []collectorstatus.Problem{{
			Code:     "LicenseExpired",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("the Odigos licence token for this Collector expired at %s, so the Collector "+
				"shuts itself down and its container restarts in a loop. This is not a configuration failure "+
				"and cannot be rolled back — renew the licence, then redeploy the Collector so it picks up the "+
				"new token", expiresAt.Format(time.RFC3339)),
		}}
	}
	if now.Add(LicenseRenewBefore).After(expiresAt) {
		remedy := "renew the licence and redeploy the Collector so it picks up the new token"
		if canRedeploy {
			remedy = "renew the licence; Odigos redeploys the Collector to pick up the new token once the " +
				"renewed one is available"
		}
		return []collectorstatus.Problem{{
			Code:     "LicenseRenewalDue",
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("the Odigos licence token for this Collector expires at %s. When it does, the "+
				"Collector shuts itself down — %s", expiresAt.Format(time.RFC3339), remedy),
		}}
	}
	return nil
}

// LicenseSource documents which licence token is installed into a customer's cloud account.
// It is the customer's own token (already in their odigos-central cluster / connector env) — never
// Odigos-the-vendor's, and never another customer's. The exp claim is readable without a secret
// (JWT verified against a public key), which is what makes CheckLicense possible.
const LicenseSource = "the connector's own " + EnvOnPremToken + ", copied into the customer's secret store"
