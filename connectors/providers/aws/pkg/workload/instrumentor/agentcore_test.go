package instrumentor

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	actypes "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol/types"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	dest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
)

type agentCoreMemory struct {
	records   []agentcore.Runtime
	sources   []basestore.SourceRecord
	target    dest.OTLPTarget
	headers   map[string]string
	state     map[string]any
	snapshots map[string][]byte
}

func (s *agentCoreMemory) ListAgentCore(context.Context) ([]agentcore.Runtime, error) {
	return s.records, nil
}
func (s *agentCoreMemory) UpsertAgentCore(_ context.Context, r agentcore.Runtime) error {
	s.records = []agentcore.Runtime{r}
	return nil
}
func (s *agentCoreMemory) ListSources(context.Context) ([]basestore.SourceRecord, error) {
	return s.sources, nil
}
func (s *agentCoreMemory) OTLPExportTarget(context.Context) (dest.OTLPTarget, bool, error) {
	return s.target, s.target.HTTPEndpoint != "" || s.target.Endpoint != "", nil
}
func (s *agentCoreMemory) OTLPExportHeaders(context.Context) (map[string]string, error) {
	return s.headers, nil
}
func (s *agentCoreMemory) SetAgentCoreState(_ context.Context, _ string, v map[string]any) error {
	maps.Copy(s.state, v)
	return nil
}
func (s *agentCoreMemory) AgentCoreState(context.Context, string) (map[string]any, error) {
	return s.state, nil
}
func (s *agentCoreMemory) SaveAgentCoreSnapshot(_ context.Context, _ string, id string, data []byte) error {
	// Emulate JSONB's whitespace normalization to exercise canonical digest recovery.
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	raw, _ := json.MarshalIndent(value, "", "  ")
	s.snapshots[id] = raw
	return nil
}
func (s *agentCoreMemory) LoadAgentCoreSnapshot(_ context.Context, _ string, id string) ([]byte, error) {
	v, ok := s.snapshots[id]
	if !ok {
		return nil, fmt.Errorf("missing snapshot")
	}
	return v, nil
}

type agentCoreFake struct {
	agentcore.Client
	live           ac.GetAgentRuntimeOutput
	updates        []*ac.UpdateAgentRuntimeInput
	updateError    error
	readError      error
	custom         bool
	defaultVersion string
}

func (f *agentCoreFake) GetAgentRuntime(context.Context, *ac.GetAgentRuntimeInput, ...func(*ac.Options)) (*ac.GetAgentRuntimeOutput, error) {
	if f.readError != nil {
		return nil, f.readError
	}
	return &f.live, nil
}
func (f *agentCoreFake) ListAgentRuntimeEndpoints(context.Context, *ac.ListAgentRuntimeEndpointsInput, ...func(*ac.Options)) (*ac.ListAgentRuntimeEndpointsOutput, error) {
	e := []actypes.AgentRuntimeEndpoint{{Name: aws.String("DEFAULT")}}
	if f.custom {
		e = append(e, actypes.AgentRuntimeEndpoint{Name: aws.String("prod")})
	}
	return &ac.ListAgentRuntimeEndpointsOutput{RuntimeEndpoints: e}, nil
}
func (f *agentCoreFake) GetAgentRuntimeEndpoint(_ context.Context, in *ac.GetAgentRuntimeEndpointInput, _ ...func(*ac.Options)) (*ac.GetAgentRuntimeEndpointOutput, error) {
	version := f.live.AgentRuntimeVersion
	if aws.ToString(in.EndpointName) == "DEFAULT" && f.defaultVersion != "" {
		version = aws.String(f.defaultVersion)
	}
	if aws.ToString(in.EndpointName) == "prod" {
		version = aws.String("1")
	}
	return &ac.GetAgentRuntimeEndpointOutput{Name: in.EndpointName, LiveVersion: version, TargetVersion: version, Status: actypes.AgentRuntimeEndpointStatusReady}, nil
}
func (f *agentCoreFake) UpdateAgentRuntime(_ context.Context, in *ac.UpdateAgentRuntimeInput, _ ...func(*ac.Options)) (*ac.UpdateAgentRuntimeOutput, error) {
	f.updates = append(f.updates, in)
	if f.updateError != nil {
		return nil, f.updateError
	}
	f.live.EnvironmentVariables = maps.Clone(in.EnvironmentVariables)
	f.live.AgentRuntimeArtifact = in.AgentRuntimeArtifact
	f.live.AgentRuntimeVersion = aws.String(fmt.Sprint(len(f.updates) + 1))
	return &ac.UpdateAgentRuntimeOutput{AgentRuntimeVersion: f.live.AgentRuntimeVersion}, nil
}
func newAgentCoreTest(language string) (*AgentCore, *agentCoreMemory, *agentCoreFake) {
	p, _ := json.Marshal(agentcore.Package{Version: 1, Language: language, Distros: []string{language + "-agentcore-adot", language + "-agentcore-openllmetry"}, ModuleFormat: "commonjs"})
	f := &agentCoreFake{live: ac.GetAgentRuntimeOutput{AgentRuntimeArn: aws.String("arn:aws:bedrock-agentcore:us-west-2:123456789012:runtime/demo-1234567890"), AgentRuntimeId: aws.String("demo-1234567890"), AgentRuntimeName: aws.String("demo"), AgentRuntimeVersion: aws.String("1"), Status: actypes.AgentRuntimeStatusReady,
		AgentRuntimeArtifact: &actypes.AgentRuntimeArtifactMemberContainerConfiguration{Value: actypes.ContainerConfiguration{ContainerUri: aws.String("public.ecr.aws/example/app@sha256:test")}}, RoleArn: aws.String("arn:aws:iam::123456789012:role/existing"),
		NetworkConfiguration: &actypes.NetworkConfiguration{NetworkMode: actypes.NetworkModeVpc}, ProtocolConfiguration: &actypes.ProtocolConfiguration{ServerProtocol: actypes.ServerProtocolHttp},
		EnvironmentVariables: map[string]string{agentcore.PackageEnv: string(p), "APP_SETTING": "keep"}}}
	arn := aws.ToString(f.live.AgentRuntimeArn)
	s := &agentCoreMemory{records: []agentcore.Runtime{agentcore.Describe("us-west-2", &f.live, nil)}, sources: []basestore.SourceRecord{{Name: "demo", Kind: agentcore.Kind, ExternalResourceID: arn, Enabled: true}}, target: dest.OTLPTarget{HTTPEndpoint: "https://otlp.example.test", Traces: true, Metrics: true, Logs: true}, state: map[string]any{}, snapshots: map[string][]byte{}}
	// The runtime is prepared: detection finds the package and leaves the artifact as deployed.
	r := &AgentCore{store: s, client: func(context.Context, string) (agentcore.Client, error) { return f, nil }, installer: &agentCoreInstallerFake{mode: agentCoreSDKOdigos}}
	return r, s, f
}

