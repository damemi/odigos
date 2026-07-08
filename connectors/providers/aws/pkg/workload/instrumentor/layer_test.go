package instrumentor

import (
	"context"
	"strings"
	"testing"
)

// Lambda layers are region-specific: a function must be instrumented with a layer that lives in its own
// region. These tests guard that the resolver keys and selects by region, and never borrows another
// region's ARN.
func TestPublicLayerResolverIsRegional(t *testing.T) {
	arns := map[string]string{
		"us-east-1/python/x86_64":    "arn:aws:lambda:us-east-1:000000000000:layer:otel-python:1",
		"il-central-1/python/x86_64": "arn:aws:lambda:il-central-1:000000000000:layer:otel-python:1",
	}
	r := NewPublicLayerResolver(arns)

	east, err := r.Resolve(context.Background(), "us-east-1", "python3.12", "x86_64")
	if err != nil {
		t.Fatalf("us-east-1 resolve: %v", err)
	}
	il, err := r.Resolve(context.Background(), "il-central-1", "python3.12", "x86_64")
	if err != nil {
		t.Fatalf("il-central-1 resolve: %v", err)
	}
	if east == il {
		t.Fatalf("expected region-specific layer ARNs, both regions resolved to %q", east)
	}
	if east != arns["us-east-1/python/x86_64"] {
		t.Errorf("us-east-1 ARN = %q, want %q", east, arns["us-east-1/python/x86_64"])
	}

	// A region with no configured layer must fail (ErrLayerUnavailable) rather than reuse another
	// region's ARN — instrumenting with a cross-region layer would be invalid.
	if _, err := r.Resolve(context.Background(), "eu-west-1", "python3.12", "x86_64"); err == nil {
		t.Fatal("expected an error for an unconfigured region, got nil")
	}
}

func TestLanguageFromRuntime(t *testing.T) {
	cases := map[string]string{
		"python3.12": "python",
		"nodejs20.x": "nodejs",
		"java17":     "java",
		"ruby3.3":    "ruby",
		// No opentelemetry-lambda layer exists for these -> unsupported.
		"dotnet8": "",
		"go1.x":   "",
	}
	for runtime, want := range cases {
		if got := LanguageFromRuntime(runtime); got != want {
			t.Errorf("LanguageFromRuntime(%q) = %q, want %q", runtime, got, want)
		}
	}
}

// The exec wrapper is language-specific: Python uses /opt/otel-instrument, Node.js/Java/Ruby use
// /opt/otel-handler. Setting the Python wrapper on a Java function makes the layer bootstrap fail with
// "/opt/otel-instrument: does not exist" — the bug this guards against.
func TestExecWrapperForRuntime(t *testing.T) {
	cases := map[string]string{
		"python3.12": execWrapperInstrument,
		"nodejs20.x": execWrapperHandler,
		"java17":     execWrapperHandler,
		"java21":     execWrapperHandler,
		"ruby3.3":    execWrapperHandler,
		"dotnet8":    "", // unsupported -> no wrapper
		"go1.x":      "", // unsupported -> no wrapper
	}
	for runtime, want := range cases {
		if got := execWrapperForRuntime(runtime); got != want {
			t.Errorf("execWrapperForRuntime(%q) = %q, want %q", runtime, got, want)
		}
	}

	if !isOdigosExecWrapper(execWrapperInstrument) || !isOdigosExecWrapper(execWrapperHandler) {
		t.Error("both language wrappers should be recognized as ours")
	}
	if isOdigosExecWrapper("/opt/custom-wrapper") {
		t.Error("a customer wrapper must not be recognized as ours")
	}
}

