package instrumentor

import (
	"context"
	"fmt"
	"maps"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/agentcore"
)

type agentCoreArtifactInstaller struct {
	io agentCoreArtifactIO
}

func (i *agentCoreArtifactInstaller) Resolve(ctx context.Context, live *ac.GetAgentRuntimeOutput, src basestore.SourceRecord) (agentCoreInstallation, *agentCoreWaiting, error) {
	source, err := agentCoreArtifact(live.AgentRuntimeArtifact)
	if err != nil {
		return agentCoreInstallation{}, nil, err
	}
	r := agentcore.Describe("", live, nil)
	parts := strings.Split(r.ARN, ":")
	if len(parts) < 6 {
		return agentCoreInstallation{}, nil, fmt.Errorf("invalid runtime ARN")
	}
	region := parts[3]
	language, version := src.Config.RuntimeOverride.Language, src.Config.RuntimeOverride.Version
	override := src.Config.DistroOverride.OtelDistroName
	if source.Image != "" {
		return i.resolveImage(ctx, live, source, language, version, override)
	}
	if language == "" {
		language = r.Language
	}
	if version == "" {
		version = r.RuntimeVersion
	}
	if !agentCoreVersionMatches(language, version, r.RuntimeVersion) {
		return agentCoreInstallation{}, nil, fmt.Errorf("%w: runtime version override disagrees with AWS managed runtime", ErrUnsupportedRuntime)
	}
	if r.Language != language {
		return agentCoreInstallation{}, nil, fmt.Errorf("%w: runtime language override disagrees with AWS managed runtime", ErrUnsupportedRuntime)
	}
	code, err := i.io.OpenCode(ctx, region, source)
	if err != nil {
		return agentCoreInstallation{}, nil, err
	}
	started := time.Now()
	facts, err := detectAgentCoreZIP(code, language, version, source.EntryPoint, live.EnvironmentVariables)
	if s3code, ok := code.(*agentCoreS3Code); ok {
		fetched, requests := s3code.stats()
		agentCoreLog.Debug("agentcore ZIP read", "runtime", aws.ToString(live.AgentRuntimeName), "zip_bytes", code.Size(),
			"fetched_bytes", fetched, "requests", requests, "duration", time.Since(started).String())
	}
	if err != nil {
		return agentCoreInstallation{}, nil, err
	}
	sdk, waiting, err := chooseAgentCoreSDK(facts, override)
	if err != nil || waiting != nil {
		return agentCoreInstallation{}, waiting, err
	}
	installation := agentCoreInstallation{SDK: sdk, Artifact: source, LoaderEnv: map[string]string{}, Package: agentCoreDetectedPackage(facts, sdk)}
	switch sdk {
	case agentCoreSDKOdigos:
		// AgentCore accepts a second entry point element only after its own opentelemetry-instrument prefix, so
		// the wrapper is the entry point alone and reads the agent's script from the environment. Both are runtime
		// configuration: the snapshot restores them on rollback.
		entry, script, err := agentCoreWrapperEntryPoint(language, source.EntryPoint, live.EnvironmentVariables, facts.StartWrapper)
		if err != nil {
			return agentCoreInstallation{}, nil, err
		}
		installation.Artifact.EntryPoint = entry
		installation.LoaderEnv[agentcore.EntrypointEnv] = script
	case agentCoreSDKADOT:
		// AWS's documented direct-code form for both languages: AgentCore starts ADOT through its
		// opentelemetry-instrument prefix (for Node.js, the ADOT package in node_modules). An agent whose
		// NODE_OPTIONS already requires ADOT starts it as it is.
		if !facts.LauncherInEnv {
			if installation.Artifact.EntryPoint, err = agentCoreLauncherEntryPoint(language, source.EntryPoint, live.EnvironmentVariables); err != nil {
				return agentCoreInstallation{}, nil, err
			}
		}
	}
	return installation, nil, nil
}