func TestAgentCoreLifecycleAndDistroSwitch(t *testing.T) {
	for _, language := range []string{"python", "nodejs"} {
		t.Run(language, func(t *testing.T) {
			r, s, f := newAgentCoreTest(language)
			ctx := context.Background()
			original := maps.Clone(f.live.EnvironmentVariables)
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.updates) != 1 || f.live.EnvironmentVariables[agentcore.DistroEnv] != language+"-agentcore-adot" {
				t.Fatal("default distro was not applied")
			}
			in := f.updates[0]
			if in.RoleArn != f.live.RoleArn || in.NetworkConfiguration != f.live.NetworkConfiguration || in.AgentRuntimeArtifact != f.live.AgentRuntimeArtifact {
				t.Fatal("lost unrelated deployment configuration")
			}
			if aws.ToString(in.ClientToken) == "" || strings.Contains(f.live.EnvironmentVariables["ODIGOS_MANAGED"], "APP_SETTING") {
				t.Fatal("missing idempotency token or public rollback data")
			}
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.updates) != 1 {
				t.Fatal("converged runtime updated")
			}
			s.sources[0].Config.DistroOverride.OtelDistroName = language + "-agentcore-openllmetry"
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.updates) != 2 {
				t.Fatal("distro switch did not deploy exactly once")
			}
			if f.live.EnvironmentVariables["TRACELOOP_TRACE_CONTENT"] != "false" || f.live.EnvironmentVariables["OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"] != "https://otlp.example.test/v1/traces" {
				t.Fatal("wrong export/content recipe")
			}
			f.live.EnvironmentVariables["APP_SETTING"] = "customer-change"
			s.sources[0].Enabled = false
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			original["APP_SETTING"] = "customer-change"
			if !maps.Equal(original, f.live.EnvironmentVariables) {
				t.Fatalf("rollback did not preserve original/customer settings: %v", f.live.EnvironmentVariables)
			}
		})
	}
}

