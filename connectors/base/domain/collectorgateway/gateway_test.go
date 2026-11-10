package collectorgateway

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

var now = time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

func binding() collectorcredential.Binding {
	return collectorcredential.Binding{
		CollectorID:  "col-1",
		ConnectorUID: "uid-1",
		Provider:     "aws",
		AccountID:    "123456789012",
		Region:       "us-east-1",
		NetworkID:    "vpc-1",
	}
}

func hashOf(t *testing.T, v string) string {
	t.Helper()
	h, err := collectorcredential.NewSecret(v).Hash()
	if err != nil {
		t.Fatalf("hashing %q: %v", v, err)
	}
	return h
}

func record(t *testing.T, mutate func(*collectorcredential.Record)) *collectorcredential.Record {
	t.Helper()
	r := &collectorcredential.Record{
		CredentialID: "cred-1",
		Binding:      binding(),
		TokenSHA256:  hashOf(t, "the-token"),
		State:        collectorcredential.StateActive,
		IssuedAt:     now.Add(-time.Hour),
	}
	if mutate != nil {
		mutate(r)
	}
	return r
}

func connector() *Connector {
	return &Connector{
		UID:       "uid-1",
		Name:      "aws-prod",
		Namespace: "odigos-system",
		Provider:  "aws",
		AccountID: "123456789012",
	}
}

// TestAuthorize covers every path a handshake can take. Each row is a decision a customer's Collector
// or an attacker's probe actually produces, so a changed verdict here is a changed security property.
func TestAuthorize(t *testing.T) {
	future := now.Add(24 * time.Hour)
	past := now.Add(-24 * time.Hour)

	cases := []struct {
		name    string
		hash    string
		cred    *collectorcredential.Record
		conn    *Connector
		outcome Outcome
		refusal Refusal
	}{
		{
			name: "active credential, registered connector", hash: hashOf(t, "the-token"),
			cred: record(t, nil), conn: connector(), outcome: OutcomeAccept,
		},
		{
			// The rotation window (§6.4): the outgoing credential stays accepted while the old tasks
			// drain. Refusing it here would make every routine rotation an outage.
			name: "superseded credential is still accepted", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) {
				r.State = collectorcredential.StateSuperseded
				r.SupersededBy = "cred-2"
			}),
			conn: connector(), outcome: OutcomeAccept,
		},
		{
			name: "no credential presented", hash: "",
			cred: record(t, nil), conn: connector(), outcome: OutcomeRefuse, refusal: RefusalNoCredential,
		},
		{
			// Nothing legitimate mints this. A row carrying it is a failed issuance recorded as a
			// success, and it must not become a credential that authenticates.
			name: "the digest of an empty string never matches", hash: collectorcredential.EmptyDigest,
			cred: record(t, func(r *collectorcredential.Record) { r.TokenSHA256 = collectorcredential.EmptyDigest }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalEmptyDigest,
		},
		{
			name: "no row for the presented hash", hash: hashOf(t, "some-other-token"),
			cred: nil, conn: connector(), outcome: OutcomeRefuse, refusal: RefusalUnknownCredential,
		},
		{
			// A store that stopped looking up by hash. Cheap to check, and the failure it catches is
			// "any token authenticates as this Collector".
			name: "row whose hash is not the presented one", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { r.TokenSHA256 = hashOf(t, "different") }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalHashMismatch,
		},
		{
			name: "row with an incomplete binding", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { r.Binding.NetworkID = "" }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalIncompleteBinding,
		},
		{
			name: "pending credential was never installed", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { r.State = collectorcredential.StatePending }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalNotYetActive,
		},
		{
			name: "revoked credential", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) {
				r.State = collectorcredential.StateRevoked
				r.RevokedAt = &past
				r.RevokedReason = collectorcredential.ReasonCompromise
			}),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalRevoked,
		},
		{
			name: "credential expiring in the future is accepted", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { r.ExpiresAt = &future }),
			conn: connector(), outcome: OutcomeAccept,
		},
		{
			name: "expired credential", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { r.ExpiresAt = &past }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalExpired,
		},
		{
			// exp is exclusive: dead AT the instant, matching collectorimage.CheckLicense so the two
			// cannot disagree about what an expiry time means.
			name: "credential expiring exactly now", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { n := now; r.ExpiresAt = &n }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalExpired,
		},
		{
			// The recreation case (§6.4). Not the Collector's fault and not cured by retrying, but not
			// an authentication failure either — the credential is intact, its connector is gone.
			name: "bound connector is not registered", hash: hashOf(t, "the-token"),
			cred: record(t, nil), conn: nil, outcome: OutcomeUnavailable, refusal: RefusalConnectorUnknown,
		},
		{
			// Teardown revokes before deleting; a connection in this window is one the revoke has not
			// reached. Handing it a configuration would hand it the destination credential on the way out.
			name: "connector is being deleted", hash: hashOf(t, "the-token"),
			cred:    record(t, nil),
			conn:    &Connector{UID: "uid-1", Provider: "aws", AccountID: "123456789012", Terminating: true},
			outcome: OutcomeUnavailable, refusal: RefusalConnectorTerminating,
		},
		{
			name: "credential provider disagrees with the connector", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { r.Binding.Provider = "azure" }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalProviderMismatch,
		},
		{
			// The cross-account attempt this check exists for: a real credential for account A pointed
			// at a connector that owns account B.
			name: "credential account disagrees with the connector", hash: hashOf(t, "the-token"),
			cred: record(t, func(r *collectorcredential.Record) { r.Binding.AccountID = "999999999999" }),
			conn: connector(), outcome: OutcomeRefuse, refusal: RefusalAccountMismatch,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Authorize(tc.hash, tc.cred, tc.conn, now)
			if got.Outcome != tc.outcome {
				t.Errorf("Outcome = %v, want %v (refusal %q)", got.Outcome, tc.outcome, got.Refusal)
			}
			if got.Refusal != tc.refusal {
				t.Errorf("Refusal = %q, want %q", got.Refusal, tc.refusal)
			}
			if tc.outcome == OutcomeAccept {
				if !got.Accepted() {
					t.Error("Accepted() is false on an accepting decision")
				}
				if got.Identity.CredentialID != tc.cred.CredentialID {
					t.Errorf("CredentialID = %q, want %q", got.Identity.CredentialID, tc.cred.CredentialID)
				}
				if !got.Identity.Binding.Complete() {
					t.Errorf("accepted with an incomplete binding: %+v", got.Identity.Binding)
				}
				return
			}
			if got.Accepted() {
				t.Error("Accepted() is true on a non-accepting decision")
			}
			if got.Identity != (Identity{}) {
				t.Errorf("a refused decision carries an identity: %+v", got.Identity)
			}
		})
	}
}