// agentCoreDetectedPackage is the preparation contract an installation declares on the runtime, so Describe and
// the inventory keep reading language, SDK and distros from the one variable every installation sets.
func agentCoreDetectedPackage(f agentCoreArtifactFacts, sdk agentCoreSDKKind) agentcore.Package {
	p := agentcore.Package{Version: 1, Language: f.Language, Mode: agentCoreAdopt, SDK: string(sdk)}
	switch {
	case sdk == agentCoreSDKADOT:
		p.Distros = []string{f.Language + "-agentcore-adot"}
	case f.OdigosPackage != nil:
		p.Distros = append([]string(nil), f.OdigosPackage.Distros...)
	case f.PreparedBundle:
		p.Distros, p.ModuleFormat = append([]string(nil), f.BundlePackage.Distros...), f.BundlePackage.ModuleFormat
	}
	if f.Language == "nodejs" && p.ModuleFormat == "" {
		p.ModuleFormat = "commonjs"
		if f.ESM {
			p.ModuleFormat = "esm"
		}
	}
	return p
}

// agentCoreLauncherEntryPoint makes a direct-code entry point start the agent's script through AWS's
// opentelemetry-instrument launcher, the documented ADOT form. AgentCore accepts at most two elements, so an entry
// point that already passes the script an argument cannot also name the launcher; one that starts through the
// launcher or an Odigos start wrapper has it replaced, never stacked.
func agentCoreLauncherEntryPoint(language string, entry []string, env map[string]string) ([]string, error) {
	var script string
	switch {
	case len(entry) == 1 && agentCoreStartsThrough(entry[0]) && env[agentcore.EntrypointEnv] != "":
		// The Odigos start wrapper alone, with the agent's script in the environment.
		script = env[agentcore.EntrypointEnv]
	case len(entry) == 1 && agentCoreStartsThrough(entry[0]):
		// A launcher or start wrapper alone names no script to start.
		return nil, fmt.Errorf("%w: the entry point is %s alone, without the agent's script", ErrUnsupportedInstrumentation, entry[0])
	case len(entry) == 1:
		script = entry[0]
	case len(entry) == 2 && agentCoreStartsThrough(entry[0]):
		script = entry[1]
	default:
		return nil, fmt.Errorf("%w: the entry point must name only the agent's script so it can start through %s (AgentCore accepts two entry point elements)", ErrUnsupportedInstrumentation, agentCoreLauncher)
	}
	if err := agentCoreScript([]string{script}, language); err != nil {
		return nil, err
	}
	return []string{agentCoreLauncher, script}, nil
}

// agentCoreWrapperEntryPoint makes a direct-code entry point the start wrapper alone and returns the agent's script,
// which the wrapper reads from agentcore.EntrypointEnv.
func agentCoreWrapperEntryPoint(language string, entry []string, env map[string]string, wrapper string) ([]string, string, error) {
	switch {
	case len(entry) == 1 && agentCoreStartsThrough(entry[0]) && env[agentcore.EntrypointEnv] == "":
		return nil, "", fmt.Errorf("%w: the entry point is a start wrapper without the agent's script", ErrUnsupportedInstrumentation)
	case len(entry) == 2 && !agentCoreStartsThrough(entry[0]), len(entry) > 2:
		return nil, "", fmt.Errorf("%w: the entry point must name only the agent's script so it can start through %s", ErrUnsupportedInstrumentation, wrapper)
	}
	script := agentCoreEntryScript(entry, env)
	if err := agentCoreScript([]string{script}, language); err != nil {
		return nil, "", err
	}
	return []string{wrapper}, script, nil
}

