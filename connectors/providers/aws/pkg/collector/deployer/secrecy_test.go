package deployer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// This file is the acceptance criterion "no raw token in state or logs", turned into checks.
//
// It matters more than the usual secrecy hygiene because of what the OpAMP credential is worth: an
// authenticated OpAMP connection is answered with the Collector's desired configuration, and that
// configuration carries the destination's own credential. So a credential in a log line is the
// customer's destination credential one step away, in the one place that gets copied into support
// tickets.

// installedCredential returns what reached the secret store for one Collector, and the bare token
// inside it.
//
// The two differ on purpose: the secret holds the COMPLETE Authorization header, because the Supervisor
// copies ODIGOS_OPAMP_AUTHORIZATION into the header verbatim — while the verifier the connector
// persists is the hash of the bare token, which is what the gateway hashes after stripping the scheme.
func installedCredential(t *testing.T, secrets *fakeSecrets) (header, token string) {
	t.Helper()
	s := secrets.secrets[CredentialSecretName(testConnector, testRegion)]
	if s == nil || len(s.values) != 1 {
		t.Fatalf("expected exactly one credential version, got %v", s)
	}
	header = s.values[0]
	scheme, value, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		t.Fatalf("the installed credential is not a complete Authorization header: a bare token arrives with no scheme and the gateway refuses it")
	}
	return header, value
}

// packageSource returns every non-test .go file in this package.
func packageSource(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("no source files found; this test has stopped checking anything")
	}
	return out
}

// TestOnlyOneCallSiteRevealsASecret. collectorcredential.Secret closes every implicit path out — fmt
// renders "<redacted>", JSON and text marshalling fail — so Reveal is the only way a value escapes, and
// keeping it to one call site is what makes "where can a credential leave this process" a question with
// an answer.
//
// CreateSecret can also carry a value, and if it did there would be two. That is why EnsureSecret
// creates the secret empty and PutSecretValue is a separate function.
func TestOnlyOneCallSiteRevealsASecret(t *testing.T) {
	var sites []string
	for name, src := range packageSource(t) {
		for i, line := range strings.Split(src, "\n") {
			if strings.Contains(line, ".Reveal()") {
				sites = append(sites, fmt.Sprintf("%s:%d", name, i+1))
			}
		}
	}
	if len(sites) != 1 {
		t.Fatalf("Reveal is called at %d sites (%v), want exactly one — PutSecretValue in secrets.go", len(sites), sites)
	}
	if !strings.HasPrefix(sites[0], "secrets.go:") {
		t.Errorf("the only Reveal call site is %s, want it in secrets.go", sites[0])
	}
}

// TestNoLogCallCanCarryASecret. A Secret rendered through fmt says "<redacted>", so this is defence in
// depth rather than the only guard — but a log line is exactly where a value would end up if somebody
// reached past the type, and zap's reflected encoding would route a raw string straight through.
func TestNoLogCallCanCarryASecret(t *testing.T) {
	for name, src := range packageSource(t) {
		lines := strings.Split(src, "\n")
		for i, line := range lines {
			if !strings.HasPrefix(strings.TrimSpace(line), "log.") {
				continue
			}
			// The WHOLE call, not just its first line. A log call wraps across lines as soon as it has
			// a few key/value pairs, and every one of those pairs is a place a value could be passed —
			// checking the first line only would miss exactly the argument somebody would add.
			call, depth := "", 0
			for _, l := range lines[i:] {
				call += l
				depth += strings.Count(l, "(") - strings.Count(l, ")")
				if depth <= 0 {
					break
				}
			}
			for _, forbidden := range []string{".Reveal()", "SecretString", "p.License", "in.License", "Minted.Secret"} {
				if strings.Contains(call, forbidden) {
					t.Errorf("%s:%d logs %s:\n%s", name, i+1, forbidden, call)
				}
			}
		}
	}
}

