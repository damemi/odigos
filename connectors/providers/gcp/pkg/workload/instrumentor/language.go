package instrumentor

import (
	"path"
	"regexp"
	"strings"

	runpb "cloud.google.com/go/run/apiv2/runpb"
)

// Supported languages for Cloud Run non-eBPF instrumentation. These mirror the OSS Odigos community
// distros; Cloud Run does not support eBPF, so — like Odigos on Kubernetes — the language agent is
// activated purely via env vars over files staged into a shared volume.
const (
	LangJava   = "java"
	LangPython = "python"
	LangNodeJS = "nodejs"
	LangDotNet = "dotnet"
)

// agentsDir is the shared in-memory volume mount path the language agents are staged into. It matches the
// Odigos-on-Kubernetes convention (k8sconsts.OdigosAgentsDirectory = /var/odigos) so the same agent
// layout and env-var values apply unchanged.
const agentsDir = "/var/odigos"

// imageAgentsDir is where the odigos-agents image holds the agent files (its WORKDIR). The sidecar copies
// <imageAgentsDir>/<dir> into <agentsDir>/<dir>.
const imageAgentsDir = "/instrumentations"

// appendVar is a loader-style env var whose value is our agent activation string appended to the
// customer's original value with a separator (e.g. JAVA_TOOL_OPTIONS, NODE_OPTIONS, PYTHONPATH). Kept
// separate from static vars so a re-instrument rebuilds from the customer's original value (recorded in
// the ownership manifest) rather than accumulating our additions.
type appendVar struct {
	name  string
	value string
	sep   string
}

// languageSpec describes how to inject a language's OpenTelemetry agent into a Cloud Run container: which
// agent directories the sidecar stages from the image, the static activation env vars, and the loader
// (append-style) activation env vars.
type languageSpec struct {
	agentDirs []string
	staticEnv map[string]string
	appendEnv []appendVar
}

// languageSpecs is the per-language injection recipe. Java/Python/Node.js activate via a loader env var
// (append-style); .NET activates via static CoreCLR/OTEL env vars. The paths mirror the OSS Odigos
// community distro YAMLs resolved against /var/odigos.
var languageSpecs = map[string]languageSpec{
	LangJava: {
		agentDirs: []string{"java"},
		appendEnv: []appendVar{
			{name: "JAVA_TOOL_OPTIONS", value: "-javaagent:" + agentsDir + "/java/javaagent.jar", sep: " "},
		},
	},
	LangPython: {
		agentDirs: []string{"python"},
		staticEnv: map[string]string{
			"OTEL_PYTHON_CONFIGURATOR": "odigos-python-configurator",
		},
		appendEnv: []appendVar{
			{name: "PYTHONPATH", value: agentsDir + "/python:" + agentsDir + "/python/opentelemetry/instrumentation/auto_instrumentation", sep: ":"},
		},
	},
	LangNodeJS: {
		// Upstream env-driven agent — the Odigos nodejs-community distro is OpAMP-driven (no OpAMP on Cloud Run).
		agentDirs: []string{"nodejs"},
		appendEnv: []appendVar{
			{name: "NODE_OPTIONS", value: "--require " + agentsDir + "/nodejs/node_modules/@opentelemetry/auto-instrumentations-node/build/src/register.js", sep: " "},
		},
	},
	LangDotNet: {
		// .NET is glibc-only for Cloud Run (the standard managed runtime); the musl variant is not selected.
		agentDirs: []string{"dotnet"},
		staticEnv: map[string]string{
			"CORECLR_ENABLE_PROFILING": "1",
			"CORECLR_PROFILER":         "{918728DD-259F-4A6A-AC2B-B85E1B658318}",
			"CORECLR_PROFILER_PATH":    agentsDir + "/dotnet/linux-glibc/OpenTelemetry.AutoInstrumentation.Native.so",
			"OTEL_DOTNET_AUTO_HOME":    agentsDir + "/dotnet",
			"DOTNET_STARTUP_HOOKS":     agentsDir + "/dotnet/net/OpenTelemetry.AutoInstrumentation.StartupHook.dll",
			"DOTNET_ADDITIONAL_DEPS":   agentsDir + "/dotnet/AdditionalDeps",
			"DOTNET_SHARED_STORE":      agentsDir + "/dotnet/store",
		},
	},
}

// SupportedLanguage reports whether the connector can instrument the given (normalized) language.
func SupportedLanguage(lang string) bool {
	_, ok := languageSpecs[lang]
	return ok
}

// normalizeLanguage maps common language aliases to the canonical connector language key, or "" when the
// value is empty/unknown. This lets a source runtime override use natural names (javascript, node, py,
// c#, .net) that resolve to the connector's language set.
func normalizeLanguage(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "java", "jvm", "kotlin", "scala":
		return LangJava
	case "python", "py":
		return LangPython
	case "nodejs", "node", "node.js", "javascript", "js", "typescript", "ts":
		return LangNodeJS
	case "dotnet", ".net", "net", "csharp", "c#":
		return LangDotNet
	default:
		return ""
	}
}

// pythonExeRe matches a Python interpreter basename (python, python3, python3.12). Ported from the OSS
// Odigos python inspector QuickScan (procdiscovery/pkg/inspectors/python), applied here to the container
// entry command instead of a live /proc/<pid>/exe.
var pythonExeRe = regexp.MustCompile(`^python(\d+(\.\d+)?)?$`)

