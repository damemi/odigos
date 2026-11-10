package collectorcredential

import (
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

// v1 is the shipping configuration: the Supervisor does not accept OpAMP connection settings, and
// automatic mode can force a redeploy while manual mode cannot.
var (
	v1Automatic = RotationPath{AcceptsOpAMPConnectionSettings: false, OdigosMayRedeploy: true}
	v1Manual    = RotationPath{AcceptsOpAMPConnectionSettings: false, OdigosMayRedeploy: false}
)

func at(d time.Duration) *time.Time { t := now.Add(d); return &t }

var now = time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)

func codes(ps []collectorstatus.Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Code)
	}
	return out
}

// TestLongLivedIsAlwaysAllowed: no expiry is what v1 issues, in both modes, and it must never produce a
// complaint. A warning on the shipping default trains people to ignore warnings.
func TestLongLivedIsAlwaysAllowed(t *testing.T) {
	for name, p := range map[string]RotationPath{"automatic": v1Automatic, "manual": v1Manual} {
		if ps := CheckLifetime(nil, now, p); len(ps) != 0 {
			t.Errorf("%s: a long-lived credential reported %v", name, codes(ps))
		}
	}
}

// TestExpiringRefusedWithoutARotationPath is the unambiguous half of "long-lived versus expiring". In
// manual mode nothing but the customer can replace the credential, so an expiry is a Collector that
// stops at a wall-clock time with no automatic cure.
func TestExpiringRefusedWithoutARotationPath(t *testing.T) {
	ps := CheckLifetime(at(365*24*time.Hour), now, v1Manual)
	if !collectorstatus.HasError(ps) {
		t.Fatalf("problems = %v, want a refusal", codes(ps))
	}
	if ps[0].Code != "ExpiringCredentialWithoutRotationPath" {
		t.Errorf("code = %q", ps[0].Code)
	}
	if !strings.Contains(ps[0].Message, "long-lived") {
		t.Errorf("the refusal does not name the alternative: %s", ps[0].Message)
	}

	// The same expiry is permitted once something can replace it unattended — either mode of rotation.
	for name, p := range map[string]RotationPath{
		"automatic mode redeploys":        v1Automatic,
		"supervisor accepts new settings": {AcceptsOpAMPConnectionSettings: true},
	} {
		if ps := CheckLifetime(at(365*24*time.Hour), now, p); collectorstatus.HasError(ps) {
			t.Errorf("%s: refused a valid expiry: %v", name, codes(ps))
		}
	}
}

// TestExpiringRefusedWhenTooShort: rotation replaces a deployment, so a credential has to outlive a
// change window. A three-day credential is a three-day outage clock.
func TestExpiringRefusedWhenTooShort(t *testing.T) {
	ps := CheckLifetime(at(3*24*time.Hour), now, v1Automatic)
	if !collectorstatus.HasError(ps) {
		t.Fatalf("problems = %v, want a refusal", codes(ps))
	}
	if ps[0].Code != "ExpiringCredentialTooShort" {
		t.Errorf("code = %q", ps[0].Code)
	}
	// Exactly at the minimum is allowed; a minute under is not.
	if ps := CheckLifetime(at(MinExpiringLifetime), now, v1Automatic); collectorstatus.HasError(ps) {
		t.Errorf("the minimum lifetime was refused: %v", codes(ps))
	}
	if ps := CheckLifetime(at(MinExpiringLifetime-time.Minute), now, v1Automatic); !collectorstatus.HasError(ps) {
		t.Error("a lifetime under the minimum was accepted")
	}
}

// TestRenewalDue: a long-lived credential is never due, which is the point of it. An expiring one comes
// due a full lead time early, because rotation is a deployment rather than an API call.
func TestRenewalDue(t *testing.T) {
	longLived := Record{State: StateActive}
	if due, ps := RenewalDue(longLived, now); due || len(ps) != 0 {
		t.Errorf("a long-lived credential reported due=%v %v", due, codes(ps))
	}

	comfortable := Record{State: StateActive, ExpiresAt: at(90 * 24 * time.Hour)}
	if due, _ := RenewalDue(comfortable, now); due {
		t.Error("a credential 90 days out reported due")
	}

	soon := Record{State: StateActive, ExpiresAt: at(RenewBefore - time.Hour)}
	due, ps := RenewalDue(soon, now)
	if !due {
		t.Fatal("a credential inside the renewal lead time did not report due")
	}
	if len(ps) != 1 || ps[0].Severity != collectorstatus.SeverityDegraded {
		t.Errorf("problems = %v, want one degraded", codes(ps))
	}

	expired := Record{State: StateActive, ExpiresAt: at(-time.Hour)}
	due, ps = RenewalDue(expired, now)
	if !due || !collectorstatus.HasError(ps) {
		t.Errorf("an expired credential reported due=%v %v, want an error", due, codes(ps))
	}

	// A revoked credential is not renewed; it is replaced.
	if due, _ := RenewalDue(Record{State: StateRevoked, ExpiresAt: at(-time.Hour)}, now); due {
		t.Error("a revoked credential reported renewal due")
	}
}

