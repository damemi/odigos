package collectorcredential

import (
	"fmt"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

const (
	// MinExpiringLifetime is the shortest expiry that may be issued. Rotation is a deployment, not an
	// API call, so the credential must outlive a change window (a 3-day credential is a 3-day outage clock).
	MinExpiringLifetime = 30 * 24 * time.Hour
	// RenewBefore is how far ahead of expiry rotation must start.
	RenewBefore = 7 * 24 * time.Hour
)

// RotationPath is how a new credential can reach a running Collector. A credential may only expire
// when something other than a human can replace it.
type RotationPath struct {
	// AcceptsOpAMPConnectionSettings mirrors the Supervisor flag. False in v1 — the static
	// Authorization header cannot be replaced in place.
	AcceptsOpAMPConnectionSettings bool
	// OdigosMayRedeploy is true in automatic mode. False in manual — only the customer can redeploy.
	OdigosMayRedeploy bool
}

// Unattended reports whether a new credential can reach the Collector without human action.
func (p RotationPath) Unattended() bool {
	return p.AcceptsOpAMPConnectionSettings || p.OdigosMayRedeploy
}

// CheckLifetime is the long-lived-versus-expiring rule.
//
// No expiry is always allowed (what v1 issues). An expiry requires an unattended rotation path and a
// lifetime of at least MinExpiringLifetime. Manual mode has neither, so an expiry is refused.
//
// expires_at must exist for when the Supervisor accepts connection settings; this check stops the
// column being set just because it is available.
func CheckLifetime(expiresAt *time.Time, issuedAt time.Time, p RotationPath) []collectorstatus.Problem {
	if expiresAt == nil {
		return nil
	}
	if !p.Unattended() {
		return []collectorstatus.Problem{{
			Code:     "ExpiringCredentialWithoutRotationPath",
			Severity: collectorstatus.SeverityError,
			Message: "refusing to issue an expiring Collector credential: the Supervisor does not accept " +
				"OpAMP connection settings and Odigos cannot redeploy this Collector, so nothing can replace " +
				"the credential before it expires. Issue a long-lived, individually revocable credential instead",
		}}
	}
	if lifetime := expiresAt.Sub(issuedAt); lifetime < MinExpiringLifetime {
		return []collectorstatus.Problem{{
			Code:     "ExpiringCredentialTooShort",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("refusing to issue a Collector credential valid for %s: rotation replaces the "+
				"Collector's deployment, so a credential must outlive a change window. The minimum is %s",
				lifetime.Round(time.Hour), MinExpiringLifetime),
		}}
	}
	return []collectorstatus.Problem{{
		Code:     "ExpiringCredentialRotatesByRedeploy",
		Severity: collectorstatus.SeverityInfo,
		Message: fmt.Sprintf("this credential expires; Odigos will rotate it starting %s beforehand, which "+
			"replaces the Collector's tasks. A Collector on ephemeral storage loses its queued telemetry when "+
			"that happens", RenewBefore),
	}}
}

// RenewalDue reports whether rotation must start, plus what to report. Long-lived credentials are never due.
func RenewalDue(r Record, now time.Time) (bool, []collectorstatus.Problem) {
	if r.ExpiresAt == nil || !r.Live() {
		return false, nil
	}
	if now.After(*r.ExpiresAt) {
		return true, []collectorstatus.Problem{{
			Code:     "CredentialExpired",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("credential %s expired at %s; this Collector cannot connect until it is "+
				"redeployed with a new one", FingerprintOfHash(r.TokenSHA256), r.ExpiresAt.Format(time.RFC3339)),
		}}
	}
	if now.Add(RenewBefore).After(*r.ExpiresAt) {
		return true, []collectorstatus.Problem{{
			Code:     "CredentialRenewalDue",
			Severity: collectorstatus.SeverityDegraded,
			Message: fmt.Sprintf("credential %s expires at %s and rotation replaces this Collector's "+
				"deployment; it must be rotated now rather than at expiry",
				FingerprintOfHash(r.TokenSHA256), r.ExpiresAt.Format(time.RFC3339)),
		}}
	}
	return false, nil
}

// Action is one step of a credential operation. Order from PlanRotation / PlanTeardown is load-bearing.
type Action string

const (
	// ActionRevoke stops the gateway accepting a credential.
	ActionRevoke Action = "revoke"
	// ActionIssue mints a replacement bound to the current connector UID.
	ActionIssue Action = "issue"
	// ActionWriteSecret writes the value as a new version of the Collector's existing secret.
	ActionWriteSecret Action = "write-secret"
	// ActionInstructCustomer reveals once and reports steps — manual mode's substitute for write+redeploy.
	ActionInstructCustomer Action = "instruct-customer"
	// ActionRedeploy replaces the Collector's tasks — the step that actually rotates anything.
	ActionRedeploy Action = "redeploy"
	// ActionAwaitConnection waits for a successful OpAMP connection under the new credential.
	ActionAwaitConnection Action = "await-connection"
	// ActionDeleteWorkload removes the Collector's compute (teardown only, not capability removal).
	ActionDeleteWorkload Action = "delete-workload"
)

// Step is one ordered action and why it sits where it does.
type Step struct {
	Action Action
	// CredentialID is the credential acted on, or empty for the replacement (not yet issued).
	CredentialID string
	Why          string
}

// Plan is an ordered sequence of steps plus anything worth reporting while it runs.
type Plan struct {
	Steps    []Step
	Problems []collectorstatus.Problem
}

// IndexOf returns the position of the first step with this action, or -1.
func (p Plan) IndexOf(a Action) int {
	for i, s := range p.Steps {
		if s.Action == a {
			return i
		}
	}
	return -1
}

// PlanRotation returns the ordered steps to replace a Collector's credential.
//
// Routine rotation revokes last (after the replacement connects) so the running Collector is never
// disconnected for a non-urgent operation. StateSuperseded exists for that drain window.
//
// Compromise revokes first: the holder can read desired config (including the destination credential).
// Accept the outage until the replacement deploys.
//
// Contrast PlanTeardown, which also revokes first — there the connection is meant to die.
func PlanRotation(current Record, cause Reason, p RotationPath) Plan {
	var plan Plan

	install := func() {
		if p.OdigosMayRedeploy {
			plan.Steps = append(plan.Steps,
				Step{ActionWriteSecret, "", "write the value as a new version of " + current.SecretRef},
				Step{ActionRedeploy, "", "running tasks hold the value injected at start, so writing the secret alone rotates nothing"},
			)
			return
		}
		plan.Steps = append(plan.Steps, Step{ActionInstructCustomer, "",
			"reveal the value once, then the customer writes the secret version and redeploys; Odigos does not touch their infrastructure"})
	}

	if cause == ReasonCompromise {
		plan.Steps = append(plan.Steps, Step{ActionRevoke, current.CredentialID,
			"revoked first: whoever holds it can read the desired configuration, which carries the destination credential"})
		plan.Steps = append(plan.Steps, Step{ActionIssue, "", "bound to the current connector UID"})
		install()
		plan.Steps = append(plan.Steps, Step{ActionAwaitConnection, "", "the Collector is disconnected until this lands"})
		plan.Problems = append(plan.Problems, collectorstatus.Problem{
			Code:     "CredentialCompromiseOutage",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("credential %s was revoked as compromised, so this Collector is disconnected "+
				"until it is redeployed with its replacement. Telemetry already queued on the Collector is "+
				"unaffected; the configuration plane is down", FingerprintOfHash(current.TokenSHA256)),
		})
		return plan
	}

	plan.Steps = append(plan.Steps, Step{ActionIssue, "", "bound to the current connector UID"})
	install()
	plan.Steps = append(plan.Steps,
		Step{ActionAwaitConnection, "", "the outgoing credential stays accepted until this succeeds"},
		Step{ActionRevoke, current.CredentialID, "revoked last, so the running Collector is never disconnected by a routine rotation"},
	)

	if cause == ReasonConnectorRecreated {
		plan.Problems = append(plan.Problems, collectorstatus.Problem{
			Code:     "CredentialNotInheritedOnRecreation",
			Severity: collectorstatus.SeverityDegraded,
			Message: "this connector was recreated, so its CR UID changed and the Collector's existing " +
				"credential no longer routes to it. The infrastructure is adopted by tag, but the credential " +
				"is not inherited: the Collector stays disconnected until it is redeployed with a new one",
		})
	}
	if !p.OdigosMayRedeploy {
		plan.Problems = append(plan.Problems, collectorstatus.Problem{
			Code:     "CredentialRotationAwaitingCustomer",
			Severity: collectorstatus.SeverityPending,
			Message: "a replacement credential is ready. Retrieve it once, write it to this Collector's secret " +
				"and redeploy; the current credential keeps working until you do, and Odigos revokes it only " +
				"after the replacement connects",
		})
	}
	return plan
}

// PlanTeardown returns the ordered steps to tear down a Collector's credentials.
//
// Revoke every live credential before deleting compute (opposite of routine rotation): a draining or
// stuck task must not keep an authenticated OpAMP session and keep receiving destination credentials.
// A teardown during rotation has two live credentials; both go.
//
// Capability removal revokes but does not delete — the Collector keeps running on its last config.
func PlanTeardown(records []Record, reason Reason) Plan {
	var plan Plan
	revokeWhy := "revoked before the compute is deleted, so a draining task cannot keep an authenticated session open"
	if reason == ReasonCapabilityRemoved {
		revokeWhy = "revoked so nothing keeps pulling a configuration that carries the destination credential"
	}
	for _, r := range records {
		if !r.Live() {
			continue
		}
		plan.Steps = append(plan.Steps, Step{ActionRevoke, r.CredentialID, revokeWhy})
	}
	if reason == ReasonCapabilityRemoved {
		plan.Problems = append(plan.Problems, collectorstatus.Problem{
			Code:     "CredentialsRevokedCollectorLeft",
			Severity: collectorstatus.SeverityDegraded,
			Message: "the managed-Collector capability was removed, so this Collector's credentials are revoked " +
				"and Odigos no longer configures it. It keeps running and exporting under its last " +
				"configuration until you remove it",
		})
		return plan
	}
	plan.Steps = append(plan.Steps, Step{ActionDeleteWorkload, "", "gated per object by collectorownership.MayDelete"})
	return plan
}

// ConcurrentConnectionsExpected is how many OpAMP sessions one credential may carry.
// The credential authenticates the Collector, not a connection — N replicas → N sessions with
// different instance UIDs. Treating a second session as replay breaks scale-out.
func ConcurrentConnectionsExpected(replicas int) int {
	if replicas < 1 {
		return 1
	}
	return replicas
}
