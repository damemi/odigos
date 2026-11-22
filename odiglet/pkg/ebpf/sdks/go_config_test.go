package sdks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	"github.com/odigos-io/odigos/common/api/agentsignalconfig"
	"github.com/odigos-io/odigos/common/api/instrumentationrules"
	commonapisampling "github.com/odigos-io/odigos/common/api/sampling"
	goAutoConfig "go.opentelemetry.io/auto/config"
	"go.opentelemetry.io/collector/consumer/consumertest"
)

func float64ptr(v float64) *float64 { return &v }
func boolptr(v bool) *bool          { return &v }

// TestNewGoInstrumentationFactoryEnterpriseRejectsNilConsumer pins the #3587
// constructor contract: every instrumented Go process shares one traces
// consumer. A nil consumer used to be an OTLP conn check; accepting nil now
// would drop every Go span on the floor with no setup error.
func TestNewGoInstrumentationFactoryEnterpriseRejectsNilConsumer(t *testing.T) {
	t.Setenv(GoOffsetsFilePathEnv, "")
	_, err := NewGoInstrumentationFactoryEnterprise(0, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "traces consumer is nil") {
		t.Fatalf("nil traces consumer: %v", err)
	}

	got, err := NewGoInstrumentationFactoryEnterprise(0, nil, consumertest.NewNop())
	if err != nil {
		t.Fatalf("valid consumer: %v", err)
	}
	if got == nil {
		t.Fatal("valid consumer must return a factory")
	}
}

