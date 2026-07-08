package store

import "testing"

// TestOdigosInstrumented guards the ownership predicate that gates rollback (issue 1): only our marker or
// our odigos-namespaced collector vars count as Odigos-owned. A customer's own OpenTelemetry/ADOT exec
// wrapper must NOT be treated as ours, or rollback would strip a customer's instrumentation.
func TestOdigosInstrumented(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"nil env", nil, false},
		{"empty env", map[string]string{}, false},
		{"marker present", map[string]string{EnvOdigosManaged: `{"v":1}`}, true},
		{"odigos collector endpoint", map[string]string{envCollectorEndpoint: "collector:4317"}, true},
		{"odigos collector insecure", map[string]string{envCollectorInsecure: "true"}, true},
		// Customer's own OTel/ADOT setup — not ours, must be left alone.
		{"customer exec wrapper only", map[string]string{EnvExecWrapper: "/opt/otel-instrument"}, false},
		{"customer otel service name only", map[string]string{"OTEL_SERVICE_NAME": "svc"}, false},
	}
	for _, tc := range cases {
		if got := OdigosInstrumented(tc.env); got != tc.want {
			t.Errorf("%s: OdigosInstrumented = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSplitRuntime(t *testing.T) {
	cases := map[string][2]string{
		"java17":       {"java", "17"},
		"python3.11":   {"python", "3.11"},
		"nodejs20.x":   {"nodejs", "20.x"},
		"ruby3.3":      {"ruby", "3.3"},
		"dotnet8":      {"dotnet", "8"},
		"go1.x":        {"go", "1.x"},
		"provided.al2": {"provided.al", "2"},
		"":             {"", ""},
	}
	for runtime, want := range cases {
		lang, ver := splitRuntime(runtime)
		if lang != want[0] || ver != want[1] {
			t.Errorf("splitRuntime(%q) = (%q,%q), want (%q,%q)", runtime, lang, ver, want[0], want[1])
		}
	}
}
