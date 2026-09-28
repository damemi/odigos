package collectorconfig

import (
	"os"
	"strings"
	"testing"
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
	raw, err := os.ReadFile(builderConfig)
	if err != nil {
		// Fatal, not skipped: a silent skip turns this guard off exactly when the layout changes.
		t.Fatalf("read %s: %v", builderConfig, err)
	}
	manifest := string(raw)

	// Component name as it appears in the rendered document -> the Go module that provides it.
	for _, c := range []struct{ component, module string }{
		{receiverOTLP, "receiver/otlpreceiver"},
		{processorMemoryLimit, "processor/memorylimiterprocessor"},
		{processorBatch, "processor/batchprocessor"},
		{processorResourceDet, "processor/resourcedetectionprocessor"},
		{processorResource, "processor/resourceprocessor"},
		{exporterOTLP, "exporter/otlpexporter"},
		{exporterOTLPHTTP, "exporter/otlphttpexporter"},
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
	// Keyed by "section.component": the name spaces are per section, and otlp is both a receiver and an
	// exporter — different components that happen to share a name.
	known := map[string]bool{
		"receivers." + receiverOTLP:          true,
		"processors." + processorMemoryLimit: true,
		"processors." + processorBatch:       true,
		"processors." + processorResourceDet: true,
		"processors." + processorResource:    true,
		"exporters." + exporterOTLP:          true,
		"exporters." + exporterOTLPHTTP:      true,
		"exporters." + exporterNop:           true,
		"extensions." + extensionHealthCheck: true,
	}

	// Every shape the renderer can produce, so a component that only appears in one of them is still seen.
	inputs := []Input{
		input(nil),
		input(func(in *Input) { in.Destinations = nil }),
		input(func(in *Input) { in.Region = "" }),
		input(func(in *Input) {
			in.Destinations[0].Type = "otlphttp"
			in.Destinations[0].Config = map[string]any{"OTLP_HTTP_ENDPOINT": "https://otlp.vendor.example.com"}
		}),
	}

	for _, in := range inputs {
		doc := parse(t, render(t, in).YAML)
		for _, section := range []string{"receivers", "processors", "exporters", "extensions"} {
			for name := range doc[section].(map[string]any) {
				if !known[section+"."+name] {
					t.Errorf("the rendered document names %s.%s, which TestEveryRenderedComponentIsCompiledIn "+
						"does not check is compiled into the Collector image", section, name)
				}
			}
		}
	}
}