// In HANDLER mode (no web adapter), a non-standard Java handler like "run.sh" cannot be wrapped and is
// unsupported; a normal Java handler and non-Java runtimes are fine. Web-adapter functions do NOT go
// through this check — they are instrumented in web-server mode (see TestWebServerLoader).
func TestUnsupportedHandlerReason(t *testing.T) {
	if r := unsupportedHandlerReason("java17", "run.sh", ""); r == "" {
		t.Error("java handler-mode function with a non-Java handler (run.sh) should be unsupported")
	}
	if r := unsupportedHandlerReason("java17", "com.example.App::handleRequest", ""); r != "" {
		t.Errorf("normal Java handler should be supported, got %q", r)
	}
	if r := unsupportedHandlerReason("python3.12", "lambda_function.handler", ""); r != "" {
		t.Errorf("python handler should be supported, got %q", r)
	}

	// A function carrying a foreign exec wrapper (e.g. Datadog) must be refused — handler-mode
	// instrumentation would overwrite the single AWS_LAMBDA_EXEC_WRAPPER and disable it.
	if r := unsupportedHandlerReason("python3.12", "lambda_function.handler", "/opt/datadog_wrapper"); r == "" {
		t.Error("function with a foreign exec wrapper should be unsupported (would clobber it)")
	}
	// Our own wrapper (a re-instrument of a function we already own) is fine.
	if r := unsupportedHandlerReason("python3.12", "lambda_function.handler", execWrapperInstrument); r != "" {
		t.Errorf("our own exec wrapper must not be treated as foreign, got %q", r)
	}
	if r := unsupportedHandlerReason("nodejs20.x", "index.handler", execWrapperHandler); r != "" {
		t.Errorf("our own handler wrapper must not be treated as foreign, got %q", r)
	}
}

// Web-adapter functions are instrumented via language-native loaders (java agent / node --import / python
// PYTHONPATH), not the handler wrapper. Ruby has no verified web-server loader yet.
func TestWebServerLoader(t *testing.T) {
	cases := map[string]struct {
		key   string
		value string
		ok    bool
	}{
		"java":   {EnvJavaToolOptions, javaAgentOption, true},
		"nodejs": {EnvNodeOptions, nodeImportOption, true},
		"python": {EnvPythonPath, pythonInstrPath, true},
		"ruby":   {"", "", false},
	}
	for lang, want := range cases {
		key, value, _, ok := webServerLoader(lang)
		if ok != want.ok || key != want.key || value != want.value {
			t.Errorf("webServerLoader(%q) = (%q,%q,%v), want (%q,%q,%v)", lang, key, value, ok, want.key, want.value, want.ok)
		}
	}

	// Python's loader must put the app's dependency dirs on PYTHONPATH — a web-server process is a plain
	// `python app.py` with no automatic /var/task on sys.path, and OTel skips instrumenting libraries
	// (Flask, requests, …) that aren't importable when sitecustomize runs at startup.
	for _, want := range []string{pythonAutoInstrDir, "/var/task", "/var/task/site-packages"} {
		if !strings.Contains(pythonInstrPath, want) {
			t.Errorf("pythonInstrPath %q must include %q", pythonInstrPath, want)
		}
	}

	// Python must be pinned to the HTTP OTLP exporter — the layer bundles only that one, and the SDK would
	// otherwise default to gRPC and fail ("otlp_proto_grpc not found"). Other languages need no extra env.
	if got := webServerExtraEnv("python"); got[EnvOtlpProtocol] != otlpProtocolHTTP {
		t.Errorf("python extra env = %v, want %s=%s", got, EnvOtlpProtocol, otlpProtocolHTTP)
	}
	if got := webServerExtraEnv("java"); len(got) != 0 {
		t.Errorf("java should need no extra env, got %v", got)
	}
	if got := webServerExtraEnv("nodejs"); len(got) != 0 {
		t.Errorf("nodejs should need no extra env, got %v", got)
	}

	// addLoaderValue is idempotent and preserves the customer's existing value.
	if got := addLoaderValue("--enable-source-maps", nodeImportOption, " "); got != "--enable-source-maps "+nodeImportOption {
		t.Errorf("append NODE_OPTIONS = %q", got)
	}
	if got := addLoaderValue("--enable-source-maps "+nodeImportOption, nodeImportOption, " "); got != "--enable-source-maps "+nodeImportOption {
		t.Errorf("append must be idempotent, got %q", got)
	}
	if got := addLoaderValue("/app/site-packages", pythonInstrPath, ":"); got != pythonInstrPath+":/app/site-packages" {
		t.Errorf("PYTHONPATH must prepend ours, got %q", got)
	}
}

