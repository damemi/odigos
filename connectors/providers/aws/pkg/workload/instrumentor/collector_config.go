package instrumentor

import (
	"fmt"
	"strconv"
	"strings"

	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
)

// The in-Lambda collector's configuration, rendered per function and delivered inline through the collector
// layer's own OPENTELEMETRY_COLLECTOR_CONFIG_URI.
//
// The layer still ships a baked /opt/collector-config/config.yaml, but that file is ONE document shared by
// every function in every account, so it can only reach a destination through env indirection —
// "${env:SOMETHING}" — and every name it could indirect through is a name this connector invented. That
// indirection is what made three separate things impossible at once:
//
//   - AUTH. A destination's credential is a headers map, not a scalar. A baked file can substitute a value
//     but cannot conditionally grow a `headers:` block, so an authenticated destination was unreachable.
//   - OTLP/HTTP DESTINATIONS. The baked file hardcodes a gRPC `otlp` exporter. An otlphttp-only destination
//     resolves with an empty gRPC endpoint, and the collector does not degrade — it fails to build the
//     pipeline and exits, so the function is instrumented, reported conformant, and exports nothing.
//   - PER-SIGNAL PIPELINES. The baked file declares all three pipelines unconditionally, so a signal the
//     destination does not accept is received and then fails on export rather than never being built.
//
// Rendering the whole document per function removes the indirection instead of adding more of it, which is
// also why the only Odigos-namespaced variable left on an instrumented function is ODIGOS_MANAGED. Every
// other variable is either the collector layer's own or the OpenTelemetry SDK's.

// EnvCollectorConfigURI is the collector layer's own config-source variable (upstream's name, not ours). Set
// to a "yaml:" URI it carries the entire configuration inline, so no second variable is needed to hold it.
//
// Upstream also reads the older OPENTELEMETRY_COLLECTOR_CONFIG_FILE and logs a deprecation warning for it;
// this is the current name.
const EnvCollectorConfigURI = "OPENTELEMETRY_COLLECTOR_CONFIG_URI"

// collectorConfigScheme is the confmap provider that reads a configuration out of the URI itself. The
// collector registers env/file/yaml/http/https/s3 providers; `yaml:` is the only one that needs no second
// variable and no network read on a cold start.
const collectorConfigScheme = "yaml:"

// renderCollectorConfig builds the complete collector configuration for one destination as an inline
// OPENTELEMETRY_COLLECTOR_CONFIG_URI value.
//
// The document is emitted in FLOW style (a single line) because it has to survive as an environment variable
// value. That carries one non-obvious rule: in a YAML flow mapping a colon must be followed by a space, or
// the parser reads "a:b" as the plain scalar "a:b" rather than a mapping. Written without the spaces the
// collector accepts the variable, reports the URI it read, and then fails to start with a bare
// "otelcol state is Closed" — no parse error, no mention of the config. Every ": " below is load-bearing.
func renderCollectorConfig(dest domaindest.OTLPTarget, headers map[string]string) (string, error) {
	exporter, err := renderExporter(dest, headers)
	if err != nil {
		return "", err
	}
	pipelines := renderPipelines(dest, exporterNameFor(dest))
	if pipelines == "" {
		if dest.Profiles {
			return "", fmt.Errorf("destination selects only profiles: the bundled Lambda layer cannot generate or forward OTLP profiles")
		}
		return "", fmt.Errorf("destination accepts no telemetry signals, so the collector would have no pipeline to build")
	}

	var b strings.Builder
	b.WriteString(collectorConfigScheme)
	b.WriteString("{receivers: {otlp: {protocols: ")
	// Bound to localhost: the receiver exists only for the in-process SDK. Binding it wider would expose an
	// unauthenticated OTLP endpoint on the sandbox's network interface.
	b.WriteString(`{grpc: {endpoint: "localhost:4317"}, http: {endpoint: "localhost:4318"}}}}, `)
	b.WriteString("processors: ")
	b.WriteString(collectorProcessors)
	b.WriteString(", exporters: ")
	b.WriteString(exporter)
	b.WriteString(", service: {telemetry: {metrics: {level: none}}, pipelines: ")
	b.WriteString(pipelines)
	b.WriteString("}}")
	return b.String(), nil
}