// TestNoReturnedTypeCanHoldAValue. What this package hands back is what a caller stores, reports and
// logs, so the guarantee has to be structural: none of these types has a field a value could live in.
// collectorimage.Delivery is the same idea one layer down — refs only, no field for a value.
func TestNoReturnedTypeCanHoldAValue(t *testing.T) {
	secretType := reflect.TypeOf(collectorcredential.Secret{})
	for _, sample := range []any{
		Prerequisites{}, Credential{}, Shared{}, SecretState{},
		LogGroupInput{}, SecretInput{}, CredentialInput{}, NetworkInput{}, SharedInput{},
	} {
		typ := reflect.TypeOf(sample)
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.Type == secretType {
				t.Errorf("%s.%s holds a credential value; only Provisioner may, and only between reading it and writing it", typ.Name(), f.Name)
			}
			if f.Type.Kind() == reflect.String && strings.Contains(strings.ToLower(f.Name), "token") {
				t.Errorf("%s.%s is a bare string named like a token", typ.Name(), f.Name)
			}
		}
	}
}

// TestTheProvisionerRedactsItsLicence. The Provisioner does hold a value — it is the connector's own
// licence token, on its way into the customer's secret store — so the guarantee there is the type's:
// every implicit path out is closed, including the two that a struct dump and a JSON encoder take.
func TestTheProvisionerRedactsItsLicence(t *testing.T) {
	p := testProvisioner(newFakeClients(), newFakeStore(), testLicense)

	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		rendered := fmt.Sprintf(format, *p)
		if strings.Contains(rendered, testLicense) {
			t.Errorf("fmt %s renders the licence token: %s", format, rendered)
		}
		if !strings.Contains(rendered, "redacted") {
			t.Errorf("fmt %s does not show the value as redacted: %s", format, rendered)
		}
	}

	// Marshalling FAILS rather than emitting a placeholder, and that is the intended behaviour: a
	// "<redacted>" placeholder is what gets written to a secret store and produces a Collector
	// authenticating with the literal string.
	if b, err := json.Marshal(p); err == nil {
		t.Errorf("the Provisioner marshalled to JSON: %s", b)
	} else if !strings.Contains(err.Error(), "cannot be serialized") {
		t.Errorf("marshalling failed for the wrong reason: %v", err)
	}
}

// TestAFullProvisionLogsNoSecret runs the whole flow with logging captured and asserts that neither the
// minted OpAMP credential nor the licence token appears anywhere in it — including through zap's
// reflected encoding, which is the path that would carry a raw string straight to stdout.
func TestAFullProvisionLogsNoSecret(t *testing.T) {
	var buf bytes.Buffer
	restore := captureLogs(&buf)
	defer restore()

	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	provisionOnce(t, p, sharedInput())

	logged := buf.String()
	if logged == "" {
		t.Fatal("nothing was logged, so this test proved nothing about what is logged")
	}
	if strings.Contains(logged, testLicense) {
		t.Error("the licence token was logged")
	}

	// The credential is minted inside the flow, so it is read back out of the secret store to check the
	// exact value that exists in this run.
	header, token := installedCredential(t, clients.secrets)
	for name, value := range map[string]string{"header": header, "token": token} {
		if strings.Contains(logged, value) {
			t.Errorf("the OpAMP credential's %s was logged", name)
		}
		// Not even a prefix long enough to be useful. A token is high-entropy, so any substantial run of
		// it in a log line means the whole thing is there.
		if len(value) > 24 && strings.Contains(logged, value[:24]) {
			t.Errorf("a usable prefix of the OpAMP credential's %s was logged", name)
		}
	}

	// The fingerprint IS expected — it is what correlates a log line with the credential row and the
	// audit event, and eight hex characters of a SHA-256 are useless for anything else.
	fingerprint := collectorcredential.NewSecret(token).Fingerprint()
	if !strings.Contains(logged, fingerprint) {
		t.Errorf("the credential's fingerprint was not logged, so nothing correlates this installation with its row: %s", logged)
	}
}

