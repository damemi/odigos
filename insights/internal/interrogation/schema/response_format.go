package schema

import "encoding/json"

// StrictResponseFormat is the OpenAI chat-completions response_format that
// pins the model to AnalysisResponse. Gateways that do not support
// json_schema+strict should be swapped, not papered over in the worker.
func StrictResponseFormat() map[string]any {
	return map[string]any{
		"type": "json_schema",
		"json_schema": map[string]any{
			"name":   "interrogation_analysis",
			"strict": true,
			"schema": responseJSONSchema(),
		},
	}
}

func responseJSONSchema() map[string]any {
	var out map[string]any
	if err := json.Unmarshal(analysisResponseSchemaJSON, &out); err != nil {
		panic("schema: invalid embedded analysis_response.schema.json: " + err.Error())
	}
	return out
}
