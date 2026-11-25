package instrumentor

import (
	"encoding/json"
	"reflect"
	"testing"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
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

// LambdaServiceName feeds orphan adoption, which records it on a manual-mode source, so it must read the name the
// function actually reports in every layout the CDK and the connector write.
func TestLambdaServiceName(t *testing.T) {
	compact := `{"version":2,"service":"my-svc","endpoint":"collector:4317","signals":["traces"]}`
	iac := `{"v":1,"source":"cdk"}`
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"no Odigos marker", map[string]string{EnvServiceName: "my-svc"}, ""},
		{"plain OTEL_SERVICE_NAME (CDK 0.0.17 and earlier)", map[string]string{store.EnvOdigosIaC: iac, EnvServiceName: "my-svc"}, "my-svc"},
		{"compact ODIGOS_LAMBDA_CONFIG (CDK 0.0.18 and later)", map[string]string{store.EnvOdigosIaC: iac, EnvLambdaConfig: compact}, "my-svc"},
		{"configuration embedded in the marker", map[string]string{store.EnvOdigosIaC: `{"v":1,"c":` + compact + `}`}, "my-svc"},
		{"unreadable configuration", map[string]string{store.EnvOdigosIaC: iac, EnvLambdaConfig: `{`}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LambdaServiceName(tc.env); got != tc.want {
				t.Errorf("LambdaServiceName = %q, want %q", got, tc.want)
			}
		})
	}
}