// TestNoSecretReachesTheStore is the "state" half of the criterion: every string this package hands the
// database has to be a ref, an id or a hash — never a value.
func TestNoSecretReachesTheStore(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	provisionOnce(t, p, sharedInput())
	header, token := installedCredential(t, clients.secrets)

	for _, call := range store.calls {
		for _, value := range []string{header, token, testLicense} {
			if strings.Contains(call, value) {
				t.Errorf("a secret value reached the database: %s", call)
			}
		}
	}
	for _, record := range store.credentials {
		if record.TokenSHA256 == token || record.TokenSHA256 == header {
			t.Error("the credential VALUE was persisted where its hash belongs")
		}
		// The verifier is the hash of the BARE token, because that is what the gateway hashes after
		// stripping the scheme. Hashing the header instead would produce a credential that authenticates
		// nothing and a Collector reported as deployed but never connected.
		hash, err := collectorcredential.NewSecret(token).Hash()
		if err != nil || record.TokenSHA256 != hash {
			t.Errorf("token_sha256 does not verify the token inside the installed header: %v", err)
		}
	}
	for _, row := range store.rows {
		if strings.Contains(row.CredentialRef, token) {
			t.Error("credential_ref carries the value rather than the secret it lives in")
		}
	}
}

// TestTheSecretStoreIsWriteOnly. The connector holds no GetSecretValue at all, so no code path here can
// be made to return a credential — which is what makes the one-time reveal at issuance structural
// rather than a policy somebody could relax.
func TestTheSecretStoreIsWriteOnly(t *testing.T) {
	typ := reflect.TypeOf((*SecretsClient)(nil)).Elem()
	for i := 0; i < typ.NumMethod(); i++ {
		if name := typ.Method(i).Name; strings.Contains(name, "GetSecretValue") {
			t.Errorf("SecretsClient declares %s; the connector never reads a secret back", name)
		}
	}
	// And the credential store has no accessor that could return one either.
	storeType := reflect.TypeOf((*CredentialStore)(nil)).Elem()
	secretType := reflect.TypeOf(collectorcredential.Secret{})
	for i := 0; i < storeType.NumMethod(); i++ {
		m := storeType.Method(i)
		for j := 0; j < m.Type.NumOut(); j++ {
			if m.Type.Out(j) == secretType {
				t.Errorf("CredentialStore.%s returns a credential value", m.Name)
			}
		}
	}
}

// TestManualModeNeverMintsAnything, which is the strongest form of "no token": in manual mode the
// customer holds the Collector and Odigos issues nothing into their account at all.
func TestManualModeNeverMintsAnything(t *testing.T) {
	clients, store := newFakeClients(), newFakeStore()
	p := testProvisioner(clients, store, testLicense)
	in := sharedInput()
	in.Mode = spec.ModeManual
	provisionOnce(t, p, in)

	for _, s := range clients.secrets.secrets {
		if len(s.values) > 0 {
			t.Errorf("a value was written in manual mode")
		}
	}
	if len(store.credentials) != 0 {
		t.Errorf("credentials were issued in manual mode: %d", len(store.credentials))
	}
}

// captureLogs installs a zap logger writing into buf and returns a function restoring the previous one.
//
// JSON encoding rather than console, deliberately: it is the format a production connector emits, and
// it is the one that routes a struct field through zap's reflected encoder — which is the path a raw
// credential would actually take out of the process.
func captureLogs(buf *bytes.Buffer) func() {
	previous := commonlogger.Logger()
	if previous == nil {
		// Nothing has initialised process logging in a unit test, and UpdateInstance(nil) panics.
		previous = zap.NewNop()
	}
	encoder := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	core := zapcore.NewCore(encoder, zapcore.AddSync(buf), zapcore.DebugLevel)
	commonlogger.UpdateInstance(zap.New(core))
	return func() { commonlogger.UpdateInstance(previous) }
}
