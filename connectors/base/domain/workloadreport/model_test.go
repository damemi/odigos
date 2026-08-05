package workloadreport

import (
	"strings"
	"testing"

	"github.com/odigos-io/agents-api/go/server/graph/model"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// The instrumentor records a live phase as it works; it must surface as the progress stage so a
// WorkloadReport reflects real-time progress (source matched -> Verifying -> LayerReady -> Instrumenting
// -> Instrumented).
func TestToModelLiveInstrumentationPhase(t *testing.T) {
	for _, phase := range []string{"Verifying", "LayerReady", "Instrumenting"} {
		w := api.DiscoveredWorkload{
			ServiceName:                "fn",
			Kind:                       "aws.lambda",
			InstrumentationType:        "aws.lambda",
			InstrumentationAvailable:   true,
			InstrumentationImplemented: true,
			SourceMatched:              true,
			SourceEnabled:              true,
			InstrumentationPhase:       phase,
		}
		m := ToModel(w)
		if m.Progress.Stage != phase {
			t.Errorf("phase %q: stage = %q, want %q", phase, m.Progress.Stage, phase)
		}
		// An in-flight phase must report the distinct in-progress state so the UI/CLI show it as
		// progressing, not NotInstrumented/disabled.
		if m.Current.State != api.WorkloadStateInstrumenting {
			t.Errorf("phase %q: state = %q, want %q", phase, m.Current.State, api.WorkloadStateInstrumenting)
		}
	}

	// Settling: the config was applied (flag true) but the async Lambda update hasn't settled, so the phase
	// is still Instrumenting. The in-flight phase must win — show in-progress, not prematurely Instrumented.
	if m := ToModel(api.DiscoveredWorkload{
		ServiceName: "fn", Kind: "aws.lambda", InstrumentationType: "aws.lambda",
		InstrumentationAvailable: true, InstrumentationImplemented: true,
		SourceMatched: true, SourceEnabled: true, Instrumented: true,
		InstrumentationPhase: "Instrumenting",
	}); m.Current.State != api.WorkloadStateInstrumenting || m.Progress.Stage != "Instrumenting" {
		t.Errorf("settling: state=%q stage=%q, want Instrumenting/Instrumenting", m.Current.State, m.Progress.Stage)
	}

	// The terminal Instrumented phase reports the Instrumented state, not the in-progress one.
	if m := ToModel(api.DiscoveredWorkload{
		ServiceName: "fn", Kind: "aws.lambda", InstrumentationType: "aws.lambda",
		InstrumentationAvailable: true, InstrumentationImplemented: true,
		SourceMatched: true, SourceEnabled: true, Instrumented: true,
		InstrumentationPhase: api.PhaseInstrumented,
	}); m.Current.State != api.WorkloadStateInstrumented {
		t.Errorf("instrumented: state = %q, want %q", m.Current.State, api.WorkloadStateInstrumented)
	}

	// A recorded error still overrides the live phase (a failed workload shows Error, not its last phase).
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		InstrumentationPhase:       "Instrumenting",
		InstrumentationError:       "boom",
	}
	if m := ToModel(w); m.Progress.Stage != "Error" {
		t.Errorf("error must override live phase: stage = %q, want Error", m.Progress.Stage)
	}
}

