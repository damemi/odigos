package collectorcredential

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

func fullBinding() Binding {
	return Binding{
		CollectorID:  "col_existing",
		ConnectorUID: "uid-1",
		Provider:     "aws",
		AccountID:    "123456789012",
		Region:       "us-east-1",
		NetworkID:    "vpc-1",
	}
}

func issueRequest(mutate func(*IssueRequest)) IssueRequest {
	req := IssueRequest{
		Binding:   fullBinding(),
		SecretRef: "odigos/collector/aws-prod/us-east-1/vpc-1",
		Actor:     ActorConnector,
	}
	if mutate != nil {
		mutate(&req)
	}
	return req
}

// TestValidateAcceptsAComplete v1 request: no expiry, whole binding, an actor recorded.
func TestValidateAcceptsACompleteRequest(t *testing.T) {
	if problems := issueRequest(nil).Validate(now); len(problems) != 0 {
		t.Errorf("a complete request was refused: %v", codes(problems))
	}
}

// TestValidateRefusesAnIncompleteBinding: the gateway compares the WHOLE tuple, so a row missing one
// field authenticates a Collector whose account or network was never established.
func TestValidateRefusesAnIncompleteBinding(t *testing.T) {
	fields := map[string]func(*IssueRequest){
		"connector UID": func(r *IssueRequest) { r.Binding.ConnectorUID = "" },
		"provider":      func(r *IssueRequest) { r.Binding.Provider = "" },
		"account id":    func(r *IssueRequest) { r.Binding.AccountID = "" },
		"region":        func(r *IssueRequest) { r.Binding.Region = "" },
		"network id":    func(r *IssueRequest) { r.Binding.NetworkID = "" },
		"secret ref":    func(r *IssueRequest) { r.SecretRef = "" },
	}
	for field, drop := range fields {
		t.Run(field, func(t *testing.T) {
			problems := issueRequest(drop).Validate(now)
			if !collectorstatus.HasError(problems) {
				t.Fatalf("a request with no %s was accepted", field)
			}
			// The message has to name the field, or the operator gets "incomplete" and no way forward.
			var named bool
			for _, p := range problems {
				if strings.Contains(p.Message, field) {
					named = true
				}
			}
			if !named {
				t.Errorf("no problem names the missing %s: %v", field, problems)
			}
		})
	}
}

// TestValidateAllowsAnAbsentCollectorID: a Collector with no identity yet is the normal first issuance.
// It is the one field this package may allocate.
func TestValidateAllowsAnAbsentCollectorID(t *testing.T) {
	req := issueRequest(func(r *IssueRequest) { r.Binding.CollectorID = "" })
	if problems := req.Validate(now); collectorstatus.HasError(problems) {
		t.Errorf("a first issuance was refused: %v", codes(problems))
	}
	if !req.NeedsCollectorID() {
		t.Error("NeedsCollectorID is false for a request with no Collector identity")
	}
	if issueRequest(nil).NeedsCollectorID() {
		t.Error("NeedsCollectorID is true for a request that already carries an identity")
	}
}

// TestValidateRequiresAnActor: the value is never stored and the hash is not reversible, so the audit
// trail is the only account of a credential's life. An unattributed issuance leaves none.
func TestValidateRequiresAnActor(t *testing.T) {
	problems := issueRequest(func(r *IssueRequest) { r.Actor = "" }).Validate(now)
	if !collectorstatus.HasError(problems) {
		t.Error("a credential was issuable with no actor recorded")
	}
}

// TestValidateReportsEveryMissingFieldAtOnce: an incomplete binding usually has several holes, and
// reporting one at a time turns one fix into several rounds.
func TestValidateReportsEveryMissingFieldAtOnce(t *testing.T) {
	req := issueRequest(func(r *IssueRequest) {
		r.Binding.Region = ""
		r.Binding.NetworkID = ""
		r.SecretRef = ""
	})
	var count int
	for _, p := range req.Validate(now) {
		if p.Code == "CredentialBindingIncomplete" {
			count++
		}
	}
	if count != 3 {
		t.Errorf("reported %d missing fields, want 3", count)
	}
}

// TestValidateIsDeterministic: the same bad request must produce the same output twice, or a status
// message shuffles between reconciles and a support transcript becomes useless.
func TestValidateIsDeterministic(t *testing.T) {
	req := issueRequest(func(r *IssueRequest) {
		r.Binding = Binding{}
		r.SecretRef = ""
		r.Actor = ""
	})
	first := fmt.Sprint(codes(req.Validate(now)))
	for i := 0; i < 20; i++ {
		if got := fmt.Sprint(codes(req.Validate(now))); got != first {
			t.Fatalf("problem order changed between runs:\n%s\n%s", first, got)
		}
	}
}

