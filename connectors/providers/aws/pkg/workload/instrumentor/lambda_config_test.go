package instrumentor

import (
	"encoding/json"
	"reflect"
	"testing"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
)

func TestLambdaConfigUsesReadableV2Fields(t *testing.T) {
	c := lambdaBootstrapConfig{
		Version: lambdaConfigVersion, Service: "checkout", Signals: 7, Endpoint: "collector:4317",
		HTTP: true, Insecure: true, Headers: map[string]string{"authorization": "Bearer token"},
		ApplicationSignals: true, FunctionLogs: true, Debug: true,
	}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "service", "signals", "endpoint", "http", "insecure", "headers", "applicationSignals", "functionLogs", "debug"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("readable configuration omitted %q: %s", key, raw)
		}
	}
	for _, key := range []string{"v", "s", "t", "e", "p", "i", "h", "a", "l", "d", "w"} {
		if _, ok := fields[key]; ok {
			t.Errorf("configuration still emitted legacy key %q: %s", key, raw)
		}
	}
	if !reflect.DeepEqual(fields["signals"], []any{"traces", "metrics", "logs"}) {
		t.Fatalf("signals are not explicit names: %s", raw)
	}
}

func TestLambdaNamedSignalsValidation(t *testing.T) {
	for _, signals := range []string{`[]`, `["traces","traces"]`, `["profiles"]`, `null`} {
		if _, err := parseLambdaConfig(`{"version":2,"service":"checkout","endpoint":"collector:4317","signals":` + signals + `}`); err == nil {
			t.Errorf("invalid signal set accepted: %s", signals)
		}
	}
	c, err := parseLambdaConfig(`{"version":2,"service":"checkout","endpoint":"collector:4317","signals":["metrics","traces"]}`)
	if err != nil || !c.target().Traces || !c.target().Metrics || c.target().Logs {
		t.Fatalf("named signals did not select traces and metrics: %+v, %v", c, err)
	}
}

func TestParseLambdaConfigAcceptsV1DuringMigration(t *testing.T) {
	c, err := parseLambdaConfig(`{"v":1,"s":"checkout","t":7,"e":"collector:4317","p":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 || c.Service != "checkout" || c.Signals != 7 || c.Endpoint != "collector:4317" || !c.HTTP {
		t.Fatalf("legacy config parsed as %+v", c)
	}
}

func TestCompactLambdaEnvironmentDoesNotStoreCollectorConfig(t *testing.T) {
	env := map[string]string{}
	target := domaindest.OTLPTarget{Endpoint: "collector:4317", Traces: true}
	manifest := buildManifest(env, "layer")
	applyHandlerEnv(env, "nodejs22.x", "", "checkout", target, false, "yaml:{full: collector-config}")
	if err := compactLambdaEnvironment(env, manifest, false, "nodejs22.x", "", "checkout", target, nil, false); err != nil {
		t.Fatal(err)
	}
	if _, found := env[EnvCollectorConfigURI]; found {
		t.Fatalf("stored Lambda environment unexpectedly contains %s", EnvCollectorConfigURI)
	}
	if _, found := env[EnvLambdaConfig]; !found {
		t.Fatalf("stored Lambda environment is missing %s", EnvLambdaConfig)
	}
}
