package deployer

import (
	"context"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// fakeClients hands out one fake per service, so a test can inspect every call the whole flow made.
type fakeClients struct {
	logs    *fakeLogs
	secrets *fakeSecrets
}

func newFakeClients() *fakeClients {
	return &fakeClients{logs: newFakeLogs(), secrets: newFakeSecrets()}
}

func (f *fakeClients) Logs(context.Context, string) (LogsClient, error) {
	return f.logs, nil
}
func (f *fakeClients) Secrets(context.Context, string) (SecretsClient, error) {
	return f.secrets, nil
}

const testLicense = "eyJhbGciOiJSUzI1NiJ9.licence-token-payload.signature"

func testProvisioner(clients *fakeClients, store *fakeStore, license string) *Provisioner {
	return &Provisioner{
		Identity:  testIdentity(),
		Provider:  "aws",
		AccountID: testAccount,
		License:   collectorcredential.NewSecret(license),
		Store:     store,
		Clients:   clients,
	}
}

func provisionOnce(t *testing.T, p *Provisioner, in SharedInput) (Shared, Prerequisites) {
	t.Helper()
	shared, err := p.EnsureShared(context.Background(), in)
	if err != nil {
		t.Fatalf("EnsureShared: %v", err)
	}
	prereq, err := p.EnsureNetwork(context.Background(), NetworkInput{
		Key: testKey(), HostNetworkID: testNetwork, Kind: testKind, Mode: in.Mode, Shared: shared, RetentionDays: 7,
		ExecutionRoleARN: "arn:aws:iam::" + testAccount + ":role/OdigosCollectorExecutionRole",
	})
	if err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	return shared, prereq
}

func sharedInput() SharedInput {
	return SharedInput{Regions: []string{testRegion}, Mode: spec.ModeAutomatic}
}

// TestProvisionEndToEnd walks the whole thing: a validated customer role, a log group, two secrets,
// and one credential.
func TestProvisionEndToEnd(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)

	shared, prereq := provisionOnce(t, p, sharedInput())
	if !shared.Ready() {
		t.Fatalf("shared = %+v", shared)
	}
	if !prereq.Ready() {
		t.Fatalf("prerequisites are not ready: %+v", prereq.Problems)
	}

	if prereq.LogGroup != LogGroupName(testConnector) {
		t.Errorf("log group = %q", prereq.LogGroup)
	}
	// The two secrets stay separate: different issuers, lifetimes and revocation authorities, so one
	// holding both would make every credential rotation rewrite the licence.
	if problems := collectorimage.DeliveryProblems(prereq.Delivery); len(problems) > 0 {
		t.Fatalf("delivery = %+v: %v", prereq.Delivery, problems)
	}
	if !strings.Contains(prereq.Delivery.LicenseSecretRef, "/license") {
		t.Errorf("licence ref = %q", prereq.Delivery.LicenseSecretRef)
	}
	if !strings.Contains(prereq.Delivery.CredentialSecretRef, "/"+testRegion+"/") || strings.Contains(prereq.Delivery.CredentialSecretRef, testNetwork) {
		t.Errorf("credential ref = %q", prereq.Delivery.CredentialSecretRef)
	}
	if !prereq.Credential.Issued {
		t.Error("no credential was issued")
	}

	// The licence value that reached the customer's account is the connector's own.
	license := clients.secrets.secrets[LicenseSecretName(testConnector, testRegion)]
	if len(license.values) != 1 || license.values[0] != testLicense {
		t.Errorf("the licence secret holds %d version(s); want this install's own token", len(license.values))
	}
	if license.tags[TagLicenseFingerprint] == "" {
		t.Error("no licence fingerprint was recorded, so the next pass would rewrite the value")
	}
}

// TestASecondPassWritesNothing is the acceptance criterion that matters most in a reconcile loop. The
// first pass creates; every pass after it must converge to silence — no new secret version, no new
// credential, no new role.
func TestASecondPassWritesNothing(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	provisionOnce(t, p, sharedInput())

	clients.logs.calls, clients.secrets.calls, store.calls = nil, nil, nil

	for pass := 0; pass < 5; pass++ {
		_, prereq := provisionOnce(t, p, sharedInput())
		if !prereq.Ready() {
			t.Fatalf("pass %d is not ready: %v", pass, prereq.Problems)
		}
		if prereq.Credential.Issued {
			t.Fatalf("pass %d issued a new credential, which rotates the Collector every reconcile", pass)
		}
	}

	// Writes that would each be a real cost: a new secret version or credential minted.
	for _, call := range append(clients.secrets.calls, store.calls...) {
		for _, forbidden := range []string{"PutSecretValue", "CreateSecret:", "IssueCollectorCredential", "RestoreSecret"} {
			if strings.HasPrefix(call, forbidden) {
				t.Errorf("a steady-state pass called %s", call)
			}
		}
	}
	// One version of each secret, five passes later.
	for name, s := range clients.secrets.secrets {
		if len(s.values) > 1 {
			t.Errorf("secret %s has %d versions after five steady passes", name, len(s.values))
		}
	}
}