// Drift lifecycle: an instrumented workload (no in-flight phase) derives the terminal Instrumented stage
// from the live flag; if it later loses instrumentation (customer redeploy), the flag flips to false and
// the status falls back to SourceMatched/NotInstrumented rather than a stale "Instrumented".
func TestToModelDriftLifecycle(t *testing.T) {
	base := api.DiscoveredWorkload{
		ServiceName: "fn", Kind: "aws.lambda", InstrumentationType: "aws.lambda",
		InstrumentationAvailable: true, InstrumentationImplemented: true,
		SourceMatched: true, SourceEnabled: true,
	}

	// Steady state: instrumented, no persisted phase -> Instrumented derived from the flag.
	done := base
	done.Instrumented = true
	if m := ToModel(done); m.Current.State != api.WorkloadStateInstrumented || m.Progress.Stage != api.PhaseInstrumented {
		t.Errorf("instrumented: state=%q stage=%q, want %q/%q", m.Current.State, m.Progress.Stage, api.WorkloadStateInstrumented, api.PhaseInstrumented)
	}

	// Drifted: lost instrumentation (flag false, no phase) -> NotInstrumented / SourceMatched, not a stale
	// Instrumented.
	drifted := base
	drifted.Instrumented = false
	m := ToModel(drifted)
	if m.Current.State != api.WorkloadStateNotInstrumented {
		t.Errorf("drifted: state = %q, want %q", m.Current.State, api.WorkloadStateNotInstrumented)
	}
	if m.Progress.Stage == api.PhaseInstrumented {
		t.Errorf("drifted: stage must not be a stale %q", api.PhaseInstrumented)
	}
}

// A source-matched workload whose instrumentation type is declared but not built must report
// NotImplemented (a non-error), never as instrumented/working.
func TestToModelNotImplemented(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.fargate-task",
		InstrumentationType:        "aws.fargate-task",
		InstrumentationImplemented: false,
		SourceMatched:              true,
		SourceEnabled:              true,
	}
	m := ToModel(w)
	if m.Current.State != "NotImplemented" {
		t.Errorf("state = %q, want NotImplemented", m.Current.State)
	}
	if m.Progress.Stage != "NotImplemented" {
		t.Errorf("stage = %q, want NotImplemented", m.Progress.Stage)
	}
	if len(m.Issues) != 1 || m.Issues[0].Code != "InstrumentationNotImplemented" {
		t.Errorf("issues = %+v, want one InstrumentationNotImplemented", m.Issues)
	}
}

// A workload whose runtime has no Odigos instrumentation is recorded with an unsupported-runtime error
// and must report as Unsupported (a terminal state) rather than a retryable instrumentation failure.
func TestToModelUnsupportedRuntime(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		InstrumentationError:       api.ErrPrefixUnsupportedRuntime + ` "go1.x": Odigos has no OpenTelemetry instrumentation for this Lambda language`,
	}
	m := ToModel(w)
	if m.Current.State != "Unsupported" {
		t.Errorf("state = %q, want Unsupported", m.Current.State)
	}
	if len(m.Issues) != 1 || m.Issues[0].Code != "UnsupportedRuntime" {
		t.Errorf("issues = %+v, want one UnsupportedRuntime", m.Issues)
	}
}

// A generic instrumentation failure remains a retryable Error (not Unsupported).
func TestToModelGenericErrorStillError(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		InstrumentationError:       "publish layer failed: throttled",
	}
	m := ToModel(w)
	if m.Current.State != "Error" {
		t.Errorf("state = %q, want Error", m.Current.State)
	}
	if len(m.Issues) != 1 || m.Issues[0].Code != "InstrumentationFailed" {
		t.Errorf("issues = %+v, want one InstrumentationFailed", m.Issues)
	}
}

// With a matched source runtime override, the report surfaces detected vs override vs running runtime.
func TestToModelWithRuntimeOverride(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		RuntimeLanguage:            "python",
		RuntimeVersion:             "3.11",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		Overrides: api.SourceOverrides{
			RuntimeLanguage: "java",
			RuntimeVersion:  "17",
		},
	}
	m := ToModel(w)
	if m.Current.Runtime.Override == nil || m.Current.Runtime.Override.Language == nil || *m.Current.Runtime.Override.Language != "java" {
		t.Fatalf("override runtime language = %+v, want java", m.Current.Runtime.Override)
	}
	if m.Current.Runtime.Running.Language == nil || *m.Current.Runtime.Running.Language != "java" {
		t.Fatalf("running runtime language = %+v, want java", m.Current.Runtime.Running.Language)
	}
	if m.Current.RuntimeName == nil || *m.Current.RuntimeName != "java" {
		t.Errorf("deprecated runtime name mirror = %v, want java", m.Current.RuntimeName)
	}
}

