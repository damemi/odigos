// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// bootstrapDocument is a plausible rendered configuration: a real OTLP receiver and a real exporter,
// which is what makes it worth serving during an outage and what the inert bundled config is not.
const bootstrapDocument = `receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:4317
exporters:
  otlphttp/dest:
    endpoint: https://ingest.example.test
extensions:
  health_check:
    endpoint: 0.0.0.0:13133
service:
  extensions:
    - health_check
  pipelines:
    traces:
      receivers: [otlp]
      exporters: [otlphttp/dest]
`

// encodedBootstrapDocument pins the WIRE FORMAT across a module boundary. The connector encodes this
// value (connectors/base/domain/collectorimage.EncodeBootstrapConfig) and this binary decodes it, and
// the two are built and released separately — so the encoding is a contract, not an implementation
// detail. connectors/base/domain/collectorimage/bootstrap_test.go asserts the same pair from its side.
const encodedBootstrapDocument = "cmVjZWl2ZXJzOgogIG90bHA6CiAgICBwcm90b2NvbHM6CiAgICAgIGdycGM6CiAgICAgICAgZW5kcG9pbnQ6IDAuMC4wLjA6NDMxNwpleHBvcnRlcnM6CiAgb3RscGh0dHAvZGVzdDoKICAgIGVuZHBvaW50OiBodHRwczovL2luZ2VzdC5leGFtcGxlLnRlc3QKZXh0ZW5zaW9uczoKICBoZWFsdGhfY2hlY2s6CiAgICBlbmRwb2ludDogMC4wLjAuMDoxMzEzMwpzZXJ2aWNlOgogIGV4dGVuc2lvbnM6CiAgICAtIGhlYWx0aF9jaGVjawogIHBpcGVsaW5lczoKICAgIHRyYWNlczoKICAgICAgcmVjZWl2ZXJzOiBbb3RscF0KICAgICAgZXhwb3J0ZXJzOiBbb3RscGh0dHAvZGVzdF0K"

// TestTheEncodingIsTheContract. The connector and this binary are released separately, so an encoding
// either side changed on its own would produce a Collector that refuses every bootstrap document with a
// base64 error — reported as "the deployment supplied something unreadable" rather than as a version
// skew, which is a long way from the fix.
func TestTheEncodingIsTheContract(t *testing.T) {
	if got := base64.StdEncoding.EncodeToString([]byte(bootstrapDocument)); got != encodedBootstrapDocument {
		t.Fatalf("the encoding of the pinned document changed:\n%s", got)
	}
	document, err := decodeBootstrapConfig(encodedBootstrapDocument)
	if err != nil {
		t.Fatalf("decode the pinned value: %v", err)
	}
	if string(document) != bootstrapDocument {
		t.Fatalf("round trip changed the document:\n%s", document)
	}

	// The ALPHABET, pinned separately: the document above happens to encode without either of the two
	// characters the standard and URL-safe alphabets disagree about, so it would pass under both — and a
	// mismatch produces a document that decodes to nothing on roughly half of all inputs and works on
	// the rest, which is a bug that reproduces per Collector.
	alphabet, err := decodeBootstrapConfig("///+////")
	if err != nil {
		t.Fatalf("standard base64 was refused: %v", err)
	}
	if string(alphabet) != "\xff\xff\xfe\xff\xff\xff" {
		t.Fatalf("the decoding alphabet is not standard base64: %q", alphabet)
	}
}

