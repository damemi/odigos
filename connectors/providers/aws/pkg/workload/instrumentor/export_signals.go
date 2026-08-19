package instrumentor

import (
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
)

// The asymmetric export-signal rule, shared by every platform's observer.
//
// It lives in a file of its own because it is the one piece of conformance logic all platforms must apply
// identically, and neither Lambda nor Fargate is the right owner of a rule the other depends on.

// narrowedSignals is the asymmetric rule itself: given the signals a destination accepts, it returns the set a
// manually-instrumented workload should be HELD TO, read from the workload's own OTEL_*_EXPORTER variables.
//
// It is shared by every platform's observer on purpose. "Drift" meaning one thing on Lambda and another on
// Fargate is not a cosmetic inconsistency — it is the same workload shape reported as healthy or broken
// depending on which service it happens to run on, and a customer being told to re-apply instrumentation they
// deliberately narrowed. One predicate makes that divergence impossible rather than merely unlikely, and the
// per-platform wrappers (narrowToExportedSignals, narrowExportToContainerSignals) hold nothing but the shape
// conversion.
//
// Two inputs deliberately return the destination UNCHANGED, so that the caller's comparison — which is an
// equality check against the destination's set — reports them as drift:
//
//   - a signal exported that the destination does not accept. This is the harmful direction: the signal has no
//     pipeline at the far end, so every batch is refused and retried on a backoff, paying for telemetry that
//     is discarded.
//   - NO signal exported at all. That is not a narrower choice but an agent asked to produce nothing, which
//     leaves the workload instrumented and silent — the failure a conformance verdict exists to catch.
//
// Absence of a variable means the signal IS exported (see signalExported), so a workload only narrows by saying
// "none" explicitly — which is exactly what the manual-mode plan tells customers to do per signal.
func narrowedSignals(env map[string]string, traces, metrics, logs bool) (bool, bool, bool) {
	t := signalExported(env, EnvTracesExporter)
	m := signalExported(env, EnvMetricsExporter)
	l := signalExported(env, EnvLogsExporter)

	if (t && !traces) || (m && !metrics) || (l && !logs) {
		return traces, metrics, logs
	}
	if !t && !m && !l {
		return traces, metrics, logs
	}
	return t, m, l
}

// signalExported reports whether the SDK is configured to produce a signal, from its OTEL_*_EXPORTER variable.
// Absent means the SDK default ("otlp") applies, so absence is ENABLED — the same convention
// exporterOverrideFor writes, read back.
func signalExported(env map[string]string, key string) bool {
	return env[key] != exporterNone
}

// narrowToExportedSignals returns dest restricted to the signals a Lambda function actually exports, which is
// what makes the export check ASYMMETRIC: exporting FEWER signals than the destination accepts is a legitimate
// choice, exporting one it does not accept is drift.
//
// The direction is the whole point. A signal the destination accepts but the function does not send costs
// nothing and loses nothing Odigos was promised — a customer instrumenting for traces against a destination
// that also takes metrics and logs has configured exactly what they meant. A signal the function sends that the
// destination does NOT accept is the harmful case: it has no pipeline to be received on, so every batch is
// refused and the in-Lambda collector retries it on an exponential backoff inside billed duration.
//
// Narrowing rather than comparing signal sets directly is what keeps ONE comparator. The rest of the export
// configuration — transport, endpoint, credential, processors, receivers — must still match byte for byte, and
// re-rendering the desired document for the narrowed signal set means the existing string comparison enforces
// all of that unchanged, while the pipelines it contains are the ones this function is entitled to have.
//
// This is the OBSERVER's rule and not the instrumentor's. When the connector owns a function it writes the
// export configuration itself and must converge it to the destination's full signal set; tolerating a subset
// there would let a function keep exporting less than Odigos asked for, forever. Manual mode is the opposite:
// the customer's deployment definition is the author, and a narrower choice in it is a decision to respect.
func narrowToExportedSignals(dest domaindest.OTLPTarget, env map[string]string) domaindest.OTLPTarget {
	dest.Traces, dest.Metrics, dest.Logs = narrowedSignals(env, dest.Traces, dest.Metrics, dest.Logs)
	return dest
}
