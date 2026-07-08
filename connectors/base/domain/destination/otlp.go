package destination

import "strings"

// Telemetry signal keys as stored on a Destination (match the GraphQL TelemetrySignal enum).
const (
	SignalTraces  = "TRACES"
	SignalMetrics = "METRICS"
	SignalLogs    = "LOGS"
)

// OTLP destination config keys (the OTLP destination type's non-secret fields).
const (
	configOTLPGRPCEndpoint   = "OTLP_GRPC_ENDPOINT"
	configOTLPGRPCTLSEnabled = "OTLP_GRPC_TLS_ENABLED"
)

// OTLPTarget is the resolved OTLP export target for the connector: the collector's downstream gRPC
// "host:port" endpoint, whether TLS is disabled (insecure), and which telemetry signals the destination
// actually accepts. It is connector-wide (provider-agnostic): any workload instrumentation exporting to
// the connector's destination must respect the signal flags, because exporting a signal the destination
// does not implement (e.g. metrics/logs to a traces-only backend such as Jaeger) fails on every batch.
type OTLPTarget struct {
	Endpoint string
	Insecure bool
	Traces   bool
	Metrics  bool
	Logs     bool
}

// ResolveOTLPTarget picks the first usable OTLP destination and returns its export target plus whether one
// was found. A destination that records no signals falls back to traces-only: traces are the one signal
// every OTLP backend implements, and it avoids exporting to an unimplemented service.
func ResolveOTLPTarget(dests []Destination) (OTLPTarget, bool) {
	for _, d := range dests {
		ep := configString(d.Config, configOTLPGRPCEndpoint)
		if ep == "" {
			continue
		}
		t := OTLPTarget{
			Endpoint: ep,
			// The collector's OTLP gRPC exporter takes host:port + a TLS-insecure flag (no URL scheme):
			// insecure unless the destination explicitly enabled TLS.
			Insecure: configString(d.Config, configOTLPGRPCTLSEnabled) != "true",
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

func configString(cfg map[string]any, key string) string {
	if v, ok := cfg[key].(string); ok {
		return v
	}
	return ""
}
