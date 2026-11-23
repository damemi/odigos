package api

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestOpenAPISpecValid parses the embedded contract and asserts the core
// endpoints are present, so a malformed or truncated spec fails the build.
func TestOpenAPISpecValid(t *testing.T) {
	if len(OpenAPISpec) == 0 {
		t.Fatal("embedded OpenAPISpec is empty")
	}

	var doc struct {
		OpenAPI string                 `yaml:"openapi"`
		Paths   map[string]any         `yaml:"paths"`
		Comps   map[string]any         `yaml:"components"`
		Info    map[string]interface{} `yaml:"info"`
	}
	if err := yaml.Unmarshal(OpenAPISpec, &doc); err != nil {
		t.Fatalf("openapi.yaml is not valid YAML: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Error("openapi version missing")
	}

	wantPaths := []string{
		"/healthz",
		"/readyz",
		"/api/v1/services",
		"/api/v1/service-names",
		"/api/v1/services/profile",
		"/api/v1/transactions",
		"/api/v1/transactions/{transaction_id}/baseline",
		"/api/v1/transactions/{transaction_id}/baseline/{class}/promote",
		"/api/v1/transactions/{transaction_id}/observations",
		"/api/v1/policies",
		"/api/v1/learning-policies",
		"/api/v1/findings",
		"/api/v1/anomalies",
		"/api/v1/anomalies/{transaction_id}/{signature}",
		"/api/v1/guardrails",
		"/api/v1/guardrails/seed",
		"/api/v1/guardrails/violations",
		"/api/v1/guardrails/violations/evidence",
		"/api/v1/guardrails/violations/accept",
		"/api/v1/guardrails/violations/dismiss",
		"/api/v1/guardrails/violations/reopen",
		"/api/v1/transactions/{transaction_id}",
		"/api/v1/transactions/{transaction_id}/baseline/{class}/reset",
		"/api/v1/transactions/{transaction_id}/baselines/reset",
		"/api/v1/catalog",
		"/api/v1/system-settings",
		"/api/v1/storage-health",
		"/api/v1/interrogations",
		"/api/v1/interrogations/summary",
		"/api/v1/interrogations/options",
		"/api/v1/interrogations/sources",
		"/api/v1/interrogations/sources/transactions",
		"/api/v1/interrogations/{interrogation_id}",
		"/api/v1/interrogations/{interrogation_id}/events",
		"/api/v1/interrogations/{interrogation_id}/stop",
		"/api/v1/interrogations/{interrogation_id}/extend",
	}
	for _, p := range wantPaths {
		if _, ok := doc.Paths[p]; !ok {
			t.Errorf("spec is missing path %q", p)
		}
	}
}