// TestAuthorizeChecksCredentialBeforeConnector: an unauthenticated caller must not learn which
// connectors exist. With both a bad credential and a missing connector, the credential refusal is what
// comes back — the ordering is the property, not an accident of how the function is written.
func TestAuthorizeChecksCredentialBeforeConnector(t *testing.T) {
	d := Authorize(hashOf(t, "nope"), nil, nil, now)
	if d.Refusal != RefusalUnknownCredential {
		t.Errorf("Refusal = %q, want %q — connector state leaked ahead of authentication", d.Refusal, RefusalUnknownCredential)
	}
	if d.Outcome != OutcomeRefuse {
		t.Errorf("Outcome = %v, want OutcomeRefuse", d.Outcome)
	}
}

// TestZeroDecisionDenies: OutcomeRefuse is the zero value, so a Decision nobody filled in — an early
// return, a path added later — denies rather than admits.
func TestZeroDecisionDenies(t *testing.T) {
	var d Decision
	if d.Accepted() {
		t.Error("the zero Decision accepts")
	}
}

// TestAuthorizeIsPure: Authorize must not edit the caller's record. The gateway holds rows from a
// shared cache, and a mutation here would be a per-connection edit of state other connections read.
func TestAuthorizeIsPure(t *testing.T) {
	rec := record(t, nil)
	before := *rec
	conn := connector()
	connBefore := *conn

	Authorize(hashOf(t, "the-token"), rec, conn, now)

	if *rec != before {
		t.Errorf("Authorize mutated the record: %+v -> %+v", before, *rec)
	}
	if *conn != connBefore {
		t.Errorf("Authorize mutated the connector: %+v -> %+v", connBefore, *conn)
	}
}
