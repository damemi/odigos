package instrumentor

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
)

// newAgentCoreZIPTest is a direct-code runtime managed through the real installer, with S3 replaced by memory.
func newAgentCoreZIPTest(t *testing.T, language string, entry []string, files map[string]string) (*AgentCore, *agentCoreMemory, *agentCoreFake, *agentCoreLocalArtifacts) {
	t.Helper()
	r, s, f := newAgentCoreTest(language)
	delete(f.live.EnvironmentVariables, agentcore.PackageEnv)
	runtime := "PYTHON_3_12"
	if language == "nodejs" {
		runtime = "NODE_22"
	}
	f.live.AgentRuntimeArtifact = (agentCoreArtifactRef{Bucket: "code", Key: "agent.zip", Version: "v1", Runtime: runtime, EntryPoint: entry}).sdk()
	backend := &agentCoreLocalArtifacts{code: testAgentCoreZIP(t, files)}
	r.installer = &agentCoreArtifactInstaller{io: backend}
	return r, s, f, backend
}

// agentCoreTestWrapper is a start wrapper from a release that reads the agent's script from the environment.
const agentCoreTestWrapper = "script = os.environ.get('ODIGOS_AGENTCORE_ENTRYPOINT')"

func liveAgentCoreArtifact(t *testing.T, f *agentCoreFake) agentCoreArtifactRef {
	t.Helper()
	a, err := agentCoreArtifact(f.live.AgentRuntimeArtifact)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func agentCoreRoutingNote(t *testing.T, s *agentCoreMemory) *baseapi.WorkloadRoutingNote {
	t.Helper()
	note, _ := s.state["routing_note"].(*baseapi.WorkloadRoutingNote)
	return note
}

// TestAgentCoreAdoptsADOTThroughEnvironmentOnly: an agent that already runs ADOT is configured without touching its
// code. Its own OTel settings were configuration for that SDK, not a conflict: they are overridden and come back on
// rollback.
func TestAgentCoreAdoptsADOTThroughEnvironmentOnly(t *testing.T) {
	for _, tc := range []struct {
		name, language   string
		entry, wantEntry []string
		files            map[string]string
		env              map[string]string
		wantNodeOptions  string
	}{
		{name: "python launcher", language: "python", entry: []string{"opentelemetry-instrument", "app.py"}, wantEntry: []string{"opentelemetry-instrument", "app.py"},
			files: map[string]string{"app.py": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""},
			env:   map[string]string{"OTEL_SERVICE_NAME": "customer-service", "OTEL_PROPAGATORS": "xray", "AGENT_OBSERVABILITY_ENABLED": "true"}},
		{name: "python without its launcher", language: "python", entry: []string{"app.py"}, wantEntry: []string{"opentelemetry-instrument", "app.py"},
			files: map[string]string{"app.py": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""}},
		// AWS's documented Node.js form: AgentCore starts the ADOT package through the prefix.
		{name: "node", language: "nodejs", entry: []string{"index.js"}, wantEntry: []string{"opentelemetry-instrument", "index.js"},
			files:           map[string]string{"index.js": "", agentCoreNodeADOT + "package.json": `{"version":"0.7.0"}`},
			env:             map[string]string{"NODE_OPTIONS": "--max-old-space-size=256", "OTEL_RESOURCE_ATTRIBUTES": "team=agents"},
			wantNodeOptions: "--max-old-space-size=256"},
		{name: "node with its launcher", language: "nodejs", entry: []string{"opentelemetry-instrument", "index.js"}, wantEntry: []string{"opentelemetry-instrument", "index.js"},
			files: map[string]string{"index.js": "", agentCoreNodeADOT + "package.json": `{"version":"0.7.0"}`}},
		{name: "node already requiring ADOT", language: "nodejs", entry: []string{"index.js"}, wantEntry: []string{"index.js"},
			files:           map[string]string{"index.js": "", agentCoreNodeADOT + "package.json": `{"version":"0.7.0"}`},
			env:             map[string]string{"NODE_OPTIONS": "--require @aws/aws-distro-opentelemetry-node-autoinstrumentation/register"},
			wantNodeOptions: "--require @aws/aws-distro-opentelemetry-node-autoinstrumentation/register"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, f, _ := newAgentCoreZIPTest(t, tc.language, tc.entry, tc.files)
			maps.Copy(f.live.EnvironmentVariables, tc.env)
			original := maps.Clone(f.live.EnvironmentVariables)
			originalArtifact := liveAgentCoreArtifact(t, f)
			ctx := context.Background()
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if len(f.updates) != 1 {
				t.Fatalf("updates = %d, state %v", len(f.updates), s.state)
			}
			deployed := liveAgentCoreArtifact(t, f)
			want := originalArtifact
			want.EntryPoint = tc.wantEntry
			if !deployed.equal(want) {
				t.Fatalf("adopt changed the artifact beyond its entry point: %+v", deployed)
			}
			env := f.live.EnvironmentVariables
			if _, ok := env[agentcore.EnabledEnv]; ok {
				t.Error("adopt enabled the Odigos bootstrap")
			}
			if _, ok := env[agentcore.DistroEnv]; ok {
				t.Error("adopt selected a bootstrap distro")
			}
			if env["OTEL_PROPAGATORS"] != "tracecontext,baggage" || env["OTEL_SERVICE_NAME"] != "demo" || env["DISABLE_ADOT_OBSERVABILITY"] != "true" || env["AGENT_OBSERVABILITY_ENABLED"] != "false" {
				t.Errorf("adopt recipe not applied: %v", env)
			}
			if tc.language == "python" && (env["OTEL_PYTHON_DISTRO"] != "aws_distro" || env["OTEL_PYTHON_CONFIGURATOR"] != "aws_configurator") {
				t.Errorf("explicit ADOT settings missing: %v", env)
			}
			if env["NODE_OPTIONS"] != tc.wantNodeOptions && tc.language == "nodejs" {
				t.Errorf("NODE_OPTIONS = %q, want %q", env["NODE_OPTIONS"], tc.wantNodeOptions)
			}
			pkg := agentcore.ParsePackage(env[agentcore.PackageEnv])
			if pkg.Mode != "adopt" || strings.Join(pkg.Distros, ",") != tc.language+"-agentcore-adot" {
				t.Errorf("package contract %+v", pkg)
			}
			if s.state["waiting_for"] != "" || s.state["instrumentation_error"] != "" {
				t.Errorf("state %v", s.state)
			}
			if err := r.Reconcile(ctx); err != nil || len(f.updates) != 1 {
				t.Fatalf("converged adopt redeployed: %d %v", len(f.updates), err)
			}
			s.sources[0].Enabled = false
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(original, f.live.EnvironmentVariables) {
				t.Errorf("rollback did not restore the customer's settings:\n got %v\nwant %v", f.live.EnvironmentVariables, original)
			}
			if !liveAgentCoreArtifact(t, f).equal(originalArtifact) {
				t.Errorf("rollback did not restore the entry point: %+v", liveAgentCoreArtifact(t, f))
			}
		})
	}
}