func TestAgentCoreObserverIsReadOnlyAndPreservesVerdictOnReadFailure(t *testing.T) {
	r, s, f := newAgentCoreTest("python")
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	delete(f.live.EnvironmentVariables, "ODIGOS_MANAGED")
	f.live.EnvironmentVariables["ODIGOS_IAC"] = `{"v":1}`
	o := &AgentCoreObserver{runtime: r}
	if err := o.Observe(ctx); err != nil {
		t.Fatal(err)
	}
	if s.state["conformance"] != baseapi.ConformanceConformant {
		t.Fatalf("configuration not conformant: %v", s.state)
	}
	f.live.EnvironmentVariables["OTEL_SERVICE_NAME"] = "drift"
	if err := o.Observe(ctx); err != nil {
		t.Fatal(err)
	}
	if s.state["conformance"] != baseapi.ConformancePartial {
		t.Fatal("drift not observed")
	}
	f.readError = fmt.Errorf("read denied")
	if err := o.Observe(ctx); err == nil {
		t.Fatal("read failure hidden")
	}
	if s.state["conformance"] != baseapi.ConformancePartial || s.state["observation_error"] == "" || len(f.updates) != 1 {
		t.Fatal("read failed verdict was erased or manual mode wrote AWS")
	}
}

// TestAgentCoreObserverValidatesTheManualAdoptRoute: a manual-mode runtime prepared with ADOT only carries the adopt
// contract and agentCoreADOTEnv. The observer judges it against that recipe, reports one changed key as partial,
// and never writes the runtime.
func TestAgentCoreObserverValidatesTheManualAdoptRoute(t *testing.T) {
	for _, language := range []string{"python", "nodejs"} {
		t.Run(language, func(t *testing.T) {
			r, s, f := newAgentCoreTest(language)
			s.headers = map[string]string{"Authorization": "Bearer token"}
			d, err := ResolveAgentCoreDistro(language, language+"-agentcore-adot")
			if err != nil {
				t.Fatal(err)
			}
			env := agentCoreADOTEnv(d, "demo", s.target, s.headers)
			for k, v := range env {
				if v == "" {
					delete(env, k)
				}
			}
			env[agentcore.PackageEnv] = agentCoreAdoptPackageValue(language)
			env["ODIGOS_IAC"] = agentCoreIaCMarkerValue(d.Name)
			env["APP_SETTING"] = "keep"
			f.live.EnvironmentVariables = env
			s.records = []agentcore.Runtime{agentcore.Describe("us-west-2", &f.live, nil)}
			before := maps.Clone(env)
			ctx := context.Background()
			o := &AgentCoreObserver{runtime: r}
			if err := o.Observe(ctx); err != nil {
				t.Fatal(err)
			}
			if s.state["conformance"] != baseapi.ConformanceConformant {
				t.Fatalf("adopt route not conformant: %v", s.state)
			}
			// The package recipe's propagators on an adopt runtime are drift: the contract decides the recipe.
			f.live.EnvironmentVariables["OTEL_PROPAGATORS"] = "tracecontext,baggage,xray"
			if err := o.Observe(ctx); err != nil {
				t.Fatal(err)
			}
			if s.state["conformance"] != baseapi.ConformancePartial {
				t.Fatalf("changed key not reported partial: %v", s.state)
			}
			f.live.EnvironmentVariables["OTEL_PROPAGATORS"] = "tracecontext,baggage"
			f.live.EnvironmentVariables[agentcore.EnabledEnv] = "true"
			if err := o.Observe(ctx); err != nil {
				t.Fatal(err)
			}
			if s.state["conformance"] != baseapi.ConformancePartial {
				t.Fatalf("bootstrap switch on an adopt runtime not reported partial: %v", s.state)
			}
			delete(f.live.EnvironmentVariables, agentcore.EnabledEnv)
			if !maps.Equal(f.live.EnvironmentVariables, before) || len(f.updates) != 0 {
				t.Fatalf("manual-mode observer wrote the runtime: %d updates", len(f.updates))
			}
		})
	}
}

func TestAgentCoreRejectsUnsafeAutomaticChanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*agentCoreMemory, *agentCoreFake)
	}{
		{"native esm", func(_ *agentCoreMemory, f *agentCoreFake) {
			p := agentcore.ParsePackage(f.live.EnvironmentVariables[agentcore.PackageEnv])
			p.ModuleFormat = "esm"
			v, _ := json.Marshal(p)
			f.live.EnvironmentVariables[agentcore.PackageEnv] = string(v)
		}},
		{"iac owned", func(_ *agentCoreMemory, f *agentCoreFake) { f.live.EnvironmentVariables["ODIGOS_IAC"] = "1" }},
		{"grpc only", func(s *agentCoreMemory, _ *agentCoreFake) {
			s.target = dest.OTLPTarget{Endpoint: "collector:4317", Traces: true}
		}},
		{"collector only", func(s *agentCoreMemory, _ *agentCoreFake) { s.target = dest.OTLPTarget{} }},
		{"env limit", func(_ *agentCoreMemory, f *agentCoreFake) {
			for i := range 50 {
				f.live.EnvironmentVariables[fmt.Sprint("CUSTOM_", i)] = "x"
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r, s, f := newAgentCoreTest("nodejs")
			test.change(s, f)
			_ = r.Reconcile(context.Background())
			if len(f.updates) != 0 || s.state["instrumentation_error"] == "" {
				t.Fatalf("unsafe configuration accepted: %v", s.state)
			}
		})
	}
}

