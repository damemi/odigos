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
	} else if w.WaitingFor != "" && w.SourceMatched && w.SourceEnabled && !w.Instrumented {
		// Marked for instrumentation, but a connector-wide precondition is unmet so the reconcile is
		// deliberately skipping it. The STATE stays NotInstrumented — nothing has failed and nothing
		// about this workload needs fixing — which a UI renders as pending for an enabled source. The
		// stage/message/waitingFor carry the explanation and the fact that it resolves on its own.
		msg := waitingForMessage(w.WaitingFor)
		progress.Stage = "WaitingFor" + capitalizeFirst(w.WaitingFor)
		progress.Message = &msg
		progress.WaitingFor = strPtrOrNil(w.WaitingFor)
		issues = append(issues, &model.WorkloadIssue{
			Code:     "WaitingFor" + capitalizeFirst(w.WaitingFor),
			Severity: "unknown",
			Message:  msg,
			// Transient: it clears itself the moment the precondition is met, with no action on the
			// workload — the customer configures a destination, not this source.
			Transient: boolPtr(true),
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
		Details:   detailsToModel(w),
		UpdatedAt: w.LastSeenAt.UTC().Format(time.RFC3339),
	}
}

// detailsToModel narrows the provider's flat details struct to the GraphQL type matching its kind.
// Kinds a provider has nothing extra to say about return nil rather than an empty block.
func detailsToModel(w api.DiscoveredWorkload) model.WorkloadDetails {
	d := w.Details
	if d == nil {
		return nil
	}
	kind := d.Kind
	if kind == "" {
		kind = w.Kind
	}
	switch {
	case strings.HasPrefix(kind, "aws.fargate-task"), strings.HasPrefix(kind, "aws.ecs"):
		return model.EcsWorkloadDetails{
			Kind:                 kind,
			ClusterArn:           strPtrOrNil(d.ClusterArn),
			TaskDefinition:       strPtrOrNil(d.TaskDefinition),
			LaunchType:           strPtrOrNil(d.LaunchType),
			DeploymentController: strPtrOrNil(d.DeploymentController),
			Containers:           containersToModel(d.Containers),
		}
	case strings.HasPrefix(kind, "aws.lambda"):
		return model.LambdaWorkloadDetails{
			Kind:         kind,
			FunctionArn:  strPtrOrNil(d.FunctionArn),
			Runtime:      strPtrOrNil(d.Runtime),
			Architecture: strPtrOrNil(d.Architecture),
			LayerArn:     strPtrOrNil(d.LayerArn),
		}
	case strings.HasPrefix(kind, "gcp.cloud-run"):
		return model.CloudRunWorkloadDetails{
			Kind:       kind,
			Service:    strPtrOrNil(d.Service),
			Revision:   strPtrOrNil(d.Revision),
			Containers: containersToModel(d.Containers),
		}
	default:
		return nil
	}
}

func containersToModel(rows []api.WorkloadContainer) []*model.WorkloadContainer {
	if len(rows) == 0 {
		return nil
	}
	out := make([]*model.WorkloadContainer, 0, len(rows))
	for i := range rows {
		c := rows[i]
		out = append(out, &model.WorkloadContainer{
			Name:  c.Name,
			Image: strPtrOrNil(c.Image),
			// Pids is deliberately left nil: a connector rewrites a task definition from the
			// control plane and never observes a process inside a container. Empty means "no
			// process visibility", which is exactly the truth here.
			Runtime:      containerRuntimeSnapshot(c),
			Instrumented: c.Instrumented,
			// Essential / OdigosManaged deliberately do not travel: they are provider-specific facts
			// (an ECS container definition flag, an Odigos ownership marker) and a client would have
			// to know each platform's rules to turn them into anything renderable. The provider has
			// already applied those rules, so what crosses the wire is the outcome —
			// InstrumentationEligible plus the ExcludedReason naming the cause.
			InstrumentationEligible: c.Eligible,
			ExcludedReason:          strPtrOrNil(c.ExcludedReason),
			Distro:                  strPtrOrNil(c.Distro),
		})
	}
	return out
}

// containerRuntimeSnapshot mirrors buildRuntimeSnapshot at container granularity: detection is what
// the provider found in this container, and running is that unless a per-container override wins.
func containerRuntimeSnapshot(c api.WorkloadContainer) *model.WorkloadRuntimeSnapshot {
	runningLang, runningVer := c.RuntimeLanguage, c.RuntimeVersion
	if c.OverrideLanguage != "" {
		runningLang = c.OverrideLanguage
	}
	if c.OverrideVersion != "" {
		runningVer = c.OverrideVersion
	}
	snap := &model.WorkloadRuntimeSnapshot{
		Detected: &model.WorkloadRuntime{
			Language: strPtrOrNil(c.RuntimeLanguage),
			Version:  strPtrOrNil(c.RuntimeVersion),
		},
		Running: &model.WorkloadRuntime{
			Language: strPtrOrNil(runningLang),
			Version:  strPtrOrNil(runningVer),
		},
	}
	if c.OverrideLanguage != "" || c.OverrideVersion != "" {
		snap.Override = &model.WorkloadRuntime{
			Language: strPtrOrNil(c.OverrideLanguage),
			Version:  strPtrOrNil(c.OverrideVersion),
		}
	}
	return snap
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

// waitingForMessage is the customer-facing explanation for a blocked prerequisite: what is missing,
// why it blocks instrumentation, and what resolves it — the last part matters most, since the state
// otherwise reads as something wrong with the workload.
func waitingForMessage(waitingFor string) string {
	switch waitingFor {
	case api.WaitingForDestination:
		return "waiting for a destination: this workload is marked for instrumentation, but the connector has no OTLP destination to export telemetry to. Instrumentation starts automatically once a destination is configured."
	default:
		return "waiting for " + waitingFor
	}
}

func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
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