// collectorProcessors mirrors the baked config's processor set, and the ORDER is the contract:
//
//   - memory_limiter first, so an export backlog is shed before it is charged to the same memory budget the
//     customer's handler allocates from. Percentages, not limit_mib, because one layer serves functions from
//     128MB to 10GB.
//   - decouple LAST. Lambda freezes the sandbox once the handler returns and every extension has called
//     /next; a frozen sandbox runs no timers, so a batch sitting behind its timeout is flushed on a later
//     invocation or dropped when the sandbox is reaped. decouple acknowledges the receive path immediately
//     and tracks in-flight exports, which is what lets the extension hold /next open until the queue drains
//     — after the caller already has its response.
const collectorProcessors = `{memory_limiter: {check_interval: 1s, limit_percentage: 50, spike_limit_percentage: 20}, ` +
	`batch: {send_batch_size: 512, timeout: 200ms}, decouple: null}`

// exporterNameFor picks the exporter by the transport the destination actually configured. A destination
// carries a gRPC endpoint (type otlp) or an OTLP/HTTP one (type otlphttp); the two are different collector
// components with different endpoint grammars — host:port versus a scheme-qualified URL — so this is a
// genuine branch and not a formatting difference.
func exporterNameFor(dest domaindest.OTLPTarget) string {
	if dest.Endpoint != "" {
		return "otlp"
	}
	return "otlphttp"
}

func renderExporter(dest domaindest.OTLPTarget, headers map[string]string) (string, error) {
	var b strings.Builder
	switch {
	case dest.Endpoint != "":
		b.WriteString(`{otlp: {endpoint: `)
		b.WriteString(yamlString(dest.Endpoint))
		// tls.insecure applies to the gRPC exporter only; the HTTP exporter takes the scheme from the URL.
		b.WriteString(`, tls: {insecure: `)
		b.WriteString(strconv.FormatBool(dest.Insecure))
		b.WriteString(`}`)
	case dest.HTTPEndpoint != "":
		b.WriteString(`{otlphttp: {endpoint: `)
		b.WriteString(yamlString(dest.HTTPEndpoint))
	default:
		// Reached when a destination resolves with neither transport. Previously this produced an empty
		// "${env:ODIGOS_OTLP_ENDPOINT}" and a collector that exited on startup while the function was still
		// reported conformant; failing here instead surfaces it as an instrumentation error on the workload.
		return "", fmt.Errorf("destination has neither an OTLP gRPC endpoint nor an OTLP/HTTP endpoint, so the in-Lambda collector has nowhere to export")
	}
	b.WriteString(renderHeaders(headers))
	b.WriteString(`}}`)
	return b.String(), nil
}

// renderHeaders emits the destination's per-request headers — in practice its credential. Empty when the
// destination needs none, so no header block is written at all rather than an empty one.
func renderHeaders(headers map[string]string) string {
	if len(headers) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(`, headers: {`)
	for i, k := range sortedKeys(headers) {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(yamlString(k))
		b.WriteString(": ")
		b.WriteString(yamlString(headers[k]))
	}
	b.WriteString(`}`)
	return b.String()
}

// renderPipelines builds a pipeline only for the signals the destination accepts. A signal with no pipeline
// is refused at the receiver, which is the behavior we want: the SDK is told not to produce it either (see
// applySignalExporters), and the two together mean an unaccepted signal is never generated, never exported,
// and never a per-batch error against a backend that does not implement it.
func renderPipelines(dest domaindest.OTLPTarget, exporterName string) string {
	var parts []string
	for _, sig := range []struct {
		name    string
		enabled bool
	}{
		{"traces", dest.Traces},
		{"metrics", dest.Metrics},
		{"logs", dest.Logs},
	} {
		if !sig.enabled {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: {receivers: [otlp], processors: [memory_limiter, batch, decouple], exporters: [%s]}",
			sig.name, exporterName))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// yamlString renders a double-quoted YAML scalar. Go's quoting and YAML's double-quoted style share the
// escape vocabulary that matters here (\" and \\), and quoting unconditionally keeps values such as a bare
// "host:4317" from being re-read as a nested mapping in flow context.
func yamlString(s string) string {
	return strconv.Quote(s)
}

// sortedMapKeys is sortedKeys for a map whose keys are the interesting part — used to list the valid distro
// override values in an error a customer reads.
func sortedMapKeys(m map[string]string) []string { return sortedKeys(m) }

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Stable order so the rendered config is byte-identical across reconciles; the config string is compared
	// against the live env to decide whether an update is needed, and map order would otherwise make every
	// pass look like drift and churn a new function version.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
