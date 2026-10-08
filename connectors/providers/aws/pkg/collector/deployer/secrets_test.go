package deployer

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"

	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"
)

func credentialSecretInput() SecretInput {
	return SecretInput{
		Name:        CredentialSecretName(testConnector, testRegion),
		Description: "test",
		Connector:   testConnector,
		ConnectorID: testUID,
		Region:      testRegion,
		NetworkID:   testNetwork,
		Identity:    testIdentity(),
		Mode:        spec.ModeAutomatic,
	}
}

// TestASecretIsCreatedEmpty is the ordering the rest of the credential flow depends on: a credential
// row records where its value WILL live, so the ARN has to exist before anything is minted. It is also
// what keeps the number of places a value can leave this process at one.
func TestASecretIsCreatedEmpty(t *testing.T) {
	api := newFakeSecrets()
	in := credentialSecretInput()

	state, problems, err := EnsureSecret(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecret: %v / %v", err, problems)
	}
	if state.ARN == "" {
		t.Fatal("no ARN returned")
	}
	if state.HasValue {
		t.Error("a freshly created secret reports a value")
	}
	for _, c := range api.calls {
		if strings.HasPrefix(c, "CreateSecretWithValue:") {
			t.Fatalf("CreateSecret carried a value; PutSecretValue must be the only way a value leaves this package: %v", api.calls)
		}
	}
	parsed := baseapi.ParseCollectorTags(api.secrets[in.Name].tags)
	if !parsed.IsCollector() || !parsed.HasOwnershipMarker() || parsed.Region != testRegion || parsed.Network != testNetwork {
		t.Errorf("secret tags = %v; a credential secret is regional with host metadata", api.secrets[in.Name].tags)
	}
}

func TestTheLicenceSecretCarriesNoNetworkTag(t *testing.T) {
	api := newFakeSecrets()
	name := LicenseSecretName(testConnector, testRegion)
	if _, problems, err := EnsureSecret(context.Background(), api, SecretInput{
		Name: name, Connector: testConnector, ConnectorID: testUID,
		Region:   testRegion,
		Identity: testIdentity(), Mode: spec.ModeAutomatic,
	}); err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecret: %v / %v", err, problems)
	}
	if _, present := api.secrets[name].tags[baseapi.TagNetwork]; present {
		t.Error("the licence secret is tagged with a network; one serves every Collector in the region")
	}
}

func TestAnExistingSecretIsReusedAndRetagged(t *testing.T) {
	in := credentialSecretInput()
	// Tags stripped down to what still attributes it, as a customer poking at tags would leave it.
	api := newFakeSecrets().withSecret(in.Name, map[string]string{
		baseapi.TagComponent:   baseapi.ComponentCollector,
		baseapi.TagConnectorID: testUID,
	}, true)

	state, problems, err := EnsureSecret(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecret: %v / %v", err, problems)
	}
	if !state.HasValue {
		t.Error("an existing secret with a version reports no value")
	}
	for _, c := range api.calls {
		if strings.HasPrefix(c, "CreateSecret:") {
			t.Errorf("recreated an existing secret: %v", api.calls)
		}
	}
	if !baseapi.ParseCollectorTags(api.secrets[in.Name].tags).HasOwnershipMarker() {
		t.Error("the ownership marker was not restored, so this secret could never be deleted at teardown")
	}
}

// TestASecretThatIsNotOursIsNeverWritten. Overwriting a secret Odigos does not own destroys whatever
// the customer kept in it, and nothing about the name looking right makes that recoverable.
func TestASecretThatIsNotOursIsNeverWritten(t *testing.T) {
	in := credentialSecretInput()
	api := newFakeSecrets().withSecret(in.Name, map[string]string{"owner": "platform-team"}, true)

	state, problems, err := EnsureSecret(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureSecret: %v", err)
	}
	if state.ARN != "" {
		t.Error("returned the ARN of a secret Odigos does not own")
	}
	if !collectorstatus.HasError(problems) || problems[0].Code != ProblemObjectNotOurs {
		t.Fatalf("problems = %v, want the not-ours refusal", problems)
	}
	for _, c := range api.calls {
		if strings.HasPrefix(c, "TagResource:") || strings.HasPrefix(c, "PutSecretValue:") {
			t.Errorf("wrote to a secret Odigos does not own: %v", api.calls)
		}
	}
}