// langSignals is a source-agnostic bundle of the static hints language detection scans, so the same
// QuickScan logic applies to either the Cloud Run service spec or the container image config. argv is the
// executable command line (argv[0] is the executable); args are trailing arguments included only in the
// loose image-string scan; image is the image reference (empty when scanning the image config, whose ref
// the caller scans via the service spec instead).
type langSignals struct {
	image string
	argv  []string
	args  []string
	env   map[string]string
}

// guessStrongFromSignals runs only the high-confidence signals (declared version/activation env vars and
// the executable basename QuickScan). It deliberately omits the loose image-string scan so a weak
// substring match can't pre-empt a stronger signal from another source.
func guessStrongFromSignals(s langSignals) string {
	if l := guessFromEnv(s.env); l != "" {
		return l
	}
	return guessFromCommand(s.argv)
}

// guessLanguageFromService heuristically infers the language from the ingress container's service spec.
// Cloud Run images rarely encode the runtime in the spec (command/env are usually empty unless the
// customer overrode them), so this is a best-effort fallback only (the reliable path is the source
// runtime override, then the image config). Signals are tried strongest-first and a wrong guess is
// avoided by returning "" when nothing recognizable matches:
//  1. declared version/activation env vars (official base images set JAVA_VERSION/JAVA_HOME, PYTHON_VERSION,
//     NODE_VERSION; .NET sets the CoreCLR profiler vars) — the OSS runtime detector keys off the same env
//     keys (procdiscovery process.LangsVersionEnvs), which Cloud Run exposes statically,
//  2. the entry command's executable basename (QuickScan-style exact-name match, ported from the OSS
//     per-language inspectors' QuickScan),
//  3. a loose substring match over image + command + args (weakest; catches launchers like gunicorn).
func guessLanguageFromService(svc *runpb.Service) string {
	c := ingressContainer(svc)
	if c == nil {
		return ""
	}
	s := langSignals{
		image: c.GetImage(),
		argv:  c.GetCommand(),
		args:  c.GetArgs(),
		env:   containerEnvMap(c),
	}
	if l := guessStrongFromSignals(s); l != "" {
		return l
	}
	return guessFromImageString(s)
}

// signalsFromImageConfig builds language signals from an image config, mirroring how OSS procdiscovery
// reads a live process: Entrypoint+Cmd is the argv (Entrypoint[0], or Cmd[0] when there is no entrypoint,
// is the executable), and Env is the process environment. The image reference is left empty — the loose
// image-string scan runs against it via the service spec path.
func signalsFromImageConfig(cfg *ImageConfig) langSignals {
	argv, args := cfg.Entrypoint, cfg.Cmd
	if len(argv) == 0 {
		argv, args = cfg.Cmd, nil
	}
	return langSignals{argv: argv, args: args, env: envSliceToMap(cfg.Env)}
}

// envSliceToMap parses OCI-config KEY=VALUE env entries into a map.
func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			out[k] = v
		}
	}
	return out
}

// guessFromEnv infers the language from distinctive runtime env vars declared on the container. These are
// the version keys the OSS detector reads from /proc/<pid>/environ (process.LangsVersionEnvs) plus the
// .NET CoreCLR profiler vars — all set by the official language base images, so they are a strong static
// signal on Cloud Run. Only supported languages are reported.
func guessFromEnv(env map[string]string) string {
	switch {
	case hasEnv(env, "JAVA_VERSION") || hasEnv(env, "JAVA_HOME"):
		return LangJava
	case hasEnv(env, "PYTHON_VERSION"):
		return LangPython
	case hasEnv(env, "NODE_VERSION"):
		return LangNodeJS
	case hasEnv(env, "CORECLR_ENABLE_PROFILING") || hasEnv(env, "DOTNET_STARTUP_HOOKS") || hasEnv(env, "OTEL_DOTNET_AUTO_HOME"):
		return LangDotNet
	}
	return ""
}

// guessFromCommand infers the language from the entry command's executable basename, mirroring the OSS
// inspectors' QuickScan (which matches filepath.Base(/proc/<pid>/exe) against a per-language name set).
// The container entry command's first element is the executable.
func guessFromCommand(cmd []string) string {
	if len(cmd) == 0 {
		return ""
	}
	base := strings.ToLower(path.Base(cmd[0]))
	switch {
	case base == "java":
		return LangJava
	case pythonExeRe.MatchString(base):
		return LangPython
	case base == "node" || base == "npm" || base == "npx" || base == "yarn":
		return LangNodeJS
	case base == "dotnet":
		return LangDotNet
	}
	return ""
}

// guessFromImageString is the weakest fallback: a substring scan over the image reference plus the entry
// command/args. It catches launchers that don't name the runtime as the executable (gunicorn/uvicorn) and
// runtime hints baked into the image tag (openjdk, aspnet).
func guessFromImageString(s langSignals) string {
	parts := append([]string{s.image}, s.argv...)
	parts = append(parts, s.args...)
	hay := strings.ToLower(strings.Join(parts, " "))
	switch {
	case strings.Contains(hay, "java") || strings.Contains(hay, "openjdk") || strings.Contains(hay, "-jar "):
		return LangJava
	case strings.Contains(hay, "python") || strings.Contains(hay, "uvicorn") || strings.Contains(hay, "gunicorn"):
		return LangPython
	case strings.Contains(hay, "node") || strings.Contains(hay, "npm") || strings.Contains(hay, "yarn"):
		return LangNodeJS
	case strings.Contains(hay, "dotnet") || strings.Contains(hay, "aspnet"):
		return LangDotNet
	default:
		return ""
	}
}

// hasEnv reports whether a non-empty env var is declared.
func hasEnv(env map[string]string, key string) bool {
	v, ok := env[key]
	return ok && v != ""
}
