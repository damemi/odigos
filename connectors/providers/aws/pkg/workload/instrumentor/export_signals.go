package instrumentor

import (
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

// The asymmetric export-signal rule, shared by the Lambda and Fargate observers.
//
// It lives in a file of its own because it is the one piece of conformance logic BOTH platforms must apply
// identically. Two platforms disagreeing about what drift means is the bug this exists to prevent, and a rule
// duplicated per platform is a rule that drifts — so each observer narrows through narrowedSignals rather than
// re-deriving the comparison itself.

// narrowedSignals restricts a destination's (traces, metrics, logs) to the signals a workload actually exports.
//
// The DIRECTION is the whole rule. Exporting FEWER signals than the destination accepts is a legitimate choice a
// manually-instrumented workload's own definition is entitled to make: it costs nothing and loses nothing Odigos
// was promised, so reporting it as drift told a customer to re-apply instrumentation over a decision they made
// deliberately. Exporting one the destination does NOT accept is the harmful direction — the signal has no
// pipeline at the far end, so every batch is refused and the collector retries it on an exponential backoff,
// inside billed duration for a Lambda and against the task's own CPU for Fargate.
//
// Two cases deliberately do NOT narrow, so the caller's exact comparison reports them:
//
//   - a signal exported that the destination does not accept (the harmful direction above);
//   - a workload exporting NO signal at all, which is not a narrower choice but a collector with no pipeline —
//     it fails to start, leaving the workload instrumented and silent.
//
// This is the OBSERVER's rule and never the mutating path's. When the connector owns a workload it writes the
// export configuration itself and must converge it to the destination's full signal set; tolerating a subset
// there would let it keep exporting less than Odigos asked for, forever, with nothing left to report it.
func narrowedSignals(env map[string]string, traces, metrics, logs bool) (bool, bool, bool) {
	exportsTraces := signalExported(env, EnvTracesExporter)
	exportsMetrics := signalExported(env, EnvMetricsExporter)
	exportsLogs := signalExported(env, EnvLogsExporter)

	if (exportsTraces && !traces) || (exportsMetrics && !metrics) || (exportsLogs && !logs) {
		return traces, metrics, logs
	}
	if !exportsTraces && !exportsMetrics && !exportsLogs {
		return traces, metrics, logs
	}
	return exportsTraces, exportsMetrics, exportsLogs
}

// signalExported reports whether a workload's SDK is configured to produce a signal, read back off its
// OTEL_*_EXPORTER variable. Absent means the SDK default ("otlp") applies, so ABSENCE IS ENABLED — the same
// convention exporterFor writes.
func signalExported(env map[string]string, key string) bool {
	return env[key] != exporterNone
}

// narrowToExportedSignals is narrowedSignals over an OTLPTarget — the Lambda observer's shape. Only the three
// signal flags move; the transport (endpoint, HTTP endpoint, TLS) is carried through untouched, because those
// stay an exact comparison in instrumentationDiff.
func narrowToExportedSignals(dest domaindest.OTLPTarget, env map[string]string) domaindest.OTLPTarget {
	dest.Traces, dest.Metrics, dest.Logs = narrowedSignals(env, dest.Traces, dest.Metrics, dest.Logs)
	return dest
}
