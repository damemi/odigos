package instrumentor

import (
	"fmt"
	"strings"
	"sync"

	"github.com/odigos-io/odigos/distros"
	"github.com/odigos-io/odigos/distros/distro"
)

// Languages the Fargate sidecar can instrument — the complete env-driven set: every language with a
// zero-code OpenTelemetry agent that activates purely via environment variables over files staged into a
// shared volume. Go (and every eBPF distro) is impossible on Fargate by platform design: the launch type
// forbids privileged containers/host PID, so there is nowhere to run an eBPF agent.
const (
	LangJava   = "java"
	LangPython = "python"
	LangNodeJS = "nodejs"
	LangDotNet = "dotnet"
	LangPHP    = "php"
	LangRuby   = "ruby"
)

// agentsDir is the shared task-volume mount path the language agents are staged into. It matches the
// Odigos convention (k8sconsts.OdigosAgentsDirectory = /var/odigos, also used by the vm-agent) so the
// distro env recipes apply unchanged.
const agentsDir = "/var/odigos"

// imageAgentsDir is where the odigos-fargate-agents image holds the agent files. The sidecar copies
// <imageAgentsDir>/<dir> into <agentsDir>/<dir>.
const imageAgentsDir = "/instrumentations"

// fargateLibcType is the LIBC_TYPE distro parameter for Fargate: task images are overwhelmingly
// glibc-based, and the agents image bundles only the glibc .NET native profiler. musl (Alpine) .NET
// detection is a documented follow-up.
const fargateLibcType = "glibc"

// appendEnvVar is a loader-style env var whose value is built from the distro's ReplacePattern — the
// customer's original value plus our agent activation string (e.g. JAVA_TOOL_OPTIONS, NODE_OPTIONS,
// PYTHONPATH). Kept separate from static vars so a re-instrument rebuilds from the customer's original
// value (recorded in the ownership manifest) rather than accumulating our additions.
type appendEnvVar struct {
	name string
	// pattern is the distro ReplacePattern, carrying the {{ORIGINAL_ENV_VALUE}} and {{ODIGOS_AGENTS_DIR}}
	// placeholders; evaluate() resolves it for a given original value.
	pattern string
}

// evaluate resolves the append pattern for the given original (pre-Odigos) value.
func (a appendEnvVar) evaluate(original string) string {
	return distro.EvaluateReplacePattern(a.pattern, original, agentsDir)
}

// separator is the delimiter the pattern joins the customer's original value with ours: the character
// immediately after the {{ORIGINAL_ENV_VALUE}} placeholder.
//
// It is DERIVED from the pattern rather than declared, because the pattern is the authority — it comes from
// the upstream distro, differs by language (a space for JAVA_TOOL_OPTIONS/NODE_OPTIONS, a colon for
// PYTHONPATH), and can change on somebody else's release. Assuming one would be silently destructive in a way
// nothing here would catch: a space-joined PYTHONPATH is one unusable path rather than two, and the process
// still starts.
//
// Its consumer is the published instrumentation plan, which has to tell a customer HOW to join their value to
// ours rather than handing them a finished literal. Returns "" when the pattern does not append at all.
func (a appendEnvVar) separator() string {
	i := strings.Index(a.pattern, distro.OriginalEnvValuePlaceholder)
	if i < 0 {
		return ""
	}
	rest := a.pattern[i+len(distro.OriginalEnvValuePlaceholder):]
	if rest == "" {
		return ""
	}
	return rest[:1]
}

// agentRecipe describes how to inject a language's OpenTelemetry agent into a Fargate task container:
// which agent directories the sidecar stages from the agents image, the static activation env vars
// (params already rendered), the loader (append-style) activation env vars, and which OTLP protocol the
// agent exports with.
type agentRecipe struct {
	lang      string
	agentDirs []string
	staticEnv map[string]string
	appendEnv []appendEnvVar
	// otlpProtocol is "grpc" or "http/protobuf" — the protocol this language's agent is configured to
	// export OTLP with. It is DERIVED FROM THE DISTRO, not assumed: an explicit
	// OTEL_EXPORTER_OTLP_PROTOCOL static var wins, else the distro's OtlpHttpLocalNode declaration ("this
	// distribution uses the OTLP HTTP protocol"), else gRPC. Every OSS community distro sets
	// OtlpHttpLocalNode, so in practice they all prefer http/protobuf — pointing them at a gRPC endpoint
	// yields an HTTP request against an HTTP/2 gRPC listener, which fails as a connection reset.
	otlpProtocol string
	// protocolFixed marks a distro that pins the protocol in its own env and therefore cannot be switched
	// to the other transport (php/ruby: their community SDKs are http/protobuf-only). When false the agent
	// honors OTEL_EXPORTER_OTLP_PROTOCOL, so the connector may fall back to whichever transport the
	// destination actually offers.
	protocolFixed bool
}

