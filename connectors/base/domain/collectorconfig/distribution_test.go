package collectorconfig

import (
	"os"
	"strings"
	"testing"

	odigosconfig "github.com/odigos-io/odigos/common/config"
	odigosdest "github.com/odigos-io/odigos/destinations"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoraction"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

// builderConfig is the OpenTelemetry Collector Builder manifest for the enterprise Collector — the list
// of components actually compiled into the image a managed Collector runs.
const builderConfig = "../../../../collector/builder-config.yaml"

// TestEveryRenderedComponentIsCompiledIn.
//
// A remote configuration can only name components the binary already has. Naming one it does not is not
// a degraded pipeline: the Collector refuses the whole document, the Supervisor rolls back to the last
// good one, and the Collector reports healthy while running a configuration Odigos did not send — so
// the symptom is "Odigos changed nothing", nowhere near the component that was dropped.
//
// Nothing else catches it. This package renders a document and never runs a Collector; the builder
// manifest lives in another directory and is edited for unrelated reasons.
func TestEveryRenderedComponentIsCompiledIn(t *testing.T) {
	manifest := readManifest(t)

	// Component name as it appears in the rendered document -> the Go module that provides it. These are
	// the Collector's own; what destinations add is checked by TestEveryCatalogTypeRendersCompiledInComponents.
	for _, c := range []struct{ component, module string }{
		{receiverOTLP, "receiver/otlpreceiver"},
		{processorMemoryLimit, "processor/memorylimiterprocessor"},
		{processorBatch, "processor/batchprocessor"},
		{processorResourceDet, "processor/resourcedetectionprocessor"},
		{processorResource, "processor/resourceprocessor"},
		{exporterNop, "exporter/nopexporter"},
		{extensionHealthCheck, "extension/healthcheckextension"},
	} {
		if !strings.Contains(manifest, c.module) {
			t.Errorf("the rendered configuration can name %q, but %s declares no %s",
				c.component, builderConfig, c.module)
		}
	}
}

// TestRenderedDocumentNamesNothingUnexpected: the check above is a list, and a list only covers what is
// on it. This walks an actual document and fails on any component name the list does not know, so a
// component added to the renderer cannot skip the compiled-in check by not being added here too.
func TestRenderedDocumentNamesNothingUnexpected(t *testing.T) {
	manifest := readManifest(t)

	// Every shape the renderer can produce, so a component that only appears in one of them is still seen.
	inputs := []Input{
		input(nil),
		input(func(in *Input) { in.Destinations = nil }),
		input(func(in *Input) { in.Region = "" }),
		input(func(in *Input) {
			in.Destinations[0].Type = "otlphttp"
			in.Destinations[0].Config = map[string]any{"OTLP_HTTP_ENDPOINT": "https://otlp.vendor.example.com"}
		}),
		input(func(in *Input) {
			in.Actions = []collectoraction.Action{action(t, "latency", collectoraction.TypeSpanMetrics, []string{"TRACES"}, map[string]any{})}
		}),
	}
	for _, in := range inputs {
		assertCompiledIn(t, manifest, "", render(t, in).YAML)
	}
}

// TestEveryCatalogTypeRendersCompiledInComponents renders each type the managed Collector offers, with
// every field given a plausible value, and checks each component the configurer named against the
// builder manifest. The catalog is the Odigos one and grows without this package changing; this is what
// notices when it grows a type whose exporter the image does not ship.
//
// A type whose configurer refuses every placeholder is logged rather than failed: its components are
// unknown, not wrong, and the write-time dry run refuses such a destination anyway.
func TestEveryCatalogTypeRendersCompiledInComponents(t *testing.T) {
	manifest := readManifest(t)
	if err := odigosdest.Load(); err != nil {
		t.Fatal(err)
	}
	configers, err := odigosconfig.LoadConfigers()
	if err != nil {
		t.Fatal(err)
	}

	for _, spec := range odigosdest.Get() {
		if _, ok := configers[spec.Metadata.Type]; !ok {
			continue
		}
		var signals []string
		for sig, sup := range map[string]bool{
			"TRACES": spec.Spec.Signals.Traces.Supported, "METRICS": spec.Spec.Signals.Metrics.Supported,
			"LOGS": spec.Spec.Signals.Logs.Supported,
		} {
			if sup {
				signals = append(signals, sig)
			}
		}
		if len(signals) == 0 {
			continue
		}

		// The excluded types are rendered with the exclusion lifted, so the list is checked in both
		// directions: an entry whose components became available fails here as stale.
		reason, excluded := unavailableTypes[spec.Metadata.Type]
		delete(unavailableTypes, spec.Metadata.Type)
		doc, rendered := renderWithPlaceholders(spec, signals)
		if excluded {
			unavailableTypes[spec.Metadata.Type] = reason
		}
		if !rendered {
			t.Logf("%s: no placeholder values its configurer accepts; its components are unchecked", spec.Metadata.Type)
			continue
		}

		missing := missingComponents(t, manifest, doc)
		switch {
		case excluded && len(missing) == 0:
			t.Errorf("%s is listed in unavailableTypes, but everything it renders is compiled in", spec.Metadata.Type)
		case !excluded && len(missing) > 0:
			t.Errorf("%s renders %v, which %s does not declare; add it to unavailableTypes or to the image",
				spec.Metadata.Type, missing, builderConfig)
		}
	}
}

// endpointPlaceholders are tried in turn, because configurers disagree on the endpoint grammar: a URL,
// a bare host:port, or a URL with a vendor-specific path.
var endpointPlaceholders = []string{
	"https://placeholder.example.com:4318",
	"placeholder.example.com:4317",
	"https://placeholder.example.com/api/prom/push",
}

func renderWithPlaceholders(spec odigosdest.Destination, signals []string) (string, bool) {
	for _, endpoint := range endpointPlaceholders {
		d := destination.Destination{Name: "probe", Type: string(spec.Metadata.Type), Config: map[string]any{}, Signals: signals}
		for _, f := range spec.Spec.Fields {
			d.Config[f.Name] = placeholder(f, endpoint)
		}
		doc, problems := Render(input(func(in *Input) { in.Destinations = []destination.Destination{d} }))
		if len(problems) == 0 {
			return doc.YAML, true
		}
	}
	return "", false
}

// componentModules maps a component type to the module suffix the builder names it by, where the two are
// not simply type+kind.
var componentModules = map[string]string{
	"otlp_grpc":    "otlpexporter",
	"otlp_http":    "otlphttpexporter",
	"oauth2client": "oauth2clientauthextension",
	"spanmetrics":  "spanmetricsconnector",
}

func assertCompiledIn(t *testing.T, manifest, destType, doc string) {
	t.Helper()
	for _, c := range missingComponents(t, manifest, doc) {
		t.Errorf("%s: the rendered document names %s, which %s does not declare", destType, c, builderConfig)
	}
}

// missingComponents lists the components a document names whose module the builder manifest does not.
func missingComponents(t *testing.T, manifest, doc string) []string {
	t.Helper()
	parsed := parse(t, doc)
	var missing []string
	for section, kind := range map[string]string{
		"receivers": "receiver", "processors": "processor", "exporters": "exporter",
		"extensions": "extension", "connectors": "connector",
	} {
		components, _ := parsed[section].(map[string]any)
		for name := range components {
			componentType := strings.SplitN(name, "/", 2)[0]
			module, ok := componentModules[componentType]
			if !ok {
				module = strings.ReplaceAll(componentType, "_", "") + kind
			}
			if !strings.Contains(manifest, "/"+module+" ") {
				missing = append(missing, section+"."+name)
			}
		}
	}
	return missing
}

// placeholder is a value a configurer is likely to accept for a field: its declared initial value, the
// first option of a dropdown, or an endpoint-shaped string.
func placeholder(f odigosdest.Field, endpoint string) string {
	if f.InitialValue != "" {
		return f.InitialValue
	}
	if values, ok := f.ComponentProps["values"].([]any); ok && len(values) > 0 {
		if v, ok := values[0].(string); ok {
			return v
		}
		if m, ok := values[0].(map[string]any); ok {
			if v, ok := m["id"].(string); ok {
				return v
			}
		}
	}
	switch f.ComponentType {
	case "checkbox":
		return "false"
	case "keyValuePairs":
		return "[]"
	case "multiInput":
		return `["` + endpoint + `"]`
	}
	return endpoint
}

func readManifest(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(builderConfig)
	if err != nil {
		// Fatal, not skipped: a silent skip turns this guard off exactly when the layout changes.
		t.Fatalf("read %s: %v", builderConfig, err)
	}
	return string(raw)
}