// TestValidateEnforcesTheExpiryRule: an expiry may only be issued when something can replace the
// credential unattended, and only with enough runway to schedule a redeploy rather than scramble.
func TestValidateEnforcesTheExpiryRule(t *testing.T) {
	auto := RotationPath{OdigosMayRedeploy: true}
	manual := RotationPath{}

	cases := []struct {
		name     string
		expires  *time.Time
		path     RotationPath
		wantCode string
	}{
		{name: "no expiry, manual mode", path: manual},
		{name: "no expiry, automatic mode", path: auto},
		{
			name:    "long expiry in automatic mode is allowed and reported",
			expires: at(90 * 24 * time.Hour), path: auto,
			wantCode: "ExpiringCredentialRotatesByRedeploy",
		},
		{
			// Only the customer can redeploy, so the Collector would stop at a wall-clock time with no
			// automatic cure — quite possibly while they are asleep.
			name:    "any expiry in manual mode is refused",
			expires: at(90 * 24 * time.Hour), path: manual,
			wantCode: "ExpiringCredentialWithoutRotationPath",
		},
		{
			name:    "a short expiry is refused even in automatic mode",
			expires: at(72 * time.Hour), path: auto,
			wantCode: "ExpiringCredentialTooShort",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := issueRequest(func(r *IssueRequest) {
				r.ExpiresAt = tc.expires
				r.Path = tc.path
			})
			problems := req.Validate(now)
			if tc.wantCode == "" {
				if len(problems) != 0 {
					t.Fatalf("expected no problems, got %v", codes(problems))
				}
				return
			}
			var found bool
			for _, p := range problems {
				if p.Code == tc.wantCode {
					found = true
				}
			}
			if !found {
				t.Errorf("problems = %v, want one with code %q", codes(problems), tc.wantCode)
			}
		})
	}
}

// TestNewRecordPersistsTheVerifierAndTheWholeBinding is the "verifier/binding persisted" criterion.
func TestNewRecordPersistsTheVerifierAndTheWholeBinding(t *testing.T) {
	minted, err := Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	req := issueRequest(func(r *IssueRequest) { r.Binding.CollectorID = "" })

	rec := NewRecord(req, "col_new", "cred_new", minted.Hash, now)

	if rec.TokenSHA256 != minted.Hash {
		t.Errorf("TokenSHA256 = %q, want the minted hash %q", rec.TokenSHA256, minted.Hash)
	}
	if !rec.Binding.Complete() {
		t.Errorf("the persisted binding is incomplete: %+v", rec.Binding)
	}
	if rec.Binding.CollectorID != "col_new" {
		t.Errorf("CollectorID = %q, want the allocated one", rec.Binding.CollectorID)
	}
	// Every other binding field must come from the request untouched — a default invented here is a
	// binding that authorizes something nobody chose.
	want := fullBinding()
	want.CollectorID = "col_new"
	if rec.Binding != want {
		t.Errorf("binding:\n got %+v\nwant %+v", rec.Binding, want)
	}
	if rec.SecretRef != req.SecretRef {
		t.Errorf("SecretRef = %q, want %q", rec.SecretRef, req.SecretRef)
	}
	if !rec.IssuedAt.Equal(now) {
		t.Errorf("IssuedAt = %v, want %v", rec.IssuedAt, now)
	}
}

// TestNewRecordStartsPending: the value exists but has not reached the provider secret store yet, and
// the gateway refuses a pending credential. Starting active would accept a credential whose value was
// not supposed to have left the issuer.
func TestNewRecordStartsPending(t *testing.T) {
	rec := NewRecord(issueRequest(nil), "col_1", "cred_1", "a"+strings.Repeat("0", 63), now)
	if rec.State != StatePending {
		t.Errorf("State = %q, want %q", rec.State, StatePending)
	}
	if rec.Live() {
		t.Error("a freshly issued credential is already accepted by the gateway")
	}
	if rec.ActivatedAt != nil || rec.RevokedAt != nil || rec.SupersededBy != "" {
		t.Errorf("a fresh record carries lifecycle state it has not reached: %+v", rec)
	}
}

// TestRecordHoldsNoValue is the "raw token shown only once" criterion at the type level: the struct
// that gets persisted has nowhere to put a credential, so no store bug can write one.
func TestRecordHoldsNoValue(t *testing.T) {
	minted, err := Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	rec := NewRecord(issueRequest(nil), "col_1", "cred_1", minted.Hash, now)

	// A Record marshals (unlike Secret), which is what makes this worth asserting: it is the shape that
	// reaches reports and logs, so the value must not be reachable through it.
	blob, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling a Record: %v", err)
	}
	if strings.Contains(string(blob), minted.Secret.Reveal()) {
		t.Fatal("a serialized Record contains the credential value")
	}
	if !strings.Contains(string(blob), minted.Hash) {
		t.Error("a serialized Record does not contain the verifier, which is what it exists to carry")
	}
}

