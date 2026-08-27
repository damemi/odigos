package instrumentor

import (
	"context"
	"encoding/json"
	"path"
	"regexp"
	"strings"

	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// Language + runtime-version detection for Fargate task containers. The resolution mirrors the GCP Cloud
// Run connector (and OSS procdiscovery's static signals): source runtime override → previously stored
// detection → the container image config (CNB buildpack labels, then the image's own entrypoint/cmd/env)
// → the task definition's container signals → a loose image-string scan. The parsers here are local
// copies of the small pure helpers used by the other connectors (providers stay self-contained — no
// cross-provider imports).

// detectionConfidence ranks how trustworthy a language detection is. It matters when a task definition
// has several essential containers and the connector must pick which one is the application: a strong
// signal (a declared override, buildpack metadata, a runtime env var, an interpreter executable) beats a
// loose substring match. Without this, a proxy sidecar whose config text merely MENTIONS a runtime — an
// nginx container whose inline fastcgi config names index.php — looks as much like the app as the PHP-FPM
// container behind it.
type detectionConfidence int

const (
	confidenceNone detectionConfidence = iota
	// confidenceWeak: a substring scan over the image reference and entry command.
	confidenceWeak
	// confidenceStrong: an explicit override, a previously stored detection, CNB buildpack metadata, a
	// declared runtime version env var, or an interpreter executable basename.
	confidenceStrong
)

// resolvedRuntime is the outcome of language/version resolution for a Fargate container. Version is the
// runtime major.minor ("" when unknown) — load-bearing for php/ruby, whose agent paths are per-version;
// informational for the rest.
type resolvedRuntime struct {
	language   string
	version    string
	confidence detectionConfidence
}

// resolveECSRuntime resolves the workload language and runtime major.minor version, strongest signal
// first. images may be nil (image-config detection disabled — the other layers still apply). Returns
// empty language when undetermined; the caller then reports the workload as unsupported until an
// override is set.
func resolveECSRuntime(ctx context.Context, images ImageInspector, overrideLang, overrideVersion, storedLang, storedVersion string, app *ecstypes.ContainerDefinition) resolvedRuntime {
	env := ecsContainerEnv(app)
	signals := langSignals{image: strDeref(app.Image), argv: app.EntryPoint, args: app.Command, env: env}

	// The image config is fetched once and feeds both language and version detection. Best-effort: any
	// failure just removes this layer.
	var cfg *ImageConfig
	if images != nil && signals.image != "" {
		c, err := images.ImageConfig(ctx, signals.image)
		if err != nil {
			log.Debug("read image config for language detection failed", "image", signals.image, "error", err)
		} else {
			cfg = c
		}
	}

	lang, confidence := normalizeLanguage(overrideLang), confidenceStrong
	if lang == "" {
		lang = normalizeLanguage(storedLang)
	}
	// A definitive detection of a language Odigos CANNOT cover on Fargate (e.g. Go, which is eBPF-only)
	// is remembered so the unsupported report can name the real constraint instead of "undetermined".
	uncoverable := ""
	if lang == "" && cfg != nil {
		// CNB buildpack labels are authoritative for buildpack-built images; then the image's own process
		// signals (entrypoint/cmd/env).
		if raw := languageFromBuildpackLabels(cfg.Labels); raw != "" {
			if l := normalizeLanguage(raw); l != "" {
				lang = l
			} else {
				uncoverable = raw
			}
		}
		if lang == "" {
			lang = normalizeLanguage(guessStrongFromSignals(signalsFromImageConfig(cfg)))
		}
	}
	if lang == "" {
		lang = guessStrongFromSignals(signals)
	}
	if lang == "" {
		// Weakest layer: a substring scan over the image reference and entry command.
		if lang = guessFromImageString(signals); lang != "" {
			confidence = confidenceWeak
		}
	}
	if lang == "" {
		lang, confidence = uncoverable, confidenceStrong
	}
	if lang == "" {
		confidence = confidenceNone
	}

	version := majorMinor(overrideVersion)
	if version == "" {
		version = majorMinor(storedVersion)
	}
	if version == "" && cfg != nil {
		version = runtimeVersionFromEnv(lang, envSliceToMap(cfg.Env))
	}
	if version == "" {
		version = runtimeVersionFromEnv(lang, env)
	}
	if version == "" {
		version = runtimeVersionFromImageTag(signals.image)
	}
	return resolvedRuntime{language: lang, version: version, confidence: confidence}
}

func ecsContainerEnv(c *ecstypes.ContainerDefinition) map[string]string {
	if c == nil {
		return nil
	}
	return containerEnvMap(c)
}

func strDeref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// --- language signals (static analog of OSS procdiscovery QuickScan) ---

// pythonExeRe matches a Python interpreter basename (python, python3, python3.12).
var pythonExeRe = regexp.MustCompile(`^python(\d+(\.\d+)?)?$`)

// langSignals is a source-agnostic bundle of the static hints language detection scans, so the same
// QuickScan logic applies to either the task definition's container or the container image config. argv
// is the executable command line (argv[0] is the executable); args are trailing arguments included only
// in the loose image-string scan.
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

// signalsFromImageConfig builds language signals from an image config: Entrypoint+Cmd is the argv
// (Entrypoint[0], or Cmd[0] when there is no entrypoint, is the executable), and Env is the process
// environment. The image reference is left empty — the loose image-string scan runs against the task
// definition's image reference instead.
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

// guessFromEnv infers the language from distinctive runtime env vars declared on the container — the
// version keys the official language base images set (JAVA_VERSION, PYTHON_VERSION, NODE_VERSION,
// PHP_VERSION, RUBY_VERSION) plus the .NET CoreCLR profiler vars. Only coverable languages are reported.
func guessFromEnv(env map[string]string) string {
	switch {
	case hasEnv(env, "JAVA_VERSION") || hasEnv(env, "JAVA_HOME"):
		return LangJava
	case hasEnv(env, "PYTHON_VERSION"):
		return LangPython
	case hasEnv(env, "NODE_VERSION"):
		return LangNodeJS
	case hasEnv(env, "CORECLR_ENABLE_PROFILING") || hasEnv(env, "DOTNET_STARTUP_HOOKS") || hasEnv(env, "OTEL_DOTNET_AUTO_HOME") || hasEnv(env, "DOTNET_VERSION") || hasEnv(env, "ASPNET_VERSION"):
		return LangDotNet
	case hasEnv(env, "PHP_VERSION") || hasEnv(env, "PHP_INI_DIR"):
		return LangPHP
	case hasEnv(env, "RUBY_VERSION"):
		return LangRuby
	}
	return ""
}

// guessFromCommand infers the language from the entry command's executable basename, mirroring the OSS
// inspectors' QuickScan. The container entry command's first element is the executable.
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
	case base == "php" || base == "php-fpm" || strings.HasPrefix(base, "php-fpm"):
		return LangPHP
	case base == "ruby" || base == "bundle" || base == "rails" || base == "puma" || base == "unicorn":
		return LangRuby
	}
	return ""
}

