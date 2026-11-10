package deployer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos/connectors/spec"
)

func credentialInput(secrets *fakeSecrets, hasValue bool) CredentialInput {
	name := CredentialSecretName(testConnector, testRegion)
	secrets.withSecret(name, ourTags(testNetwork), hasValue)
	return CredentialInput{
		Key:            testKey(),
		Kind:           testKind,
		Mode:           spec.ModeAutomatic,
		Binding:        BindingFor(testUID, "aws", testAccount, testKey(), testNetwork),
		SecretARN:      secrets.secrets[name].arn,
		SecretHasValue: hasValue,
	}
}

func TestEnsureCredentialIssuesInstallsAndActivates(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	in := credentialInput(secrets, false)

	cred, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureCredential: %v / %v", err, problems)
	}
	if !cred.Issued || cred.CollectorID == "" || cred.CredentialID == "" {
		t.Fatalf("credential = %+v", cred)
	}

	// The whole sequence, in order, because each step exists to survive the previous one failing: the
	// row before the identity, the value before the activation, and the gateway refusing anything still
	// pending.
	want := []string{
		"UpsertManagedCollector:" + testRegion + "/" + testNetwork + "/" + string(collectorownership.OriginOdigos),
		"IssueCollectorCredential:" + testNetwork,
		"MarkCollectorCredentialActive:" + cred.CredentialID + "@v1",
		"RecordCollectorIdentity:" + testRegion + "/" + cred.CollectorID,
	}
	if strings.Join(store.calls, "|") != strings.Join(want, "|") {
		t.Errorf("store calls =\n  %v\nwant\n  %v", store.calls, want)
	}
	// What reached the secret store is the COMPLETE Authorization header, because the Supervisor copies
	// ODIGOS_OPAMP_AUTHORIZATION into the header verbatim — a bare token arrives with no scheme and the
	// gateway refuses it.
	stored := secrets.secrets[CredentialSecretName(testConnector, testRegion)].values
	if len(stored) != 1 || !strings.HasPrefix(stored[0], "Bearer "+collectorcredential.TokenPrefix) {
		t.Fatalf("what reached the secret store is not a Bearer header carrying an Odigos Collector credential: %d version(s)", len(stored))
	}
	record, err := store.CollectorCredential(context.Background(), cred.CollectorID, cred.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != collectorcredential.StateActive {
		t.Errorf("state = %q, want active: the gateway refuses a pending credential", record.State)
	}
	if record.TokenSHA256 == "" || record.TokenSHA256 == collectorcredential.EmptyDigest {
		t.Errorf("token_sha256 = %q; the digest of the empty string reads as a successfully issued credential", record.TokenSHA256)
	}
	if record.SecretRef != in.SecretARN {
		t.Errorf("secret_ref = %q, want the secret's ARN", record.SecretRef)
	}
	if record.Binding.Region != testRegion || record.Binding.NetworkID != testNetwork ||
		record.Binding.ConnectorUID != testUID || record.Binding.AccountID != testAccount {
		t.Errorf("binding = %+v; the gateway matches the whole tuple", record.Binding)
	}
	if record.ExpiresAt != nil {
		t.Error("an expiry was issued. The Supervisor cannot replace the header in place, so the Collector would stop at a wall-clock time with no automatic cure")
	}
}

// TestASecondPassIssuesNothing is the idempotency requirement, and the reason it matters is not API
// calls: minting again is a rotation, a rotation is a redeploy, and a redeploy on every
// reconcile pass is every instrumented workload on the network churning for ever.
func TestASecondPassIssuesNothing(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	in := credentialInput(secrets, false)

	first, _, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil {
		t.Fatal(err)
	}

	// What the next pass sees: the row now names a live credential, and the secret holds a version.
	in.SecretHasValue = true
	store.calls = nil
	secrets.calls = nil

	second, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureCredential: %v / %v", err, problems)
	}
	if second.Issued {
		t.Fatal("the second pass minted a new credential, which rotates the Collector on every reconcile")
	}
	if second.CollectorID != first.CollectorID || second.CredentialID != first.CredentialID {
		t.Errorf("second pass = %+v, want the credential the first pass installed (%+v)", second, first)
	}
	if store.called("IssueCollectorCredential") {
		t.Error("issued again")
	}
	for _, c := range secrets.calls {
		if strings.HasPrefix(c, "PutSecretValue") {
			t.Errorf("wrote a new secret version on an unchanged pass: %v", secrets.calls)
		}
	}
	// Twenty more passes, because a churn bug that fires every hundredth pass is still a churn bug.
	for i := 0; i < 20; i++ {
		if c, _, err := EnsureCredential(context.Background(), store, secrets, in); err != nil || c.Issued {
			t.Fatalf("pass %d issued: %+v / %v", i, c, err)
		}
	}
}

// TestAValuelessSecretForcesAReissue. The paperwork says a credential is active and the secret holds
// nothing, so whatever was issued never reached the Collector — a half-finished earlier pass, or a
// secret restored from deletion. Trusting the row here leaves a Collector permanently unable to connect
// while everything reports as provisioned.
func TestAValuelessSecretForcesAReissue(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	store.withLiveCredential(testKey(), collectorcredential.StateActive)
	in := credentialInput(secrets, false)

	cred, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureCredential: %v / %v", err, problems)
	}
	if !cred.Issued {
		t.Fatal("did not reissue for a secret that holds nothing")
	}
}