// The report fills the runtime language+version (detected == running, plus the deprecated mirrors) and
// the OpenTelemetry sdk type when instrumentation is available.
func TestToModelFillsRuntimeAndSdk(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "frontend",
		Kind:                       "aws.lambda",
		Region:                     "il-central-1",
		RuntimeLanguage:            "java",
		RuntimeVersion:             "17",
		InstrumentationType:        "aws.lambda",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		Instrumented:               true,
	}
	m := ToModel(w)

	if got := m.Current.Runtime.Detected; got.Language == nil || *got.Language != "java" || got.Version == nil || *got.Version != "17" {
		t.Errorf("detected runtime = %+v, want java/17", got)
	}
	if got := m.Current.Runtime.Running; got.Language == nil || *got.Language != "java" {
		t.Errorf("running runtime = %+v, want java", got)
	}
	if m.Current.RuntimeName == nil || *m.Current.RuntimeName != "java" || m.Current.RuntimeVersion == nil || *m.Current.RuntimeVersion != "17" {
		t.Errorf("deprecated runtime mirror not filled: name=%v version=%v", m.Current.RuntimeName, m.Current.RuntimeVersion)
	}
	if m.Desired.SdkType == nil || *m.Desired.SdkType != "opentelemetry" {
		t.Errorf("sdkType = %v, want opentelemetry", m.Desired.SdkType)
	}
	if m.Region == nil || *m.Region != "il-central-1" {
		t.Errorf("region = %v", m.Region)
	}
}

// An implemented type with no source stays a normal discovered workload (no NotImplemented noise).
func TestToModelImplementedDiscovered(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "fn",
		Kind:                       "aws.lambda",
		InstrumentationType:        "aws.lambda",
		InstrumentationImplemented: true,
		InstrumentationAvailable:   true,
	}
	m := ToModel(w)
	if m.Current.State != "NotInstrumented" {
		t.Errorf("state = %q, want NotInstrumented", m.Current.State)
	}
	if len(m.Issues) != 0 {
		t.Errorf("issues = %+v, want none", m.Issues)
	}
}

// The container layer is the whole reason details exist: an ECS task definition can pack several
// applications, which the flat report's single runtime cannot describe. The report must narrow the
// provider's details to the ECS wire type and carry every container through.
func TestToModelEcsDetailsCarryContainers(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName: "dev-simple-demo-single-fargate",
		Kind:        "aws.fargate-task",
		Region:      "il-central-1",
		Details: &api.WorkloadDetails{
			Kind:                 "aws.fargate-task",
			ClusterArn:           "arn:aws:ecs:il-central-1:1:cluster/dev",
			TaskDefinition:       "dev-simple-demo-single-fargate:42",
			LaunchType:           "FARGATE",
			DeploymentController: "ECS",
			Containers: []api.WorkloadContainer{
				{Name: "frontend", Image: "openjdk:21", RuntimeLanguage: "java", Eligible: true, Instrumented: true, Distro: "java", Essential: true},
				{Name: "geolocation", Image: "golang:1.23", RuntimeLanguage: "go", ExcludedReason: "eBPF is not available on Fargate", Essential: true},
				{Name: "odigos-agent", OdigosManaged: true, ExcludedReason: "Odigos agents sidecar"},
			},
		},
	}

	got := ToModel(w)
	ecs, ok := got.Details.(model.EcsWorkloadDetails)
	if !ok {
		t.Fatalf("details = %T, want model.EcsWorkloadDetails", got.Details)
	}
	if ptr(ecs.ClusterArn) != w.Details.ClusterArn || ptr(ecs.TaskDefinition) != "dev-simple-demo-single-fargate:42" {
		t.Errorf("cluster/task definition not carried: %+v", ecs)
	}
	if ptr(ecs.LaunchType) != "FARGATE" || ptr(ecs.DeploymentController) != "ECS" {
		t.Errorf("launch type / controller not carried: %+v", ecs)
	}
	if len(ecs.Containers) != 3 {
		t.Fatalf("got %d containers, want 3", len(ecs.Containers))
	}

	front := ecs.Containers[0]
	if !front.Instrumented || !front.InstrumentationEligible {
		t.Errorf("frontend should be instrumented and eligible: %+v", front)
	}
	if front.Runtime == nil || ptr(front.Runtime.Running.Language) != "java" {
		t.Errorf("frontend runtime = %+v, want java", front.Runtime)
	}
	// A connector rewrites task definitions from the control plane and never sees a process, so PIDs
	// must stay empty rather than being faked — empty means "no process visibility".
	if len(front.Pids) != 0 {
		t.Errorf("connector-reported containers must carry no PIDs, got %v", front.Pids)
	}
	if geo := ecs.Containers[1]; geo.InstrumentationEligible || ptr(geo.ExcludedReason) == "" {
		t.Errorf("an uninstrumentable container must be ineligible with a reason: %+v", geo)
	}
	// Odigos's own sidecar carries no flag of its own — the wire has no `odigosManaged`, on purpose.
	// A client tells it apart the same way it tells apart every other uninstrumented container: it is
	// ineligible and the excluded reason says why. Same for ECS's `essential`.
	if side := ecs.Containers[2]; side.InstrumentationEligible || ptr(side.ExcludedReason) != "Odigos agents sidecar" {
		t.Errorf("the sidecar must be reported as ineligible with its reason, not a flag: %+v", side)
	}
}