// errNeedsRuntimeVersion marks a language whose agent paths are runtime-version-templated (php/ruby) but
// no major.minor version could be resolved. Wrapped into an unsupported-runtime error with a "set a
// runtime override" hint by the caller.
var errNeedsRuntimeVersion = fmt.Errorf("runtime major.minor version required")

// recipes lazily loads the language injection recipes from the OSS Odigos distros module — the canonical
// per-language env recipes the Kubernetes init-container mount method uses — so the Fargate connector
// never hand-copies (and silently drifts from) the agent activation env. Node.js is the one deliberate
// divergence: the Odigos nodejs-community distro is OpAMP-driven (skips startup without
// ODIGOS_OPAMP_SERVER_HOST, which serverless Fargate cannot provide), so the upstream env-driven
// @opentelemetry/auto-instrumentations-node is used instead, exactly as the GCP Cloud Run connector does.
type recipes struct {
	once   sync.Once
	getter *distros.Getter
	err    error
}

// communityDistroNames maps a canonical language to the OSS community distro carrying its env recipe.
// nodejs is absent — see the recipes doc comment.
var communityDistroNames = map[string]string{
	LangJava:   "java-community",
	LangPython: "python-community",
	LangDotNet: "dotnet-community",
	LangPHP:    "php-community",
	LangRuby:   "ruby-community",
}

func (r *recipes) load() (*distros.Getter, error) {
	r.once.Do(func() {
		r.getter, r.err = distros.NewCommunityGetter()
	})
	return r.getter, r.err
}

// recipeFor resolves the injection recipe for a (normalized) language. runtimeVersion is the workload's
// runtime major.minor, required by version-templated agents (php/ruby) and ignored by the rest. An
// unsupported language yields an error the caller classifies as unsupported-runtime.
func (r *recipes) recipeFor(lang, runtimeVersion string) (agentRecipe, error) {
	if lang == LangNodeJS {
		return nodeJSRecipe(), nil
	}
	name, ok := communityDistroNames[lang]
	if !ok {
		return agentRecipe{}, fmt.Errorf("no Fargate agent recipe for language %q", lang)
	}
	getter, err := r.load()
	if err != nil {
		return agentRecipe{}, fmt.Errorf("load odigos distros: %w", err)
	}
	d := getter.GetDistroByName(name)
	if d == nil {
		return agentRecipe{}, fmt.Errorf("odigos distro %q not found", name)
	}

	params, err := distroParams(d, runtimeVersion)
	if err != nil {
		return agentRecipe{}, err
	}
	staticEnv, err := renderStaticEnv(d, params)
	if err != nil {
		return agentRecipe{}, fmt.Errorf("render %s env: %w", name, err)
	}
	// The bundled .NET auto-instrumentation 1.17 is self-contained under net/ and no
	// longer ships AdditionalDeps/ or store/. The OSS recipe still names those
	// legacy paths; keep the Fargate recipe aligned with the bundled 1.17 agent.
	if lang == LangDotNet {
		delete(staticEnv, "DOTNET_ADDITIONAL_DEPS")
		delete(staticEnv, "DOTNET_SHARED_STORE")
	}
	rec := agentRecipe{
		lang:         lang,
		staticEnv:    staticEnv,
		otlpProtocol: otlpProtocolGRPC,
	}
	// "This distribution uses the OTLP HTTP protocol to emit telemetry" — the distro's own declaration of
	// how its agent exports. In OSS Kubernetes this is what points the agent at the node collector's 4318
	// HTTP port rather than 4317.
	if d.EnvironmentVariables.OtlpHttpLocalNode {
		rec.otlpProtocol = otlpProtocolHTTP
	}
	// A distro that sets the protocol in its own static env pins it (php/ruby); lift it out — the injector
	// owns the OTLP export vars and sets the protocol alongside the endpoint.
	if p, ok := staticEnv[EnvOtlpProtocol]; ok {
		rec.otlpProtocol = p
		rec.protocolFixed = true
		delete(staticEnv, EnvOtlpProtocol)
	}
	for _, a := range d.EnvironmentVariables.AppendOdigosVariables {
		rec.appendEnv = append(rec.appendEnv, appendEnvVar{name: a.EnvName, pattern: a.ReplacePattern})
	}
	if d.RuntimeAgent != nil {
		for _, dir := range d.RuntimeAgent.DirectoryNames {
			rec.agentDirs = append(rec.agentDirs, strings.TrimPrefix(dir, distro.AgentPlaceholderDirectory+"/"))
		}
	}
	return rec, nil
}

