package collectordestination

import (
	"strings"
	"testing"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

func valid() destination.Destination {
	return destination.Destination{
		Region:  "us-east-1",
		Name:    "vendor",
		Type:    "otlp",
		Config:  map[string]any{"OTLP_GRPC_ENDPOINT": "otlp.vendor.example.com:4317"},
		Signals: []string{"TRACES", "METRICS"},
	}
}

func TestValidate(t *testing.T) {
	if err := Validate(valid()); err != nil {
		t.Fatalf("a valid destination was refused: %v", err)
	}

	for name, tc := range map[string]struct {
		mutate func(*destination.Destination)
		want   string
	}{
		"no region":     {func(d *destination.Destination) { d.Region = "" }, "region"},
		"padded region": {func(d *destination.Destination) { d.Region = " us-east-1" }, "whitespace"},
		"component-unsafe name": {
			// The name becomes part of a component id; a space or slash would split or break it.
			func(d *destination.Destination) { d.Name = "my vendor/1" }, "letters, digits",
		},
		"route marker": {
			func(d *destination.Destination) { d.Type = destination.CatalogTypeManagedCollector }, "routes workloads",
		},
		"no signals":   {func(d *destination.Destination) { d.Signals = nil }, "at least one signal"},
		"profiles":     {func(d *destination.Destination) { d.Signals = []string{"PROFILES"} }, "not exported"},
		"unknown type": {func(d *destination.Destination) { d.Type = "no-such-vendor" }, "not available"},
		"type the image cannot run": {
			func(d *destination.Destination) {
				d.Type = "splunk"
				d.Config = map[string]any{"SPLUNK_REALM": "us0", "SPLUNK_ACCESS_TOKEN": "t"}
				d.Signals = []string{"TRACES"}
			}, "not available",
		},
		"missing required field": {
			func(d *destination.Destination) { d.Config = map[string]any{} }, "OTLP_GRPC_ENDPOINT",
		},
		"signal the type does not take": {
			func(d *destination.Destination) {
				d.Type = "jaeger"
				d.Config = map[string]any{"JAEGER_URL": "jaeger.example.com:4317"}
				d.Signals = []string{"TRACES", "METRICS"}
			}, "METRICS",
		},
		"value the configurer rejects": {
			// Present, so the catalog check passes — and still not an endpoint the configurer can use.
			func(d *destination.Destination) { d.Config["OTLP_GRPC_ENDPOINT"] = "://" }, "OTLP_GRPC_ENDPOINT",
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := valid()
			tc.mutate(&d)
			err := Validate(d)
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// TestTypes: the whole Odigos catalog a Collector can run, and nothing that would be accepted and then
// left out of its configuration.
func TestTypes(t *testing.T) {
	types, err := Types()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for i, dt := range types {
		seen[dt.Type] = true
		if i > 0 && types[i-1].Type >= dt.Type {
			t.Errorf("types are not ordered: %s before %s", types[i-1].Type, dt.Type)
		}
	}
	for _, want := range []string{"otlp", "otlphttp", "datadog", "honeycomb", "s3"} {
		if !seen[want] {
			t.Errorf("catalog is missing %s", want)
		}
	}
	for _, excluded := range []string{"splunk", "pyroscope", destination.CatalogTypeManagedCollector} {
		if seen[excluded] {
			t.Errorf("catalog offers %s, which a managed Collector cannot export to", excluded)
		}
	}
}

func TestApplyUpdateInputKeepsOmittedFields(t *testing.T) {
	base := valid()
	updated := ApplyUpdateInput(base, model.UpdateManagedCollectorDestinationInput{
		Region: base.Region, Name: base.Name, Signals: []model.TelemetrySignal{model.TelemetrySignalLogs},
	})
	if updated.Type != base.Type || updated.Config["OTLP_GRPC_ENDPOINT"] != base.Config["OTLP_GRPC_ENDPOINT"] {
		t.Errorf("an update that set only signals changed other fields: %+v", updated)
	}
	if len(updated.Signals) != 1 || updated.Signals[0] != "LOGS" {
		t.Errorf("signals = %v, want [LOGS]", updated.Signals)
	}

	typed := "OTLPHTTP"
	if got := ApplyUpdateInput(base, model.UpdateManagedCollectorDestinationInput{Type: &typed}); got.Type != "otlphttp" {
		t.Errorf("type = %q, want the normalized key", got.Type)
	}
}