// TestASecretScheduledForDeletionIsRestored. Deleting a secret only SCHEDULES it and its name stays
// reserved for up to 30 days, so without this a Collector torn down last week could not be
// re-provisioned for a month — and the error would name a secret that no longer appears in the console.
func TestASecretScheduledForDeletionIsRestored(t *testing.T) {
	in := credentialSecretInput()
	api := newFakeSecrets().withSecret(in.Name, ourTags(testNetwork), true)
	deleted := time.Now().Add(-time.Hour)
	api.secrets[in.Name].deletedDate = &deleted

	state, problems, err := EnsureSecret(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecret: %v / %v", err, problems)
	}
	if !state.Restored {
		t.Error("the secret was not reported as restored")
	}
	if api.secrets[in.Name].deletedDate != nil {
		t.Error("RestoreSecret was not called")
	}
	// A restored secret keeps its old versions, and the credential in them was revoked at teardown. So
	// it has to report as valueless, or the next step would deploy a Collector authenticating with
	// something the gateway now refuses.
	if state.HasValue {
		t.Error("a restored secret reports a usable value; its old credential was revoked at teardown")
	}
}

// TestACreateBlockedByAScheduledDeletionIsResolvedByRereading. CreateSecret reports that case as a
// generic InvalidRequest with no dedicated type, so it is answered by describing the secret rather than
// by matching on an error message.
func TestACreateBlockedByAScheduledDeletionIsResolvedByRereading(t *testing.T) {
	in := credentialSecretInput()
	api := newFakeSecrets()
	api.createErr = &smtypes.InvalidRequestException{}
	// The name is held by a secret that DescribeSecret cannot see yet in this fake's first call, so seed
	// it as present-but-deleted, which is what the real API shows.
	api.withSecret(in.Name, ourTags(testNetwork), true)
	deleted := time.Now().Add(-time.Hour)
	api.secrets[in.Name].deletedDate = &deleted

	state, problems, err := EnsureSecret(context.Background(), api, in)
	if err != nil || len(problems) > 0 {
		t.Fatalf("EnsureSecret: %v / %v", err, problems)
	}
	if !state.Restored || state.ARN == "" {
		t.Fatalf("state = %+v, want a restored secret", state)
	}
}

// TestPutSecretValueRefusesAnEmptyValue. An empty version is a Collector that starts, sends an empty
// Authorization header, and is reported as "deployed but never connected" — the one failure that says
// the connector cannot diagnose, because it does not read the secret back.
func TestPutSecretValueRefusesAnEmptyValue(t *testing.T) {
	api := newFakeSecrets().withSecret("s", ourTags(testNetwork), false)
	version, err := PutSecretValue(context.Background(), api, api.secrets["s"].arn, collectorcredential.Secret{})
	if err == nil {
		t.Fatalf("wrote an empty value, version %q", version)
	}
	if len(api.calls) != 0 {
		t.Errorf("called AWS with an empty value: %v", api.calls)
	}
	if !strings.Contains(err.Error(), "never connected") {
		t.Errorf("error does not say what the failure looks like: %v", err)
	}
}

func TestPutSecretValueWritesTheValueAndReturnsItsVersion(t *testing.T) {
	api := newFakeSecrets().withSecret("s", ourTags(testNetwork), false)
	minted, err := collectorcredential.Mint()
	if err != nil {
		t.Fatal(err)
	}

	version, err := PutSecretValue(context.Background(), api, api.secrets["s"].arn, minted.Secret)
	if err != nil {
		t.Fatalf("PutSecretValue: %v", err)
	}
	if version == "" {
		t.Error("no version returned; without it the credential cannot be recorded as active")
	}
	stored := api.secrets["s"].values
	if len(stored) != 1 || stored[0] != minted.Secret.Reveal() {
		t.Fatal("the value that reached the secret store is not the one that was minted")
	}
	// The credential in the store must be the one whose hash was persisted, or the gateway can never
	// authenticate it — reported as "deployed but never connected", with nothing to diagnose it.
	rehashed := collectorcredential.NewSecret(stored[0])
	hash, err := rehashed.Hash()
	if err != nil || hash != minted.Hash {
		t.Errorf("the stored value does not hash to the persisted verifier: %v", err)
	}
}

func TestPutSecretValueSurfacesAFailure(t *testing.T) {
	api := newFakeSecrets().withSecret("s", ourTags(testNetwork), false)
	api.putErr = errors.New("throttled")
	minted, _ := collectorcredential.Mint()

	if _, err := PutSecretValue(context.Background(), api, api.secrets["s"].arn, minted.Secret); err == nil {
		t.Fatal("no error")
	} else if strings.Contains(err.Error(), minted.Secret.Reveal()) {
		t.Fatal("the error message carries the credential value")
	}
}

func TestManualModeProvisionsNoSecret(t *testing.T) {
	in := credentialSecretInput()
	in.Mode = spec.ModeManual
	api := newFakeSecrets()

	state, problems, err := EnsureSecret(context.Background(), api, in)
	if err != nil {
		t.Fatalf("EnsureSecret: %v", err)
	}
	if state.ARN != "" || len(api.calls) != 0 {
		t.Errorf("provisioned a secret in manual mode: %+v / %v", state, api.calls)
	}
	if len(problems) != 1 || problems[0].Code != ProblemManualModeProvisioning {
		t.Errorf("problems = %v", problems)
	}
}
