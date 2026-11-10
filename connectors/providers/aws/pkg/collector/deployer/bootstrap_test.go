package deployer

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// taskDefinitionInputWith is the workload input for a delivery a provisioning pass produced, so the
// definition under test references the secrets that pass actually installed.
func taskDefinitionInputWith(prereq Prerequisites) TaskDefinitionInput {
	in := taskDefinitionInput()
	in.Delivery = prereq.Delivery
	return in
}

// bootstrapSecret returns the cold-start secret this connector would have created for the test network.
func bootstrapSecret(clients *fakeClients) *fakeSecret {
	return clients.secrets.secrets[BootstrapSecretName(testConnector, testRegion)]
}

// TestTheColdStartConfigurationIsInstalledAsASecret. its mitigation: the connector already renders
// the configuration it would push, so it hands the same document over at deploy time and a cold start
// during an outage comes up serving the last configuration Odigos published instead of nothing.
func TestTheColdStartConfigurationIsInstalledAsASecret(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	config := testConfigDocument()

	_, prereq := provisionOnceWith(t, p, sharedInput(), config)
	if !prereq.Ready() {
		t.Fatalf("prerequisites are not ready: %v", prereq.Problems)
	}
	if prereq.Delivery.BootstrapSecretRef == "" {
		t.Fatalf("no cold-start configuration was installed: %v", clients.secrets.calls)
	}

	secret := bootstrapSecret(clients)
	if secret == nil {
		t.Fatalf("no secret at %s: %v",
			BootstrapSecretName(testConnector, testRegion), clients.secrets.calls)
	}
	if len(secret.values) != 1 {
		t.Fatalf("the secret holds %d version(s), want 1", len(secret.values))
	}

	// Base64 of exactly the document that would be pushed — the same bytes, or a cold start serves a
	// configuration that was never published and the difference is invisible.
	decoded, err := base64.StdEncoding.DecodeString(secret.values[0])
	if err != nil {
		t.Fatalf("what reached the secret store is not base64: %v", err)
	}
	if string(decoded) != config.YAML {
		t.Errorf("the installed document is not the rendered one:\n%s", decoded)
	}
	// The CONFIGURATION's hash, so a reader can tell whether what a cold start would serve is what
	// Odigos last pushed — it is the same value managed_collectors.config_hash records.
	if got := secret.tags[TagBootstrapFingerprint]; got != config.Hash {
		t.Errorf("fingerprint = %q, want the configuration hash %q", got, config.Hash)
	}
}

// TestTheColdStartConfigurationIsNeverAPlainEnvironmentValue — the "no plaintext exporter secret"
// criterion. A Collector configuration carries its exporters' credentials, and on ECS every holder of
// ecs:DescribeTaskDefinition can read a plain environment value: a far wider set of people than those
// who can read a secret.
func TestTheColdStartConfigurationIsNeverAPlainEnvironmentValue(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	config := testConfigDocument()
	_, prereq := provisionOnceWith(t, p, sharedInput(), config)

	spec, problems := RenderTaskDefinition(taskDefinitionInputWith(prereq))
	if collectorstatus.HasError(problems) {
		t.Fatalf("render: %v", codes(problems))
	}
	container := spec.Containers[0]

	if got := container.Secrets[collectorimage.EnvBootstrapConfig]; got != prereq.Delivery.BootstrapSecretRef {
		t.Errorf("%s is populated from %q, want the secret ref %q",
			collectorimage.EnvBootstrapConfig, got, prereq.Delivery.BootstrapSecretRef)
	}
	if _, plain := container.Environment[collectorimage.EnvBootstrapConfig]; plain {
		t.Error("the cold-start configuration is a plain environment value")
	}
	// The shared check, which is the one the published artifacts run too.
	if problems := collectorimage.EnvProblems(container.Environment); len(problems) > 0 {
		t.Errorf("EnvProblems = %v", codes(problems))
	}

	// And nothing anywhere in the rendered definition carries the destination's own credential — not the
	// environment, not the log options, not the command.
	rendered := renderedStrings(spec)
	if strings.Contains(rendered, testDestinationCredential) {
		t.Error("the destination credential appears in the task definition")
	}
	if strings.Contains(rendered, config.YAML) || strings.Contains(rendered, base64of(config.YAML)) {
		t.Error("the rendered configuration appears in the task definition")
	}
}

