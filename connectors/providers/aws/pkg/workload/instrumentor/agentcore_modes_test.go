package instrumentor

import (
	"errors"
	"maps"
	"strings"
	"testing"

	dest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/agentcore"
)

// TestChooseAgentCoreSDK: adoption starts the override's SDK when the agent carries one that can run it, otherwise
// the Odigos package, otherwise ADOT, including when the Odigos package cannot run in that agent.
func TestChooseAgentCoreSDK(t *testing.T) {
	pkg := &agentCoreOdigosPackage{Distros: []string{"python-agentcore-adot"}}
	bothPkg := &agentCoreOdigosPackage{Distros: []string{"python-agentcore-adot", "python-agentcore-openllmetry"}}
	nodePkg := &agentCoreOdigosPackage{Distros: []string{"nodejs-agentcore-adot", "nodejs-agentcore-openllmetry"}}
	adot := &agentCoreSDK{}
	// A ZIP's Odigos package starts through its start wrapper.
	pyZIP := func(f agentCoreArtifactFacts) agentCoreArtifactFacts {
		f.StartWrapper, f.StartWrapperReadsEnv = agentCorePythonStart, true
		return f
	}
	nodeZIP := func(f agentCoreArtifactFacts) agentCoreArtifactFacts {
		f.StartWrapper, f.StartWrapperReadsEnv = agentCoreNodePackageStart, true
		return f
	}
	for _, tc := range []struct {
		name     string
		facts    agentCoreArtifactFacts
		override string
		want     agentCoreSDKKind
		hint     []string // every fragment must appear; non-nil means a wait is expected
		refused  bool
	}{
		{name: "Odigos wins over ADOT", facts: pyZIP(agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, ADOT: adot}), want: agentCoreSDKOdigos},
		{name: "prepared bundle is Odigos", facts: pyZIP(agentCoreArtifactFacts{Language: "python", PreparedBundle: true}), want: agentCoreSDKOdigos},
		{name: "ADOT zip", facts: agentCoreArtifactFacts{Language: "python", ADOT: adot}, want: agentCoreSDKADOT},
		{name: "ADOT python image through its launcher", facts: agentCoreArtifactFacts{Language: "python", ADOT: adot, Image: true, LauncherPresent: true}, want: agentCoreSDKADOT},
		{name: "ADOT node image", facts: agentCoreArtifactFacts{Language: "nodejs", ADOT: adot, Image: true}, want: agentCoreSDKADOT},
		{name: "python image ADOT without launcher waits", facts: agentCoreArtifactFacts{Language: "python", ADOT: adot, Image: true}, hint: []string{"opentelemetry-instrument", "odigos-agentcore[adot]"}},
		{name: "python without an SDK waits", facts: agentCoreArtifactFacts{Language: "python"}, hint: []string{"odigos-agentcore[adot]", "redeploy."}},
		{name: "node without an SDK waits", facts: agentCoreArtifactFacts{Language: "nodejs", Image: true}, hint: []string{"@odigos/agentcore", "redeploy."}},
		{name: "upstream SDK is not adopted", facts: agentCoreArtifactFacts{Language: "nodejs", OtherSDK: true}, hint: []string{"not ADOT", "@odigos/agentcore"}},
		{name: "Odigos without an SDK extra waits", facts: pyZIP(agentCoreArtifactFacts{Language: "python", OdigosPackage: &agentCoreOdigosPackage{}}), hint: []string{"odigos-agentcore[adot]", "extra"}},
		{name: "Odigos zip without its start wrapper waits", facts: agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg}, hint: []string{"start wrapper", agentCorePythonStart}},
		{name: "Odigos zip with an earlier wrapper waits", facts: agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, StartWrapper: agentCoreNodePackageStart}, hint: []string{"earlier release", "@odigos/agentcore"}},
		{name: "Odigos refuses sitecustomize", facts: pyZIP(agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, Sitecustomize: true}), refused: true},
		{name: "Odigos image accepts sitecustomize", facts: agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, Image: true, Sitecustomize: true}, want: agentCoreSDKOdigos},
		{name: "prepared bundle image refuses sitecustomize", facts: agentCoreArtifactFacts{Language: "python", PreparedBundle: true, Image: true, Sitecustomize: true}, refused: true},
		{name: "zip node register plus Odigos", facts: nodeZIP(agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, LauncherPresent: true, LauncherInEnv: true}), refused: true},
		{name: "node upstream preload plus Odigos", facts: agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, Image: true, UpstreamPreload: true}, refused: true},
		{name: "zip node launcher prefix plus Odigos is replaced", facts: nodeZIP(agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, LauncherPresent: true}), want: agentCoreSDKOdigos},
		{name: "Odigos refuses ESM", facts: nodeZIP(agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, ESM: true}), refused: true},
		{name: "python image launcher plus Odigos", facts: agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, Image: true, LauncherPresent: true}, refused: true},
		{name: "node image register plus Odigos", facts: agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, Image: true, LauncherPresent: true}, refused: true},
		{name: "zip launcher plus Odigos is replaced, not refused", facts: pyZIP(agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, LauncherPresent: true}), want: agentCoreSDKOdigos},
		{name: "ADOT accepts sitecustomize", facts: agentCoreArtifactFacts{Language: "python", ADOT: adot, Sitecustomize: true}, want: agentCoreSDKADOT},
		{name: "ADOT accepts ESM", facts: agentCoreArtifactFacts{Language: "nodejs", ADOT: adot, ESM: true}, want: agentCoreSDKADOT},

		// Fallback: the Odigos package that cannot run in this agent gives way to the ADOT it also carries.
		{name: "Odigos ESM falls back to ADOT", facts: nodeZIP(agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, ADOT: adot, ESM: true}), want: agentCoreSDKADOT},
		{name: "image launcher falls back to ADOT", facts: agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, ADOT: adot, Image: true, LauncherPresent: true}, want: agentCoreSDKADOT},
		{name: "zip without the wrapper falls back to ADOT", facts: agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, ADOT: adot}, want: agentCoreSDKADOT},

		// Override: the selected distro decides, through the Odigos package when it can start it.
		{name: "override ADOT through the Odigos package", facts: pyZIP(agentCoreArtifactFacts{Language: "python", OdigosPackage: pkg, ADOT: adot}), override: "python-agentcore-adot", want: agentCoreSDKOdigos},
		{name: "override ADOT the Odigos package lacks", facts: pyZIP(agentCoreArtifactFacts{Language: "python", OdigosPackage: &agentCoreOdigosPackage{Distros: []string{"python-agentcore-openllmetry"}}, ADOT: adot}), override: "python-agentcore-adot", want: agentCoreSDKADOT},
		{name: "override OpenLLMetry through the Odigos package", facts: pyZIP(agentCoreArtifactFacts{Language: "python", OdigosPackage: bothPkg, ADOT: adot}), override: "python-agentcore-openllmetry", want: agentCoreSDKOdigos},
		{name: "override OpenLLMetry with ADOT only waits", facts: agentCoreArtifactFacts{Language: "python", ADOT: adot}, override: "python-agentcore-openllmetry", hint: []string{"python-agentcore-openllmetry", "odigos-agentcore[openllmetry]", "select python-agentcore-adot"}},
		{name: "override the Odigos package cannot run is refused", facts: nodeZIP(agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, ADOT: adot, ESM: true}), override: "nodejs-agentcore-openllmetry", refused: true},
		{name: "override ADOT the Odigos package cannot run falls back", facts: nodeZIP(agentCoreArtifactFacts{Language: "nodejs", OdigosPackage: nodePkg, ADOT: adot, ESM: true}), override: "nodejs-agentcore-adot", want: agentCoreSDKADOT},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode, waiting, err := chooseAgentCoreSDK(tc.facts, tc.override)
			switch {
			case tc.refused:
				if err == nil || !errors.Is(err, ErrUnsupportedInstrumentation) || waiting != nil {
					t.Fatalf("want an unsupported refusal, got mode=%q waiting=%v err=%v", mode, waiting, err)
				}
			case tc.hint != nil:
				if err != nil || waiting == nil || mode != "" {
					t.Fatalf("want a wait, got mode=%q waiting=%v err=%v", mode, waiting, err)
				}
				for _, fragment := range tc.hint {
					if !strings.Contains(waiting.Hint, fragment) {
						t.Errorf("hint %q does not mention %q", waiting.Hint, fragment)
					}
				}
			default:
				if err != nil || waiting != nil || mode != tc.want {
					t.Fatalf("got mode=%q waiting=%v err=%v, want %q", mode, waiting, err, tc.want)
				}
			}
		})
	}
}