func TestIsJavaHandler(t *testing.T) {
	cases := map[string]bool{
		"com.example.App":                true,
		"com.example.App::handleRequest": true,
		"Handler":                        true,
		"run.sh":                         false,
		"bootstrap":                      false,
		"":                               false,
	}
	for h, want := range cases {
		if got := isJavaHandler(h); got != want {
			t.Errorf("isJavaHandler(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestNormalizeInstrumentationLayers(t *testing.T) {
	v2 := "arn:aws:lambda:us-east-1:025950698545:layer:odigos-otel-python-x86_64:2"
	v4 := "arn:aws:lambda:us-east-1:025950698545:layer:odigos-otel-python-x86_64:4"
	other := "arn:aws:lambda:us-east-1:025950698545:layer:some-other-layer:1"

	got := normalizeInstrumentationLayers([]string{v2, other}, v4)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2: %v", len(got), got)
	}
	if got[0] != other {
		t.Errorf("first layer = %q, want unrelated layer preserved", got[0])
	}
	if got[1] != v4 {
		t.Errorf("second layer = %q, want %q", got[1], v4)
	}

	// Duplicate versions of the same layer name are collapsed even without a desired ARN.
	dup := normalizeInstrumentationLayers([]string{v2, v4}, "")
	if len(dup) != 1 {
		t.Fatalf("duplicate collapse len = %d, want 1: %v", len(dup), dup)
	}
}

func TestMatchesInstrumentationLayerARN(t *testing.T) {
	arn := "arn:aws:lambda:us-east-1:000000000000:layer:odigos-otel-python-x86_64:1"
	if !MatchesInstrumentationLayerARN(arn) {
		t.Fatalf("expected odigos layer to match")
	}
	if MatchesInstrumentationLayerARN("arn:aws:lambda:us-east-1:000000000000:layer:my-deps:1") {
		t.Fatal("expected unrelated layer not to match")
	}
}

// removeLoaderValue is the inverse of addLoaderValue: it must strip exactly our loader addition and return
// the customer's original portion, so a legacy (pre-marker) uninstrument restores the customer's value
// rather than leaving a dangling reference to a removed layer. Round-tripping any customer value through
// add then remove must return the original.
func TestRemoveLoaderValue(t *testing.T) {
	// Append form (*_OPTIONS): our value is at the end.
	if got := removeLoaderValue("--enable-source-maps "+nodeImportOption, nodeImportOption, " "); got != "--enable-source-maps" {
		t.Errorf("append strip = %q, want %q", got, "--enable-source-maps")
	}
	// Prepend form (PYTHONPATH): our value is at the front.
	if got := removeLoaderValue(pythonInstrPath+":/app/site-packages", pythonInstrPath, ":"); got != "/app/site-packages" {
		t.Errorf("prepend strip = %q, want %q", got, "/app/site-packages")
	}
	// Only our value present -> nothing remains (rollback deletes the key).
	if got := removeLoaderValue(nodeImportOption, nodeImportOption, " "); got != "" {
		t.Errorf("pure-ours strip = %q, want empty", got)
	}
	// Our value absent (a handler-mode legacy function's own value) -> returned unchanged.
	if got := removeLoaderValue("--enable-source-maps", nodeImportOption, " "); got != "--enable-source-maps" {
		t.Errorf("absent strip = %q, want value unchanged", got)
	}
	// Round-trip: add then remove restores the customer's original for every separator style.
	for _, tc := range []struct{ cust, val, sep string }{
		{"--max-old-space-size=512", nodeImportOption, " "},
		{"-Dfoo=bar", javaAgentOption, " "},
		{"/app:/app/vendor", pythonInstrPath, ":"},
		{"", nodeImportOption, " "},
	} {
		if got := removeLoaderValue(addLoaderValue(tc.cust, tc.val, tc.sep), tc.val, tc.sep); got != tc.cust {
			t.Errorf("round-trip(%q) = %q, want original", tc.cust, got)
		}
	}
}
