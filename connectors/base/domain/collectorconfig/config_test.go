package collectorconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoringress"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares a rendered document against testdata. The document is the contract with a running
// Collector, so it is reviewed as a file rather than asserted field by field — a diff here is a change
// to what a customer's Collector will actually do.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".yaml")

	if *update {
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatalf("update %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run: go test ./domain/collectorconfig -update)", path, err)
	}
	if got != string(want) {
		t.Errorf("%s does not match the golden file.\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}

func otlpDestination(mutate func(*destination.Destination)) destination.Destination {
	d := destination.Destination{
		Name:    "customer-backend",
		Type:    "otlp",
		Config:  map[string]any{"OTLP_GRPC_ENDPOINT": "otlp.vendor.example.com:4317"},
		Signals: []string{"TRACES", "METRICS", "LOGS"},
	}
	if mutate != nil {
		mutate(&d)
	}
	return d
}

func input(mutate func(*Input)) Input {
	in := Input{
		Destinations:      []destination.Destination{otlpDestination(nil)},
		Region:            "us-east-1",
		MemoryMiB:         2048,
		ResourceDetectors: []string{"ecs"},
	}
	if mutate != nil {
		mutate(&in)
	}
	return in
}

func render(t *testing.T, in Input) Document {
	t.Helper()
	doc, problems := Render(in)
	if collectorstatus.HasError(problems) {
		t.Fatalf("Render refused: %v", problems)
	}
	return doc
}

// TestGolden pins the rendered document for the shapes that matter. The three destination cases are the
// ones the per-workload rendering documents as what environment-variable indirection got wrong: an
// authenticated destination (a credential is a map, not a scalar), an OTLP/HTTP-only destination (a
// different exporter with a different endpoint grammar), and a traces-only destination (a pipeline for
// a signal the backend does not implement fails on every batch).
func TestGolden(t *testing.T) {
	cases := map[string]Input{
		"default": input(nil),

		"authenticated_destination": input(func(in *Input) {
			in.Destinations = []destination.Destination{otlpDestination(func(d *destination.Destination) {
				d.Config["OTLP_GRPC_HEADERS"] = `[{"key":"authorization","value":"Bearer vendor-token"},` +
					`{"key":"x-tenant","value":"acme"}]`
			})}
		}),

		"http_only_destination": input(func(in *Input) {
			in.Destinations = []destination.Destination{{
				Name:    "customer-backend",
				Type:    "otlphttp",
				Config:  map[string]any{"OTLP_HTTP_ENDPOINT": "https://otlp.vendor.example.com"},
				Signals: []string{"TRACES", "METRICS"},
			}}
		}),

		"traces_only_destination": input(func(in *Input) {
			in.Destinations = []destination.Destination{otlpDestination(func(d *destination.Destination) {
				d.Signals = []string{"TRACES"}
			})}
		}),

		"no_destination": input(func(in *Input) { in.Destinations = nil }),

		"collector_terminated_tls": input(func(in *Input) {
			in.TLSMode = collectoringress.TLSCollector
			in.CertificateFile = "/etc/odigos/collector/tls/tls.crt"
			in.PrivateKeyFile = "/etc/odigos/collector/tls/tls.key"
		}),

		"small_allocation": input(func(in *Input) { in.MemoryMiB = 1024 }),

		"no_region": input(func(in *Input) { in.Region = "" }),
	}

	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			golden(t, name, render(t, in).YAML)
		})
	}
}

// TestRenderedDocumentIsValidYAML: a document the Collector cannot parse is refused at the Supervisor
// and rolled back, so the failure surfaces as "the last configuration is still running" rather than as
// anything naming this package.
func TestRenderedDocumentIsValidYAML(t *testing.T) {
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(render(t, input(nil)).YAML), &parsed); err != nil {
		t.Fatalf("the rendered document is not valid YAML: %v", err)
	}
	for _, key := range []string{"receivers", "processors", "exporters", "extensions", "service"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("the rendered document has no %s section", key)
		}
	}
	// The Supervisor rewrites service::telemetry for its own metrics after merging this document, so
	// anything written there is either overwritten or fought over.
	svc := parsed["service"].(map[string]any)
	if _, present := svc["telemetry"]; present {
		t.Error("the rendered document writes service.telemetry, which the Supervisor owns")
	}
}

