package k8s

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

func mcpArgs(args map[string]any) mcp.CallToolRequest {
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	return req
}

func TestReplaceRuleSubConfigValidatesAndWrites(t *testing.T) {
	t.Run("unknown type", func(t *testing.T) {
		var spec odigosv1.InstrumentationRuleSpec
		if err := replaceRuleSubConfig(mcpArgs(nil), "sql_injection", &spec); err == nil {
			t.Fatal("unknown rule_type must error")
		}
		if spec.PayloadCollection != nil || spec.HeadersCollection != nil {
			t.Fatal("unknown type must not mutate the spec")
		}
	})

	t.Run("headers", func(t *testing.T) {
		var spec odigosv1.InstrumentationRuleSpec
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{}), "headers", &spec); err == nil {
			t.Fatal("empty header_keys must error")
		}
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{"header_keys": []any{"Authorization", "X-Request-Id"}}), "headers", &spec); err != nil {
			t.Fatalf("valid headers: %v", err)
		}
		if spec.HeadersCollection == nil || len(spec.HeadersCollection.HeaderKeys) != 2 || spec.HeadersCollection.HeaderKeys[0] != "Authorization" {
			t.Fatalf("headers = %#v", spec.HeadersCollection)
		}
	})

	t.Run("payload", func(t *testing.T) {
		var spec odigosv1.InstrumentationRuleSpec
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{}), "payload", &spec); err == nil {
			t.Fatal("payload with no signal flags must error rather than collect nothing")
		}
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{
			"http_request":       true,
			"db_query":           true,
			"max_payload_length": float64(2048),
		}), "payload", &spec); err != nil {
			t.Fatalf("valid payload: %v", err)
		}
		if spec.PayloadCollection == nil || spec.PayloadCollection.HttpRequest == nil || spec.PayloadCollection.DbQuery == nil {
			t.Fatalf("payload = %#v", spec.PayloadCollection)
		}
		if spec.PayloadCollection.HttpResponse != nil || spec.PayloadCollection.Messaging != nil {
			t.Fatal("unset payload families must stay off (PII)")
		}
		if spec.PayloadCollection.HttpRequest.MaxPayloadLength == nil || *spec.PayloadCollection.HttpRequest.MaxPayloadLength != 2048 {
			t.Fatalf("max_payload_length = %v, want 2048", spec.PayloadCollection.HttpRequest.MaxPayloadLength)
		}
	})

	t.Run("code_attributes", func(t *testing.T) {
		var spec odigosv1.InstrumentationRuleSpec
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{}), "code_attributes", &spec); err == nil {
			t.Fatal("code_attributes with no flags must error")
		}
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{"code_function": true, "code_filepath": false}), "code_attributes", &spec); err != nil {
			t.Fatalf("valid code_attributes: %v", err)
		}
		if spec.CodeAttributes == nil || spec.CodeAttributes.Function == nil || !*spec.CodeAttributes.Function {
			t.Fatalf("function flag = %#v", spec.CodeAttributes)
		}
		if spec.CodeAttributes.FilePath == nil || *spec.CodeAttributes.FilePath {
			t.Fatal("explicit false must stay false, not become unset")
		}
		if spec.CodeAttributes.Column != nil || spec.CodeAttributes.Stacktrace != nil {
			t.Fatal("omitted code_* flags must stay unset so other rules can still resolve them")
		}
	})

	t.Run("trace_config", func(t *testing.T) {
		var spec odigosv1.InstrumentationRuleSpec
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{}), "trace_config", &spec); err == nil {
			t.Fatal("trace_config without trace_disabled must error")
		}
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{"trace_disabled": false}), "trace_config", &spec); err != nil {
			t.Fatalf("valid trace_config: %v", err)
		}
		if spec.TraceConfig == nil || spec.TraceConfig.Disabled == nil || *spec.TraceConfig.Disabled {
			t.Fatalf("explicit false must enable traces, got %#v", spec.TraceConfig)
		}
	})

	t.Run("ebpf_log_capture", func(t *testing.T) {
		var spec odigosv1.InstrumentationRuleSpec
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{}), "ebpf_log_capture", &spec); err == nil {
			t.Fatal("ebpf_log_capture without the enabled flag must error")
		}
		if err := replaceRuleSubConfig(mcpArgs(map[string]any{"ebpf_log_capture_enabled": true}), "ebpf_log_capture", &spec); err != nil {
			t.Fatalf("valid ebpf_log_capture: %v", err)
		}
		if spec.EbpfLogCapture == nil || spec.EbpfLogCapture.Enabled == nil || !*spec.EbpfLogCapture.Enabled {
			t.Fatalf("ebpf log capture = %#v", spec.EbpfLogCapture)
		}
	})
}