// TestAgentCoreAdoptEnvDiffersOnlyWhereNoOdigosCodeRuns: the ADOT recipe is the Odigos recipe minus the
// bootstrap's switches and with X-Ray propagation dropped; every export, signal and redaction setting is the same.
func TestAgentCoreAdoptEnvDiffersOnlyWhereNoOdigosCodeRuns(t *testing.T) {
	target := dest.OTLPTarget{HTTPEndpoint: "https://otlp.example.test", Traces: true, Metrics: true, Logs: true}
	for _, language := range []string{"python", "nodejs"} {
		d, err := ResolveAgentCoreDistro(language, language+"-agentcore-adot")
		if err != nil {
			t.Fatal(err)
		}
		full := agentCoreOdigosEnv(d, "svc", target, map[string]string{"k": "v"})
		adopt := agentCoreADOTEnv(d, "svc", target, map[string]string{"k": "v"})
		want := maps.Clone(full)
		want[agentcore.EnabledEnv], want[agentcore.DistroEnv], want["OTEL_PROPAGATORS"] = "", "", "tracecontext,baggage"
		if !maps.Equal(adopt, want) {
			t.Errorf("%s: adopt recipe %v, want %v", language, adopt, want)
		}
		if full["OTEL_PROPAGATORS"] != "tracecontext,baggage,xray" || full[agentcore.EnabledEnv] != "true" {
			t.Errorf("%s: deriving the adopt recipe changed the package recipe", language)
		}
	}
}

