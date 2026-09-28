package destination

import "strings"

// Telemetry signal keys as stored on a Destination (match the GraphQL TelemetrySignal enum).
const (
	SignalTraces  = "TRACES"
	SignalMetrics = "METRICS"
	SignalLogs    = "LOGS"
)

// OTLP destination config keys (the otlp / otlphttp destination types' non-secret fields).
const (
	configOTLPGRPCEndpoint   = "OTLP_GRPC_ENDPOINT"
	configOTLPGRPCTLSEnabled = "OTLP_GRPC_TLS_ENABLED"
	configOTLPHTTPEndpoint   = "OTLP_HTTP_ENDPOINT"
)

// defaultOTLPHTTPPort is the standard OTLP/HTTP receiver port, appended when the otlphttp destination's
// endpoint omits one (the catalog documents the port as optional, defaulting to 4318).
const defaultOTLPHTTPPort = "4318"

// OTLPTarget is the resolved OTLP export target for the connector: the collector's downstream gRPC
// "host:port" endpoint and/or the OTLP/HTTP endpoint URL, whether TLS is disabled (insecure), and which
// telemetry signals the destination actually accepts. It is connector-wide (provider-agnostic): any
// workload instrumentation exporting to the connector's destination must respect the signal flags,
// because exporting a signal the destination does not implement (e.g. metrics/logs to a traces-only
// backend such as Jaeger) fails on every batch.
type OTLPTarget struct {
	// Endpoint is the OTLP gRPC "host:port" (no scheme); empty when the destination is HTTP-only.
	Endpoint string
	// Insecure applies to the gRPC endpoint: TLS is off unless the destination explicitly enabled it.
	Insecure bool
	// HTTPEndpoint is the OTLP/HTTP endpoint URL (scheme + host:port); empty when not configured. Agents
	// that only speak http/protobuf (e.g. the PHP and Ruby community SDKs) require it.
	HTTPEndpoint string
	Traces       bool
	Metrics      bool
	Logs         bool
}

// ResolveOTLPTarget picks the first usable OTLP destination — one carrying a gRPC endpoint (type otlp)
// or an OTLP/HTTP endpoint (type otlphttp) — and returns its export target plus whether one was found. A
// destination that records no signals falls back to traces-only: traces are the one signal every OTLP
// backend implements, and it avoids exporting to an unimplemented service.
func ResolveOTLPTarget(dests []Destination) (OTLPTarget, bool) {
	for _, d := range dests {
		// The managed-collector destination is routing intent, not an OTLP terminal. Keep this guard
		// here (rather than relying on validation) so a malformed marker can never become an exporter.
		if isManagedCollectorType(d.Type) {
			continue
		}
		grpcEP := configString(d.Config, configOTLPGRPCEndpoint)
		httpEP := configString(d.Config, configOTLPHTTPEndpoint)
		if grpcEP == "" && httpEP == "" {
			continue
		}
		t := OTLPTarget{
			// The collector's OTLP gRPC exporter takes host:port + a TLS-insecure flag (no URL scheme):
			// insecure unless the destination explicitly enabled TLS.
			Endpoint:     grpcEP,
			Insecure:     configString(d.Config, configOTLPGRPCTLSEnabled) != "true",
			HTTPEndpoint: normalizeOTLPHTTPEndpoint(httpEP),
		}
		for _, s := range d.Signals {
			switch strings.ToUpper(s) {
			case SignalTraces:
				t.Traces = true
			case SignalMetrics:
				t.Metrics = true
			case SignalLogs:
				t.Logs = true
			}
		}
		if !t.Traces && !t.Metrics && !t.Logs {
			t.Traces = true
		}
		return t, true
	}
	return OTLPTarget{}, false
}

// normalizeOTLPHTTPEndpoint turns the otlphttp destination's endpoint value into a URL the OTel SDK env
// (OTEL_EXPORTER_OTLP_ENDPOINT) accepts: the catalog documents "host:port" with the scheme and port both
// optional, so a missing scheme defaults to plaintext http and a missing port to the standard 4318.
func normalizeOTLPHTTPEndpoint(ep string) string {
	if ep == "" {
		return ""
	}
	rest := ep
	scheme := "http://"
	if i := strings.Index(ep, "://"); i >= 0 {
		scheme, rest = ep[:i+3], ep[i+3:]
	}
	// Append the default port only to a bare host (no port, no path — a path implies a deliberate,
	// fully-specified endpoint that is left untouched).
	if !strings.Contains(rest, ":") && !strings.Contains(rest, "/") && rest != "" {
		rest += ":" + defaultOTLPHTTPPort
	}
	return scheme + rest
}

func configString(cfg map[string]any, key string) string {
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}
