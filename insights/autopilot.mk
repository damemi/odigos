# Autopilot feature developer targets (included from Makefile).
#
# Response JSON Schema for LiteLLM strict mode is generated from AnalysisResponse
# Go types (jsonschema struct tags). Regenerate after changing analysis.go types.

SCHEMA_DIR := internal/autopilot/schema
SCHEMA_JSON := $(SCHEMA_DIR)/analysis_response.schema.json
SCHEMA_GO := $(SCHEMA_DIR)/response_schema_gen.go

.PHONY: schema-gen schema-verify

## schema-gen: regenerate analysis_response.schema.json and response_schema_gen.go.
schema-gen:
	cd $(SCHEMA_DIR) && GOWORK=off go generate .

## schema-verify: fail if committed schema artifacts drift from analysis.go (CI guard).
schema-verify: schema-gen
	git diff --exit-code -- $(SCHEMA_JSON) $(SCHEMA_GO)
