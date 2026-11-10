package schema

import (
	"encoding/json"
	"testing"
)

func TestStrictResponseFormatUsesGeneratedSchema(t *testing.T) {
	rf := StrictResponseFormat()
	if rf["type"] != "json_schema" {
		t.Fatalf("type %v", rf["type"])
	}
	js, ok := rf["json_schema"].(map[string]any)
	if !ok || js["strict"] != true {
		t.Fatalf("json_schema %#v", rf["json_schema"])
	}
	schema, ok := js["schema"].(map[string]any)
	if !ok {
		t.Fatalf("schema %#v", js["schema"])
	}
	if schema["type"] != "object" {
		t.Fatalf("root type %v", schema["type"])
	}
	required, _ := schema["required"].([]any)
	if len(required) < 3 {
		t.Fatalf("required %v", required)
	}
}

func TestGeneratedSchemaMatchesParse(t *testing.T) {
	raw := []byte(`{
		"schemaVersion": "1",
		"transactionId": "GET /users",
		"recommendations": [{
			"category": "observability",
			"function": "com.example.UserService.getUser",
			"language": "java",
			"probe": {
				"className": "com.example.UserService",
				"methodName": "getUser",
				"packageName": "",
				"functionName": "",
				"receiverName": "",
				"receiverMethodName": "",
				"signature": ""
			},
			"why": "Domain operation."
		}]
	}`)
	if _, err := Parse(raw); err != nil {
		t.Fatal(err)
	}

	var doc map[string]any
	if err := json.Unmarshal(analysisResponseSchemaJSON, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["type"] != "object" {
		t.Fatalf("embedded schema root type %v", doc["type"])
	}
}