// TestANonLiveCredentialForcesAReissue walks every state the gateway would refuse.
func TestANonLiveCredentialForcesAReissue(t *testing.T) {
	// Superseded counts as live — the rotation window exists so the outgoing credential keeps working
	// while old tasks drain — so it must NOT trigger a reissue here.
	for _, tc := range []struct {
		state     collectorcredential.State
		wantIssue bool
	}{
		{collectorcredential.StateActive, false},
		{collectorcredential.StateSuperseded, false},
		{collectorcredential.StateRevoked, true},
		{collectorcredential.StatePending, true},
	} {
		t.Run(string(tc.state), func(t *testing.T) {
			store, secrets := newFakeStore(), newFakeSecrets()
			store.withLiveCredential(testKey(), tc.state)
			in := credentialInput(secrets, true)

			cred, problems, err := EnsureCredential(context.Background(), store, secrets, in)
			if err != nil || len(problems) > 0 {
				t.Fatalf("EnsureCredential: %v / %v", err, problems)
			}
			if cred.Issued != tc.wantIssue {
				t.Errorf("issued = %v for state %q, want %v", cred.Issued, tc.state, tc.wantIssue)
			}
		})
	}
}

// TestAMissingCredentialRowForcesAReissue: the row names a credential the credentials table does not
// have — a restored-from-empty database, or a row deleted by hand. Nothing the gateway would accept, so
// there is nothing to preserve.
func TestAMissingCredentialRowForcesAReissue(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	store.withLiveCredential(testKey(), collectorcredential.StateActive)
	delete(store.credentials, "col_existing/cred_existing")
	in := credentialInput(secrets, true)

	cred, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureCredential: %v / %v", err, problems)
	}
	if !cred.Issued {
		t.Fatal("did not reissue for a credential the table does not hold")
	}
}

// TestAFailedSecretWriteLeavesTheCredentialPending. The gateway refuses a pending credential, so a
// value that never left the issuer cannot authenticate anything — and the row is LEFT rather than
// cleaned up, because deleting it would throw away the only record that it was issued at all.
func TestAFailedSecretWriteLeavesTheCredentialPending(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	in := credentialInput(secrets, false)
	secrets.putErr = errors.New("throttled")

	cred, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil {
		t.Fatalf("EnsureCredential: %v", err)
	}
	if cred.CollectorID != "" {
		t.Errorf("returned a usable credential after the write failed: %+v", cred)
	}
	if !collectorstatus.HasError(problems) || problems[0].Code != ProblemCredentialIssueFailed {
		t.Fatalf("problems = %v, want the issue-failed error", problems)
	}
	if store.called("MarkCollectorCredentialActive") {
		t.Error("activated a credential whose value never reached the secret store; the gateway would then accept it")
	}
	if store.called("RecordCollectorIdentity") {
		t.Error("recorded a Collector identity around a credential that was never installed")
	}
	// The next pass finds a pending credential and issues a fresh one.
	if _, err := store.GetManagedCollector(context.Background(), testKey()); err != nil {
		t.Errorf("the Collector row was rolled back: %v", err)
	}
}

// TestAFailedIssuanceProvisionsNothing. Refusing to mint is always safe — the Collector is not deployed
// yet — and it must not be reported as a Collector that exists.
func TestAFailedIssuanceProvisionsNothing(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	in := credentialInput(secrets, false)
	store.issueErr = errors.New("constraint violation")

	cred, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil {
		t.Fatalf("EnsureCredential: %v", err)
	}
	if cred.CollectorID != "" {
		t.Errorf("credential = %+v, want nothing", cred)
	}
	if !collectorstatus.HasError(problems) {
		t.Fatalf("problems = %v, want an error", problems)
	}
	if !strings.Contains(problems[0].Message, "inert") && !strings.Contains(problems[0].Message, "bootstrap") {
		t.Errorf("message does not say what deploying anyway would produce: %s", problems[0].Message)
	}
	for _, c := range secrets.calls {
		if strings.HasPrefix(c, "PutSecretValue") {
			t.Errorf("wrote a secret value with no credential issued: %v", secrets.calls)
		}
	}
}

// TestAnIncompleteBindingIsRefusedBeforeMinting. The gateway matches the whole tuple, so a credential
// missing one field authenticates a Collector whose identity was never established.
func TestAnIncompleteBindingIsRefusedBeforeMinting(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	in := credentialInput(secrets, false)
	in.Binding.AccountID = ""

	_, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil {
		t.Fatalf("EnsureCredential: %v", err)
	}
	if !collectorstatus.HasError(problems) {
		t.Fatalf("problems = %v, want an error", problems)
	}
	if store.called("IssueCollectorCredential") {
		t.Error("minted a credential on an incomplete binding")
	}
}

func TestManualModeIssuesNoCredential(t *testing.T) {
	store, secrets := newFakeStore(), newFakeSecrets()
	in := credentialInput(secrets, false)
	in.Mode = spec.ModeManual

	cred, problems, err := EnsureCredential(context.Background(), store, secrets, in)
	if err != nil {
		t.Fatalf("EnsureCredential: %v", err)
	}
	if cred.CollectorID != "" || len(store.calls) != 0 {
		t.Errorf("issued in manual mode: %+v / %v", cred, store.calls)
	}
	if len(problems) != 1 || problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", problems)
	}
}

func TestBindingLeavesTheCollectorIDToTheStore(t *testing.T) {
	b := BindingFor(testUID, "aws", testAccount, testKey(), testNetwork)
	if b.CollectorID != "" {
		t.Error("BindingFor invents a Collector identity; the store allocates one so only one place mints identities")
	}
	if b.Complete() {
		t.Error("a binding with no Collector id reports complete")
	}
}
