package detector

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"

	store "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/workload/store"
)

func env(name, value string) *armappcontainers.EnvironmentVar {
	return &armappcontainers.EnvironmentVar{Name: to.Ptr(name), Value: to.Ptr(value)}
}

func appWithEnv(vars ...*armappcontainers.EnvironmentVar) *armappcontainers.ContainerApp {
	return &armappcontainers.ContainerApp{Properties: &armappcontainers.ContainerAppProperties{
		Template: &armappcontainers.Template{
			Containers: []*armappcontainers.Container{{Name: to.Ptr("app"), Env: vars}},
		},
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
		app  *armappcontainers.ContainerApp
		want bool
	}{
		{
			name: "marker present",
			app:  appWithEnv(env(store.EnvOdigosManaged, `{"v":1,"lang":"java"}`)),
			want: true,
		},
		{
			// The odigos-namespaced destination var is a value we own, not proof that we own the workload.
			// A leftover from a partially-reverted instrumentation must not read as instrumented.
			name: "odigos collector endpoint but no marker",
			app:  appWithEnv(env(store.EnvCollectorEndpoint, "collector:4317")),
			want: false,
		},
		{
			name: "customer's own OpenTelemetry setup",
			app: appWithEnv(
				env("OTEL_EXPORTER_OTLP_ENDPOINT", "https://customer-collector:4317"),
				env("OTEL_SERVICE_NAME", "their-app"),
			),
			want: false,
		},
		{
			name: "marker on a non-first container",
			app: &armappcontainers.ContainerApp{Properties: &armappcontainers.ContainerAppProperties{
				Template: &armappcontainers.Template{Containers: []*armappcontainers.Container{
					{Name: to.Ptr("sidecar")},
					{Name: to.Ptr("app"), Env: []*armappcontainers.EnvironmentVar{env(store.EnvOdigosManaged, "{}")}},
				}},
			}},
			want: true,
		},
		{
			name: "nil container and nil env entries are skipped, not panicked on",
			app: &armappcontainers.ContainerApp{Properties: &armappcontainers.ContainerAppProperties{
				Template: &armappcontainers.Template{Containers: []*armappcontainers.Container{
					nil,
					{Name: to.Ptr("app"), Env: []*armappcontainers.EnvironmentVar{nil, {Name: nil}}},
				}},
			}},
			want: false,
		},
		{
			name: "no template",
			app:  &armappcontainers.ContainerApp{Properties: &armappcontainers.ContainerAppProperties{}},
			want: false,
		},
		{
			name: "no properties",
			app:  &armappcontainers.ContainerApp{},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := odigosInstrumented(c.app); got != c.want {
				t.Errorf("odigosInstrumented = %v, want %v", got, c.want)
			}
		})
	}
}