// TestAgentCorePackageZIPSwapsAndRestoresTheEntryPoint: the Odigos package in a ZIP is started by making its wrapper
// the entry point alone, with the agent's script in the environment. AgentCore rejects a second element after
// anything but its opentelemetry-instrument prefix. Both are runtime configuration restored on rollback.
func TestAgentCorePackageZIPSwapsAndRestoresTheEntryPoint(t *testing.T) {
	for _, tc := range []struct {
		name, language, wantDistro, wantScript string
		entry, wantEntry                       []string
		files                                  map[string]string
	}{
		{name: "python", language: "python", wantDistro: "python-agentcore-adot", entry: []string{"app.py"}, wantEntry: []string{agentCorePythonStart}, wantScript: "app.py",
			files: map[string]string{"app.py": "", agentCorePythonStart: agentCoreTestWrapper, "odigos_agentcore-0.1.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""}},
		{name: "python replaces the ADOT launcher", language: "python", wantDistro: "python-agentcore-adot", entry: []string{"opentelemetry-instrument", "app.py"}, wantEntry: []string{agentCorePythonStart}, wantScript: "app.py",
			files: map[string]string{"app.py": "", agentCorePythonStart: agentCoreTestWrapper, "odigos_agentcore-0.1.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""}},
		// An artifact carrying one extra selects it without a distro override.
		{name: "python openllmetry extra", language: "python", wantDistro: "python-agentcore-openllmetry", entry: []string{"app.py"}, wantEntry: []string{agentCorePythonStart}, wantScript: "app.py",
			files: map[string]string{"app.py": "", agentCorePythonStart: agentCoreTestWrapper, "odigos_agentcore-0.1.0.dist-info/METADATA": "", "opentelemetry_instrumentation_bedrock-0.40.0.dist-info/METADATA": ""}},
		{name: "node", language: "nodejs", wantDistro: "nodejs-agentcore-adot", entry: []string{"index.js"}, wantEntry: []string{agentCoreNodePackageStart}, wantScript: "index.js",
			files: map[string]string{"index.js": "", agentCoreNodePackage + "package.json": `{"version":"0.1.0"}`, agentCoreNodePackageStart: agentCoreTestWrapper}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, s, f, backend := newAgentCoreZIPTest(t, tc.language, tc.entry, tc.files)
			original := maps.Clone(f.live.EnvironmentVariables)
			originalArtifact := liveAgentCoreArtifact(t, f)
			ctx := context.Background()
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			want := originalArtifact
			want.EntryPoint = tc.wantEntry
			if len(f.updates) != 1 || !liveAgentCoreArtifact(t, f).equal(want) {
				t.Fatalf("entry point not swapped in place: updates=%d artifact=%+v state=%v", len(f.updates), liveAgentCoreArtifact(t, f), s.state)
			}
			env := f.live.EnvironmentVariables
			if env[agentcore.EntrypointEnv] != tc.wantScript {
				t.Errorf("%s = %q, want %q", agentcore.EntrypointEnv, env[agentcore.EntrypointEnv], tc.wantScript)
			}
			if env[agentcore.EnabledEnv] != "true" || env[agentcore.DistroEnv] != tc.wantDistro || env["OTEL_PROPAGATORS"] != "tracecontext,baggage,xray" {
				t.Errorf("package recipe not applied: %v", env)
			}
			if pkg := agentcore.ParsePackage(env[agentcore.PackageEnv]); pkg.Mode != agentCoreAdopt || pkg.SDK != string(agentCoreSDKOdigos) {
				t.Errorf("package contract %+v", pkg)
			}
			if err := r.Reconcile(ctx); err != nil || len(f.updates) != 1 || backend.reads != 1 {
				t.Fatalf("converged runtime was re-detected or redeployed: updates=%d reads=%d %v", len(f.updates), backend.reads, err)
			}
			s.sources[0].Enabled = false
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(original, f.live.EnvironmentVariables) || !liveAgentCoreArtifact(t, f).equal(originalArtifact) {
				t.Errorf("rollback did not restore the runtime: %+v %v", liveAgentCoreArtifact(t, f), f.live.EnvironmentVariables)
			}
		})
	}
}

// TestAgentCoreRedeployWithoutTheSDKRollsBackAndWaits: the customer's next deployment dropped the package. The
// runtime is returned to the customer's configuration, keeping the artifact and entry point they just deployed.
func TestAgentCoreRedeployWithoutTheSDKRollsBackAndWaits(t *testing.T) {
	r, s, f, backend := newAgentCoreZIPTest(t, "python", []string{"app.py"},
		map[string]string{"app.py": "", agentCorePythonStart: agentCoreTestWrapper, "odigos_agentcore-0.1.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""})
	original := maps.Clone(f.live.EnvironmentVariables)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil || len(f.updates) != 1 {
		t.Fatalf("package not installed: %v %v", err, s.state)
	}
	redeployed := agentCoreArtifactRef{Bucket: "code", Key: "agent.zip", Version: "v2", Runtime: "PYTHON_3_12", EntryPoint: []string{"app.py"}}
	f.live.AgentRuntimeArtifact = redeployed.sdk()
	f.live.AgentRuntimeVersion = aws.String("10")
	backend.code = testAgentCoreZIP(t, map[string]string{"app.py": ""})
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if len(f.updates) != 2 || f.live.EnvironmentVariables["ODIGOS_MANAGED"] != "" || !maps.Equal(original, f.live.EnvironmentVariables) {
		t.Fatalf("runtime not rolled back: updates=%d env=%v", len(f.updates), f.live.EnvironmentVariables)
	}
	if !liveAgentCoreArtifact(t, f).equal(redeployed) {
		t.Fatalf("rollback replaced the customer's new deployment: %+v", liveAgentCoreArtifact(t, f))
	}
	if note := agentCoreRoutingNote(t, s); s.state["waiting_for"] != baseapi.WaitingForArtifact || note == nil || note.Code != agentCoreArtifactNote {
		t.Fatalf("waiting not reported: %v", s.state)
	}
	if s.records[0].Origin != "" {
		t.Error("the rolled-back runtime still reads as instrumented")
	}
	for range 2 {
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.updates) != 2 || s.state["waiting_for"] != baseapi.WaitingForArtifact {
		t.Fatalf("waiting runtime was updated again: %d %v", len(f.updates), s.state)
	}
}

// TestAgentCoreResolvesImagesWithoutRewritingThem: for an image, every mode changes runtime environment only,
// and never needs the repository to be writable or in the runtime's account.
func TestAgentCoreResolvesImagesWithoutRewritingThem(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      v1.Config
		files    map[string]string
		liveEnv  map[string]string
		mode     agentCoreSDKKind
		loader   map[string]string
		waitHint string
	}{
		{name: "node package", mode: agentCoreSDKOdigos,
			cfg:    v1.Config{WorkingDir: "/app", Env: []string{"NODE_VERSION=22.1.0", "NODE_OPTIONS=--max-old-space-size=128"}, Cmd: []string{"node", "index.js"}},
			files:  map[string]string{"app/node_modules/@odigos/agentcore/package.json": `{"version":"0.1.0"}`},
			loader: map[string]string{"NODE_OPTIONS": "--max-old-space-size=128 --require /app/node_modules/@odigos/agentcore/register.cjs"}},
		{name: "python package", mode: agentCoreSDKOdigos,
			cfg: v1.Config{WorkingDir: "/app", Env: []string{"PYTHON_VERSION=3.12.9"}, Cmd: []string{"python", "app.py"}},
			files: map[string]string{"usr/local/lib/python3.12/site-packages/odigos_agentcore-0.1.0.dist-info/METADATA": "",
				"usr/local/lib/python3.12/site-packages/aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""},
			loader: map[string]string{}},
		{name: "node adopt", mode: agentCoreSDKADOT,
			cfg:     v1.Config{WorkingDir: "/app", Env: []string{"NODE_VERSION=22.1.0", "NODE_OPTIONS=--image-flag"}, Cmd: []string{"node", "index.js"}},
			liveEnv: map[string]string{"NODE_OPTIONS": "--enable-source-maps"},
			files:   map[string]string{"app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json": "{}"},
			loader:  map[string]string{"NODE_OPTIONS": "--enable-source-maps --require @aws/aws-distro-opentelemetry-node-autoinstrumentation/register"}},
		{name: "node adopt outside the working directory", mode: agentCoreSDKADOT,
			cfg:    v1.Config{WorkingDir: "/app/src", Env: []string{"NODE_VERSION=22.1.0"}, Cmd: []string{"node", "index.js"}},
			files:  map[string]string{"app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json": "{}"},
			loader: map[string]string{"NODE_OPTIONS": "--require /app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/build/src/register.js"}},
		{name: "node adopt already registered", mode: agentCoreSDKADOT,
			cfg:    v1.Config{WorkingDir: "/app", Env: []string{"NODE_VERSION=22.1.0"}, Cmd: []string{"node", "--require", "@aws/aws-distro-opentelemetry-node-autoinstrumentation/register", "index.js"}},
			files:  map[string]string{"app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json": "{}"},
			loader: map[string]string{}},
		{name: "python adopt through its launcher", mode: agentCoreSDKADOT,
			cfg:    v1.Config{Env: []string{"PYTHON_VERSION=3.12.9"}, Cmd: []string{"opentelemetry-instrument", "python", "-m", "agent"}},
			files:  map[string]string{"usr/local/lib/python3.12/site-packages/aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""},
			loader: map[string]string{}},
		{name: "prepared carrier bundle", mode: agentCoreSDKOdigos,
			cfg:    v1.Config{WorkingDir: "/app", Env: []string{"PYTHON_VERSION=3.12.9", "PYTHONPATH=/customer"}},
			files:  map[string]string{agentCoreImageManifest: agentCoreTestManifestFor("python")},
			loader: map[string]string{"PYTHONPATH": "/opt/odigos/agentcore/bootstrap/python:/app:/customer"}},
		{name: "python ADOT without its launcher", waitHint: "opentelemetry-instrument",
			cfg:   v1.Config{Env: []string{"PYTHON_VERSION=3.12.9"}, Cmd: []string{"python", "app.py"}},
			files: map[string]string{"usr/local/lib/python3.12/site-packages/aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := testAgentCoreImage(t, v1.ConfigFile{Architecture: "arm64", OS: "linux", Config: tc.cfg}, tc.files)
			i := &agentCoreArtifactInstaller{io: &agentCoreLocalArtifacts{image: image}}
			source := agentCoreArtifactRef{Image: "public.ecr.aws/example/agent:1"}
			live := &ac.GetAgentRuntimeOutput{AgentRuntimeArn: aws.String("arn:aws:bedrock-agentcore:us-east-1:111122223333:runtime/demo-1234567890"),
				AgentRuntimeArtifact: source.sdk(), EnvironmentVariables: tc.liveEnv}
			result, waiting, err := i.Resolve(context.Background(), live, basestore.SourceRecord{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.waitHint != "" {
				if waiting == nil || !strings.Contains(waiting.Hint, tc.waitHint) {
					t.Fatalf("want a wait mentioning %q, got %+v %+v", tc.waitHint, waiting, result)
				}
				return
			}
			if waiting != nil || result.SDK != tc.mode || result.Package.SDK != string(tc.mode) || result.Package.Mode != agentCoreAdopt || !result.Artifact.equal(source) {
				t.Fatalf("got %+v waiting=%+v", result, waiting)
			}
			if !maps.Equal(result.LoaderEnv, tc.loader) {
				t.Errorf("loader %v, want %v", result.LoaderEnv, tc.loader)
			}
		})
	}
}

// TestAgentCoreSelectedDistroMissingFromTheArtifactWaits: a distro selection the artifact cannot start is the same
// wait as a missing SDK, whether it arrives with the first installation or changes under a managed runtime. The hint
// names the dependency that adds the SDK, and the runtime is instrumented as soon as the selection fits again.
func TestAgentCoreSelectedDistroMissingFromTheArtifactWaits(t *testing.T) {
	t.Run("python package with one extra, selected before installation", func(t *testing.T) {
		r, s, f, _ := newAgentCoreZIPTest(t, "python", []string{"app.py"},
			map[string]string{"app.py": "", agentCorePythonStart: agentCoreTestWrapper, "odigos_agentcore-0.1.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""})
		s.sources[0].Config.DistroOverride.OtelDistroName = "python-agentcore-openllmetry"
		ctx := context.Background()
		for range 2 {
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		note := agentCoreRoutingNote(t, s)
		if len(f.updates) != 0 || s.state["waiting_for"] != baseapi.WaitingForArtifact || s.state["instrumentation_error"] != "" || note == nil || note.Code != agentCoreArtifactNote {
			t.Fatalf("mismatch not reported as a wait: updates=%d state=%v", len(f.updates), s.state)
		}
		if !strings.Contains(note.Message, "odigos-agentcore[openllmetry]") || !strings.Contains(note.Message, "python-agentcore-adot") {
			t.Fatalf("hint names neither the extra nor the carried distro: %q", note.Message)
		}
		s.sources[0].Config.DistroOverride.OtelDistroName = "python-agentcore-adot"
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if len(f.updates) != 1 || s.state["waiting_for"] != "" || f.live.EnvironmentVariables[agentcore.DistroEnv] != "python-agentcore-adot" {
			t.Fatalf("fitting selection not installed: updates=%d state=%v env=%v", len(f.updates), s.state, f.live.EnvironmentVariables)
		}
	})
	t.Run("node adopt, selection changed under management", func(t *testing.T) {
		r, s, f, _ := newAgentCoreZIPTest(t, "nodejs", []string{"index.js"},
			map[string]string{"index.js": "", agentCoreNodeADOT + "package.json": `{"version":"0.7.0"}`})
		original := maps.Clone(f.live.EnvironmentVariables)
		ctx := context.Background()
		if err := r.Reconcile(ctx); err != nil || len(f.updates) != 1 {
			t.Fatalf("ADOT not adopted: %v %v", err, s.state)
		}
		s.sources[0].Config.DistroOverride.OtelDistroName = "nodejs-agentcore-openllmetry"
		for range 2 {
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if len(f.updates) != 2 || !maps.Equal(original, f.live.EnvironmentVariables) {
			t.Fatalf("runtime not returned to the customer's configuration: updates=%d env=%v", len(f.updates), f.live.EnvironmentVariables)
		}
		note := agentCoreRoutingNote(t, s)
		if s.state["waiting_for"] != baseapi.WaitingForArtifact || note == nil || !strings.Contains(note.Message, "@odigos/agentcore") || s.records[0].Origin != "" {
			t.Fatalf("waiting not reported: %v %v", s.state, s.records[0])
		}
		s.sources[0].Config.DistroOverride.OtelDistroName = ""
		if err := r.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if len(f.updates) != 3 || s.state["waiting_for"] != "" || f.live.EnvironmentVariables["ODIGOS_MANAGED"] == "" {
			t.Fatalf("ADOT not adopted again: updates=%d state=%v", len(f.updates), s.state)
		}
	})
}

// TestAgentCoreOdigosZIPWithoutItsWrapper: the Odigos package's start wrapper lands at the ZIP root when the
// package is installed there. Without it the package cannot start, so adoption falls back to the ADOT the agent
// carries (the Python package's [adot] extra installs it) and otherwise waits for the build that puts it in place.
func TestAgentCoreOdigosZIPWithoutItsWrapper(t *testing.T) {
	t.Run("falls back to the ADOT it carries", func(t *testing.T) {
		r, s, f, _ := newAgentCoreZIPTest(t, "python", []string{"app.py"},
			map[string]string{"app.py": "", "odigos_agentcore-0.1.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""})
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		pkg := agentcore.ParsePackage(f.live.EnvironmentVariables[agentcore.PackageEnv])
		if len(f.updates) != 1 || pkg.SDK != string(agentCoreSDKADOT) || !slices.Equal(liveAgentCoreArtifact(t, f).EntryPoint, []string{agentCoreLauncher, "app.py"}) {
			t.Fatalf("ADOT not adopted: updates=%d contract=%+v state=%v", len(f.updates), pkg, s.state)
		}
	})
	t.Run("waits without ADOT", func(t *testing.T) {
		r, s, f, _ := newAgentCoreZIPTest(t, "nodejs", []string{"index.js"},
			map[string]string{"index.js": "", agentCoreNodePackage + "package.json": `{"version":"0.1.0"}`})
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		note := agentCoreRoutingNote(t, s)
		if len(f.updates) != 0 || s.state["waiting_for"] != baseapi.WaitingForArtifact || note == nil || !strings.Contains(note.Message, agentCoreNodePackageStart) {
			t.Fatalf("missing wrapper not reported as a wait: updates=%d state=%v", len(f.updates), s.state)
		}
	})
}

// TestAgentCoreOdigosZIPWithAnEarlierWrapper: wrappers released before direct-code support only read the agent's
// script from a second entry point element, which AgentCore rejects. Such a package cannot start: adoption falls
// back to the ADOT the agent carries, and otherwise waits with the upgrade to apply, never an entry point AgentCore
// would refuse or a wrapper that cannot find the agent.
func TestAgentCoreOdigosZIPWithAnEarlierWrapper(t *testing.T) {
	old := "const [entry, ...args] = process.argv.slice(2);"
	t.Run("falls back to the ADOT it carries", func(t *testing.T) {
		r, s, f, _ := newAgentCoreZIPTest(t, "python", []string{"app.py"}, map[string]string{"app.py": "", agentCorePythonStart: old,
			"odigos_agentcore-0.0.25.dist-info/METADATA": "", "aws_opentelemetry_distro-0.21.0.dist-info/METADATA": ""})
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if pkg := agentcore.ParsePackage(f.live.EnvironmentVariables[agentcore.PackageEnv]); len(f.updates) != 1 || pkg.SDK != string(agentCoreSDKADOT) {
			t.Fatalf("ADOT not adopted: updates=%d contract=%+v state=%v", len(f.updates), pkg, s.state)
		}
	})
	t.Run("waits without ADOT", func(t *testing.T) {
		r, s, f, _ := newAgentCoreZIPTest(t, "nodejs", []string{"index.js"}, map[string]string{"index.js": "", agentCoreNodePackageStart: old,
			agentCoreNodePackage + "package.json": `{"version":"0.0.25"}`})
		if err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		note := agentCoreRoutingNote(t, s)
		if len(f.updates) != 0 || s.state["waiting_for"] != baseapi.WaitingForArtifact || note == nil || !strings.Contains(note.Message, "Upgrade @odigos/agentcore") {
			t.Fatalf("earlier wrapper not reported as a wait: updates=%d state=%v note=%+v", len(f.updates), s.state, note)
		}
	})
}

// TestAgentCorePackageZIPRedeployKeepingTheWrapper: a deployment that replaces the code but keeps the entry point
// Odigos set is detected against the customer's own script, and a rollback after it puts that script back as the
// entry point rather than leaving the wrapper without the variable that names it.
func TestAgentCorePackageZIPRedeployKeepingTheWrapper(t *testing.T) {
	files := map[string]string{"app.py": "", agentCorePythonStart: agentCoreTestWrapper, "odigos_agentcore-0.1.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": ""}
	r, s, f, backend := newAgentCoreZIPTest(t, "python", []string{"app.py"}, files)
	original := maps.Clone(f.live.EnvironmentVariables)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil || len(f.updates) != 1 {
		t.Fatalf("package not installed: %v %v", err, s.state)
	}
	// The customer's pipeline uploads new code and copies the runtime's current entry point.
	redeployed := liveAgentCoreArtifact(t, f)
	redeployed.Version = "v2"
	f.live.AgentRuntimeArtifact = redeployed.sdk()
	f.live.AgentRuntimeVersion = aws.String("10")
	backend.code = testAgentCoreZIP(t, files)
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := liveAgentCoreArtifact(t, f); !slices.Equal(got.EntryPoint, []string{agentCorePythonStart}) || got.Version != "v2" ||
		f.live.EnvironmentVariables[agentcore.EntrypointEnv] != "app.py" || s.state["instrumentation_error"] != "" || s.state["waiting_for"] != "" {
		t.Fatalf("redeploy not re-instrumented against the customer's script: %+v env=%v state=%v", got, f.live.EnvironmentVariables, s.state)
	}
	// Rollback after another redeploy that kept the wrapper: new code, customer's entry point.
	redeployed = liveAgentCoreArtifact(t, f)
	redeployed.Version = "v3"
	f.live.AgentRuntimeArtifact = redeployed.sdk()
	f.live.AgentRuntimeVersion = aws.String("11")
	s.sources[0].Enabled = false
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	got := liveAgentCoreArtifact(t, f)
	if !slices.Equal(got.EntryPoint, []string{"app.py"}) || got.Version != "v3" || !maps.Equal(original, f.live.EnvironmentVariables) {
		t.Fatalf("rollback left the wrapper without its script: %+v env=%v", got, f.live.EnvironmentVariables)
	}
}

func TestAgentCoreWrapperEntryPoint(t *testing.T) {
	env := map[string]string{agentcore.EntrypointEnv: "agent/main.py"}
	for _, tc := range []struct {
		name       string
		entry      []string
		env        map[string]string
		want       []string
		wantScript string
		refused    bool
	}{
		{name: "script", entry: []string{"app.py"}, want: []string{agentCorePythonStart}, wantScript: "app.py"},
		{name: "launcher prefix", entry: []string{"opentelemetry-instrument", "app.py"}, want: []string{agentCorePythonStart}, wantScript: "app.py"},
		{name: "already the wrapper", entry: []string{agentCorePythonStart}, env: env, want: []string{agentCorePythonStart}, wantScript: "agent/main.py"},
		{name: "wrapper without its script", entry: []string{agentCorePythonStart}, refused: true},
		{name: "two scripts", entry: []string{"app.py", "other.py"}, refused: true},
		{name: "absolute script", entry: []string{"/var/task/app.py"}, refused: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, script, err := agentCoreWrapperEntryPoint("python", tc.entry, tc.env, agentCorePythonStart)
			if tc.refused {
				if !errors.Is(err, ErrUnsupportedInstrumentation) {
					t.Fatalf("want a refusal, got %v %q %v", got, script, err)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) || script != tc.wantScript {
				t.Fatalf("got %v %q %v", got, script, err)
			}
		})
	}
}