// TestAChangedConfigurationIsRewrittenOnce. A destination change has to reach the cold-start document,
// or a replaced task during an outage comes up exporting to somewhere the customer stopped using — and
// exactly once, not once per pass, which is what the fingerprint tag is for.
func TestAChangedConfigurationIsRewrittenOnce(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	provisionOnceWith(t, p, sharedInput(), testConfigDocument())

	changed := collectorconfig.Document{
		YAML: strings.Replace(testConfigDocument().YAML, "ingest.example.test", "ingest2.example.test", 1),
		Hash: "second-configuration-hash",
	}
	for pass := 0; pass < 3; pass++ {
		provisionOnceWith(t, p, sharedInput(), changed)
	}

	secret := bootstrapSecret(clients)
	if len(secret.values) != 2 {
		t.Fatalf("the secret has %d versions, want 2: the original and one rewrite", len(secret.values))
	}
	decoded, err := base64.StdEncoding.DecodeString(secret.values[1])
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) != changed.YAML {
		t.Errorf("the changed configuration did not reach the secret store:\n%s", decoded)
	}
	if got := secret.tags[TagBootstrapFingerprint]; got != changed.Hash {
		t.Errorf("fingerprint = %q, want %q", got, changed.Hash)
	}
}

// TestNoRenderedConfigurationInstallsNothing. Without a document a cold start serves the inert bundled
// configuration, which is v1's documented behaviour and not a fault — so nothing is created, the
// ref is withheld, and the Collector is still perfectly deployable.
func TestNoRenderedConfigurationInstallsNothing(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)

	_, prereq := provisionOnceWith(t, p, sharedInput(), collectorconfig.Document{})
	if !prereq.Ready() {
		t.Fatalf("a Collector with no cold-start document is not deployable: %v", prereq.Problems)
	}
	if prereq.Delivery.BootstrapSecretRef != "" {
		t.Errorf("a ref was published with no document: %q", prereq.Delivery.BootstrapSecretRef)
	}
	if bootstrapSecret(clients) != nil {
		t.Error("an empty secret was created; it would describe a mitigation nobody can use")
	}
	for _, call := range clients.secrets.calls {
		if strings.Contains(call, bootstrapSecretLeaf) {
			t.Errorf("the bootstrap secret was touched with no document: %s", call)
		}
	}

	// And the task definition references only the two secrets a Collector cannot run without. An entry
	// pointing at a secret with no version fails the TASK at start with a ResourceInitializationError —
	// the optional mitigation breaking the thing it exists to protect.
	spec, problems := RenderTaskDefinition(taskDefinitionInputWith(prereq))
	if collectorstatus.HasError(problems) {
		t.Fatalf("render: %v", codes(problems))
	}
	secrets := spec.Containers[0].Secrets
	if len(secrets) != 2 {
		t.Errorf("secrets = %v, want only the credential and the licence", secrets)
	}
	if _, present := secrets[collectorimage.EnvBootstrapConfig]; present {
		t.Error("the task definition references a bootstrap secret that holds nothing")
	}
}

// TestTheColdStartSecretIsItsOwn. Its lifetime is the configuration's — rewritten whenever a destination
// changes — while the licence is renewed yearly and the credential is rotated on demand. Sharing would
// make a destination change overwrite a credential, and the Collector would then present a YAML document
// as its Authorization header.
func TestTheColdStartSecretIsItsOwn(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	_, prereq := provisionOnceWith(t, p, sharedInput(), testConfigDocument())

	refs := map[string]string{
		"licence":    prereq.Delivery.LicenseSecretRef,
		"credential": prereq.Delivery.CredentialSecretRef,
		"bootstrap":  prereq.Delivery.BootstrapSecretRef,
	}
	seen := map[string]string{}
	for name, ref := range refs {
		if ref == "" {
			t.Fatalf("the %s ref is empty", name)
		}
		if other, clash := seen[ref]; clash {
			t.Errorf("the %s and %s refs are the same secret", name, other)
		}
		seen[ref] = name
	}
	if problems := collectorimage.DeliveryProblems(prereq.Delivery); len(problems) > 0 {
		t.Errorf("DeliveryProblems = %v", codes(problems))
	}
	// Under the same prefix the execution role is already scoped to, which is why this needs no policy
	// change — and one level below the credential's own name rather than beside it.
	credential := CredentialSecretName(testConnector, testRegion)
	bootstrap := BootstrapSecretName(testConnector, testRegion)
	if !strings.HasPrefix(bootstrap, credential+"/") {
		t.Errorf("bootstrap secret name = %q, want it under %q", bootstrap, credential)
	}
	if !strings.HasPrefix(bootstrap, secretPrefix+testConnector+"/") {
		t.Errorf("bootstrap secret name = %q, want it under the connector's prefix", bootstrap)
	}
}