func TestAgentCoreLauncherEntryPoint(t *testing.T) {
	for _, tc := range []struct {
		language string
		entry    []string
		env      map[string]string
		want     []string // nil: refused
	}{
		{"python", []string{"app.py"}, nil, []string{agentCoreLauncher, "app.py"}},
		{"python", []string{agentCoreLauncher, "app.py"}, nil, []string{agentCoreLauncher, "app.py"}},
		{"python", []string{agentCorePythonStart, "src/app.py"}, nil, []string{agentCoreLauncher, "src/app.py"}},
		// The Odigos start wrapper alone names its script in the environment; alone without it, it names none.
		{"python", []string{agentCorePythonStart}, map[string]string{agentcore.EntrypointEnv: "app.py"}, []string{agentCoreLauncher, "app.py"}},
		{"python", []string{agentCorePythonStart}, nil, nil},
		{"python", []string{agentCoreLauncher}, nil, nil},
		{"python", []string{"app.py", "--port"}, nil, nil},
		{"python", []string{"python", "app.py"}, nil, nil},
		{"python", []string{"../app.py"}, nil, nil},
		{"nodejs", []string{"index.js"}, nil, []string{agentCoreLauncher, "index.js"}},
		{"nodejs", []string{agentCoreNodeBundleStart, "index.js"}, nil, []string{agentCoreLauncher, "index.js"}},
		{"nodejs", []string{"index.mjs"}, nil, nil},
	} {
		got, err := agentCoreLauncherEntryPoint(tc.language, tc.entry, tc.env)
		if tc.want == nil {
			if err == nil {
				t.Errorf("%v: accepted as %v", tc.entry, got)
			}
			continue
		}
		if err != nil || strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%v = %v, %v; want %v", tc.entry, got, err, tc.want)
		}
	}
}

// TestAgentCoreLegacyRecordsReadAsTheirSDK: contracts and snapshots written before the SDK was recorded named the
// delivery instead. They keep reading as the SDK that delivery started, and the recorded SDK wins when present.
func TestAgentCoreLegacyRecordsReadAsTheirSDK(t *testing.T) {
	for _, tc := range []struct {
		pkg  agentcore.Package
		want agentCoreSDKKind
	}{
		{agentcore.Package{Version: 1}, agentCoreSDKOdigos},
		{agentcore.Package{Version: 1, Mode: "adopt"}, agentCoreSDKADOT},
		{agentcore.Package{Version: 1, Mode: "adopt", SDK: "odigos"}, agentCoreSDKOdigos},
		{agentcore.Package{Version: 1, Mode: "adopt", SDK: "adot"}, agentCoreSDKADOT},
	} {
		if got := agentCorePackageSDK(tc.pkg); got != tc.want {
			t.Errorf("contract %+v reads as %q, want %q", tc.pkg, got, tc.want)
		}
	}
	for _, tc := range []struct {
		snapshot agentCoreSnapshot
		want     agentCoreSDKKind
	}{
		{agentCoreSnapshot{}, agentCoreSDKOdigos},
		{agentCoreSnapshot{Mode: "package"}, agentCoreSDKOdigos},
		{agentCoreSnapshot{Mode: "adopt"}, agentCoreSDKADOT},
		{agentCoreSnapshot{SDK: agentCoreSDKADOT}, agentCoreSDKADOT},
	} {
		if got := agentCoreSnapshotSDK(tc.snapshot); got != tc.want {
			t.Errorf("snapshot %+v reads as %q, want %q", tc.snapshot, got, tc.want)
		}
	}
}
