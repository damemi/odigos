package spec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestGCPSetupEmbedsPolicyAndCreatesKeysOnlyForKeyAuthentication(t *testing.T) {
	for _, shell := range []string{"bash", "zsh"} {
		if _, err := exec.LookPath(shell); err != nil {
			continue
		}
		for _, keyAuth := range []bool{false, true} {
			t.Run(shell+map[bool]string{false: "/workload-identity", true: "/service-account-key"}[keyAuth], func(t *testing.T) {
				dir := t.TempDir()
				mock := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$MOCK_LOG"
case "$*" in
 'iam service-accounts keys create '*) printf '%s\n' '{"type":"test-only"}' > odigos-connector-key.json ;;
 'iam roles create '*) test -s odigos-connector-role.yaml ;;
esac
`
				if err := os.WriteFile(filepath.Join(dir, "gcloud"), []byte(mock), 0700); err != nil {
					t.Fatal(err)
				}
				values := map[string]string{"projectId": "example-project", "createKey": map[bool]string{false: "false", true: "true"}[keyAuth]}
				catalog := MustGet("gcp")
				for _, a := range catalog.Setup.Artifacts {
					if a.ID == "policy-preview" {
						values["policyDocument"] = renderSetupArtifact(a, []string{"run.services.list"}, values)
					}
				}
				var command string
				for _, a := range catalog.Setup.Artifacts {
					if a.ID == "create-role-gcloud" {
						command = renderSetupArtifact(a, nil, values)
					}
				}
				if command == "" {
					t.Fatal("missing gcloud setup")
				}
				cmd := exec.Command(shell, "-c", command)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "MOCK_LOG="+filepath.Join(dir, "calls"))
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("%v: %s", err, out)
				}
				raw, err := os.ReadFile(filepath.Join(dir, "odigos-connector-role.yaml"))
				if err != nil {
					t.Fatal(err)
				}
				var role struct {
					IncludedPermissions []string `yaml:"includedPermissions"`
				}
				if err = yaml.Unmarshal(raw, &role); err != nil || len(role.IncludedPermissions) != 1 || role.IncludedPermissions[0] != "run.services.list" {
					t.Fatalf("invalid policy: %v %s", err, raw)
				}
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				if strings.Contains(string(calls), "iam service-accounts keys create") != keyAuth {
					t.Fatalf("key creation mismatches selected auth: %s", calls)
				}
			})
		}
	}
}