func TestApplyRuleAdvancedFieldsTracksMutation(t *testing.T) {
	var spec odigosv1.InstrumentationRuleSpec
	changed := map[string]any{}
	if err := applyRuleAdvancedFields(mcpArgs(map[string]any{}), &spec, changed); err != nil {
		t.Fatalf("empty: %v", err)
	}
	if len(changed) != 0 || spec.HeadSamplingFallbackFraction != nil {
		t.Fatalf("absent advanced fields must be a no-op, changed=%v spec=%#v", changed, spec)
	}

	// 0 is a real sampling policy (drop everything). The helper defaults the
	// missing key to -1 so an omitted fraction cannot collapse to "keep none".
	changed = map[string]any{}
	if err := applyRuleAdvancedFields(mcpArgs(map[string]any{}), &spec, changed); err != nil {
		t.Fatal(err)
	}
	if spec.HeadSamplingFallbackFraction != nil {
		t.Fatal("missing head_sampling_fraction must not write 0")
	}

	changed = map[string]any{}
	if err := applyRuleAdvancedFields(mcpArgs(map[string]any{
		"head_sampling_fraction": float64(0),
		"otel_distros":           []any{"golang-enterprise", "java-ebpf"},
		"trace_verbosity_disabled_libraries": []any{
			map[string]any{"libraryName": "net/http", "Language": "go"},
		},
	}), &spec, changed); err != nil {
		t.Fatalf("valid advanced: %v", err)
	}
	if spec.HeadSamplingFallbackFraction == nil || spec.HeadSamplingFallbackFraction.FractionToKeep != 0 {
		t.Fatalf("fraction 0 must persist (sample nothing), got %#v", spec.HeadSamplingFallbackFraction)
	}
	if spec.OtelDistros == nil || len(spec.OtelDistros.OtelDistroNames) != 2 || spec.OtelDistros.OtelDistroNames[0] != "golang-enterprise" {
		t.Fatalf("otel_distros = %#v", spec.OtelDistros)
	}
	if spec.TraceVerbosity == nil || len(spec.TraceVerbosity.DisabledLibraries) != 1 || spec.TraceVerbosity.DisabledLibraries[0].LibraryName != "net/http" {
		t.Fatalf("trace verbosity = %#v", spec.TraceVerbosity)
	}
	if spec.TraceVerbosity.EnabledLibraries != nil && len(spec.TraceVerbosity.EnabledLibraries) != 0 {
		t.Fatal("providing only disabled libraries must not invent enabled ones")
	}
	if _, ok := changed["headSamplingFallbackFraction"]; !ok {
		t.Fatalf("update must record the fraction so a fraction-only edit is persisted, changed=%v", changed)
	}
	if _, ok := changed["otelDistros"]; !ok {
		t.Fatalf("update must record otelDistros, changed=%v", changed)
	}
	if _, ok := changed["traceVerbosity"]; !ok {
		t.Fatalf("update must record traceVerbosity, changed=%v", changed)
	}
}

func TestDecodeArgAbsentVsInvalid(t *testing.T) {
	var out map[string]any
	ok, err := decodeArg(mcpArgs(map[string]any{}), "operation", &out)
	if err != nil || ok {
		t.Fatalf("absent key must be (false, nil), got ok=%v err=%v", ok, err)
	}
	ok, err = decodeArg(mcpArgs(map[string]any{"operation": nil}), "operation", &out)
	if err != nil || ok {
		t.Fatalf("explicit null must be treated as absent, got ok=%v err=%v", ok, err)
	}

	type matcher struct {
		Route  string `json:"route"`
		Method string `json:"method"`
	}
	var got matcher
	ok, err = decodeArg(mcpArgs(map[string]any{
		"operation": map[string]any{"route": "/checkout", "method": "POST"},
	}), "operation", &got)
	if err != nil || !ok {
		t.Fatalf("valid object: ok=%v err=%v", ok, err)
	}
	if got.Route != "/checkout" || got.Method != "POST" {
		t.Fatalf("decoded = %+v", got)
	}

	ok, err = decodeArg(mcpArgs(map[string]any{"operation": []any{"not", "an", "object"}}), "operation", &got)
	if err == nil || ok {
		t.Fatal("wrong JSON shape must error rather than silently keep the previous value")
	}
}