func current() Record {
	return Record{CredentialID: "cred-1", SecretRef: "arn:secret:collector", State: StateActive,
		TokenSHA256: mustHash("value"), Binding: binding()}
}

// mustHash is the test's stand-in for the one call site that writes a credential to the secret store.
func mustHash(v string) string {
	h, err := NewSecret(v).Hash()
	if err != nil {
		panic(err)
	}
	return h
}

// TestRotationRevokesLast is the ordering that is easy to get backwards, and getting it backwards is a
// self-inflicted outage: revoking first disconnects a running Collector for the whole length of a
// deployment, in service of an operation that had no urgency.
func TestRotationRevokesLast(t *testing.T) {
	for _, cause := range []Reason{ReasonRotation, ReasonConnectorRecreated} {
		p := PlanRotation(current(), cause, v1Automatic)
		revoke, connect := p.IndexOf(ActionRevoke), p.IndexOf(ActionAwaitConnection)
		if revoke < 0 || connect < 0 {
			t.Fatalf("%s: plan is missing a step: %v", cause, actions(p))
		}
		if revoke < connect {
			t.Errorf("%s: revoke at %d precedes the replacement connecting at %d: %v", cause, revoke, connect, actions(p))
		}
		if issue := p.IndexOf(ActionIssue); issue < 0 || issue > revoke {
			t.Errorf("%s: the replacement is issued at %d, after the revoke at %d: %v", cause, issue, revoke, actions(p))
		}
	}
}

// TestRotationRedeploysRatherThanJustWriting: on every provider here a running task holds the value
// injected at start, so writing a new secret version rotates nothing on its own. A plan that stops at
// the write reports success while the Collector keeps using the credential about to be revoked.
func TestRotationRedeploysRatherThanJustWriting(t *testing.T) {
	p := PlanRotation(current(), ReasonRotation, v1Automatic)
	write, redeploy := p.IndexOf(ActionWriteSecret), p.IndexOf(ActionRedeploy)
	if write < 0 || redeploy < 0 {
		t.Fatalf("plan = %v, want both a secret write and a redeploy", actions(p))
	}
	if redeploy < write {
		t.Errorf("redeploy at %d precedes the secret write at %d: %v", redeploy, write, actions(p))
	}
	for _, s := range p.Steps {
		if s.Action == ActionRedeploy && !strings.Contains(s.Why, "rotates nothing") {
			t.Errorf("the redeploy step does not say why it is required: %q", s.Why)
		}
	}
}

// TestCompromiseRevokesFirst: the credential's holder can read the desired configuration, which carries
// the destination's own credential, so every second it stays valid is a second someone may be reading
// it. The Collector goes down, and that is the correct trade.
func TestCompromiseRevokesFirst(t *testing.T) {
	p := PlanRotation(current(), ReasonCompromise, v1Automatic)
	if got := p.IndexOf(ActionRevoke); got != 0 {
		t.Fatalf("revoke at index %d, want first: %v", got, actions(p))
	}
	if !collectorstatus.HasError(p.Problems) {
		t.Errorf("problems = %v, want the outage reported as an error", codes(p.Problems))
	}
	var explained bool
	for _, s := range p.Steps {
		if s.Action == ActionRevoke && strings.Contains(s.Why, "destination credential") {
			explained = true
		}
	}
	if !explained {
		t.Error("the revoke-first step does not say what the holder can read")
	}
}

// TestManualModeRotationInstructsRatherThanActs: Odigos never touches a manual-mode Collector's
// infrastructure, so its two install steps become one instruction — and the old credential must keep
// working until the customer has actually redeployed.
func TestManualModeRotationInstructsRatherThanActs(t *testing.T) {
	p := PlanRotation(current(), ReasonRotation, v1Manual)
	if p.IndexOf(ActionWriteSecret) >= 0 || p.IndexOf(ActionRedeploy) >= 0 {
		t.Errorf("manual-mode rotation writes or redeploys: %v", actions(p))
	}
	instruct := p.IndexOf(ActionInstructCustomer)
	if instruct < 0 {
		t.Fatalf("plan = %v, want a customer instruction", actions(p))
	}
	if revoke := p.IndexOf(ActionRevoke); revoke < p.IndexOf(ActionAwaitConnection) {
		t.Errorf("manual-mode rotation revokes before the replacement connects: %v", actions(p))
	}
	var pending bool
	for _, pr := range p.Problems {
		if pr.Code == "CredentialRotationAwaitingCustomer" {
			pending = true
			if pr.Severity != collectorstatus.SeverityPending {
				t.Errorf("severity = %q, want Pending: waiting on a customer is not a fault", pr.Severity)
			}
			if !strings.Contains(pr.Message, "keeps working") {
				t.Errorf("the instruction does not reassure that the current credential still works: %s", pr.Message)
			}
		}
	}
	if !pending {
		t.Errorf("problems = %v, want the wait reported", codes(p.Problems))
	}
}