// TestAColdStartSecretThatIsNotOursIsRefusedWithoutCostingTheRest. The bootstrap document is the only
// optional half of a Collector's delivery, so a refusal here must leave the two secrets it cannot run
// without exactly as they were — a Collector that stops deploying because a mitigation could not be
// installed is worse than one without the mitigation.
func TestAColdStartSecretThatIsNotOursIsRefusedWithoutCostingTheRest(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	// A secret already holding that name, belonging to the customer.
	clients.secrets.withSecret(BootstrapSecretName(testConnector, testRegion),
		map[string]string{"team": "platform"}, true)
	p := testProvisioner(clients, store, testLicense)

	_, prereq := provisionOnceWith(t, p, sharedInput(), testConfigDocument())

	if prereq.Delivery.LicenseSecretRef == "" || prereq.Delivery.CredentialSecretRef == "" {
		t.Fatalf("a refused cold-start secret cost the Collector its credentials: %+v", prereq.Delivery)
	}
	if prereq.Delivery.BootstrapSecretRef != "" {
		t.Error("a secret that is not Odigos's was published as the bootstrap ref")
	}
	if !hasCode(prereq.Problems, ProblemObjectNotOurs) {
		t.Errorf("problems = %v, want the object reported", codes(prereq.Problems))
	}
	// Nothing was written into it. Overwriting a secret that is not ours destroys whatever the customer
	// kept there, and no amount of "the name looked like ours" makes that recoverable.
	secret := bootstrapSecret(clients)
	if len(secret.values) != 0 {
		t.Errorf("the customer's secret was written to: %d version(s)", len(secret.values))
	}
}

// TestTheColdStartDocumentNeverReachesALogOrTheStore. It carries the destination's own credential, so it
// is subject to the same rule as the licence and the OpAMP credential: values live between the render and
// the secret-store write and nowhere else.
func TestTheColdStartDocumentNeverReachesALogOrTheStore(t *testing.T) {
	var buf bytes.Buffer
	restore := captureLogs(&buf)
	defer restore()

	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	config := testConfigDocument()
	provisionOnceWith(t, p, sharedInput(), config)

	logged := buf.String()
	if logged == "" {
		t.Fatal("nothing was logged, so this test proved nothing about what is logged")
	}
	for name, value := range map[string]string{
		"destination credential": testDestinationCredential,
		"rendered document":      config.YAML,
		"encoded document":       base64of(config.YAML),
	} {
		if strings.Contains(logged, value) {
			t.Errorf("the %s was logged", name)
		}
	}
	// The configuration HASH is expected: it is what correlates a log line with the row's config_hash,
	// and a hash of a document is not the document.
	if !strings.Contains(logged, config.Hash) {
		t.Errorf("the configuration hash was not logged, so nothing correlates the installed document "+
			"with its row: %s", logged)
	}

	for _, recorded := range store.calls {
		if strings.Contains(recorded, testDestinationCredential) || strings.Contains(recorded, base64of(config.YAML)) {
			t.Errorf("the cold-start document reached the database: %s", recorded)
		}
	}
}

// TestTheEncodedDocumentIsOnlyRevealedWhereEverySecretIs. One call site reveals a value in this package,
// and adding a second would make "where can a credential leave this process" two answers instead of one.
func TestTheEncodedDocumentIsOnlyRevealedWhereEverySecretIs(t *testing.T) {
	src, err := packageSourceFile("provision.go")
	if err != nil {
		t.Fatalf("read provision.go: %v", err)
	}
	if strings.Contains(src, "EncodeBootstrapConfig(in.Config.YAML).Reveal()") ||
		strings.Contains(src, "encoded.Reveal()") {
		t.Error("the cold-start document is revealed outside PutSecretValue")
	}
	if !strings.Contains(src, "PutSecretValue(ctx, api, state.ARN, encoded)") {
		t.Error("the cold-start document does not travel to the secret store as a wrapped value")
	}
}

func base64of(document string) string {
	return base64.StdEncoding.EncodeToString([]byte(document))
}

// renderedStrings flattens everything about a container definition a reader of the task definition
// could see, so one assertion covers the environment, the command and the log options together.
func renderedStrings(spec TaskDefinitionSpec) string {
	var out strings.Builder
	for _, container := range spec.Containers {
		out.WriteString(container.Image)
		out.WriteString(strings.Join(container.Command, " "))
		for name, value := range container.Environment {
			out.WriteString(name + "=" + value + "\n")
		}
		for name, value := range container.LogOptions {
			out.WriteString(name + "=" + value + "\n")
		}
	}
	return out.String()
}
