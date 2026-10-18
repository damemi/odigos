package destination

import "strings"

// ExporterFor is the shared SDK policy for destination signal selection. Always write
// both sides: SDK defaults differ by language and distribution, and a previously
// disabled exporter must be enabled again when the destination changes.
func ExporterFor(enabled bool) string {
	if enabled {
		return "otlp"
	}
	return "none"
}

// SignalExporters returns the portable SDK selectors. Profiles are retained on the
// target separately: the bundled language agents do not implement OTLP profiles,
// and inventing an OTEL_PROFILES_EXPORTER variable would not activate a profiler.
func (t OTLPTarget) SignalExporters() map[string]string {
	return map[string]string{
		"OTEL_TRACES_EXPORTER":  ExporterFor(t.Traces),
		"OTEL_METRICS_EXPORTER": ExporterFor(t.Metrics),
		"OTEL_LOGS_EXPORTER":    ExporterFor(t.Logs),
	}
}

// SignalTransportOverrides lists variables that take precedence over the shared
// OTLP target. Providers must snapshot these before removing them and restore them
// on rollback, otherwise an old per-signal endpoint can bypass destination routing.
func SignalTransportOverrides() []string {
	var keys []string
	for _, signal := range []string{"TRACES", "METRICS", "LOGS"} {
		for _, setting := range []string{"ENDPOINT", "PROTOCOL", "HEADERS", "INSECURE"} {
			keys = append(keys, "OTEL_EXPORTER_OTLP_"+signal+"_"+setting)
		}
	}
	return keys
}

// SignalActivation supplies the language-specific activation in addition to the
// portable exporter selectors. Python's SDK creates a logger provider but does
// not attach its logging handler unless explicitly requested.
func (t OTLPTarget) SignalActivation(language string) map[string]string {
	env := t.SignalExporters()
	env["OTEL_SDK_DISABLED"] = "false"
	if language == "python" {
		if t.Logs {
			env["OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED"] = "true"
		} else {
			env["OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED"] = "false"
		}
	}
	return env
}

// SignalConfigurationMatches checks the same policy used for injection. Missing
// enabled signals are drift as well as extra signals; manual deployments must
// match the destination just like connector-managed workloads.
func (t OTLPTarget) SignalConfigurationMatches(env map[string]string, language string) bool {
	for key, value := range t.SignalActivation(language) {
		if env[key] != value {
			return false
		}
	}
	for _, key := range SignalTransportOverrides() {
		if _, exists := env[key]; exists {
			return false
		}
	}
	return true
}

// LambdaSignalActivation accounts for the Lambda Node layer's restricted default
// (dns/http/net). Its bundled logging bridges are opt-in, so enabling the log
// exporter alone cannot capture pino, winston, or bunyan records. AWS SDK and
// Lambda instrumentation are always registered by the layer itself.
func (t OTLPTarget) LambdaSignalActivation(language string) map[string]string {
	env := t.SignalActivation(language)
	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "http/protobuf"
	if language == "nodejs" {
		env["OTEL_NODE_ENABLED_INSTRUMENTATIONS"] = "amqplib,bunyan,cassandra-driver,connect,dataloader,dns,express,fs,graphql,grpc,hapi,http,ioredis,kafkajs,knex,koa,memcached,mongodb,mongoose,mysql,mysql2,nestjs-core,net,pg,pino,redis,restify,socket.io,undici,winston"
	}
	return env
}

func (t OTLPTarget) LambdaSignalConfigurationMatches(env map[string]string, language string) bool {
	if !t.SignalConfigurationMatches(env, language) {
		return false
	}
	for key, value := range t.LambdaSignalActivation(language) {
		if env[key] != value {
			return false
		}
	}
	return true
}

// CollectionSummary lists the enabled signals supported by the workload's agents.
// The current bundles do not collect profiles; Ruby supports traces only.
func (t OTLPTarget) CollectionSummary(languages ...string) string {
	supportsMetricsAndLogs := len(languages) == 0
	for _, language := range languages {
		if language != "ruby" {
			supportsMetricsAndLogs = true
		}
	}
	var signals []string
	if t.Logs && supportsMetricsAndLogs {
		signals = append(signals, "logs")
	}
	if t.Metrics && supportsMetricsAndLogs {
		signals = append(signals, "metrics")
	}
	if t.Traces {
		signals = append(signals, "traces")
	}
	if len(signals) == 0 {
		return "No supported signals enabled"
	}
	if len(signals) == 1 {
		return "Collecting " + signals[0]
	}
	return "Collecting " + strings.Join(signals[:len(signals)-1], ", ") + " and " + signals[len(signals)-1]
}