// A per-container override is what resolves a multi-application task definition, so the report has to
// show it: detection stays visible under detected, and running reflects the override that won.
func TestToModelContainerRuntimeOverrideVisible(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName: "svc",
		Kind:        "aws.fargate-task",
		Details: &api.WorkloadDetails{Kind: "aws.fargate-task", Containers: []api.WorkloadContainer{{
			Name:             "app",
			RuntimeLanguage:  "python",
			OverrideLanguage: "ruby",
			OverrideVersion:  "3.3",
			Eligible:         true,
		}}},
	}
	ecs := ToModel(w).Details.(model.EcsWorkloadDetails)
	rt := ecs.Containers[0].Runtime
	if rt == nil || rt.Override == nil {
		t.Fatalf("runtime snapshot missing the override: %+v", rt)
	}
	if ptr(rt.Detected.Language) != "python" {
		t.Errorf("detection must stay visible, got %q", ptr(rt.Detected.Language))
	}
	if ptr(rt.Running.Language) != "ruby" || ptr(rt.Running.Version) != "3.3" {
		t.Errorf("running = %q/%q, want the override ruby/3.3", ptr(rt.Running.Language), ptr(rt.Running.Version))
	}
}

// Lambda has no container layer, so its details must resolve to the Lambda wire type — never to a
// container-shaped one with an empty containers list.
func TestToModelLambdaDetails(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName: "fn",
		Kind:        "aws.lambda",
		Details: &api.WorkloadDetails{
			Kind:         "aws.lambda",
			FunctionArn:  "arn:aws:lambda:us-east-1:1:function:fn",
			Runtime:      "python3.12",
			Architecture: "arm64",
		},
	}
	lam, ok := ToModel(w).Details.(model.LambdaWorkloadDetails)
	if !ok {
		t.Fatalf("details = %T, want model.LambdaWorkloadDetails", ToModel(w).Details)
	}
	if ptr(lam.FunctionArn) == "" || ptr(lam.Runtime) != "python3.12" || ptr(lam.Architecture) != "arm64" {
		t.Errorf("lambda details not carried: %+v", lam)
	}
}

// A workload the provider has nothing extra to say about must report no details at all, rather than an
// empty block every client then has to distinguish from a real one.
func TestToModelNoDetailsWhenProviderHasNone(t *testing.T) {
	if got := ToModel(api.DiscoveredWorkload{ServiceName: "t", Kind: "aws.fargate-task"}); got.Details != nil {
		t.Errorf("details = %#v, want nil", got.Details)
	}
	// An unknown kind carrying details is not guessed at either.
	w := api.DiscoveredWorkload{ServiceName: "t", Kind: "azure.container-app", Details: &api.WorkloadDetails{Kind: "azure.container-app"}}
	if got := ToModel(w); got.Details != nil {
		t.Errorf("details = %#v, want nil for an unmodeled kind", got.Details)
	}
}