// managedConfig loads the shipped Supervisor config with its absolute paths pointed at a temporary
// tree, the way TestBundledManagedConfigLoads does — so these tests run against the file the image
// actually ships rather than a fixture that can drift from it.
//
// validatorExit is what the stand-in Collector's `validate` returns, which is the only way to exercise
// a document this Collector build could not serve: the real answer comes from the real binary, and the
// upstream Agent validator is what asks it.
func managedConfig(t *testing.T, storage string, validatorExit int) config.Supervisor {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake Collector validator is a POSIX shell script")
	}

	managedDir, err := filepath.Abs(filepath.Join("..", "..", "managed"))
	if err != nil {
		t.Fatal(err)
	}
	supervisorConfig, err := os.ReadFile(filepath.Join(managedDir, "supervisor.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	fakeCollector := filepath.Join(t.TempDir(), "odigosotelcol")
	script := "#!/bin/sh\nexit " + itoa(validatorExit) + "\n"
	if err := os.WriteFile(fakeCollector, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	replacer := strings.NewReplacer(
		"/odigosotelcol", fakeCollector,
		"/etc/odigos/managed-collector/guardrails.yaml", filepath.Join(managedDir, "guardrails.yaml"),
		"/etc/odigos/managed-collector/fallback.yaml", filepath.Join(managedDir, "fallback.yaml"),
		"/var/lib/odigos/collector-supervisor", storage,
	)
	path := filepath.Join(t.TempDir(), "supervisor.yaml")
	if err := os.WriteFile(path, []byte(replacer.Replace(string(supervisorConfig))), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ODIGOS_OPAMP_ENDPOINT", "wss://central.example.test/v1/opamp")
	t.Setenv("ODIGOS_OPAMP_AUTHORIZATION", "Bearer test-credential")
	t.Setenv("ODIGOS_CLOUD_CONNECTOR_ID", "connector-test")
	t.Setenv("ODIGOS_CLOUD_PROVIDER", "aws")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load the managed Supervisor config: %v", err)
	}
	return cfg
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	return string(rune('0' + n))
}

// guardrailsPath is the local invariant file the shipped config names, resolved the same way the
// loaded config resolves it.
func guardrailsPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "managed", "guardrails.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// TestABootstrapConfigReplacesTheInertFallback — the "deterministic fallback ordering" criterion, and
// the ordering is not the one the obvious reading suggests.
//
// startup_fallback_configs is MERGED in order rather than tried in turn, and collector confmap replaces
// lists on merge. Leaving the bundled inert config after the bootstrap document would let its
// `pipelines.traces.receivers: [nop]` replace the real pipeline and produce a Collector that starts,
// reports healthy and accepts nothing — the exact outcome the bootstrap exists to prevent.
func TestABootstrapConfigReplacesTheInertFallback(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "supervisor")
	cfg := managedConfig(t, storage, 0)
	inert := cfg.Agent.StartupFallbackConfigs
	t.Setenv(EnvBootstrapConfig, encodedBootstrapDocument)

	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	want := []string{filepath.Join(storage, BootstrapConfigFileName), guardrailsPath(t)}
	if !reflect.DeepEqual(cfg.Agent.StartupFallbackConfigs, want) {
		t.Fatalf("startup_fallback_configs = %v, want %v", cfg.Agent.StartupFallbackConfigs, want)
	}
	for _, path := range inert {
		if strings.HasSuffix(path, "fallback.yaml") &&
			containsString(cfg.Agent.StartupFallbackConfigs, path) {
			t.Errorf("the inert fallback is still merged in: %v", cfg.Agent.StartupFallbackConfigs)
		}
	}

	// The document, byte for byte, and readable only by this process — it carries the exporters'
	// credentials.
	written, err := os.ReadFile(want[0])
	if err != nil {
		t.Fatalf("read the written document: %v", err)
	}
	if string(written) != bootstrapDocument {
		t.Errorf("written document:\n%s", written)
	}
	info, err := os.Stat(want[0])
	if err != nil {
		t.Fatal(err)
	}
	// The literal, not the constant: comparing against bootstrapFileMode would move with it and assert
	// nothing. The document holds the configuration's exporter credentials.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("mode = %o, want 600: it holds an exporter credential", mode)
	}
}