// guessFromImageString is the weakest fallback: a substring scan over the image reference plus the entry
// command/args. It catches launchers that don't name the runtime as the executable (gunicorn/uvicorn)
// and runtime hints baked into the image tag (openjdk, aspnet).
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
	case strings.Contains(hay, "php"):
		return LangPHP
	case strings.Contains(hay, "ruby") || strings.Contains(hay, "rails"):
		return LangRuby
	default:
		return ""
	}
}

// hasEnv reports whether a non-empty env var is declared.
func hasEnv(env map[string]string, key string) bool {
	v, ok := env[key]
	return ok && v != ""
}

// --- runtime version detection (major.minor, for the version-templated php/ruby agent paths) ---

// runtimeVersionEnvKeys maps a language to the env var its official base images declare the runtime
// version in.
var runtimeVersionEnvKeys = map[string]string{
	LangPHP:    "PHP_VERSION",
	LangRuby:   "RUBY_VERSION",
	LangPython: "PYTHON_VERSION",
	LangJava:   "JAVA_VERSION",
	LangNodeJS: "NODE_VERSION",
	LangDotNet: "DOTNET_VERSION",
}

func runtimeVersionFromEnv(lang string, env map[string]string) string {
	key, ok := runtimeVersionEnvKeys[lang]
	if !ok {
		return ""
	}
	return majorMinor(env[key])
}

