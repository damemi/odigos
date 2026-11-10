package workloadreport

import (
	"strings"
	"testing"
	"unicode"

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
		if want := stageLabel(phase); m.Progress.Stage != want {
			t.Errorf("phase %q: stage = %q, want %q", phase, m.Progress.Stage, want)
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
	}); m.Current.State != api.WorkloadStateInstrumenting || m.Progress.Stage != stageLabel("Instrumenting") {
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
	if m := ToModel(w); m.Progress.Stage != stageLabel("Error") {
		t.Errorf("error must override live phase: stage = %q, want %q", m.Progress.Stage, stageLabel("Error"))
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
	if m := ToModel(done); m.Current.State != api.WorkloadStateInstrumented || m.Progress.Stage != stageLabel(api.PhaseInstrumented) {
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
	if m.Progress.Stage != "Not implemented yet" {
		t.Errorf("stage = %q, want \"Not implemented yet\"", m.Progress.Stage)
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
	// Two, not three: the odigos-agent staging container is Odigos's own, and the report carries the
	// CUSTOMER's container inventory. See containersToModel.
	if len(ecs.Containers) != 2 {
		t.Fatalf("got %d containers, want 2", len(ecs.Containers))
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
	// Odigos's own staging sidecar is not in the inventory at all. Reporting it — even correctly, as an
	// ineligible container with a reason — put a container the customer never wrote and cannot act on in
	// front of them, with no language to detect and no instrumentation that would ever be applied.
	for _, c := range ecs.Containers {
		if c.Name == "odigos-agent" {
			t.Errorf("the Odigos staging sidecar must not appear in the reported container inventory: %+v", c)
		}
	}
}

// A task definition whose only containers are Odigos's own reports no inventory rather than an empty list:
// an empty list is a claim that the workload HAS no containers, which is a different (and false) statement.
func TestToModelOmitsInventoryThatIsOnlyOdigosContainers(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName: "svc",
		Kind:        "aws.fargate-task",
		Details: &api.WorkloadDetails{Kind: "aws.fargate-task", Containers: []api.WorkloadContainer{
			{Name: "odigos-agent", OdigosManaged: true, ExcludedReason: "Odigos agents sidecar"},
		}},
	}
	ecs := ToModel(w).Details.(model.EcsWorkloadDetails)
	if ecs.Containers != nil {
		t.Errorf("containers = %+v, want nil", ecs.Containers)
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

// TestToModelRevisionedServiceDetails covers the branch Cloud Run and Container Apps SHARE. They used to have a
// wire type each, which had drifted into calling the same field `revision` while documenting it as opposite
// facts (serving-traffic vs latest-ready) — so this asserts both kinds land on one type and that the value goes
// into the field named for what it holds.
//
// It is the mapping most exposed to a silent error: every field is a *string, so putting Service where the
// revision belongs, or dropping either, compiles and type-asserts perfectly and only shows up as a blank column
// in a UI.
func TestToModelRevisionedServiceDetails(t *testing.T) {
	cases := []struct {
		name       string
		kind       string
		containers []api.WorkloadContainer
		wantConts  int
	}{
		{
			// Cloud Run: the half that was declared, mapped, and never populated until now.
			name: "cloud run",
			kind: "gcp.cloud-run",
		},
		{
			// Container Apps resolves to the SAME type — that is the merge.
			name: "container apps",
			kind: "azure.container-apps",
		},
		{
			name: "bedrock agentcore",
			kind: "aws.bedrock-agentcore-runtime",
		},
		{
			// Containers travel when the reporter has them, on either platform.
			name:       "with a container inventory",
			kind:       "gcp.cloud-run",
			containers: []api.WorkloadContainer{{Name: "app", Instrumented: true, Eligible: true}},
			wantConts:  1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := api.DiscoveredWorkload{
				ServiceName: "checkout",
				Kind:        c.kind,
				Details: &api.WorkloadDetails{
					Kind:                c.kind,
					Service:             "projects/p/locations/us-central1/services/checkout",
					LatestReadyRevision: "checkout-00007-abc",
					Containers:          c.containers,
				},
			}
			got, ok := ToModel(w).Details.(model.RevisionedServiceWorkloadDetails)
			if !ok {
				t.Fatalf("details = %T, want model.RevisionedServiceWorkloadDetails", ToModel(w).Details)
			}
			if got.Kind != c.kind {
				t.Errorf("kind = %q, want %q; it is the discriminator a client branches on now that one type serves both", got.Kind, c.kind)
			}
			if ptr(got.Service) != w.Details.Service {
				t.Errorf("service = %q, want %q", ptr(got.Service), w.Details.Service)
			}
			if ptr(got.LatestReadyRevision) != w.Details.LatestReadyRevision {
				t.Errorf("latestReadyRevision = %q, want %q", ptr(got.LatestReadyRevision), w.Details.LatestReadyRevision)
			}
			if len(got.Containers) != c.wantConts {
				t.Errorf("containers = %d, want %d", len(got.Containers), c.wantConts)
			}
		})
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

func TestToModelSurfacesRegionalCollectorFallbackWithoutChangingState(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName: "non-vpc-lambda", Kind: "aws.lambda", Region: "eu-west-1",
		SourceMatched: true, SourceEnabled: true, Instrumented: true,
		RoutingNote: &api.WorkloadRoutingNote{
			Code:    "no-network",
			Message: "Defaulting to the global OTLP destination: this workload is not attached to a VPC and cannot reach the Odigos Managed Regional Collector.",
		},
	}

	got := ToModel(w)
	if got.Current.State != api.WorkloadStateInstrumented {
		t.Fatalf("state = %q, want instrumented", got.Current.State)
	}
	if len(got.Issues) != 1 || got.Issues[0].Code != "no-network" || got.Issues[0].Severity == "error" {
		t.Fatalf("issues = %+v, want one non-error routing note", got.Issues)
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
	if got.Progress.Stage != "Waiting for destination" {
		t.Errorf("stage = %q, want \"Waiting for a destination\"", got.Progress.Stage)
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

// An AgentCore agent whose artifact carries no SDK is a wait, and the dependency to add travels as the workload's
// routing note: the report has to carry both, or the customer sees "waiting" with nothing to act on.
func TestToModelWaitingForArtifactCarriesTheHint(t *testing.T) {
	w := api.DiscoveredWorkload{
		ServiceName:                "agent",
		Kind:                       "aws.bedrock-agentcore-runtime",
		InstrumentationType:        "aws.bedrock-agentcore-runtime",
		InstrumentationAvailable:   true,
		InstrumentationImplemented: true,
		SourceMatched:              true,
		SourceEnabled:              true,
		WaitingFor:                 api.WaitingForArtifact,
		RoutingNote:                &api.WorkloadRoutingNote{Code: "agentcore-artifact-change", Message: "Add odigos-agentcore[adot] to the agent's dependencies and redeploy."},
	}
	got := ToModel(w)
	if got.Current.State != api.WorkloadStateNotInstrumented || ptr(got.Progress.WaitingFor) != api.WaitingForArtifact {
		t.Fatalf("state %q waitingFor %q, want a NotInstrumented artifact wait", got.Current.State, ptr(got.Progress.WaitingFor))
	}
	if msg := ptr(got.Progress.Message); !strings.Contains(msg, "never rewrites the artifact") || strings.Contains(msg, "repackag") {
		t.Errorf("message = %q", msg)
	}
	codes := map[string]string{}
	for _, iss := range got.Issues {
		codes[iss.Code] = iss.Message
		if iss.Severity == "error" {
			t.Errorf("issue %+v reads as a failure", iss)
		}
	}
	if _, ok := codes["WaitingForArtifact"]; !ok {
		t.Errorf("no wait issue: %v", codes)
	}
	if !strings.Contains(codes["agentcore-artifact-change"], "odigos-agentcore[adot]") {
		t.Errorf("the dependency hint did not reach the report: %v", codes)
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
	if got.Progress.Stage != stageLabel("Error") {
		t.Errorf("stage = %q, want %q", got.Progress.Stage, stageLabel("Error"))
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

// Every stage the report can emit is a sentence a person reads, not an identifier a client has to take
// apart. This is the guarantee, and it is asserted over the whole vocabulary rather than case by case:
// progress.stage is the one field that is both the condition's name and the line shown to a customer, and
// the failure it replaced — central-ui rendering "VerifyingCustomerChanges" as "Verifyingcustomerchanges" —
// came from a client trying to reconstruct prose the report never sent.
//
// A condition added later is covered by construction: stageLabel's fallback splits it, and this test fails
// if the fallback is ever removed or a hand-written label slips back into identifier spelling.
func TestEveryStageIsReadable(t *testing.T) {
	// Every identifier ToModel assigns to progress.Stage, plus the ones stageLabels names.
	identifiers := []string{
		api.PhaseVerifying, api.PhaseLayerReady, api.PhaseInstrumenting, api.PhaseInstrumented,
		"SourceMatched", "SourceDisabled", "Discovered", "Unknown", "Error",
		"NotImplemented", "Unsupported", "VerifyingCustomerChanges", "InstrumentationDrift",
		"SourceDisabledButInstrumented", "SourceMissingButInstrumented",
		waitingForLabel(api.WaitingForDestination), waitingForLabel(api.WaitingForCustomer),
		// Not a real stage: a stand-in for one added after this test was written, which must read as a
		// sentence with no entry in stageLabels.
		"SomeConditionAddedLater",
	}
	for _, id := range identifiers {
		label := stageLabel(id)
		if label == "" {
			t.Errorf("%s: empty label", id)
			continue
		}
		// A run-together label is the exact defect: two words fused with no separator. A single word
		// ("Unknown", "Verifying") is fine, so the test is "no capital after the first character".
		for i, r := range label {
			if i > 0 && unicode.IsUpper(r) {
				t.Errorf("%s -> %q: capital mid-label at %d; a stage is a sentence, not an identifier", id, label, i)
				break
			}
		}
		// Multi-letter words and single spaces only, because central-ui rebuilds the stage into PascalCase
		// and the kit re-splits it on a lowercase-then-capital boundary. Anything that boundary cannot
		// recover is lost on the way to the customer: punctuation eats the space after it, and a
		// one-letter word's capital butts straight against the next word's ("Waiting for destination"
		// arrives as "Waiting For ADestination").
		for _, r := range label {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' {
				t.Errorf("%s -> %q: contains %q; a stage label is plain words, because clients reshape it", id, label, r)
				break
			}
		}
		for _, word := range strings.Fields(label) {
			if len([]rune(word)) < 2 {
				t.Errorf("%s -> %q: one-letter word %q; it fuses with the next word once a client "+
					"round-trips the label through PascalCase", id, label, word)
			}
		}
		if !unicode.IsUpper([]rune(label)[0]) {
			t.Errorf("%s -> %q: a stage sentence starts with a capital", id, label)
		}
	}
}

// The issue codes stay identifiers. They are what a client BRANCHES on — the report's prose is
// progress.message and progress.stage — so making them readable too would leave nothing stable to match.
func TestWaitingIssueCodesStayIdentifiers(t *testing.T) {
	for _, waitingFor := range []string{api.WaitingForCustomer, api.WaitingForDestination} {
		code := waitingForLabel(waitingFor)
		if strings.Contains(code, " ") {
			t.Errorf("%s: issue code %q contains a space; codes are matched, not read", waitingFor, code)
		}
	}
}

// A disabled source is not a request to instrument, so its stage must not say the workload is marked for
// instrumentation. It used to: the stage looked only at whether a source row existed.
func TestADisabledSourceIsNotMarkedForInstrumentation(t *testing.T) {
	w := api.DiscoveredWorkload{SourceMatched: true, InstrumentationAvailable: true}
	if got := ToModel(w).Progress.Stage; got != "Source disabled" {
		t.Errorf("disabled source: stage = %q, want %q", got, "Source disabled")
	}
	w.SourceEnabled = true
	if got := ToModel(w).Progress.Stage; got != "Marked for instrumentation" {
		t.Errorf("enabled source: stage = %q, want %q", got, "Marked for instrumentation")
	}
}