// distroParams builds the distro parameter values this platform supplies: LIBC_TYPE is glibc on Fargate,
// and RUNTIME_VERSION_MAJOR_MINOR comes from the workload's resolved runtime version (php/ruby agent
// paths are per-version).
func distroParams(d *distro.OtelDistro, runtimeVersion string) (map[string]string, error) {
	params := map[string]string{}
	for _, p := range d.RequireParameters {
		switch p {
		case "LIBC_TYPE":
			params[p] = fargateLibcType
		case distro.RuntimeVersionMajorMinorDistroParameterName:
			if runtimeVersion == "" {
				return nil, fmt.Errorf("%w for %s", errNeedsRuntimeVersion, d.Name)
			}
			params[p] = runtimeVersion
		default:
			return nil, fmt.Errorf("distro %s requires unsupported parameter %q", d.Name, p)
		}
	}
	return params, nil
}

// renderStaticEnv renders the distro's static env vars, executing the pre-parsed value templates (e.g.
// php's PHP_INI_SCAN_DIR embeds {{.RUNTIME_VERSION_MAJOR_MINOR}}) with the platform parameters.
func renderStaticEnv(d *distro.OtelDistro, params map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(d.EnvironmentVariables.StaticVariables))
	for _, sv := range d.EnvironmentVariables.StaticVariables {
		value := sv.EnvValue
		if sv.Template != nil {
			var b strings.Builder
			if err := sv.Template.Execute(&b, params); err != nil {
				return nil, fmt.Errorf("render %s: %w", sv.EnvName, err)
			}
			value = b.String()
		}
		out[sv.EnvName] = value
	}
	return out, nil
}

// nodeJSRecipe is the local upstream Node.js recipe (see the recipes doc comment for why it diverges from
// the nodejs-community distro). The register.js path matches the odigos-fargate-agents image layout.
func nodeJSRecipe() agentRecipe {
	return agentRecipe{
		lang:      LangNodeJS,
		agentDirs: []string{"nodejs"},
		appendEnv: []appendEnvVar{{
			name:    EnvNodeOptions,
			pattern: "{{ORIGINAL_ENV_VALUE}} --require {{ODIGOS_AGENTS_DIR}}/nodejs/node_modules/@opentelemetry/auto-instrumentations-node/build/src/register.js",
		}},
		// http/protobuf on both counts: it is the OpenTelemetry JS SDK's default OTLP protocol, and the
		// nodejs-community distro this replaces declares OtlpHttpLocalNode. Not fixed — the SDK honors
		// OTEL_EXPORTER_OTLP_PROTOCOL, so a gRPC-only destination still works.
		otlpProtocol: otlpProtocolHTTP,
	}
}

// CoverableLanguage reports whether the connector can instrument the given (normalized) language on
// Fargate.
func CoverableLanguage(lang string) bool {
	if lang == LangNodeJS {
		return true
	}
	_, ok := communityDistroNames[lang]
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
	case "php":
		return LangPHP
	case "ruby", "rb":
		return LangRuby
	default:
		return ""
	}
}

// uncoverableLanguageReason explains why a workload cannot be instrumented on Fargate. It deliberately
// separates two very different situations that used to read alike: a language we DETECTED but cannot
// cover (a hard platform limit — no override will help), versus one we could not detect at all (an
// actionable gap the operator closes with a runtime override).
func uncoverableLanguageReason(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "go", "golang":
		return "detected Go, which can only be auto-instrumented via eBPF — impossible on Fargate, where privileged containers and host PID are unavailable. Instrument the app with the OpenTelemetry Go SDK instead; a runtime override cannot enable this"
	case "":
		return "could not determine the workload language from the task definition or its image config — set a runtime override on the source (e.g. language: java, or language: php with version: \"8.3\"). Note that a Go or other eBPF-only runtime cannot be instrumented on Fargate at all, so an override will not help there"
	default:
		return fmt.Sprintf("detected language %q, which has no env-driven Odigos agent; only java, python, nodejs, dotnet, php and ruby can be instrumented on Fargate", raw)
	}
}

// otlpProtocolGRPC is the OTLP gRPC protocol value for OTEL_EXPORTER_OTLP_PROTOCOL (the http/protobuf
// counterpart is otlpProtocolHTTP in layer.go).
const otlpProtocolGRPC = "grpc"