// TestTheLocalInvariantsSurviveTheFallbackPath. The fallback path does not merge agent::config_files at
// all — the upstream composes only its own OpAMP-extension and own-telemetry snippets around the
// fallback — so without carrying the local files across, a cold start would run a Collector with the
// enterprise authorization extension simply not loaded.
//
// Asserted by performing the merge the Supervisor performs, because the property that matters is the
// effective configuration and not the list.
func TestTheLocalInvariantsSurviveTheFallbackPath(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "supervisor")
	cfg := managedConfig(t, storage, 0)
	t.Setenv(EnvBootstrapConfig, encodedBootstrapDocument)
	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	// The upstream merges the list in order into one conf; do the same, through the same helpers.
	merged, err := config.NewConfFromYAML(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range cfg.Agent.StartupFallbackConfigs {
		document, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if err := config.MergeConfFromYAML(merged, document); err != nil {
			t.Fatalf("merge %s: %v", path, err)
		}
	}

	// The real pipeline survived — the whole point.
	if got := merged.Get("service::pipelines::traces::receivers"); !reflect.DeepEqual(got, []any{"otlp"}) {
		t.Errorf("traces receivers = %v, want the bootstrap's own", got)
	}
	if got := merged.Get("service::pipelines::traces::exporters"); !reflect.DeepEqual(got, []any{"otlphttp/dest"}) {
		t.Errorf("traces exporters = %v, want the bootstrap's own", got)
	}
	// And the guardrail is loaded, which nothing else on this path would have done.
	extensions, ok := merged.Get("service::extensions").([]any)
	if !ok {
		t.Fatalf("service::extensions = %v", merged.Get("service::extensions"))
	}
	if !containsAny(extensions, "odigos_enterprise_auth") {
		t.Errorf("service::extensions = %v, want the enterprise authorization extension", extensions)
	}
	if !containsAny(extensions, "health_check") {
		t.Errorf("service::extensions = %v, want the bootstrap's own extension kept too", extensions)
	}
	if merged.Get("extensions::odigos_enterprise_auth") == nil &&
		!merged.IsSet("extensions::odigos_enterprise_auth") {
		t.Error("the enterprise authorization extension is listed but not defined")
	}
}

// TestRecoveryRunsTheRemoteConfigAlone. The bootstrap document is a FALLBACK and never a local config
// file: one that leaked into agent::config_files would merge its stale pipeline over every configuration
// the control plane ever pushed afterwards, so a recovered Collector would keep serving a mixture of the
// two and the drift would be invisible.
func TestRecoveryRunsTheRemoteConfigAlone(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "supervisor")
	cfg := managedConfig(t, storage, 0)
	before := append([]string(nil), cfg.Agent.ConfigFiles...)
	t.Setenv(EnvBootstrapConfig, encodedBootstrapDocument)

	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	if !reflect.DeepEqual(cfg.Agent.ConfigFiles, before) {
		t.Fatalf("agent::config_files = %v, want it untouched at %v", cfg.Agent.ConfigFiles, before)
	}
	if containsString(cfg.Agent.ConfigFiles, filepath.Join(storage, BootstrapConfigFileName)) {
		t.Fatal("the bootstrap document became a local config file, so it would merge into every push")
	}
	// The remote-config slot is still first, which is what makes a push win outright.
	if len(cfg.Agent.ConfigFiles) == 0 || cfg.Agent.ConfigFiles[0] != "$REMOTE_CONFIG" {
		t.Errorf("agent::config_files = %v", cfg.Agent.ConfigFiles)
	}
}

