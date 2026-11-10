// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/config"
)

func TestBundledManagedConfigLoads(t *testing.T) {
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

	// Config loading validates the Collector executable and invokes it to validate the fallback.
	// A tiny stand-in keeps this test focused on the shipped Supervisor schema and merge contract;
	// the generated Collector module separately validates fallback.yaml with its real factories.
	fakeCollector := filepath.Join(t.TempDir(), "odigosotelcol")
	if err := os.WriteFile(fakeCollector, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	guardrails := filepath.Join(managedDir, "guardrails.yaml")
	fallback := filepath.Join(managedDir, "fallback.yaml")
	replacer := strings.NewReplacer(
		"/odigosotelcol", fakeCollector,
		"/etc/odigos/managed-collector/guardrails.yaml", guardrails,
		"/etc/odigos/managed-collector/fallback.yaml", fallback,
		"/var/lib/odigos/collector-supervisor", t.TempDir(),
	)
	testConfigPath := filepath.Join(t.TempDir(), "supervisor.yaml")
	if err := os.WriteFile(testConfigPath, []byte(replacer.Replace(string(supervisorConfig))), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ODIGOS_OPAMP_ENDPOINT", "wss://central.example.test/v1/opamp")
	t.Setenv("ODIGOS_OPAMP_AUTHORIZATION", "Bearer test-credential")
	t.Setenv("ODIGOS_CLOUD_CONNECTOR_ID", "connector-test")
	t.Setenv("ODIGOS_CLOUD_PROVIDER", "aws")

	cfg, err := config.Load(testConfigPath)
	if err != nil {
		t.Fatalf("load managed Supervisor config: %v", err)
	}

	if !cfg.Capabilities.AcceptsRemoteConfig || !cfg.Capabilities.ReportsRemoteConfig {
		t.Fatal("managed Supervisor must accept and report remote configuration")
	}
	if !cfg.Capabilities.ReportsHealth || !cfg.Capabilities.ReportsAvailableComponents {
		t.Fatal("managed Supervisor must report health and the compiled component inventory")
	}
	if !cfg.Agent.ValidateConfig || !cfg.Agent.AutomaticConfigRollback {
		t.Fatal("managed Supervisor must validate changes and roll back failed configurations")
	}
	if got := cfg.Server.Headers.Get("Authorization"); got != "Bearer test-credential" {
		t.Fatalf("Authorization header = %q", got)
	}

	wantFiles := []string{
		"$REMOTE_CONFIG",
		"$OWN_TELEMETRY_CONFIG",
		"$OPAMP_EXTENSION_CONFIG",
		guardrails,
	}
	if !reflect.DeepEqual(cfg.Agent.ConfigFiles, wantFiles) {
		t.Fatalf("config merge order = %v, want %v", cfg.Agent.ConfigFiles, wantFiles)
	}
	if got := cfg.HealthCheck.Port(); got != 13134 {
		t.Fatalf("health check port = %d, want 13134", got)
	}
}

// Managed mode is opt-in. Both final image variants must continue to start the Collector directly
// so the existing gateway and node-collector workloads behave exactly as they did before the
// Supervisor was added to the image.
func TestCollectorImageKeepsRegularModeDefault(t *testing.T) {
	dockerfile, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}

	const regularCollectorCMD = `CMD ["/odigosotelcol"]`
	if got := strings.Count(string(dockerfile), regularCollectorCMD); got != 2 {
		t.Fatalf("regular Collector CMD count = %d, want 2 (RHEL and distroless images)", got)
	}
	if strings.Contains(string(dockerfile), `ENTRYPOINT ["/odigosopampsupervisor"]`) {
		t.Fatal("managed Supervisor must not be the image entrypoint")
	}
}

// Central owns the data pipeline, but the two local control-plane extensions are not optional.
// Pin the upstream Supervisor's extension-list merge behavior so a dependency update cannot turn
// the final guardrail file into a replacement list and silently detach authorization or OpAMP.
func TestManagedMergePreservesLocalExtensions(t *testing.T) {
	remote, err := config.NewConfFromYAML([]byte(`
extensions:
  health_check:
service:
  extensions: [health_check]
`))
	if err != nil {
		t.Fatal(err)
	}
	opamp, err := config.NewConfFromYAML([]byte(`
extensions:
  opamp:
service:
  extensions: [opamp]
`))
	if err != nil {
		t.Fatal(err)
	}
	guardrailBytes, err := os.ReadFile(filepath.Join("..", "..", "managed", "guardrails.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	guardrails, err := config.NewConfFromYAML(guardrailBytes)
	if err != nil {
		t.Fatal(err)
	}

	if err := config.MergeConf(remote, opamp); err != nil {
		t.Fatal(err)
	}
	if err := config.MergeConf(remote, guardrails); err != nil {
		t.Fatal(err)
	}

	want := []any{"health_check", "opamp", "odigos_enterprise_auth"}
	if got := remote.Get("service::extensions"); !reflect.DeepEqual(got, want) {
		t.Fatalf("merged service extensions = %v, want %v", got, want)
	}
}
