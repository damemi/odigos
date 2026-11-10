package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/commander"
	"github.com/open-telemetry/opentelemetry-collector-contrib/cmd/opampsupervisor/supervisor/config"
	"go.uber.org/zap"
)

// A Commander starts the Collector with --config effective.yaml, but must validate only the
// candidate. In v0.159 it appended its start arguments to validate, loading effective.yaml LAST.
// That replaces the candidate's metrics receiver list with the old one and strands spanmetrics.
func TestValidationDoesNotOverlayRunningConfiguration(t *testing.T) {
	executable := os.Getenv("ODIGOS_TEST_COLLECTOR")
	if executable == "" {
		t.Skip("set ODIGOS_TEST_COLLECTOR to run against a real Collector binary")
	}
	before, err := filepath.Abs("testdata/spanmetrics-before.yaml")
	if err != nil {
		t.Fatal(err)
	}
	after, err := filepath.Abs("testdata/spanmetrics-after.yaml")
	if err != nil {
		t.Fatal(err)
	}
	c, err := commander.NewCommander(zap.NewNop(), filepath.Join(t.TempDir(), "collector.log"), config.Agent{Executable: executable}, "--config", before)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.ValidateConfig(ctx, after); err != nil {
		t.Fatalf("valid spanmetrics update rejected because the old pipeline was overlaid: %v", err)
	}
	// The same bug could also accept an invalid update by masking its bad receiver list.
	document, err := os.ReadFile(before)
	if err != nil {
		t.Fatal(err)
	}
	invalid := filepath.Join(t.TempDir(), "invalid.yaml")
	if err := os.WriteFile(invalid, []byte(strings.ReplaceAll(string(document), "receivers: [otlp]", "receivers: [missing-receiver]")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateConfig(ctx, invalid); err == nil {
		t.Fatal("invalid receiver list was masked by the running configuration")
	}
}

// Exercise the argv contract in ordinary CI too, without requiring a built Collector.
func TestValidationReceivesOnlyCandidateConfig(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("the argv recorder is a POSIX shell script")
	}
	dir := t.TempDir()
	recorder := filepath.Join(dir, "collector")
	capture := filepath.Join(dir, "arguments")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+capture+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := commander.NewCommander(zap.NewNop(), filepath.Join(dir, "collector.log"), config.Agent{Executable: recorder}, "--config", "stale-effective.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ValidateConfig(context.Background(), "new-merged.yaml"); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(args); got != "validate\n--config\nnew-merged.yaml\n" || strings.Contains(got, "stale-effective.yaml") {
		t.Fatalf("validation overlaid a stale config: %q", got)
	}
}