func (i *agentCoreArtifactInstaller) resolveImage(ctx context.Context, live *ac.GetAgentRuntimeOutput, source agentCoreArtifactRef, language, version, override string) (agentCoreInstallation, *agentCoreWaiting, error) {
	image, err := i.io.Image(ctx, source.Image)
	if err != nil {
		return agentCoreInstallation{}, nil, err
	}
	cfg, err := image.ConfigFile()
	if err != nil {
		return agentCoreInstallation{}, nil, err
	}
	facts, err := detectAgentCoreImage(ctx, image, cfg, language, version, live.EnvironmentVariables)
	if err != nil {
		return agentCoreInstallation{}, nil, err
	}
	sdk, waiting, err := chooseAgentCoreSDK(facts, override)
	if err != nil || waiting != nil {
		return agentCoreInstallation{}, waiting, err
	}
	env := maps.Clone(facts.ImageEnv)
	maps.Copy(env, live.EnvironmentVariables)
	installation := agentCoreInstallation{SDK: sdk, Artifact: source, LoaderEnv: map[string]string{}, Package: agentCoreDetectedPackage(facts, sdk)}
	switch {
	case sdk == agentCoreSDKOdigos && facts.OdigosPackage != nil:
		// Python needs nothing: the wheel's .pth file imports the dormant bootstrap at interpreter start.
		if facts.Language == "nodejs" {
			installation.LoaderEnv["NODE_OPTIONS"] = strings.TrimSpace(env["NODE_OPTIONS"] + " --require " + path.Join(facts.OdigosPackage.Dir, agentCoreNodePackage, "register.cjs"))
		}
	case sdk == agentCoreSDKOdigos:
		installation.LoaderEnv = agentCoreCarrierLoader(facts.Language, facts.WorkingDir, env)
	case sdk == agentCoreSDKADOT:
		// Python starts through the image's own launcher (detection waits otherwise).
		if facts.Language == "nodejs" && !facts.LauncherPresent {
			// The bare specifier goes through the package's exports map, which is how AWS documents starting
			// ADOT; it resolves from the working directory, so it is used only when the install is there. An
			// absolute path bypasses exports and must name the file the map points at.
			register := agentCoreNodeADOTRegister
			if facts.ADOT.Dir != facts.WorkingDir {
				register = path.Join(facts.ADOT.Dir, agentCoreNodeADOT, agentCoreNodeADOTRegisterFile)
			}
			installation.LoaderEnv["NODE_OPTIONS"] = strings.TrimSpace(env["NODE_OPTIONS"] + " --require " + register)
		}
	}
	return installation, nil, nil
}

// agentCoreCarrierLoader starts the carrier bundle's dormant bootstrap from its image layout.
func agentCoreCarrierLoader(language, wd string, env map[string]string) map[string]string {
	loader := map[string]string{}
	if language == "python" {
		loader["PYTHONPATH"] = "/opt/odigos/agentcore/bootstrap/python:" + wd
		if env["PYTHONPATH"] != "" {
			loader["PYTHONPATH"] += ":" + env["PYTHONPATH"]
		}
	} else {
		loader["NODE_OPTIONS"] = strings.TrimSpace(env["NODE_OPTIONS"] + " --require /opt/odigos/agentcore/bootstrap/node/register.cjs")
	}
	return loader
}

func agentCoreVersionMatches(language, wanted, detected string) bool {
	if language == "nodejs" {
		return strings.Split(strings.TrimPrefix(wanted, "v"), ".")[0] == strings.Split(strings.TrimPrefix(detected, "v"), ".")[0]
	}
	return majorMinor(wanted) == majorMinor(detected)
}

// agentCoreManualStartsSDK reports whether a self-managed runtime's configuration starts the SDK its contract
// declares. Only a direct-code ZIP shows how it starts: AgentCore accepts a second entry point element only after
// opentelemetry-instrument, so the Odigos package starts through its start wrapper alone with the script in
// agentcore.EntrypointEnv, and ADOT through the launcher prefix (for Node.js, or ADOT's register hook in
// NODE_OPTIONS). An image starts through its own command, which the runtime does not show.
func agentCoreManualStartsSDK(live *ac.GetAgentRuntimeOutput, sdk agentCoreSDKKind) bool {
	artifact, err := agentCoreArtifact(live.AgentRuntimeArtifact)
	if err != nil || artifact.Image != "" {
		return true
	}
	entry, env := artifact.EntryPoint, live.EnvironmentVariables
	if sdk == agentCoreSDKOdigos {
		return len(entry) == 1 && agentCoreIsStartWrapper(entry[0]) && env[agentcore.EntrypointEnv] != ""
	}
	launcher := len(entry) == 2 && entry[0] == agentCoreLauncher
	if strings.HasPrefix(string(artifact.Runtime), "NODE") {
		return launcher || strings.Contains(env["NODE_OPTIONS"], agentCoreNodeADOTRegister)
	}
	return launcher
}

// agentCoreIsStartWrapper reports an Odigos start wrapper: the package's, one copied to the ZIP root, or one used in
// place inside a prepared bundle.
func agentCoreIsStartWrapper(name string) bool {
	switch path.Base(name) {
	case "odigos_agentcore_start.py", "odigos_agentcore_start.js":
		return true
	}
	return false
}
