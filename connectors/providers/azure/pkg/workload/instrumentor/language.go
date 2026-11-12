package instrumentor

import (
	"path"
	"regexp"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"
)

const (
	LangJava   = "java"
	LangPython = "python"
	LangNodeJS = "nodejs"
	LangDotNet = "dotnet"
)

const agentsDir = "/var/odigos"
const imageAgentsDir = "/instrumentations"

type appendVar struct {
	name  string
	value string
	sep   string
}

type languageSpec struct {
	agentDirs []string
	staticEnv map[string]string
	appendEnv []appendVar
}

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
		agentDirs: []string{"nodejs"},
		appendEnv: []appendVar{
			{name: "NODE_OPTIONS", value: "--require " + agentsDir + "/nodejs/node_modules/@opentelemetry/auto-instrumentations-node/build/src/register.js", sep: " "},
		},
	},
	LangDotNet: {
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

func SupportedLanguage(lang string) bool {
	_, ok := languageSpecs[lang]
	return ok
}

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

func detectLanguage(override, stored string, app *armappcontainers.ContainerApp) string {
	if l := normalizeLanguage(override); l != "" {
		return l
	}
	if l := normalizeLanguage(stored); l != "" {
		return l
	}
	return guessLanguageFromApp(app)
}

var pythonExeRe = regexp.MustCompile(`^python(\d+(\.\d+)?)?$`)

type langSignals struct {
	image string
	argv  []string
	args  []string
	env   map[string]string
}

func guessLanguageFromApp(app *armappcontainers.ContainerApp) string {
	c := primaryContainer(app)
	if c == nil {
		return ""
	}
	s := langSignals{
		image: deref(c.Image),
		argv:  derefStrings(c.Command),
		args:  derefStrings(c.Args),
		env:   containerEnvMap(c),
	}
	if l := guessFromEnv(s.env); l != "" {
		return l
	}
	if l := guessFromCommand(s.argv); l != "" {
		return l
	}
	return guessFromImageString(s)
}

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

func hasEnv(env map[string]string, key string) bool {
	v, ok := env[key]
	return ok && v != ""
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefStrings(ps []*string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p != nil {
			out = append(out, *p)
		}
	}
	return out
}