// TestARenewedLicenceIsRewrittenOnce. the licence expires and is renewed outside Odigos, so a new
// value has to reach the customer's account — and exactly once, not once per pass, which is what the
// fingerprint tag is for.
func TestARenewedLicenceIsRewrittenOnce(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	provisionOnce(t, p, sharedInput())

	p.License = collectorcredential.NewSecret("renewed." + testLicense)
	for pass := 0; pass < 3; pass++ {
		provisionOnce(t, p, sharedInput())
	}

	license := clients.secrets.secrets[LicenseSecretName(testConnector, testRegion)]
	if len(license.values) != 2 {
		t.Fatalf("the licence secret has %d versions, want 2: the original and one rewrite", len(license.values))
	}
	if license.values[1] != "renewed."+testLicense {
		t.Errorf("the renewed licence did not reach the secret store")
	}
}

// TestNoLicenceTokenRefusesRatherThanDeploying. The Collector's enterprise auth extension refuses to
// start without a valid token, so deploying anyway produces a crash loop the Supervisor's config
// rollback cannot rescue — nothing is wrong with the configuration.
func TestNoLicenceTokenRefusesRatherThanDeploying(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, "")

	_, prereq := provisionOnce(t, p, sharedInput())
	if prereq.Ready() {
		t.Fatal("reported ready with no licence token; the Collector would crash-loop")
	}
	if prereq.Delivery.LicenseSecretRef != "" {
		t.Error("published a licence secret ref with nothing in the secret")
	}
	var codes []string
	for _, problem := range prereq.Problems {
		codes = append(codes, problem.Code)
	}
	if !containsString(codes, ProblemLicenseTokenMissing) {
		t.Fatalf("problems = %v, want the missing-token cause", codes)
	}
	// The cause reads before the effect: the connector holding no token, then the delivery being
	// unusable because of it.
	if i, j := indexOfString(codes, ProblemLicenseTokenMissing), indexOfString(codes, "LicenseSecretMissing"); j >= 0 && i > j {
		t.Errorf("problems = %v, want the cause before the consequence", codes)
	}
	// The message has to name where the token comes from, because the fix is in the cluster and not in
	// AWS at all.
	for _, problem := range prereq.Problems {
		if problem.Code == ProblemLicenseTokenMissing && !strings.Contains(problem.Message, "odigos-pro") {
			t.Errorf("message does not name the source of the token: %s", problem.Message)
		}
	}
	// A credential is still installed: it is the licence that is missing, and reporting one cause is
	// better than two.
	if !prereq.Credential.Issued {
		t.Error("no credential was issued; the two secrets are independent by design")
	}
}

// TestMissingExecutionRoleStopsEverythingElse. Provisioning secrets without a task execution role that
// can read them would leave objects behind for a Collector that can never start.
func TestMissingExecutionRoleStopsEverythingElse(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)

	shared, err := p.EnsureShared(context.Background(), sharedInput())
	if err != nil {
		t.Fatalf("EnsureShared: %v", err)
	}
	prereq, err := p.EnsureNetwork(context.Background(), NetworkInput{
		Key: testKey(), HostNetworkID: testNetwork, Kind: testKind, Mode: spec.ModeAutomatic,
		Shared: shared, RetentionDays: 7,
	})
	if err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	if prereq.Ready() {
		t.Fatal("prerequisites report ready with no execution role")
	}
	if len(clients.secrets.calls) != 0 {
		t.Errorf("provisioned secrets with no usable identity to read them: %v", clients.secrets.calls)
	}
	if len(store.calls) != 0 {
		t.Errorf("touched the database with no usable identity: %v", store.calls)
	}
	if !collectorstatus.HasError(prereq.Problems) {
		t.Errorf("problems = %v, want an error", prereq.Problems)
	}
}

// TestNoRegionsProvisionsNothingAndSaysNothing. There is no Collector anywhere, and the reason belongs
// to placement, which has already said it.
func TestNoRegionsProvisionsNothingAndSaysNothing(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)

	shared, err := p.EnsureShared(context.Background(), SharedInput{Mode: spec.ModeAutomatic})
	if err != nil {
		t.Fatalf("EnsureShared: %v", err)
	}
	if shared.Ready() || len(shared.Problems) != 0 {
		t.Errorf("shared = %+v, want nothing and no complaint", shared)
	}
}

// TestThePartitionComesFromTheRegions, so a GovCloud or China account gets policies whose resource ARNs
// actually match something. A hardcoded partition does not fail — it produces a valid policy that
// matches nothing, and the task then fails on the secret rather than on the policy.
func TestThePartitionComesFromTheRegions(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)

	shared, err := p.EnsureShared(context.Background(), SharedInput{
		Regions: []string{"us-gov-east-1", "us-gov-west-1"}, Mode: spec.ModeAutomatic,
	})
	if err != nil {
		t.Fatalf("EnsureShared: %v", err)
	}
	if shared.Partition != "aws-us-gov" {
		t.Fatalf("partition = %q, want aws-us-gov", shared.Partition)
	}
}

// TestManualModeProvisionsNothingAtAll over the whole flow, not just per function.
func TestManualModeProvisionsNothingAtAll(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)

	in := sharedInput()
	in.Mode = spec.ModeManual
	shared, prereq := provisionOnce(t, p, in)

	if shared.Ready() || prereq.Ready() {
		t.Fatal("provisioned in manual mode")
	}
	for _, calls := range [][]string{clients.logs.calls, clients.secrets.calls, store.calls} {
		if len(calls) != 0 {
			t.Errorf("manual mode reached a writer: %v", calls)
		}
	}
}

func containsString(xs []string, want string) bool { return indexOfString(xs, want) >= 0 }

func indexOfString(xs []string, want string) int {
	for i, x := range xs {
		if x == want {
			return i
		}
	}
	return -1
}