func ptr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// A workload the customer marked for instrumentation that the connector is skipping for a missing
// prerequisite must say so. Before this, the skip existed only as a connector log line: the report
// showed "enabled, not instrumented" with no reason and no hint that it resolves on its own.
func TestToModelWaitingForDestination(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "svc",
		Kind:                       "aws.fargate-task",
		InstrumentationType:        "aws.fargate-task",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		WaitingFor:                 api.WaitingForDestination,
	}
	got := ToModel(w)

	// Not a failure: nothing about this workload needs fixing, so the state stays NotInstrumented,
	// which a UI renders as pending for an enabled source rather than as an error.
	if got.Current.State != api.WorkloadStateNotInstrumented {
		t.Errorf("state = %q, want %q — waiting is not a failure", got.Current.State, api.WorkloadStateNotInstrumented)
	}
	if got.Progress.Stage != "WaitingForDestination" {
		t.Errorf("stage = %q, want WaitingForDestination", got.Progress.Stage)
	}
	if ptr(got.Progress.WaitingFor) != api.WaitingForDestination {
		t.Errorf("waitingFor = %q, want %q", ptr(got.Progress.WaitingFor), api.WaitingForDestination)
	}
	msg := ptr(got.Progress.Message)
	if !strings.Contains(msg, "destination") {
		t.Errorf("message = %q, want it to name the missing destination", msg)
	}
	// The message must say it resolves itself, or the state reads as something the customer broke.
	if !strings.Contains(msg, "automatically") {
		t.Errorf("message = %q, want it to say instrumentation starts on its own", msg)
	}
	if len(got.Issues) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(got.Issues), got.Issues)
	}
	iss := got.Issues[0]
	if iss.Code != "WaitingForDestination" || iss.Severity == "error" {
		t.Errorf("issue = %+v, want a non-error WaitingForDestination", iss)
	}
	if iss.Transient == nil || !*iss.Transient {
		t.Error("the issue must be transient — it clears when a destination is added, with no action on this workload")
	}
}

// A real failure is more informative than "waiting", so an error still wins. Otherwise a workload
// that failed to instrument would be reported as merely pending once a destination went missing.
func TestToModelErrorOverridesWaiting(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "svc",
		Kind:                       "aws.fargate-task",
		InstrumentationType:        "aws.fargate-task",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		WaitingFor:                 api.WaitingForDestination,
		InstrumentationError:       "register instrumented task definition: AccessDenied",
	}
	got := ToModel(w)
	if got.Current.State != "Error" {
		t.Errorf("state = %q, want Error", got.Current.State)
	}
	if got.Progress.Stage != "Error" {
		t.Errorf("stage = %q, want Error", got.Progress.Stage)
	}
}

// Waiting is only reported for workloads that would otherwise be instrumented; an unmarked or
// already-instrumented workload must be unaffected.
func TestToModelWaitingIgnoredWhenNotApplicable(t *testing.T) {
	base := api.DiscoveredWorkload{
		ServiceName:                "svc",
		Kind:                       "aws.fargate-task",
		InstrumentationType:        "aws.fargate-task",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		WaitingFor:                 api.WaitingForDestination,
	}
	// Not marked for instrumentation: nothing is waiting on anything.
	if got := ToModel(base); got.Progress.Stage == "WaitingForDestination" {
		t.Errorf("an unmatched workload must not report waiting: %+v", got.Progress)
	}
	// Already instrumented: a destination existed when it happened.
	instrumented := base
	instrumented.SourceMatched, instrumented.SourceEnabled, instrumented.Instrumented = true, true, true
	if got := ToModel(instrumented); got.Progress.Stage == "WaitingForDestination" {
		t.Errorf("an instrumented workload must not report waiting: %+v", got.Progress)
	}
}