// TestAFirstBootCreatesItsOwnStorage — the "empty storage" criterion, and the case this whole file
// exists for: on ephemeral storage the directory does not exist the first time a task runs, and this
// writes before the Supervisor has created any state of its own.
func TestAFirstBootCreatesItsOwnStorage(t *testing.T) {
	storage := filepath.Join(t.TempDir(), "never", "existed")
	cfg := managedConfig(t, storage, 0)
	if _, err := os.Stat(storage); !os.IsNotExist(err) {
		t.Fatalf("the storage directory already exists, so this proves nothing: %v", err)
	}
	t.Setenv(EnvBootstrapConfig, encodedBootstrapDocument)

	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	written, err := os.ReadFile(filepath.Join(storage, BootstrapConfigFileName))
	if err != nil {
		t.Fatalf("the document was not written to a directory that did not exist: %v", err)
	}
	if string(written) != bootstrapDocument {
		t.Errorf("written document:\n%s", written)
	}
	if !containsString(cfg.Agent.StartupFallbackConfigs, filepath.Join(storage, BootstrapConfigFileName)) {
		t.Errorf("startup_fallback_configs = %v", cfg.Agent.StartupFallbackConfigs)
	}
}

// TestNothingSuppliedLeavesTheInertFallback. The v1 default: no bootstrap document, so a cold start
// during an outage serves the bundled inert configuration — the state exists to say out loud.
func TestNothingSuppliedLeavesTheInertFallback(t *testing.T) {
	storage := t.TempDir()
	cfg := managedConfig(t, storage, 0)
	before := append([]string(nil), cfg.Agent.StartupFallbackConfigs...)
	t.Setenv(EnvBootstrapConfig, "")

	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	if !reflect.DeepEqual(cfg.Agent.StartupFallbackConfigs, before) {
		t.Fatalf("startup_fallback_configs = %v, want %v", cfg.Agent.StartupFallbackConfigs, before)
	}
	if len(before) == 0 || !strings.HasSuffix(before[0], "fallback.yaml") {
		t.Errorf("the shipped fallback is not the bundled inert config: %v", before)
	}
	if _, err := os.Stat(filepath.Join(storage, BootstrapConfigFileName)); !os.IsNotExist(err) {
		t.Errorf("a document was written with nothing supplied: %v", err)
	}
}

// TestAnUnusableDocumentIsIgnoredRatherThanFatal. This is a mitigation for an outage, and a mitigation
// that can stop a Collector from starting is worse than the outage it mitigates. Every case leaves the
// configured fallback exactly as it was and leaves no document on disk.
func TestAnUnusableDocumentIsIgnoredRatherThanFatal(t *testing.T) {
	cases := map[string]struct {
		encoded       string
		validatorExit int
	}{
		"not base64":                         {"this is not base64 !!", 0},
		"base64 of nothing":                  {base64.StdEncoding.EncodeToString([]byte("")), 0},
		"base64 of whitespace":               {base64.StdEncoding.EncodeToString([]byte("\n  \n")), 0},
		"a document this build cannot serve": {encodedBootstrapDocument, 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			storage := t.TempDir()
			cfg := managedConfig(t, storage, tc.validatorExit)
			before := append([]string(nil), cfg.Agent.StartupFallbackConfigs...)
			t.Setenv(EnvBootstrapConfig, tc.encoded)

			installBootstrapConfig(&cfg, zaptest.NewLogger(t))

			if !reflect.DeepEqual(cfg.Agent.StartupFallbackConfigs, before) {
				t.Fatalf("startup_fallback_configs = %v, want the configured %v",
					cfg.Agent.StartupFallbackConfigs, before)
			}
			if _, err := os.Stat(filepath.Join(storage, BootstrapConfigFileName)); !os.IsNotExist(err) {
				t.Errorf("a rejected document was left on disk: %v", err)
			}
		})
	}
}

// TestAWithdrawnDocumentIsRemoved. On persistent storage a document from an earlier deployment would
// otherwise sit there holding an exporter credential in a file nothing reads — and the point of
// withdrawing the variable is that it is gone.
func TestAWithdrawnDocumentIsRemoved(t *testing.T) {
	storage := t.TempDir()
	path := filepath.Join(storage, BootstrapConfigFileName)
	if err := os.WriteFile(path, []byte(bootstrapDocument), bootstrapFileMode); err != nil {
		t.Fatal(err)
	}
	cfg := managedConfig(t, storage, 0)
	t.Setenv(EnvBootstrapConfig, "")

	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a withdrawn document survived: %v", err)
	}
}

