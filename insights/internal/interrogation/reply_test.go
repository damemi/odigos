package interrogation

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

func validReply() Reply {
	return Reply{
		AnalysisResponse: apschema.AnalysisResponse{
			SchemaVersion: apschema.Version,
			TransactionID: "8841",
			Recommendations: []apschema.Recommendation{{
				Category: apschema.CategoryObservability,
				Function: "com.claims.IntakeService.validate",
				Language: apschema.LangJava,
				Probe:    apschema.Probe{ClassName: "com.claims.IntakeService", MethodName: "validate"},
				Why:      "IntakeService.validate is called from ClaimController.submit.",
			}},
		},
		Answer:    "Time goes to IntakeService.validate, called from ClaimController.submit.",
		Certainty: 45,
	}
}

func TestParseReply(t *testing.T) {
	b, err := json.Marshal(validReply())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(b)
	withUnknown := strings.Replace(valid, `"done":false`, `"done":false,"confidenceNote":"x"`, 1)
	if withUnknown == valid {
		t.Fatal("test reply has no done field")
	}

	for name, raw := range map[string]string{
		"plain":             valid,
		"surrounding space": "\n  " + valid + "\n",
		"unknown field":     withUnknown,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseReply(raw)
			if err != nil {
				t.Fatalf("parseReply: %v", err)
			}
			if want := validReply(); !reflect.DeepEqual(*got, want) {
				t.Fatalf("parseReply = %+v, want %+v", *got, want)
			}
		})
	}

	// The loop's decoder accepts the same unknown field.
	if _, err := apschema.Parse([]byte(withUnknown)); err != nil {
		t.Fatalf("schema.Parse refuses the unknown field: %v", err)
	}

	for name, raw := range map[string]string{
		"empty":   "  ",
		"prose":   "The answer is that validate is slow.",
		"fenced":  "```json\n" + valid + "\n```",
		"invalid": strings.Replace(valid, `"certainty":45`, `"certainty":150`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseReply(raw); err == nil {
				t.Fatalf("parseReply = %+v, want an error", got)
			}
		})
	}
}

func TestReplySchema(t *testing.T) {
	s := replySchema()
	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Fatalf("root is not a closed object: type %v, additionalProperties %v", s["type"], s["additionalProperties"])
	}
	var required []string
	for _, r := range s["required"].([]any) {
		required = append(required, r.(string))
	}
	if want := []string{"schemaVersion", "transactionId", "recommendations", "answer", "certainty", "done"}; !reflect.DeepEqual(required, want) {
		t.Fatalf("required = %v, want %v", required, want)
	}
	certainty := s["properties"].(map[string]any)["certainty"].(map[string]any)
	if certainty["type"] != "integer" || certainty["minimum"] != 0.0 || certainty["maximum"] != 100.0 {
		t.Fatalf("certainty = %v, want an integer 0..100", certainty)
	}

	// The probes the model may recommend follow the loop's schema.
	raw, err := os.ReadFile("../autopilot/schema/analysis_response.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var loopSchema map[string]any
	if err := json.Unmarshal(raw, &loopSchema); err != nil {
		t.Fatal(err)
	}
	for _, def := range []string{"Recommendation", "Probe"} {
		got := s["$defs"].(map[string]any)[def]
		want := loopSchema["$defs"].(map[string]any)[def]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("$defs.%s = %v, want the loop's %v", def, got, want)
		}
	}
}