// imageTagVersionRe extracts a leading major.minor from an image tag (php:8.3-fpm → 8.3, ruby:3.3.5 → 3.3).
var imageTagVersionRe = regexp.MustCompile(`^v?(\d+\.\d+)`)

// runtimeVersionFromImageTag pulls a major.minor version out of the image reference's tag, the common
// convention for official runtime images (php:8.3-fpm, ruby:3.3-alpine).
func runtimeVersionFromImageTag(image string) string {
	tag := image
	if i := strings.LastIndex(tag, ":"); i >= 0 {
		tag = tag[i+1:]
	} else {
		return ""
	}
	if strings.Contains(tag, "/") {
		return "" // the colon belonged to a registry port — no tag present
	}
	m := imageTagVersionRe.FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return m[1]
}

// majorMinorRe matches a version's leading major.minor.
var majorMinorRe = regexp.MustCompile(`^v?(\d+\.\d+)`)

// majorMinor normalizes a version string to its major.minor prefix ("8.3.13" → "8.3"); anything without
// both components yields "" (the version-templated agents need an exact major.minor directory).
func majorMinor(v string) string {
	m := majorMinorRe.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return ""
	}
	return m[1]
}

// --- CNB buildpack labels (registry-standard; set by pack/Paketo/cloud builders) ---

// labelBuildMetadata is the CNB build metadata label. Its JSON payload lists every buildpack that ran.
const labelBuildMetadata = "io.buildpacks.build.metadata"

// buildMetadata is the (partial) shape of the io.buildpacks.build.metadata label payload.
type buildMetadata struct {
	Buildpacks []struct {
		ID string `json:"id"`
	} `json:"buildpacks"`
}

// languageFromBuildpackLabels infers the workload language from CNB image config labels. It returns a
// raw language token (java/python/nodejs/dotnet/go/ruby/php) or "" when no buildpack signal is present;
// the caller normalizes it to the connector's coverable set. Buildpacks with a "runtime" component are
// preferred as the definitive signal, then any recognizable buildpack id.
func languageFromBuildpackLabels(labels map[string]string) string {
	raw := labels[labelBuildMetadata]
	if raw == "" {
		return ""
	}
	var meta buildMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return ""
	}
	for _, bp := range meta.Buildpacks {
		if strings.Contains(strings.ToLower(bp.ID), "runtime") {
			if l := languageFromBuildpackID(bp.ID); l != "" {
				return l
			}
		}
	}
	for _, bp := range meta.Buildpacks {
		if l := languageFromBuildpackID(bp.ID); l != "" {
			return l
		}
	}
	return ""
}

// languageFromBuildpackID maps a single buildpack id to a raw language token. It handles Google Cloud
// buildpacks (google.<lang>.<component>) and common Paketo ids (paketo-buildpacks/<name>). Returns ""
// for language-agnostic buildpacks (utility, base image, etc.).
func languageFromBuildpackID(id string) string {
	id = strings.ToLower(id)

	if strings.HasPrefix(id, "google.") {
		if parts := strings.Split(id, "."); len(parts) >= 2 {
			switch parts[1] {
			case "go", "nodejs", "python", "java", "dotnet", "ruby", "php":
				return parts[1]
			}
		}
	}

	switch {
	case strings.HasSuffix(id, "/go") || strings.Contains(id, "/go-") || strings.Contains(id, "go-dist"):
		return "go"
	case strings.Contains(id, "node"):
		return "nodejs"
	case strings.Contains(id, "python") || strings.Contains(id, "cpython"):
		return "python"
	case strings.Contains(id, "java") || strings.Contains(id, "jvm") || strings.Contains(id, "liberica") ||
		strings.Contains(id, "maven") || strings.Contains(id, "gradle") || strings.Contains(id, "sdkman"):
		return "java"
	case strings.Contains(id, "dotnet") || strings.Contains(id, ".net"):
		return "dotnet"
	case strings.Contains(id, "ruby"):
		return "ruby"
	case strings.Contains(id, "php"):
		return "php"
	}
	return ""
}