// TestWrappedBase64IsAccepted. Infrastructure-as-code tools wrap long values, and a document refused
// for a newline somebody's template inserted would be refused identically on every deployment, with an
// error about base64 rather than about the wrapping.
func TestWrappedBase64IsAccepted(t *testing.T) {
	var wrapped strings.Builder
	for i, r := range encodedBootstrapDocument {
		if i > 0 && i%64 == 0 {
			wrapped.WriteString("\n  ")
		}
		wrapped.WriteRune(r)
	}

	storage := t.TempDir()
	cfg := managedConfig(t, storage, 0)
	t.Setenv(EnvBootstrapConfig, " \n"+wrapped.String()+"\n ")

	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	written, err := os.ReadFile(filepath.Join(storage, BootstrapConfigFileName))
	if err != nil {
		t.Fatalf("a wrapped value was refused: %v", err)
	}
	if string(written) != bootstrapDocument {
		t.Errorf("written document:\n%s", written)
	}
}

// TestASecondBootReplacesTheDocument. The variable is resolved from the secret store when a task
// starts, so a replaced task carries whatever the control plane had published by then — and a stale
// document left in place on persistent storage would outrank it for ever.
func TestASecondBootReplacesTheDocument(t *testing.T) {
	storage := t.TempDir()
	path := filepath.Join(storage, BootstrapConfigFileName)
	if err := os.WriteFile(path, []byte("receivers:\n  nop:\n"), bootstrapFileMode); err != nil {
		t.Fatal(err)
	}
	cfg := managedConfig(t, storage, 0)
	t.Setenv(EnvBootstrapConfig, encodedBootstrapDocument)

	installBootstrapConfig(&cfg, zaptest.NewLogger(t))

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != bootstrapDocument {
		t.Fatalf("the earlier document survived:\n%s", written)
	}
}

// TestTheLocalFileListDropsTheSupervisorsOwnPlaceholders. $REMOTE_CONFIG and the two composed snippets
// are meaningless in a fallback list — the fallback IS the remote config's replacement, and the upstream
// merges its own snippets around it — and naming one would fail the upstream's own validation, which
// stats every entry.
func TestTheLocalFileListDropsTheSupervisorsOwnPlaceholders(t *testing.T) {
	got := localConfigFiles([]string{
		"$REMOTE_CONFIG", "$OWN_TELEMETRY_CONFIG", "$OPAMP_EXTENSION_CONFIG",
		"/etc/odigos/managed-collector/guardrails.yaml",
	})
	want := []string{"/etc/odigos/managed-collector/guardrails.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("localConfigFiles = %v, want %v", got, want)
	}
	if len(localConfigFiles(nil)) != 0 {
		t.Error("an empty list produced entries")
	}
}

// TestAMissingStorageDirectoryThatCannotBeCreatedIsNotFatal, which is the same posture as every other
// failure here: a Collector that will not start is worse than one serving the inert configuration.
func TestAMissingStorageDirectoryThatCannotBeCreatedIsNotFatal(t *testing.T) {
	// A file where the directory should be, so MkdirAll cannot succeed.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := managedConfig(t, blocked, 0)
	before := append([]string(nil), cfg.Agent.StartupFallbackConfigs...)
	t.Setenv(EnvBootstrapConfig, encodedBootstrapDocument)

	installBootstrapConfig(&cfg, zap.NewNop())

	if !reflect.DeepEqual(cfg.Agent.StartupFallbackConfigs, before) {
		t.Fatalf("startup_fallback_configs = %v, want the configured %v",
			cfg.Agent.StartupFallbackConfigs, before)
	}
}

func containsString(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func containsAny(values []any, want string) bool {
	for _, v := range values {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}
