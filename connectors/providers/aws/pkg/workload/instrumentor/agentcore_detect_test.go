package instrumentor

import (
	"bytes"
	"context"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
)

const agentCoreTestManifest = `{"package":{"v":1,"language":"%s","distros":["%s-agentcore-adot"],"moduleFormat":"commonjs"},"sha256":{"a":"b"}}`

func agentCoreTestManifestFor(language string) string {
	return strings.ReplaceAll(agentCoreTestManifest, "%s", language)
}

func TestDetectAgentCoreZIP(t *testing.T) {
	for _, tc := range []struct {
		name     string
		language string
		entry    []string
		env      map[string]string
		files    map[string]string
		check    func(*testing.T, agentCoreArtifactFacts)
	}{
		{name: "python package with the adot extra", language: "python", entry: []string{"app.py"},
			files: map[string]string{"app.py": "", "odigos_agentcore_start.py": agentCoreTestWrapper, "odigos_agentcore-0.1.0.dist-info/METADATA": "",
				"aws_opentelemetry_distro-0.10.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/RECORD": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.OdigosPackage == nil || strings.Join(f.OdigosPackage.Distros, ",") != "python-agentcore-adot" {
					t.Fatalf("package not detected with its extra: %+v", f.OdigosPackage)
				}
				if f.ADOT == nil || f.StartWrapper != agentCorePythonStart || f.LauncherPresent || f.Image {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "python package with both extras", language: "python", entry: []string{"app.py"},
			files: map[string]string{"app.py": "", "odigos_agentcore-0.1.0.dist-info/METADATA": "", "aws_opentelemetry_distro-0.10.0.dist-info/METADATA": "",
				"opentelemetry_instrumentation_bedrock-0.40.0.dist-info/METADATA": "", "opentelemetry_distro-0.50b0.dist-info/METADATA": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if strings.Join(f.OdigosPackage.Distros, ",") != "python-agentcore-adot,python-agentcore-openllmetry" || f.StartWrapper != "" {
					t.Fatalf("facts %+v / %+v", f, f.OdigosPackage)
				}
			}},
		{name: "python package without an extra", language: "python", entry: []string{"app.py"},
			files: map[string]string{"app.py": "", "Odigos_AgentCore-0.1.0.dist-info/METADATA": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.OdigosPackage == nil || len(f.OdigosPackage.Distros) != 0 || f.ADOT != nil {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "python ADOT through its launcher", language: "python", entry: []string{"opentelemetry-instrument", "src/app.py"},
			files: map[string]string{"src/app.py": "", "aws_opentelemetry_distro-0.12.1.dist-info/METADATA": "", "src/sitecustomize.py": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.ADOT == nil || f.OdigosPackage != nil || !f.LauncherPresent || !f.Sitecustomize {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "python upstream distro", language: "python", entry: []string{"app.py"},
			files: map[string]string{"app.py": "", "opentelemetry_distro-0.50b0.dist-info/METADATA": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if !f.OtherSDK || f.ADOT != nil || f.Sitecustomize {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "python prepared bundle", language: "python", entry: []string{agentCorePythonStart, "app.py"},
			files: map[string]string{"app.py": "", agentCorePythonStart: agentCoreTestWrapper, agentCoreZIPManifest: agentCoreTestManifestFor("python"), "sitecustomize.py": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if !f.PreparedBundle || f.BundlePackage.Language != "python" || f.StartWrapper != agentCorePythonStart || !f.Sitecustomize {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "node package", language: "nodejs", entry: []string{"index.js"},
			files: map[string]string{"index.js": "", "package.json": `{"type":"commonjs"}`, agentCoreNodePackage + "package.json": `{"version":"0.2.0"}`,
				agentCoreNodePackageStart: agentCoreTestWrapper, agentCoreNodeADOT + "package.json": `{"version":"0.7.0"}`, agentCoreNodeUpstream + "package.json": "{}"},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.OdigosPackage == nil || len(f.OdigosPackage.Distros) != 2 || f.StartWrapper != agentCoreNodePackageStart {
					t.Fatalf("facts %+v", f)
				}
				if f.ADOT == nil || !f.OtherSDK || f.ESM {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "node ESM scope above the script", language: "nodejs", entry: []string{"src/index.js"},
			files: map[string]string{"src/index.js": "", "package.json": `{"type":"module"}`, "node_modules/dep/package.json": `{"type":"module"}`},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if !f.ESM {
					t.Fatalf("ESM not detected: %+v", f)
				}
			}},
		{name: "node ESM in a dependency only", language: "nodejs", entry: []string{"index.js"},
			files: map[string]string{"index.js": "", "node_modules/dep/package.json": `{"type":"module"}`, "other/package.json": `{"type":"module"}`},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.ESM {
					t.Fatal("a dependency's or sibling's module scope was read as the agent's")
				}
			}},
		{name: "node ADOT already required", language: "nodejs", entry: []string{"index.js"},
			env:   map[string]string{"NODE_OPTIONS": "--require @aws/aws-distro-opentelemetry-node-autoinstrumentation/register"},
			files: map[string]string{"index.js": "", agentCoreNodeADOT + "package.json": `{"version":"0.7.0"}`},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.ADOT == nil || !f.LauncherPresent || f.OdigosPackage != nil {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "node prepared bundle", language: "nodejs", entry: []string{agentCoreNodeBundleStart, "index.js"},
			files: map[string]string{"index.js": "", agentCoreNodeBundleStart: "", agentCoreZIPManifest: agentCoreTestManifestFor("nodejs")},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if !f.PreparedBundle || f.StartWrapper != agentCoreNodeBundleStart {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "bare", language: "python", entry: []string{"app.py"}, files: map[string]string{"app.py": "", "requests-2.0.dist-info/METADATA": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.OdigosPackage != nil || f.ADOT != nil || f.OtherSDK || f.PreparedBundle {
					t.Fatalf("facts %+v", f)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := detectAgentCoreZIP(bytes.NewReader(testAgentCoreZIP(t, tc.files)), tc.language, "3.12", tc.entry, tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if f.Language != tc.language {
				t.Fatalf("language %q", f.Language)
			}
			tc.check(t, f)
		})
	}
	if _, err := detectAgentCoreZIP(bytes.NewReader(testAgentCoreZIP(t, map[string]string{"app.py": "", agentCoreZIPManifest: agentCoreTestManifestFor("nodejs")})), "python", "3.12", []string{"app.py"}, nil); err == nil {
		t.Fatal("a bundle for another language was accepted")
	}
	if _, err := detectAgentCoreZIP(bytes.NewReader([]byte("not a zip")), "python", "3.12", []string{"app.py"}, nil); err == nil {
		t.Fatal("unreadable ZIP accepted")
	}
}

func TestDetectAgentCoreImage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cfg      v1.Config
		files    map[string]string
		liveEnv  map[string]string
		language string
		check    func(*testing.T, agentCoreArtifactFacts)
	}{
		{name: "python venv package beside its extras", cfg: v1.Config{WorkingDir: "/app", Env: []string{"PYTHON_VERSION=3.12.9"}, Cmd: []string{"opentelemetry-instrument", "python", "app.py"}},
			files: map[string]string{
				"app/.venv/lib/python3.12/site-packages/odigos_agentcore-0.1.0.dist-info/METADATA":                       "",
				"app/.venv/lib/python3.12/site-packages/aws_opentelemetry_distro-0.10.0.dist-info/METADATA":              "",
				"app/.venv/lib/python3.12/site-packages/opentelemetry_instrumentation_bedrock-0.40.0.dist-info/METADATA": "",
				// An SDK in another environment is not one the package can start.
				"usr/local/lib/python3.12/site-packages/opentelemetry_instrumentation_bedrock-0.40.0.dist-info/METADATA": "",
			},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.Language != "python" || f.RuntimeVersion != "3.12" || !f.Image || f.WorkingDir != "/app" {
					t.Fatalf("facts %+v", f)
				}
				if f.OdigosPackage == nil || f.OdigosPackage.Dir != "/app/.venv/lib/python3.12/site-packages" || len(f.OdigosPackage.Distros) != 2 {
					t.Fatalf("package %+v", f.OdigosPackage)
				}
				if f.ADOT == nil || !f.LauncherPresent || f.Sitecustomize {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "python language from the SDK it carries", cfg: v1.Config{Entrypoint: []string{"/entry.sh"}},
			files: map[string]string{"usr/lib/python3/dist-packages/aws_opentelemetry_distro-0.10.0.dist-info/METADATA": "", "sitecustomize.py": ""},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.Language != "python" || f.ADOT == nil || f.ADOT.Dir != "/usr/lib/python3/dist-packages" || f.LauncherPresent || !f.Sitecustomize {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "node package in the app beside a nested ADOT", cfg: v1.Config{WorkingDir: "/srv/app", Env: []string{"NODE_VERSION=22.1.0"}, Cmd: []string{"node", "index.js"}},
			files: map[string]string{
				"srv/app/node_modules/@odigos/agentcore/package.json":                                                                     `{"version":"0.2.0"}`,
				"node_modules/@odigos/agentcore/package.json":                                                                             `{"version":"0.1.0"}`,
				"srv/app/node_modules/@odigos/agentcore/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json": `{"version":"0.7.0"}`,
				"srv/app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json":                                `{"version":"0.8.0"}`,
				"srv/app/package.json": `{"type":"commonjs"}`,
			},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.Language != "nodejs" || f.RuntimeVersion != "22.1" || f.OdigosPackage == nil || f.OdigosPackage.Dir != "/srv/app" {
					t.Fatalf("package %+v", f.OdigosPackage)
				}
				if f.ADOT == nil || f.ADOT.Dir != "/srv/app" || f.ESM || f.LauncherPresent {
					t.Fatalf("facts %+v / %+v", f, f.ADOT)
				}
			}},
		{name: "node ADOT register in the runtime's NODE_OPTIONS", cfg: v1.Config{WorkingDir: "/app", Env: []string{"NODE_VERSION=22.1.0"}, Cmd: []string{"node", "index.mjs"}},
			liveEnv: map[string]string{"NODE_OPTIONS": "--require @aws/aws-distro-opentelemetry-node-autoinstrumentation/register"},
			files:   map[string]string{"app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json": "{}"},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if f.ADOT == nil || !f.LauncherPresent || !f.ESM {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "node upstream SDK from the toolkit", cfg: v1.Config{WorkingDir: "/app", Env: []string{"NODE_VERSION=22.1.0"}, Cmd: []string{"node", "--require", "@opentelemetry/auto-instrumentations-node/register", "index.js"}},
			files: map[string]string{"app/node_modules/@opentelemetry/auto-instrumentations-node/package.json": "{}", "app/package.json": `{"type":"module"}`},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if !f.OtherSDK || f.ADOT != nil || f.LauncherPresent || !f.ESM {
					t.Fatalf("facts %+v", f)
				}
			}},
		{name: "prepared carrier bundle", language: "nodejs", cfg: v1.Config{},
			files: map[string]string{agentCoreImageManifest: agentCoreTestManifestFor("nodejs")},
			check: func(t *testing.T, f agentCoreArtifactFacts) {
				if !f.PreparedBundle || f.BundlePackage.Language != "nodejs" || f.WorkingDir != "/" {
					t.Fatalf("facts %+v", f)
				}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := v1.ConfigFile{Architecture: "arm64", OS: "linux", Config: tc.cfg}
			image := testAgentCoreImage(t, cfg, tc.files)
			stored, err := image.ConfigFile()
			if err != nil {
				t.Fatal(err)
			}
			f, err := detectAgentCoreImage(context.Background(), image, stored, tc.language, "", tc.liveEnv)
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, f)
		})
	}
	bare := testAgentCoreImage(t, v1.ConfigFile{Config: v1.Config{Cmd: []string{"/agent"}}}, map[string]string{"agent": ""})
	cfg, _ := bare.ConfigFile()
	if _, err := detectAgentCoreImage(context.Background(), bare, cfg, "", "", nil); err == nil {
		t.Fatal("an image with no language signal was accepted")
	}
	python := testAgentCoreImage(t, v1.ConfigFile{Config: v1.Config{Env: []string{"PYTHON_VERSION=3.12.1"}}}, map[string]string{"app.py": ""})
	cfg, _ = python.ConfigFile()
	if _, err := detectAgentCoreImage(context.Background(), python, cfg, "", "3.11", nil); err == nil {
		t.Fatal("a version override disagreeing with the image was accepted")
	}
}

// TestAgentCoreZIPLaunchersAndWrappers: a launcher in the entry point is AgentCore's own prefix and can be replaced;
// one in NODE_OPTIONS cannot. A wrapper counts as current only when it reads the agent's script from the environment.
func TestAgentCoreZIPLaunchersAndWrappers(t *testing.T) {
	node := map[string]string{"index.js": "", agentCoreNodePackage + "package.json": `{"version":"0.0.26"}`, agentCoreNodePackageStart: agentCoreTestWrapper}
	for _, tc := range []struct {
		name                      string
		entry                     []string
		env                       map[string]string
		files                     map[string]string
		launcher, inEnv, readsEnv bool
		wantWrapper               string
	}{
		{name: "prefix", entry: []string{"opentelemetry-instrument", "index.js"}, files: node, launcher: true, readsEnv: true, wantWrapper: agentCoreNodePackageStart},
		{name: "NODE_OPTIONS", entry: []string{"index.js"}, env: map[string]string{"NODE_OPTIONS": "--require " + agentCoreNodeADOTRegister}, files: node, launcher: true, inEnv: true, readsEnv: true, wantWrapper: agentCoreNodePackageStart},
		{name: "earlier wrapper", entry: []string{"index.js"}, wantWrapper: agentCoreNodePackageStart,
			files: map[string]string{"index.js": "", agentCoreNodePackage + "package.json": `{"version":"0.0.25"}`, agentCoreNodePackageStart: "process.argv.slice(2)"}},
		{name: "wrapper alone names the script in the environment", entry: []string{agentCoreNodePackageStart}, env: map[string]string{agentcore.EntrypointEnv: "index.js"}, files: node, readsEnv: true, wantWrapper: agentCoreNodePackageStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := detectAgentCoreZIP(bytes.NewReader(testAgentCoreZIP(t, tc.files)), "nodejs", "22", tc.entry, tc.env)
			if err != nil {
				t.Fatal(err)
			}
			if f.LauncherPresent != tc.launcher || f.LauncherInEnv != tc.inEnv || f.StartWrapperReadsEnv != tc.readsEnv || f.StartWrapper != tc.wantWrapper {
				t.Fatalf("launcher=%v inEnv=%v readsEnv=%v wrapper=%q", f.LauncherPresent, f.LauncherInEnv, f.StartWrapperReadsEnv, f.StartWrapper)
			}
		})
	}
}