// TestConnectorRecreationDoesNotInheritTheCredential: the infrastructure is adopted by tag, the
// credential is not. The two behave differently on purpose and the difference has a visible cost — the
// Collector is disconnected until it is redeployed — which has to be reported rather than discovered.
func TestConnectorRecreationDoesNotInheritTheCredential(t *testing.T) {
	p := PlanRotation(current(), ReasonConnectorRecreated, v1Automatic)
	var reported bool
	for _, pr := range p.Problems {
		if pr.Code == "CredentialNotInheritedOnRecreation" {
			reported = true
			if !strings.Contains(pr.Message, "adopted by tag") {
				t.Errorf("the message does not contrast with the infrastructure: %s", pr.Message)
			}
		}
	}
	if !reported {
		t.Errorf("problems = %v, want the non-inheritance reported", codes(p.Problems))
	}
}

// TestTeardownRevokesFirst is the inversion of the rotation rule, and the contrast is the point: here
// the connection is MEANT to die. A task that outlives the delete call would otherwise keep an
// authenticated session open and keep being handed a configuration containing the destination credential.
func TestTeardownRevokesFirst(t *testing.T) {
	records := []Record{
		{CredentialID: "cred-1", State: StateSuperseded},
		{CredentialID: "cred-2", State: StateActive},
		{CredentialID: "cred-0", State: StateRevoked},
	}
	p := PlanTeardown(records, ReasonTeardown)
	del := p.IndexOf(ActionDeleteWorkload)
	if del < 0 {
		t.Fatalf("plan = %v, want a delete step", actions(p))
	}
	var revoked []string
	for i, s := range p.Steps {
		if s.Action != ActionRevoke {
			continue
		}
		revoked = append(revoked, s.CredentialID)
		if i > del {
			t.Errorf("credential %s revoked after the workload was deleted", s.CredentialID)
		}
	}
	// Every LIVE credential, not just the active one: a teardown during a rotation has two.
	if len(revoked) != 2 {
		t.Errorf("revoked %v, want both live credentials and not the already-revoked one", revoked)
	}
	for _, id := range revoked {
		if id == "cred-0" {
			t.Error("an already-revoked credential was revoked again")
		}
	}
}

// TestCapabilityRemovedRevokesButLeavesTheCollector: removing the capability stops Odigos configuring
// the Collector, and removing it does not delete. The credential still goes, so nothing keeps pulling
// a configuration carrying the destination credential — and the customer is told the Collector is still
// running on its last config.
func TestCapabilityRemovedRevokesButLeavesTheCollector(t *testing.T) {
	p := PlanTeardown([]Record{{CredentialID: "cred-1", State: StateActive}}, ReasonCapabilityRemoved)
	if p.IndexOf(ActionRevoke) != 0 {
		t.Errorf("plan = %v, want the revoke first", actions(p))
	}
	if p.IndexOf(ActionDeleteWorkload) >= 0 {
		t.Errorf("plan = %v, capability removal must not delete the Collector", actions(p))
	}
	var told bool
	for _, pr := range p.Problems {
		if pr.Code == "CredentialsRevokedCollectorLeft" {
			told = true
			if !strings.Contains(pr.Message, "keeps running") {
				t.Errorf("the message does not say the Collector is still running: %s", pr.Message)
			}
		}
	}
	if !told {
		t.Errorf("problems = %v, want the left-running Collector reported", codes(p.Problems))
	}
}

// TestConcurrentConnectionsExpected: a credential authenticates a COLLECTOR, not a connection. A
// Collector scaled to N replicas opens N sessions under one credential, and anything treating the second
// as a replay breaks scale-out at exactly the moment a customer's traffic grew enough to need it.
func TestConcurrentConnectionsExpected(t *testing.T) {
	if got := ConcurrentConnectionsExpected(3); got != 3 {
		t.Errorf("three replicas expect %d concurrent sessions, want 3", got)
	}
	for _, replicas := range []int{0, -1, 1} {
		if got := ConcurrentConnectionsExpected(replicas); got != 1 {
			t.Errorf("%d replicas expect %d, want at least 1", replicas, got)
		}
	}
}

// TestEveryPlanStepExplainsItself: an ordering nobody can read is an ordering the next person reorders.
func TestEveryPlanStepExplainsItself(t *testing.T) {
	plans := []Plan{
		PlanRotation(current(), ReasonRotation, v1Automatic),
		PlanRotation(current(), ReasonRotation, v1Manual),
		PlanRotation(current(), ReasonCompromise, v1Automatic),
		PlanRotation(current(), ReasonConnectorRecreated, v1Automatic),
		PlanTeardown([]Record{{CredentialID: "c", State: StateActive}}, ReasonTeardown),
	}
	for i, p := range plans {
		if len(p.Steps) == 0 {
			t.Errorf("plan %d is empty", i)
		}
		for _, s := range p.Steps {
			if s.Why == "" {
				t.Errorf("plan %d: step %s has no rationale", i, s.Action)
			}
		}
		for _, pr := range p.Problems {
			if pr.Code == "" || pr.Message == "" {
				t.Errorf("plan %d: problem %+v is not reportable", i, pr)
			}
		}
	}
}

func actions(p Plan) []Action {
	var out []Action
	for _, s := range p.Steps {
		out = append(out, s.Action)
	}
	return out
}