// TestRecreationVictims is the "recreation invalidates old credentials" criterion.
//
// A recreated CR gets a new UID and a credential is bound to the UID at issue, so the Collector's
// infrastructure is adopted by tag while its credential is not. That asymmetry is deliberate.
func TestRecreationVictims(t *testing.T) {
	live := func(id, uid string, state State) Record {
		b := fullBinding()
		b.ConnectorUID = uid
		return Record{CredentialID: id, Binding: b, State: state}
	}

	records := []Record{
		live("cred_old_active", "uid-old", StateActive),
		live("cred_old_superseded", "uid-old", StateSuperseded),
		live("cred_old_revoked", "uid-old", StateRevoked),
		live("cred_old_pending", "uid-old", StatePending),
		live("cred_current", "uid-new", StateActive),
		live("cred_other_old", "uid-older-still", StateActive),
	}

	got := RecreationVictims(records, "uid-new")

	var ids []string
	for _, r := range got {
		ids = append(ids, r.CredentialID)
	}
	want := []string{"cred_old_active", "cred_old_superseded", "cred_other_old"}
	if fmt.Sprint(ids) != fmt.Sprint(want) {
		t.Errorf("victims = %v, want %v", ids, want)
	}
}

// TestRecreationVictimsRefusesToGuessWithoutOwnIdentity: a connector that does not know its own UID
// cannot tell its predecessor's credentials from its own, and concluding "all of them" would take a
// live fleet's configuration plane down.
func TestRecreationVictimsRefusesToGuessWithoutOwnIdentity(t *testing.T) {
	records := []Record{{CredentialID: "cred_1", Binding: fullBinding(), State: StateActive}}
	if got := RecreationVictims(records, ""); got != nil {
		t.Errorf("victims = %v with no current UID, want none", got)
	}
}

// TestRecreationVictimsLeavesTheCurrentUIDAlone: the same connector restarting is not a recreation, and
// revoking on a restart would disconnect every Collector every time a pod rolled.
func TestRecreationVictimsLeavesTheCurrentUIDAlone(t *testing.T) {
	records := []Record{
		{CredentialID: "cred_1", Binding: fullBinding(), State: StateActive},
		{CredentialID: "cred_2", Binding: fullBinding(), State: StateSuperseded},
	}
	if got := RecreationVictims(records, fullBinding().ConnectorUID); len(got) != 0 {
		t.Errorf("victims = %v on a plain restart, want none", got)
	}
}

// TestAuditDetailCarriesNoSecretMaterial is the audit half of "no plaintext credential appears in
// definitions, logs, or artifacts" — and it withholds the hash too. The hash is the verifier, and an
// audit table is read by more people and exported to more places than the credentials table.
func TestAuditDetailCarriesNoSecretMaterial(t *testing.T) {
	minted, err := Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	revokedAt := now
	rec := NewRecord(issueRequest(nil), "col_1", "cred_1", minted.Hash, now)
	rec.State = StateRevoked
	rec.RevokedAt = &revokedAt
	rec.RevokedReason = ReasonCompromise
	rec.SupersededBy = "cred_2"

	detail := AuditDetail(rec, ActorCLI)
	blob, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshalling audit detail: %v", err)
	}
	rendered := string(blob)

	if strings.Contains(rendered, minted.Secret.Reveal()) {
		t.Fatal("an audit row contains the credential value")
	}
	if strings.Contains(rendered, minted.Hash) {
		t.Fatal("an audit row contains the verifier hash")
	}
	if got := detail["fingerprint"]; got != FingerprintOfHash(minted.Hash) {
		t.Errorf("fingerprint = %v, want %q", got, FingerprintOfHash(minted.Hash))
	}

	// And it must carry enough to trace the operation, or there was no point recording it.
	for _, key := range []string{
		"collector_id", "credential_id", "connector_uid", "provider", "account_id",
		"region", "network_id", "secret_ref", "state", "actor", "revoked_reason", "superseded_by",
	} {
		if v, ok := detail[key]; !ok || v == "" {
			t.Errorf("audit detail is missing %s", key)
		}
	}
	if detail["actor"] != string(ActorCLI) {
		t.Errorf("actor = %v, want %q", detail["actor"], ActorCLI)
	}
}

// TestAuditDetailOmitsUnsetLifecycleFields: an audit row for an issuance should not claim a revocation
// reason of "" or an expiry of the zero time, both of which read as facts.
func TestAuditDetailOmitsUnsetLifecycleFields(t *testing.T) {
	rec := NewRecord(issueRequest(nil), "col_1", "cred_1", strings.Repeat("a", 64), now)

	detail := AuditDetail(rec, ActorConnector)
	for _, key := range []string{"revoked_reason", "superseded_by", "expires_at"} {
		if _, present := detail[key]; present {
			t.Errorf("audit detail for a fresh issuance carries %s", key)
		}
	}

	// And a v1 credential has no expiry at all.
	if rec.ExpiresAt != nil {
		t.Error("a default issuance carries an expiry")
	}
}

// TestAuditEventNamesAreDistinct: the three events are read back by anybody auditing a credential's
// life, and two that collide make an issuance indistinguishable from a revocation.
func TestAuditEventNamesAreDistinct(t *testing.T) {
	events := []string{EventCredentialIssued, EventCredentialSuperseded, EventCredentialRevoked}
	seen := map[string]bool{}
	for _, e := range events {
		if e == "" {
			t.Error("an audit event has no name")
		}
		if seen[e] {
			t.Errorf("duplicate audit event name %q", e)
		}
		seen[e] = true
	}
}