// TestNoUnsupportedPipelines is the criterion in its own right: a pipeline for a signal the destination
// does not accept is received, batched, and then fails on export against a backend that will never take
// it — repeatedly, at volume.
func TestNoUnsupportedPipelines(t *testing.T) {
	cases := []struct {
		name    string
		signals []string
		want    []string
	}{
		{name: "all three", signals: []string{"TRACES", "METRICS", "LOGS"}, want: []string{"logs", "metrics", "traces"}},
		{name: "traces only", signals: []string{"TRACES"}, want: []string{"traces"}},
		{name: "metrics only", signals: []string{"METRICS"}, want: []string{"metrics"}},
		{name: "logs only", signals: []string{"LOGS"}, want: []string{"logs"}},
		{name: "traces and logs", signals: []string{"TRACES", "LOGS"}, want: []string{"logs", "traces"}},
		{
			// ResolveOTLPTarget falls back to traces-only for a destination that records no signals:
			// traces are the one signal every OTLP backend implements.
			name: "no signals recorded", signals: []string{}, want: []string{"traces"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input(func(in *Input) {
				in.Destinations = []destination.Destination{otlpDestination(func(d *destination.Destination) {
					d.Signals = tc.signals
				})}
			})
			got := pipelineNames(t, render(t, in).YAML)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("pipelines = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWorkloadResourceAttributesArePreserved is the criterion this Collector's whole position depends
// on. It is a gateway: telemetry from many workloads passes through one process, each carrying the
// identity its own SDK set.
//
// resourcedetection defaults to override: true, which would stamp this Collector's region, host and
// task identity over every one of them — so a hundred services would arrive describing one Collector.
// The resource processor is `insert` for the same reason.
func TestWorkloadResourceAttributesArePreserved(t *testing.T) {
	doc := parse(t, render(t, input(nil)).YAML)
	processors := doc["processors"].(map[string]any)

	detection := processors[processorResourceDet].(map[string]any)
	override, ok := detection["override"]
	if !ok {
		t.Fatal("resourcedetection does not set override, and it defaults to true")
	}
	if override != false {
		t.Errorf("resourcedetection override = %v, want false", override)
	}

	resource := processors[processorResource].(map[string]any)
	for _, raw := range resource["attributes"].([]any) {
		attr := raw.(map[string]any)
		if attr["action"] != "insert" {
			t.Errorf("resource attribute %v uses action %q; only insert leaves what the workload sent",
				attr["key"], attr["action"])
		}
	}
}

// TestMemoryLimiterIsDerivedFromTheAllocation: the sizes are §8.1's, and memory_limiter is the reason a
// size change is a configuration push as well as a workload change.
func TestMemoryLimiterIsDerivedFromTheAllocation(t *testing.T) {
	cases := []struct {
		allocation, limit, spike int
	}{
		{allocation: 1024, limit: 819, spike: 163},  // small
		{allocation: 2048, limit: 1638, spike: 327}, // default
		{allocation: 4096, limit: 3276, spike: 655}, // large
		{allocation: 256, limit: 204, spike: 40},    // the floor
	}

	for _, tc := range cases {
		limits, problems := DeriveMemoryLimits(tc.allocation)
		if collectorstatus.HasError(problems) {
			t.Fatalf("%d MiB was refused: %v", tc.allocation, problems)
		}
		if limits.LimitMiB != tc.limit || limits.SpikeLimitMiB != tc.spike {
			t.Errorf("%d MiB -> limit %d spike %d, want limit %d spike %d",
				tc.allocation, limits.LimitMiB, limits.SpikeLimitMiB, tc.limit, tc.spike)
		}
		// The hard limit must leave real headroom: the process sits above limit_mib in RSS and the
		// Supervisor shares the same allocation, so a limit at the allocation is reached only after the
		// kernel has already killed the task.
		if limits.LimitMiB >= tc.allocation {
			t.Errorf("%d MiB allocation produced a %d MiB limit, leaving no headroom", tc.allocation, limits.LimitMiB)
		}
		if limits.SpikeLimitMiB >= limits.LimitMiB {
			t.Errorf("spike limit %d is not below the hard limit %d, so the soft limit never triggers first",
				limits.SpikeLimitMiB, limits.LimitMiB)
		}
	}
}

// TestTooLittleMemoryIsRefused: below the floor the derived limits refuse telemetry as fast as the
// receiver accepts it, which reads as a broken destination rather than as a Collector given too little.
func TestTooLittleMemoryIsRefused(t *testing.T) {
	for _, allocation := range []int{0, -1, 64, MinAllocationMiB - 1} {
		doc, problems := Render(input(func(in *Input) { in.MemoryMiB = allocation }))
		if !collectorstatus.HasError(problems) {
			t.Errorf("%d MiB was accepted", allocation)
		}
		if doc.YAML != "" || doc.Hash != "" {
			t.Errorf("%d MiB was refused but still produced a document", allocation)
		}
	}
}

// TestCollectorTerminatedTLSNeedsMaterial: rendering plaintext receivers when the mode says the
// Collector terminates would publish a TLS endpoint that answers in the clear.
func TestCollectorTerminatedTLSNeedsMaterial(t *testing.T) {
	for name, mutate := range map[string]func(*Input){
		"neither": func(in *Input) { in.TLSMode = collectoringress.TLSCollector },
		"no key": func(in *Input) {
			in.TLSMode = collectoringress.TLSCollector
			in.CertificateFile = "/etc/odigos/collector/tls/tls.crt"
		},
		"no certificate": func(in *Input) {
			in.TLSMode = collectoringress.TLSCollector
			in.PrivateKeyFile = "/etc/odigos/collector/tls/tls.key"
		},
	} {
		t.Run(name, func(t *testing.T) {
			doc, problems := Render(input(mutate))
			if !collectorstatus.HasError(problems) {
				t.Error("collector-terminated TLS was rendered without material")
			}
			if doc.YAML != "" {
				t.Error("a refused render still produced a document")
			}
		})
	}
}

// TestOnlyCollectorModeTerminatesTLS: in every other mode something in front terminates, and receivers
// that also terminated would be a second, unmanaged certificate to rotate.
func TestOnlyCollectorModeTerminatesTLS(t *testing.T) {
	for _, mode := range []collectoringress.TLSMode{
		"", collectoringress.TLSOff, collectoringress.TLSPlatform, collectoringress.TLSLoadBalancer,
	} {
		in := input(func(in *Input) {
			in.TLSMode = mode
			// Material present but unused: the mode decides, not the presence of a certificate.
			in.CertificateFile = "/etc/odigos/collector/tls/tls.crt"
			in.PrivateKeyFile = "/etc/odigos/collector/tls/tls.key"
		})
		if strings.Contains(render(t, in).YAML, "cert_file") {
			t.Errorf("mode %q rendered TLS on the receivers", mode)
		}
	}
}

// TestHashIsStable is what makes a push mean something: the same input renders the same bytes, so the
// steady state is zero pushes rather than one per reconcile.
func TestHashIsStable(t *testing.T) {
	first := render(t, input(nil))
	for i := 0; i < 50; i++ {
		again := render(t, input(nil))
		if again.Hash != first.Hash {
			t.Fatalf("hash changed between renders on pass %d: %s then %s", i, first.Hash, again.Hash)
		}
		if again.YAML != first.YAML {
			t.Fatalf("the document changed between renders on pass %d", i)
		}
	}

	// Headers and detectors are the two places a map or a caller's slice could leak iteration order into
	// the bytes. Many headers, rendered repeatedly, is where that would show.
	withHeaders := input(func(in *Input) {
		in.ResourceDetectors = []string{"ecs", "env", "system"}
		in.Destinations = []destination.Destination{otlpDestination(func(d *destination.Destination) {
			d.Config["OTLP_GRPC_HEADERS"] = `[{"key":"authorization","value":"Bearer t"},` +
				`{"key":"x-a","value":"1"},{"key":"x-b","value":"2"},{"key":"x-c","value":"3"},` +
				`{"key":"x-d","value":"4"},{"key":"x-e","value":"5"},{"key":"x-f","value":"6"}]`
		})}
	})
	base := render(t, withHeaders)
	for i := 0; i < 50; i++ {
		if got := render(t, withHeaders); got.Hash != base.Hash {
			t.Fatalf("hash changed with headers on pass %d", i)
		}
	}
}

// TestHashTracksTheDocument: a hash that did not move when the document did would leave a Collector on
// a stale configuration for ever, and one that moved when the document did not would push on every
// reconcile.
func TestHashTracksTheDocument(t *testing.T) {
	base := render(t, input(nil))

	changes := map[string]func(*Input){
		"memory":     func(in *Input) { in.MemoryMiB = 4096 },
		"region":     func(in *Input) { in.Region = "eu-west-1" },
		"detectors":  func(in *Input) { in.ResourceDetectors = []string{"ec2"} },
		"signals":    func(in *Input) { in.Destinations[0].Signals = []string{"TRACES"} },
		"endpoint":   func(in *Input) { in.Destinations[0].Config["OTLP_GRPC_ENDPOINT"] = "other.example.com:4317" },
		"credential": func(in *Input) { in.Destinations[0].Config["OTLP_GRPC_HEADERS"] = `[{"key":"a","value":"b"}]` },
	}
	for name, mutate := range changes {
		t.Run(name, func(t *testing.T) {
			got := render(t, input(mutate))
			if got.Hash == base.Hash {
				t.Errorf("changing the %s did not change the hash", name)
			}
			if got.Hash != sha256Of(got.YAML) {
				t.Error("the hash is not the hash of the document")
			}
		})
	}

	// And a change that does not reach the document must not move the hash. A destination's display
	// name is the customer's label for it, not part of what the Collector runs.
	renamed := render(t, input(func(in *Input) { in.Destinations[0].Name = "renamed-in-the-ui" }))
	if renamed.Hash != base.Hash {
		t.Error("renaming a destination pushed a new configuration")
	}
}

// TestNoCredentialInTheDocumentUnlessTheDestinationNeedsOne: the exporter's headers ARE the destination's
// credential, so they belong in the document — but only when the destination configured them, and never
// synthesized from anywhere else.
func TestCredentialAppearsOnlyWhenConfigured(t *testing.T) {
	plain := render(t, input(nil)).YAML
	if strings.Contains(plain, "headers") {
		t.Error("a destination with no credential rendered a headers block")
	}

	authenticated := render(t, input(func(in *Input) {
		in.Destinations = []destination.Destination{otlpDestination(func(d *destination.Destination) {
			d.Config["OTLP_GRPC_HEADERS"] = `[{"key":"authorization","value":"Bearer vendor-token"}]`
		})}
	})).YAML
	if !strings.Contains(authenticated, "Bearer vendor-token") {
		t.Error("an authenticated destination lost its credential, so every export would be rejected")
	}
}

// TestNoDestinationStillRuns: unlike workload instrumentation a Collector is not gated on a destination.
// One that is running and reachable is what lets a destination be added as a configuration push rather
// than a redeployment.
func TestNoDestinationStillRuns(t *testing.T) {
	doc := render(t, input(func(in *Input) { in.Destinations = nil }))

	parsed := parse(t, doc.YAML)
	exporters := parsed["exporters"].(map[string]any)
	if _, ok := exporters[exporterNop]; !ok {
		t.Errorf("exporters = %v, want a nop exporter", exporters)
	}
	if names := pipelineNames(t, doc.YAML); len(names) != 3 {
		t.Errorf("pipelines = %v, want all three so any signal a workload sends is accepted", names)
	}
	// The health check has to be there, or the load balancer's targets never go healthy and no workload
	// can reach the Collector to send anything in the first place.
	extensions := parsed["extensions"].(map[string]any)
	if _, ok := extensions[extensionHealthCheck]; !ok {
		t.Error("no health_check extension, so the load balancer's targets never pass")
	}
}

// TestProcessorOrderIsTheContract: memory_limiter first so a backlog is shed at the door, batch last
// because it exists to feed the exporters, and attribute processors in between so attributes attach to
// spans rather than to whatever batch they landed in.
func TestProcessorOrderIsTheContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Input
		want []string
	}{
		{
			name: "with a region", in: input(nil),
			want: []string{processorMemoryLimit, processorResourceDet, processorResource, processorBatch},
		},
		{
			name: "without a region", in: input(func(in *Input) { in.Region = "" }),
			want: []string{processorMemoryLimit, processorResourceDet, processorBatch},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := parse(t, render(t, tc.in).YAML)
			pipelines := doc["service"].(map[string]any)["pipelines"].(map[string]any)

			for name, raw := range pipelines {
				var got []string
				for _, p := range raw.(map[string]any)["processors"].([]any) {
					got = append(got, p.(string))
				}
				if strings.Join(got, ",") != strings.Join(tc.want, ",") {
					t.Errorf("pipeline %s processors = %v, want %v", name, got, tc.want)
				}
			}

			// Every named processor must be defined, or the Collector refuses the whole configuration.
			defined := doc["processors"].(map[string]any)
			for _, name := range tc.want {
				if _, ok := defined[name]; !ok {
					t.Errorf("pipeline names processor %q, which the document does not define", name)
				}
			}
			if len(defined) != len(tc.want) {
				t.Errorf("the document defines %d processors but pipelines use %d", len(defined), len(tc.want))
			}
		})
	}
}

// TestEnvDetectorIsAlwaysPresent: OTEL_RESOURCE_ATTRIBUTES on the Collector's own workload is how a
// deployer labels it without this package knowing anything about the platform. Deduped so a caller
// passing "env" does not declare it twice, which the Collector rejects.
func TestEnvDetectorIsAlwaysPresent(t *testing.T) {
	for _, configured := range [][]string{nil, {}, {"ecs"}, {"env"}, {"env", "ecs"}, {"ecs", "env", "ecs"}} {
		in := input(func(in *Input) { in.ResourceDetectors = configured })
		doc := parse(t, render(t, in).YAML)
		detection := doc["processors"].(map[string]any)[processorResourceDet].(map[string]any)

		var got []string
		for _, d := range detection["detectors"].([]any) {
			got = append(got, d.(string))
		}
		if len(got) == 0 || got[0] != defaultResourceDetect {
			t.Errorf("detectors %v for input %v, want env first", got, configured)
		}
		seen := map[string]bool{}
		for _, d := range got {
			if seen[d] {
				t.Errorf("detector %q declared twice for input %v", d, configured)
			}
			seen[d] = true
		}
	}
}

func parse(t *testing.T, doc string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(doc), &out); err != nil {
		t.Fatalf("parse rendered document: %v", err)
	}
	return out
}

func pipelineNames(t *testing.T, doc string) []string {
	t.Helper()
	pipelines := parse(t, doc)["service"].(map[string]any)["pipelines"].(map[string]any)
	names := make([]string, 0, len(pipelines))
	for name := range pipelines {
		names = append(names, name)
	}
	// Sorted so the assertion does not depend on map order.
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
