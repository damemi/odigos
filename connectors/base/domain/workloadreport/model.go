package workloadreport

import (
	"strings"
	"time"
	"unicode"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/spec"
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
	phase := livePhase(w)
	if inFlightPhase(phase) {
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
		// Who instruments this workload: "automatic" (Odigos applies and repairs it) or "manual" (the
		// customer applies it and Odigos observes). Nil when this workload's type is not enabled for
		// instrumentation in any mode, so a client can tell "not applicable" from either mode.
		Mode: strPtrOrNil(w.ManagementMode),
	}
	// The connector only instruments with OpenTelemetry; advertise the SDK type when this workload's
	// instrumentation is actually available (spec-enabled + implemented).
	if w.InstrumentationAvailable && w.InstrumentationImplemented {
		sdk := "opentelemetry"
		desired.SdkType = &sdk
	}

	desired.OtelDistroName = strPtrOrNil(w.EffectiveDistro)
	desired.InstrumentationVersion = strPtrOrNil(w.InstrumentationVersion)
	if w.AvailableDistros != nil {
		desired.AvailableDistros = make([]*model.InstrumentationDistribution, 0, len(w.AvailableDistros))
		for _, d := range w.AvailableDistros {
			desired.AvailableDistros = append(desired.AvailableDistros, &model.InstrumentationDistribution{Name: d.Name, DisplayName: d.DisplayName, Language: d.Language, Version: d.Version, IsDefault: d.IsDefault})
		}
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
		//
		// Manual (IaC) mode shares this branch rather than getting its own: awaiting the customer's own
		// deployment is exactly the same shape of state — a precondition outside this workload, no failure,
		// self-resolving — and the only difference is who satisfies it. `since` says how long the wait has
		// run, which is what separates "just marked" from "marked weeks ago and never applied".
		msg := waitingForMessage(w.WaitingFor)
		// Asserted, not assumed: a workload can arrive here with a stale in-flight phase from an earlier
		// automatic-mode life (or from a destination that was removed mid-instrumentation), which set the state
		// to Instrumenting above. Nothing is in flight — the reconcile is deliberately skipping this workload —
		// so leaving that state would report it as permanently busy.
		state = api.WorkloadStateNotInstrumented
		progress.Stage = waitingForLabel(w.WaitingFor)
		progress.Message = &msg
		progress.WaitingFor = strPtrOrNil(w.WaitingFor)
		issues = append(issues, &model.WorkloadIssue{
			Code:     waitingForLabel(w.WaitingFor),
			Severity: "unknown",
			Message:  msg,
			Since:    sinceOrNil(w.SourceIntentAt),
			// Transient: it clears itself the moment the precondition is met, with no action on the
			// workload — the customer configures a destination, or applies the plan, not this source.
			Transient: boolPtr(true),
		})
	} else if w.ManagementMode == spec.ModeManual && w.SourceMatched && w.SourceEnabled && w.Settling {
		// The customer's own change is still being applied — a Lambda configuration update in flight, a
		// revision rolling out. Mid-apply state looks exactly like a partial application, so this MUST be
		// checked before the drift branch below: telling a customer their deployment has drifted while it is
		// seconds from completing is worse than saying nothing.
		//
		// It sits after the WaitingFor branch, so a missing destination still wins and a workload with no
		// marker yet still reads as awaiting the customer. That costs nothing in practice: the platforms
		// report a settling change together with its new configuration, so a workload whose apply is in
		// flight already carries the ownership marker and reaches this branch.
		msg := "verifying your changes: a configuration change to this workload is still being applied. Odigos re-checks it automatically once it settles."
		state = api.WorkloadStateInstrumenting
		progress.Stage = "VerifyingCustomerChanges"
		progress.Message = &msg
	} else if w.ManagementMode == spec.ModeManual && w.SourceMatched && w.SourceEnabled && w.Instrumented &&
		w.ConformanceObservable && w.ObservationError == "" &&
		(w.WaitingFor == api.WaitingForDestination || w.Conformance == api.ConformanceUnknown) {
		// Instrumentation IS applied, and Odigos has not established that it matches. Saying so is the whole
		// point: api.Conformance defines its zero value as "a workload nobody has observed must not read as
		// either conformant or drifted", and without this branch such a workload rendered byte-for-byte
		// identically to one observed and confirmed Conformant — state Instrumented, no issues at all. An
		// adopted workload hit that every time: adoption records the marker, and the observation that would
		// judge it had not run yet, or could not.
		//
		// Deliberately NOT drift. Drift asserts a comparison against a desired state, and the two reasons this
		// branch fires are the two where no such comparison happened. A missing destination in particular means
		// the desired export endpoint does not exist, which is why the observers refuse to compute a verdict at
		// all there rather than reporting every correctly instrumented workload as decayed on its export
		// settings. The tell is that configuring a destination would clear an "error" without anything about the
		// workload changing — so it was never an error with the workload.
		//
		// Equally NOT the NotInstrumented wait above: agents are live and exporting telemetry, and the state
		// has to keep saying so, exactly as the source-gone branch below keeps instrumented true. So the state
		// stays Instrumented (already set) and severity is "unknown" rather than "error" — nothing is broken,
		// something is unconfirmed. Transient, because it resolves with no action on the workload itself.
		//
		// It precedes the drift branch so a missing destination outranks a STALE verdict: a Partial recorded
		// while a destination existed describes a comparison against an endpoint that is now gone. With a
		// destination present, WaitingFor is not "destination" and a Partial is not Unknown, so a genuine drift
		// still falls through and reports as drift.
		//
		// ConformanceObservable is required because both messages below promise a verification. On a manual-mode
		// type this connector registers no observer for, Unknown is permanent rather than pending, and every one
		// of its workloads would carry a transient issue advertising a pass that never runs. That gap is a
		// connector capability limit, and activeObservers already warns about it once at startup.
		msg := notVerifiedMessage(w.WaitingFor)
		progress.Stage = "InstrumentationNotVerified"
		progress.Message = &msg
		progress.WaitingFor = strPtrOrNil(w.WaitingFor)
		issues = append(issues, &model.WorkloadIssue{
			Code:     "InstrumentationNotVerified",
			Severity: "unknown",
			Message:  msg,
			// Dated only when the cause is a standing one. A workload merely awaiting its first observation
			// resolves within a cycle, and a timestamp on that would imply a condition worth triaging.
			Since:     verificationBlockedSince(w),
			Transient: boolPtr(true),
		})
	} else if w.ManagementMode == spec.ModeManual && w.SourceMatched && w.SourceEnabled && w.Instrumented &&
		w.Conformance == api.ConformancePartial {
		// Instrumentation is attributable to Odigos but does not match what it should be — a component was
		// never applied, or carries a stale value after the destination or service name changed. In automatic
		// mode this is a non-event (the next reconcile converges it), which is exactly why it needs reporting
		// here: nothing converges a manual-mode workload, so a half-applied instrumentation stays half-applied
		// and would otherwise report as healthy on the strength of its marker alone.
		//
		// Instrumented is asserted rather than assumed: the marker flag is refreshed by discovery and the
		// verdict by the observation pass, so between the two a workload the customer just stripped can still
		// carry a stale Partial. Requiring the marker keeps this branch from reporting drift on a workload
		// that now has nothing applied at all (which the WaitingFor branch above already describes correctly).
		msg := driftMessage(w.MissingComponents)
		state = "Error"
		progress.Stage = "InstrumentationDrift"
		progress.Message = &msg
		issues = append(issues, &model.WorkloadIssue{
			Code:     "InstrumentationPartiallyApplied",
			Severity: "error",
			Message:  msg,
			// How long it has stood this way, which is what separates a deployment that half-landed minutes
			// ago from one that has been broken for weeks.
			Since: sinceOrNil(w.ConformanceSince),
			// The components as DATA, alongside the prose that already names them. The message is written to be
			// read; a client that wants to list the missing pieces, or offer a targeted fix for one, would
			// otherwise have to parse that sentence back apart — and would break the moment it was reworded.
			Details: model.InstrumentationDriftDetails{
				Code:              "InstrumentationPartiallyApplied",
				MissingComponents: nonNilComponents(w.MissingComponents),
			},
		})
	} else if w.Instrumented && !(w.SourceMatched && w.SourceEnabled) &&
		(w.ManagementMode == spec.ModeManual || w.InstrumentationOrigin == "external") {
		// Customer-applied instrumentation without an enabled source will not be removed by an automatic
		// connector's rollback. Report it even when this workload type is configured for automatic mode.
		// Connector-owned instrumentation in automatic mode can remain briefly while rollback completes,
		// so that case retains its observed state without a persistent source-gone error.
		//
		// Keep the observed flag true: the source controls intent, not whether instrumentation is still
		// present on the workload. The issue tells the operator why the two disagree.
		state = "Error"
		code, msg := sourceGoneIssue(w.SourceMatched)
		progress.Stage = code
		progress.Message = &msg
		issues = append(issues, &model.WorkloadIssue{
			Code:     code,
			Severity: "error",
			Message:  msg,
			Since:    sinceOrNil(w.SourceIntentAt),
		})
	}

	// An observation that could not be completed is reported ALONGSIDE whatever state was decided above,
	// leaving that state untouched: the last confirmed verdict is still the best information available, and
	// downgrading a workload because Odigos failed to read it would blame the customer for our failure. It is
	// transient — the next pass either succeeds or repeats this.
	if w.ObservationError != "" {
		msg := "could not verify this workload's instrumentation: " + w.ObservationError + ". The last confirmed state is shown; Odigos retries automatically."
		issues = append(issues, &model.WorkloadIssue{
			Code:      "InstrumentationObservationFailed",
			Severity:  "unknown",
			Message:   msg,
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

	if w.RoutingNote != nil && w.RoutingNote.Message != "" {
		issues = append(issues, &model.WorkloadIssue{
			Code:      w.RoutingNote.Code,
			Severity:  "unknown",
			Message:   w.RoutingNote.Message,
			Transient: boolPtr(w.RoutingNote.Transient),
		})
	}

	// One normalization for every branch above, rather than prose at each assignment. The branches decide
	// WHICH condition holds and say so in the vocabulary the rest of this file (and every issue Code) uses;
	// this turns that one identifier into the sentence a person reads. Doing it here is what makes the
	// guarantee total: a condition added later is readable without its author having to remember to be.
	progress.Stage = stageLabel(progress.Stage)

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
	case strings.HasPrefix(kind, "gcp.cloud-run"), strings.HasPrefix(kind, "azure.container-apps"), kind == "aws.bedrock-agentcore-runtime":
		// AgentCore uses the same identity/latest-ready-version shape, with no container inventory.
		// Cloud Run and Container Apps share ONE wire type, because every fact here means the same thing on
		// both: the platform resource identity, the revision the platform last reported ready, and the
		// containers in that revision's template. `kind` still tells a client which platform it is, so two
		// types for one shape bought nothing — and the two they replaced had already drifted apart in the one
		// way that mattered, each calling its revision field `revision` while documenting it as the opposite
		// fact (serving-traffic vs latest-ready). Both connectors populate latest-ready, which is what the
		// field is now named for.
		//
		// The revision is the point of this block. Revisions are immutable on both platforms, so
		// instrumentation always creates a new one, and a traffic split can keep weighting an older,
		// uninstrumented revision — a customer who applies instrumentation correctly and never moves traffic
		// sees no telemetry, and nothing on the wire could explain that before.
		//
		// Containers travel when the reporter has them. Container Apps keeps no per-container inventory today,
		// so the field is null there; that is the same per-reporter nullability EcsWorkloadDetails is built on
		// ("each fills the subset it can see"), not a slot nobody ever fills.
		return model.RevisionedServiceWorkloadDetails{
			Kind:                kind,
			Service:             strPtrOrNil(d.Service),
			LatestReadyRevision: strPtrOrNil(d.LatestReadyRevision),
			Containers:          containersToModel(d.Containers),
		}
	default:
		return nil
	}
}

// containersToModel renders the container inventory a client sees: the containers of the CUSTOMER's
// application, and only those.
//
// OdigosManaged containers are dropped rather than reported as ineligible ones. They are not the customer's
// containers at all — the agents-staging sidecar the instrumentation adds is Odigos's own plumbing, created
// and removed by instrumentation itself — and listing it invites every question the customer's containers
// legitimately raise and it cannot answer: which language does it run, why is its runtime undetected, why is
// it not instrumented. A client showing "detecting runtime info" against Odigos's own sidecar is describing
// work that will never happen, on a container the customer cannot act on.
//
// It stays in the connector's own inventory (api.WorkloadContainer.OdigosManaged, the store's
// odigos_managed) — that record is what lets the instrumentor tell its sidecar from a customer container
// with the same name, which is a question the mutating path has to answer. Only the REPORT drops it.
func containersToModel(rows []api.WorkloadContainer) []*model.WorkloadContainer {
	if len(rows) == 0 {
		return nil
	}
	out := make([]*model.WorkloadContainer, 0, len(rows))
	for i := range rows {
		c := rows[i]
		if c.OdigosManaged {
			continue
		}
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
	// A task definition whose only containers were Odigos's own reports no inventory rather than an empty
	// list — the same answer a workload with no container facts at all gives, which is what a client already
	// renders correctly.
	if len(out) == 0 {
		return nil
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

// livePhase is the recorded instrumentation phase, but only where it can still be live. A phase is written by
// an Odigos instrumentor as it works, and in manual mode no instrumentor runs for the type at all — so any
// phase on a manual-mode workload is a leftover from a previous life under automatic mode, and nothing will
// ever advance or clear it.
//
// Without this, a workload whose capability flipped from automatic to manual mid-instrumentation reports
// state=Instrumenting forever, next to stage=WaitingForCustomer and instrumented=false: it looks permanently
// busy while in truth it is waiting on the customer and nothing is happening.
func livePhase(w api.DiscoveredWorkload) string {
	if w.ManagementMode == spec.ModeManual {
		return ""
	}
	return w.InstrumentationPhase
}

func stageFor(w api.DiscoveredWorkload) string {
	if phase := livePhase(w); phase != "" {
		return phase
	}
	if w.Instrumented {
		return api.PhaseInstrumented
	}
	// A disabled source is not a request to instrument, so it does not read as one.
	if w.SourceMatched && w.SourceEnabled {
		return "SourceMatched"
	}
	if w.SourceMatched {
		return "SourceDisabled"
	}
	return "Discovered"
}

// waitingForLabel is the issue-code name for a blocked prerequisite: a stable PascalCase identifier, like
// every other issue Code. It also seeds progress.Stage, which stageLabel then renders for a reader — so this
// is spelled as an identifier and nothing here needs to be readable.
//
// Anything consuming these as identifiers must match the value, not reconstruct it from the WaitingFor key.
func waitingForLabel(waitingFor string) string {
	return "WaitingFor" + capitalizeFirst(waitingFor)
}

// stageLabel renders a progress stage as the sentence a person reads, from the PascalCase identifier the
// branches in ToModel assign.
//
// progress.Stage is the one field on the report that is BOTH the condition's name and the line a UI puts in
// front of a customer, and it used to travel as the identifier. Every client then had to turn
// "VerifyingCustomerChanges" into words, which is a job with no single right answer done in as many places as
// there are clients — central-ui's own attempt lowercased the tail and rendered "Verifyingcustomerchanges".
// The report is the only place that knows what each stage MEANS, so it is the place that says it. Clients
// that need to branch on the condition read issues[].Code and current.state, which stay identifiers.
//
// The map is for the stages whose split-on-capitals reading is wrong or merely clumsy; everything else falls
// through to that split. The fallback is what makes this total rather than a list to keep in step: a stage
// added tomorrow reads as a sentence with no edit here, and only needs an entry when the generic reading is
// not good enough.
func stageLabel(stage string) string {
	if label, ok := stageLabels[stage]; ok {
		return label
	}
	return sentenceFromPascal(stage)
}

var stageLabels = map[string]string{
	// The waits. The customer-facing one has to name WHO is being waited on — that is the whole
	// distinction between "Odigos is working on it" and "nothing happens until you deploy".
	waitingForLabel(api.WaitingForDestination): "Waiting for destination",
	waitingForLabel(api.WaitingForCustomer):    "Waiting for you to apply the instrumentation",
	waitingForLabel(api.WaitingForArtifact):    "Waiting for a change to the agent's artifact",

	// Terminal / in-flight conditions where the generic split is right but the wording is not: these are
	// read as a status line, so they say what is happening rather than naming a category.
	//
	// Every label here is plain multi-letter words: no punctuation, and no one-letter word. That is a
	// constraint rather than a style preference, and it comes from the client. central-ui rebuilds a stage
	// into PascalCase and the kit re-splits it on a lowercase-then-capital boundary, and that round trip
	// loses anything that boundary cannot recover — the space after a comma ("No source, instrumentation
	// still applied" arrives as "No Source,Instrumentation Still Applied") and the space after a
	// single-letter word, whose capital is immediately followed by the next word's capital ("Waiting for a
	// destination" arrives as "Waiting For ADestination"). TestEveryStageIsReadable enforces both.
	"InstrumentationDrift":          "Instrumentation does not match",
	"SourceDisabledButInstrumented": "Source disabled but still instrumented",
	"SourceMissingButInstrumented":  "No source but still instrumented",
	"NotImplemented":                "Not implemented yet",
	"Unsupported":                   "Not supported",

	// The happy path and the discovery states, where the identifier is already one word and the sentence
	// wants a little more than that word alone.
	api.PhaseInstrumented:  "Instrumented",
	api.PhaseInstrumenting: "Applying instrumentation",
	api.PhaseVerifying:     "Verifying",
	api.PhaseLayerReady:    "Instrumentation artifact ready",
	"SourceMatched":        "Marked for instrumentation",
	"SourceDisabled":       "Source disabled",
	"Discovered":           "Discovered",
	"Unknown":              "Unknown",
	"Error":                "Failed",
}

// sentenceFromPascal turns a PascalCase identifier into a sentence: "VerifyingCustomerChanges" becomes
// "Verifying customer changes". Only the first word keeps its capital, because this is read as a line of
// prose rather than as a title — and because an all-Title-Case rendering of a long condition
// ("Source Missing But Instrumented") reads as a heading for something rather than as a statement about
// this workload.
//
// A stage that is already a sentence (it contains a space) is returned untouched: re-splitting it would
// mangle a label something upstream already wrote for a reader.
func sentenceFromPascal(stage string) string {
	if stage == "" || strings.Contains(stage, " ") {
		return stage
	}
	var b strings.Builder
	for i, r := range stage {
		switch {
		case i == 0:
			b.WriteRune(unicode.ToUpper(r))
		case unicode.IsUpper(r):
			b.WriteByte(' ')
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// waitingForMessage is the customer-facing explanation for a blocked prerequisite: what is missing,
// why it blocks instrumentation, and what resolves it — the last part matters most, since the state
// otherwise reads as something wrong with the workload.
func waitingForMessage(waitingFor string) string {
	switch waitingFor {
	case api.WaitingForDestination:
		return "waiting for a destination: this workload is marked for instrumentation, but the connector has no OTLP destination to export telemetry to. Instrumentation starts automatically once a destination is configured."
	case api.WaitingForCustomer:
		return "waiting for you to apply the instrumentation: this workload type is configured for manual (infrastructure-as-code) instrumentation, so Odigos does not change the workload itself. Apply the instrumentation to your own deployment definition — this workload reports as instrumented automatically once the change is live."
	case api.WaitingForArtifact:
		return "waiting for a change to the agent's artifact: Odigos configures the OpenTelemetry SDK the artifact already carries and never rewrites the artifact. See this workload's note for what to change."
	default:
		return "waiting for " + waitingFor
	}
}

// notVerifiedMessage explains why an APPLIED instrumentation has not been confirmed to match, split by which
// of the two causes holds. Both endings matter more than the diagnosis: neither is something to fix on the
// workload, and a customer reading "not verified" will otherwise assume it is.
func notVerifiedMessage(waitingFor string) string {
	if waitingFor == api.WaitingForDestination {
		return "instrumentation is applied to this workload, but Odigos cannot verify it matches what it should be: " +
			"the connector has no OTLP destination, so there is no expected export configuration to compare against. " +
			"Odigos verifies it automatically once a destination is configured."
	}
	return "instrumentation is applied to this workload, but Odigos has not verified yet that it matches what it " +
		"should be. This resolves on its own at the next observation pass."
}

// verificationBlockedSince dates the not-verified condition only when its cause is a standing one — a missing
// destination, which holds until the customer configures one. A workload awaiting its first observation pass
// gets no timestamp: that clears within a cycle, and dating it would present a moment as a duration.
func verificationBlockedSince(w api.DiscoveredWorkload) *string {
	if w.WaitingFor != api.WaitingForDestination {
		return nil
	}
	return sinceOrNil(w.SourceIntentAt)
}

// sourceGoneIssue names instrumentation that outlived its source, split by whether the source is disabled
// or absent entirely. IaC-applied instrumentation remains outside the automatic instrumentor's ownership,
// so the same mismatch can occur even when the workload type is configured for automatic instrumentation.
func sourceGoneIssue(sourceMatched bool) (code, message string) {
	if sourceMatched {
		return "SourceDisabledButInstrumented",
			"instrumentation is still applied to this workload, but its source is disabled. Re-enable the source or remove the instrumentation. If your deployment definition applies it, remove it there."
	}
	return "SourceMissingButInstrumented",
		"instrumentation is still applied to this workload, but it has no source. Create a source to track it or remove the instrumentation. If your deployment definition applies it, remove it there."
}

// driftMessage explains a partially-applied manual-mode instrumentation. It names the components rather than
// saying "drift detected", because the customer has to go edit their own deployment definition and the
// component list is the only thing that tells them where to look. componentLabels turns the provider-neutral
// component names into that prose; an unrecognized one is passed through rather than dropped, so a provider
// adding a component still produces an actionable message before this map learns about it.
func driftMessage(missing []string) string {
	if len(missing) == 0 {
		// A Partial verdict is supposed to travel with its component list. Without one there is nothing
		// specific to say, so say the general thing rather than an empty sentence.
		return "this workload's instrumentation does not match what Odigos expects. Re-apply the instrumentation to your deployment definition."
	}
	labels := make([]string, 0, len(missing))
	for _, m := range missing {
		if label, ok := componentLabels[m]; ok {
			labels = append(labels, label)
		} else {
			labels = append(labels, m)
		}
	}
	return "this workload's instrumentation is incomplete or out of date (" + strings.Join(labels, "; ") +
		"). Odigos does not change manually-instrumented workloads, so re-apply the instrumentation to your deployment definition."
}

// componentLabels renders each component an observer can report (api.InstrumentationComponents) into the
// customer-facing prose that goes in a drift message. Keyed by the base's own constants, so a component cannot
// be declared without this map being the place it has to be labelled —
// TestEveryComponentHasALabel fails until it is.
var componentLabels = map[string]string{
	// Deliberately names neither variable. The component is reported when NO marker attributes the workload to
	// Odigos, and which marker was expected depends on how it was instrumented: the connector's own ownership
	// marker in automatic mode, an IaC marker (api.EnvOdigosIaC) where the customer's own deployment applied it.
	// Naming one would send half of manual-mode customers to set the wrong variable.
	api.ComponentOwnershipMarker:  "no marker identifying this as Odigos-instrumented is present",
	api.ComponentServiceName:      "the reported service name does not match this source",
	api.ComponentCollectorExport:  "the telemetry export settings do not match the configured destination",
	api.ComponentLogLevel:         "the telemetry log level does not match the connector's setting",
	api.ComponentAgentLoader:      "the language agent is not being loaded",
	api.ComponentLegacyExportVars: "obsolete export variables from an earlier Odigos version are still set",

	api.ComponentInstrumentationLayer: "the instrumentation layer is not attached",
	api.ComponentLayerVersionConflict: "more than one version of the instrumentation layer is attached",
	api.ComponentExecWrapper:          "the instrumentation wrapper is not set",
	api.ComponentAgentLoaderConfig:    "the language agent's configuration is incomplete",

	// On the staged-agent platforms the agent is copied by a helper container into a volume the application
	// container reads, so three things decay independently of the activation env: the staging container, the
	// volume, and the ordering between them. A missing sidecar or mount is the exact analog of Lambda's
	// detached layer — every env var is correct, and there is no agent for them to activate.
	api.ComponentAgentSidecar:           "the container that stages the Odigos agent files is missing or does not match",
	api.ComponentAgentVolume:            "the shared volume the agent files are staged into is missing",
	api.ComponentAgentVolumeMount:       "the application container does not mount the shared agent volume",
	api.ComponentAgentStartupDependency: "the application container does not wait for the agent files to be staged",
	api.ComponentAgentLanguage:          "the staged agent is for a different language than this container now runs",
	api.ComponentResourceAttributes:     "the telemetry resource attributes do not match this workload",
}

// sinceOrNil renders an issue's first-observed time, or nil when unknown (no source matched, so there is no
// intent timestamp to report). An absent `since` is honest; a zero-time one would read as 1970.
func sinceOrNil(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
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

// nonNilComponents normalizes a nil slice to an empty one so the non-null missingComponents list never
// resolves to null. An empty list is meaningful here — the verdict is Partial but the observer recorded no
// component names — and is different from the field being absent.
func nonNilComponents(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
