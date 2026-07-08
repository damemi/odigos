package workloadreport

import (
	"strings"
	"time"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// ToModel converts a discovered workload (optionally linked to a source intent) to agents-api WorkloadReport.
func ToModel(w api.DiscoveredWorkload) *model.WorkloadReport {
	runtime := buildRuntimeSnapshot(w)
	state := api.WorkloadStateNotInstrumented
	if w.Instrumented {
		state = api.WorkloadStateInstrumented
	}
	// A non-terminal live phase (Verifying/LayerReady/Instrumenting) means instrumentation is actively in
	// flight — installing the layer, applying the config, or still settling asynchronously. Report the
	// in-progress state regardless of the instrumented flag (which is set as soon as the config change is
	// applied, before it finishes settling), so central-ui and odictl show it progressing rather than
	// disabled or prematurely done. The fine phase rides on progress.stage below; a terminal
	// error/unsupported/not-implemented condition still overrides this further down.
	if inFlightPhase(w.InstrumentationPhase) {
		state = api.WorkloadStateInstrumenting
	}
	reason := "Discovered"
	if w.SourceMatched && w.SourceEnabled {
		reason = "MarkedForInstrumentation"
	}
	desired := &model.WorkloadDesiredReport{
		Enabled:       w.SourceEnabled,
		Reason:        &reason,
		SourceMatched: boolPtr(w.SourceMatched),
		SourceEnabled: boolPtr(w.SourceEnabled),
	}
	// The connector only instruments with OpenTelemetry; advertise the SDK type when this workload's
	// instrumentation is actually available (spec-enabled + implemented).
	if w.InstrumentationAvailable && w.InstrumentationImplemented {
		sdk := "opentelemetry"
		desired.SdkType = &sdk
	}

	progress := &model.WorkloadProgressReport{Stage: stageFor(w)}
	var issues []*model.WorkloadIssue
	// Source-matched but this workload's instrumentation type is declared in the spec yet not built in
	// this release (implemented:false). Surface it clearly as NotImplemented (a non-error) regardless of
	// whether the source is enabled, so an unsupported capability is never shown as instrumented/working.
	if w.SourceMatched && w.InstrumentationType != "" && !w.InstrumentationImplemented {
		state = "NotImplemented"
		msg := "instrumentation for this workload type is not yet implemented"
		progress.Stage = "NotImplemented"
		progress.Message = &msg
		issues = append(issues, &model.WorkloadIssue{
			Code:     "InstrumentationNotImplemented",
			Severity: "unknown",
			Message:  msg,
		})
	} else if w.SourceMatched && w.SourceEnabled && !w.Instrumented && !w.InstrumentationAvailable {
		// Marked for instrumentation but the connector has no instrumentation defined for this workload's
		// type (its instrumentation type is not active for this connector). Surface it as Unknown and track
		// the reason as a non-error issue — the workload simply cannot be instrumented here, it is not a
		// failure.
		state = "Unknown"
		msg := "no instrumentation defined for this workload"
		progress.Stage = "Unknown"
		progress.Message = &msg
		issues = append(issues, &model.WorkloadIssue{
			Code:     "NoInstrumentationDefined",
			Severity: "unknown",
			Message:  msg,
		})
	}
	if w.InstrumentationError != "" {
		msg := w.InstrumentationError
		// An unsupported error is a terminal "cannot instrument this workload" state (unsupported runtime,
		// or a handler the agent cannot wrap), distinct from a transient instrumentation failure — classify
		// it as Unsupported so the UI does not present it as a retryable failure.
		if strings.HasPrefix(w.InstrumentationError, api.ErrPrefixUnsupported) {
			state = "Unsupported"
			progress.Stage = "Unsupported"
			progress.Message = &msg
			issues = append(issues, &model.WorkloadIssue{
				Code:     "UnsupportedRuntime",
				Severity: "error",
				Message:  msg,
			})
		} else {
			state = "Error"
			progress.Stage = "Error"
			progress.Message = &msg
			issues = append(issues, &model.WorkloadIssue{
				Code:     "InstrumentationFailed",
				Severity: "error",
				Message:  msg,
			})
		}
	}

	return &model.WorkloadReport{
		ServiceName: w.ServiceName,
		Kind:        w.Kind,
		Region:      strPtrOrNil(w.Region),
		Desired:     desired,
		Current: &model.WorkloadCurrentReport{
			State:        state,
			Instrumented: w.Instrumented,
			// Deprecated mirror of runtime.running, kept for older UI clients.
			RuntimeName:    runtime.Running.Language,
			RuntimeVersion: runtime.Running.Version,
			Runtime:        runtime,
			ProcessCount:   0,
		},
		Progress:  progress,
		Issues:    issues,
		UpdatedAt: w.LastSeenAt.UTC().Format(time.RFC3339),
	}
}

// stageFor returns the progress stage for a workload. An in-flight phase the instrumentor recorded
// (Verifying -> LayerReady -> Instrumenting) takes precedence so the report reflects real-time progress.
// Absent an in-flight phase, the terminal "Instrumented" stage is DERIVED from the live Instrumented flag
// (not a persisted phase) — so when a workload drifts (customer redeploy strips instrumentation), the flag
// flips to false and the stage falls back to SourceMatched/Discovered instead of a stale "Instrumented".
// Terminal error/unsupported/not-implemented states are applied by the caller after this and override it.
// inFlightPhase reports whether a phase is a non-terminal, actively-progressing instrumentation phase
// (Verifying/LayerReady/Instrumenting) — i.e. work is happening. Empty or the terminal PhaseInstrumented
// are not in-flight.
func inFlightPhase(phase string) bool {
	return phase != "" && phase != api.PhaseInstrumented
}

func stageFor(w api.DiscoveredWorkload) string {
	if w.InstrumentationPhase != "" {
		return w.InstrumentationPhase
	}
	if w.Instrumented {
		return api.PhaseInstrumented
	}
	if w.SourceMatched {
		return "SourceMatched"
	}
	return "Discovered"
}

func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func boolPtr(v bool) *bool {
	b := v
	return &b
}

func buildRuntimeSnapshot(w api.DiscoveredWorkload) *model.WorkloadRuntimeSnapshot {
	detected := &model.WorkloadRuntime{
		Language: strPtrOrNil(w.RuntimeLanguage),
		Version:  strPtrOrNil(w.RuntimeVersion),
	}
	runningLang := w.RuntimeLanguage
	runningVer := w.RuntimeVersion
	if ov := w.Overrides.RuntimeLanguage; ov != "" {
		runningLang = ov
	}
	if ov := w.Overrides.RuntimeVersion; ov != "" {
		runningVer = ov
	}
	snap := &model.WorkloadRuntimeSnapshot{
		Detected: detected,
		Running: &model.WorkloadRuntime{
			Language: strPtrOrNil(runningLang),
			Version:  strPtrOrNil(runningVer),
		},
	}
	if w.Overrides.HasRuntimeOverride() {
		snap.Override = &model.WorkloadRuntime{
			Language: strPtrOrNil(w.Overrides.RuntimeLanguage),
			Version:  strPtrOrNil(w.Overrides.RuntimeVersion),
		}
	}
	return snap
}