// --- workload-level runtime from the task definition alone ---

// runtimeLanguageMultiple is the workload-level language reported for a task definition whose
// instrumentable containers resolve to DIFFERENT languages. No single value is true for such a task, so the
// flat report field says so explicitly and the container inventory carries the real per-container answers.
// normalizeLanguage does not recognize it, which is what makes every consumer of the flat field (the planner
// most of all) fall through to the per-container layer rather than treat it as a language.
const runtimeLanguageMultiple = "multiple"

// taskDefinitionRuntime resolves a service's workload-level language and major.minor version from its task
// definition ALONE — the Odigos markers already on it plus each container's static signals — with no
// image-config read, no OTLP destination and no source overrides.
//
// It exists for the observer's cheap path (refreshOwnership), which holds a freshly described task definition
// but has no desired instrumentation to compute: the full selectTargets resolution needs a destination to
// build each container's export wiring, and pays a registry fetch per container for the image-config layer.
// Everything this needs is already in hand and free, so a service the connector cannot yet JUDGE still
// reports what it RUNS instead of an empty language.
//
// The marker layer is the load-bearing one, and it is not detection at all. An instrumented task definition
// RECORDS the agent that was staged into each container (see readECSMarker), so for the case this was written
// for — a workload adopted from live instrumentation, whose language was known to whoever applied it and
// nowhere in Odigos — the answer is the instrumentation naming itself.
//
// Ranking and aggregation mirror selectTargets and aggregateRuntime, so the value does not change meaning
// between the two modes: the strongest evidence present in the task definition wins (a recorded marker or a
// runtime env var over a bare image-reference substring), and containers that disagree report
// runtimeLanguageMultiple. With no image config in play, resolveECSRuntime only ever yields a language this
// connector can cover, so nothing here can name an agent that does not exist.
func taskDefinitionRuntime(ctx context.Context, td *ecstypes.TaskDefinition) (language, version string) {
	if td == nil {
		return "", ""
	}
	// The same candidate set the mutating path uses, so a proxy sidecar is no more a source of the workload's
	// language here than it is there. An error means there is no instrumentable container at all.
	candidates, err := candidateContainerIndexes(td)
	if err != nil {
		return "", ""
	}
	var resolved []resolvedRuntime
	var top detectionConfidence
	for _, idx := range candidates {
		c := &td.ContainerDefinitions[idx]
		// images is deliberately nil: this path must stay free of network reads — see the doc comment.
		rt := resolveECSRuntime(ctx, nil, "", "", containerManifestLanguage(c), "", c)
		if rt.language == "" {
			continue
		}
		resolved = append(resolved, rt)
		if rt.confidence > top {
			top = rt.confidence
		}
	}
	for _, rt := range resolved {
		// Relative, not absolute — see selectTargets' pass 2 for why the gate has to be a comparison between
		// siblings: an nginx container that merely mentions index.php must not make a real PHP task "multiple".
		if rt.confidence < top {
			continue
		}
		switch {
		case language == "":
			language, version = rt.language, rt.version
		case rt.language != language:
			return runtimeLanguageMultiple, ""
		case rt.version != version:
			version = "" // same language, different versions: no single version is the workload's
		}
	}
	return language, version
}

// taskDefRuntimeToStore returns the runtime a task-definition-only resolution should record for a service, and
// whether it is worth a write at all.
//
// Both rules are about not letting the cheap path damage the rich one. It may FILL a gap — that is the whole
// point — but an empty resolution must never erase a language a fully-resolved pass established, and a version
// this read simply cannot see is carried forward rather than blanked: that pass had the image config and the
// source's overrides, this one has neither, so a customer who deletes their destination must not lose the
// php 8.3 the version-templated agents are staged against. An unchanged result writes nothing, so a
// steady-state service costs no database write per observation cycle.
func taskDefRuntimeToStore(language, version, storedLanguage, storedVersion string) (string, string, bool) {
	if language == "" {
		return "", "", false
	}
	if version == "" && language == storedLanguage {
		version = storedVersion
	}
	if language == storedLanguage && version == storedVersion {
		return "", "", false
	}
	return language, version, true
}
