package detector

import (
	"testing"

	runpb "cloud.google.com/go/run/apiv2/runpb"

	store "github.com/odigos-io/odigos/connectors/providers/gcp/pkg/workload/store"
)

func env(name, value string) *runpb.EnvVar {
	return &runpb.EnvVar{Name: name, Values: &runpb.EnvVar_Value{Value: value}}
}

func svcWithEnv(vars ...*runpb.EnvVar) *runpb.Service {
	return &runpb.Service{Template: &runpb.RevisionTemplate{
		Containers: []*runpb.Container{{Name: "app", Env: vars}},
	}}
}

// TestOdigosInstrumentedRequiresMarker pins ownership to EnvOdigosManaged and nothing else.
//
// The instrumentor's mutation gate and rollback both key on the marker alone, so this must agree with them
// exactly. If discovery accepted a weaker signal, a workload carrying that signal without the marker would
// be called instrumented here and not-ours there: discovery would write instrumented=true every cycle and
// the reconcile would write false every cycle, forever, without ever converging or touching the workload.
func TestOdigosInstrumentedRequiresMarker(t *testing.T) {
	cases := []struct {
		name string
		svc  *runpb.Service
		want bool
	}{
		{
			name: "marker present",
			svc:  svcWithEnv(env(store.EnvOdigosManaged, `{"v":1,"lang":"java"}`)),
			want: true,
		},
		{
			// The odigos-namespaced destination var is a value we own, not proof that we own the workload.
			// A leftover from a partially-reverted instrumentation must not read as instrumented.
			name: "odigos collector endpoint but no marker",
			svc:  svcWithEnv(env(store.EnvCollectorEndpoint, "collector.example:4317")),
			want: false,
		},
		{
			name: "customer's own OpenTelemetry setup",
			svc: svcWithEnv(
				env("OTEL_EXPORTER_OTLP_ENDPOINT", "https://customer-collector:4317"),
				env("OTEL_SERVICE_NAME", "their-svc"),
				env("JAVA_TOOL_OPTIONS", "-javaagent:/their/agent.jar"),
			),
			want: false,
		},
		{
			name: "marker on a non-first container",
			svc: &runpb.Service{Template: &runpb.RevisionTemplate{Containers: []*runpb.Container{
				{Name: "sidecar"},
				{Name: "app", Env: []*runpb.EnvVar{env(store.EnvOdigosManaged, "{}")}},
			}}},
			want: true,
		},
		{
			name: "empty marker value still marks ownership",
			// The instrumentor always writes a JSON payload, but presence is what ownership means: a marker
			// whose value was clobbered is still unmistakably ours, and treating it as unowned would strand
			// the instrumentation with nothing willing to roll it back.
			svc:  svcWithEnv(env(store.EnvOdigosManaged, "")),
			want: true,
		},
		{
			name: "no containers",
			svc:  &runpb.Service{Template: &runpb.RevisionTemplate{}},
			want: false,
		},
		{
			name: "no template",
			svc:  &runpb.Service{},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := odigosInstrumented(c.svc); got != c.want {
				t.Errorf("odigosInstrumented = %v, want %v", got, c.want)
			}
		})
	}
}