func TestAgentCoreRollbackConflictAndSnapshotRecovery(t *testing.T) {
	r, s, f := newAgentCoreTest("python")
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	s.sources[0].Enabled = false
	f.live.EnvironmentVariables["OTEL_SERVICE_NAME"] = "customer-change"
	if err := r.Reconcile(ctx); err == nil {
		t.Fatal("rollback conflict ignored")
	}
	if len(f.updates) != 1 {
		t.Fatal("conflicting setting overwritten")
	}
	f.live.EnvironmentVariables["OTEL_SERVICE_NAME"] = "demo"
	s.snapshots = map[string][]byte{}
	if err := r.Reconcile(ctx); err == nil {
		t.Fatal("missing snapshot ignored")
	}
	if len(f.updates) != 1 {
		t.Fatal("rollback without snapshot")
	}
}

func TestAgentCoreManualPlanDoesNotExposeCredentials(t *testing.T) {
	r, s, f := newAgentCoreTest("python")
	s.headers = map[string]string{"Authorization": "secret-unique-value"}
	p := NewAgentCorePlanner(s)
	result, err := p.PlanFor(context.Background(), baseapi.DiscoveredWorkload{ServiceName: "demo", ExternalID: aws.ToString(f.live.AgentRuntimeArn)})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "secret-unique-value") || !strings.Contains(string(data), "OTLP_AUTH_HEADERS") {
		t.Fatal("manual credential handling is unsafe")
	}
	if err := result.Plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 0 {
		t.Fatal("planner mutated AWS")
	}
	_ = r
}

func TestAgentCorePinnedEndpointsAndNoCollectorHealthDependency(t *testing.T) {
	r, s, f := newAgentCoreTest("python")
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	f.custom = true
	for range 3 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.updates) != 1 {
		t.Fatal("endpoint/collector reporting redeployed instrumentation")
	}
	note, ok := s.state["routing_note"].(*baseapi.WorkloadRoutingNote)
	if !ok || !strings.Contains(note.Message, "prod") {
		t.Fatal("pinned custom endpoint not reported")
	}
}

func TestAgentCoreGlobalHeadersSignalsAndRollbackWithoutDestination(t *testing.T) {
	r, s, f := newAgentCoreTest("python")
	s.headers = map[string]string{"Authorization": "Bearer a+b,= c"}
	s.target.Metrics = false
	s.target.Logs = false
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	env := f.live.EnvironmentVariables
	parts := strings.SplitN(env["OTEL_EXPORTER_OTLP_HEADERS"], "=", 2)
	decoded, err := url.PathUnescape(parts[1])
	if err != nil || decoded != s.headers["Authorization"] || strings.Contains(env["OTEL_EXPORTER_OTLP_HEADERS"], "+") {
		t.Fatalf("incorrect OTLP auth encoding: %q %v", env["OTEL_EXPORTER_OTLP_HEADERS"], err)
	}
	if env["OTEL_METRICS_EXPORTER"] != "none" || env["OTEL_LOGS_EXPORTER"] != "none" || env["OTEL_TRACES_EXPORTER"] != "otlp" || env["OTEL_SDK_DISABLED"] != "false" {
		t.Fatal("signal or SDK activation guard does not match destination")
	}
	// An unavailable/removed destination cannot prevent an explicitly disabled source rolling back.
	s.target = dest.OTLPTarget{}
	s.sources[0].Enabled = false
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 2 || f.live.EnvironmentVariables["ODIGOS_MANAGED"] != "" {
		t.Fatal("rollback was gated on destination availability")
	}
}

func TestAgentCoreReportsDefaultCutoverWithoutRedeployment(t *testing.T) {
	r, s, f := newAgentCoreTest("python")
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	f.defaultVersion = "1"
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if s.state["settling"] != true || s.state["instrumentation_phase"] != baseapi.PhaseInstrumenting || len(f.updates) != 1 {
		t.Fatalf("DEFAULT cutover was declared complete or redeployed: %v", s.state)
	}
	f.defaultVersion = ""
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if s.state["settling"] != false || s.state["instrumentation_phase"] != baseapi.PhaseInstrumented || len(f.updates) != 1 {
		t.Fatalf("DEFAULT readiness did not settle: %v", s.state)
	}
}