// TestConvertToGoInstrumentationConfigMapping pins the CR → Go auto-SDK
// translation. A dropped header key, ignored custom probe, or disabled-library
// that never reaches the agent is silent data loss / PII leak on every Go
// process that picks up the rule.
func TestConvertToGoInstrumentationConfigMapping(t *testing.T) {
	if _, err := convertToGoInstrumentationConfig("not-a-config"); err == nil {
		t.Fatal("wrong type must error rather than apply an empty policy")
	}

	empty, err := convertToGoInstrumentationConfig(&odigosv1.ContainerAgentConfig{ContainerName: "app"})
	if err != nil {
		t.Fatalf("nil traces: %v", err)
	}
	if empty.HeadSampling != nil || len(empty.CustomInstrumentations) != 0 {
		t.Fatalf("nil traces must not invent policy, got %#v", empty)
	}

	cfg := &odigosv1.ContainerAgentConfig{
		Traces: &agentsignalconfig.AgentTracesConfig{
			TraceVerbosity: &instrumentationrules.TraceVerbosity{
				DisabledLibraries: []instrumentationrules.InstrumentationLibrary{{LibraryName: "net/http"}},
				EnabledLibraries:  []instrumentationrules.InstrumentationLibrary{{LibraryName: "database/sql"}},
			},
			CodeAttributes: &instrumentationrules.CodeAttributes{
				Function:   boolptr(true),
				FilePath:   boolptr(false),
				Stacktrace: boolptr(true),
			},
			CustomInstrumentations: &instrumentationrules.CustomInstrumentations{
				Golang: []instrumentationrules.GolangCustomProbe{
					{PackageName: "net/http", FunctionName: "ListenAndServe"},
					{PackageName: "net/http", ReceiverName: "response", ReceiverMethodName: "WriteHeader"},
				},
			},
			HeadersCollection: &instrumentationrules.HttpHeadersCollection{
				HeaderKeys: []string{"Authorization", "X-Request-Id"},
			},
			PayloadCollection: &instrumentationrules.PayloadCollection{
				HttpRequest:  &instrumentationrules.HttpPayloadCollection{},
				HttpResponse: &instrumentationrules.HttpPayloadCollection{},
				DbQuery:      &instrumentationrules.DbQueryPayloadCollection{},
			},
			HeadSampling: &commonapisampling.HeadSamplingConfig{
				DryRun:          true,
				SpanMetricsMode: "include_noisy",
				NoisyOperations: []commonapisampling.NoisyOperation{
					{
						Id:               "http-server",
						Name:             "checkout",
						PercentageAtMost: float64ptr(5),
						Operation: &commonapisampling.HeadSamplingOperationMatcher{
							HttpServer: &commonapisampling.HeadSamplingHttpServerOperationMatcher{
								Route:       "/checkout",
								RoutePrefix: "/api",
								Method:      "POST",
							},
						},
					},
					{
						Id:               "http-client",
						Disabled:         true,
						PercentageAtMost: float64ptr(0),
						Operation: &commonapisampling.HeadSamplingOperationMatcher{
							HttpClient: &commonapisampling.HeadSamplingHttpClientOperationMatcher{
								ServerAddress:       "payments.internal",
								TemplatedPath:       "/charge/{id}",
								TemplatedPathPrefix: "/charge",
								Method:              "PUT",
							},
						},
					},
					{Id: "unnamed", PercentageAtMost: float64ptr(1)},
				},
			},
		},
	}

	got, err := convertToGoInstrumentationConfig(cfg)
	if err != nil {
		t.Fatalf("valid config: %v", err)
	}

	falseLib := got.InstrumentationLibraryConfigs[goAutoConfig.InstrumentationLibraryID{InstrumentedPkg: "net/http"}]
	if falseLib.TracesEnabled == nil || *falseLib.TracesEnabled {
		t.Errorf("disabled library must map TracesEnabled=false, got %#v", falseLib)
	}
	trueLib := got.InstrumentationLibraryConfigs[goAutoConfig.InstrumentationLibraryID{InstrumentedPkg: "database/sql"}]
	if trueLib.TracesEnabled == nil || !*trueLib.TracesEnabled {
		t.Errorf("enabled library must map TracesEnabled=true, got %#v", trueLib)
	}

	if !got.DefaultCodeAttributes.Function || got.DefaultCodeAttributes.FilePath || !got.DefaultCodeAttributes.Stacktrace {
		t.Errorf("code attributes = %+v", got.DefaultCodeAttributes)
	}
	if got.DefaultCodeAttributes.Column || got.DefaultCodeAttributes.LineNumber || got.DefaultCodeAttributes.Namespace {
		t.Error("unset code-attribute flags must stay false, not become true")
	}

	if len(got.CustomInstrumentations) != 2 {
		t.Fatalf("custom probes = %d, want 2 (Java probes on the same CR must not displace Go probes)", len(got.CustomInstrumentations))
	}
	if got.CustomInstrumentations[0].PackageName != "net/http" || got.CustomInstrumentations[0].FunctionName != "ListenAndServe" {
		t.Errorf("function probe = %+v", got.CustomInstrumentations[0])
	}
	if got.CustomInstrumentations[1].ReceiverName != "response" || got.CustomInstrumentations[1].ReceiverMethodName != "WriteHeader" {
		t.Errorf("method probe = %+v", got.CustomInstrumentations[1])
	}

	// The CR has one header-key list; the Go SDK splits request vs response.
	// Both sides must receive the same keys or one direction silently drops PII.
	wantHeaders := []string{"Authorization", "X-Request-Id"}
	if !stringSlicesEqual(got.HTTPHeaders.RequestKeys, wantHeaders) || !stringSlicesEqual(got.HTTPHeaders.ResponseKeys, wantHeaders) {
		t.Errorf("headers request=%v response=%v, want both %v", got.HTTPHeaders.RequestKeys, got.HTTPHeaders.ResponseKeys, wantHeaders)
	}

	if got.Payload.HTTPRequest == nil || !got.Payload.HTTPRequest.Enabled ||
		got.Payload.HTTPResponse == nil || !got.Payload.HTTPResponse.Enabled ||
		got.Payload.DbQuery == nil || !got.Payload.DbQuery.Enabled {
		t.Errorf("payload = %+v", got.Payload)
	}
	if got.Payload.Messaging != nil {
		t.Error("unset messaging payload must stay off")
	}

	hs := got.HeadSampling
	if hs == nil || !hs.DryRun || string(hs.SpanMetricsMode) != "include_noisy" {
		t.Fatalf("head sampling identity = %#v", hs)
	}
	if len(hs.NoisyOperations) != 3 {
		t.Fatalf("NoisyOperations = %d, want 3", len(hs.NoisyOperations))
	}
	server := hs.NoisyOperations[0]
	if server.ID != "http-server" || server.Operation == nil || server.Operation.HTTPServer == nil {
		t.Fatalf("HTTP server rule = %+v", server)
	}
	if server.Operation.HTTPClient != nil {
		t.Error("an HTTP-server rule must not grow a client matcher")
	}
	if server.Operation.HTTPServer.Route != "/checkout" || server.Operation.HTTPServer.Method != "POST" {
		t.Errorf("HTTP server matcher = %+v", server.Operation.HTTPServer)
	}
	client := hs.NoisyOperations[1]
	if client.Operation == nil || client.Operation.HTTPClient == nil || client.Operation.HTTPServer != nil {
		t.Fatalf("HTTP client rule = %+v", client)
	}
	if client.Operation.HTTPClient.ServerAddress != "payments.internal" || client.Operation.HTTPClient.Method != "PUT" {
		t.Errorf("HTTP client matcher = %+v", client.Operation.HTTPClient)
	}
	if hs.NoisyOperations[2].ID != "unnamed" || hs.NoisyOperations[2].Operation != nil {
		t.Errorf("matcher-less rule = %+v", hs.NoisyOperations[2])
	}
}

func TestReadAndVerifyOffsetsFileRejectsTamperedPayload(t *testing.T) {
	dir := t.TempDir()
	write := func(name, contents string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	if _, err := readAndVerifyOffsetsFile(write("not-json", "plain-text")); err == nil {
		t.Fatal("non-JSON must fail")
	}

	quoted, err := json.Marshal("offsets-without-signature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAndVerifyOffsetsFile(write("no-delim", string(quoted))); err == nil {
		t.Fatal("missing ---SIGNATURE--- delimiter must fail")
	}

	quoted, err = json.Marshal("offsets---SIGNATURE---%%%not-base64%%%")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAndVerifyOffsetsFile(write("bad-b64", string(quoted))); err == nil {
		t.Fatal("invalid signature encoding must fail")
	}

	// Well-formed envelope, wrong key material — this is the path that must
	// never load a forged offsets file into every Go process on the node.
	quoted, err = json.Marshal("forged-offsets---SIGNATURE---dGVzdA==")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAndVerifyOffsetsFile(write("forged", string(quoted))); err == nil {
		t.Fatal("forged signature must fail verification")
	}
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
