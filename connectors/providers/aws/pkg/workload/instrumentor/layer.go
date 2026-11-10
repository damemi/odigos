// Package instrumentor implements per-type AWS instrumentation, for the two workload types that are
// instrumented in place:
//
//   - aws.lambda — attach an OpenTelemetry layer and the OTEL_* env to the function;
//   - aws.fargate-task — stage the language agent into the task through a run-to-completion container and a
//     shared volume, and activate it through the app container's env.
//
// ECS on EC2 is not here and does not belong here: it is covered by a cluster-wide node agent rather than by
// mutating workloads, which makes it a compute-platform concern (pkg/computeplatform), and that install flow
// is still stubbed.
//
// Both implemented paths are reconcile-style and idempotent — they converge live AWS state to the source
// intent each cycle — and each has a read-only twin (observe_lambda.go, observe_fargate.go) that judges
// instrumentation a customer applied themselves without mutating anything.
package instrumentor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/awsclient"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

// DefaultLayerDir is where the connector image stages the bundled OTel Lambda layer artifacts,
// laid out as <DefaultLayerDir>/<language>/<arch>/layer.zip.
const DefaultLayerDir = "/var/odigos/lambda"

// Lambda env vars the instrumentor sets. The SDK auto-instrumentation (enabled by the exec wrapper)
// exports to the in-Lambda OTel collector on localhost — the layer's default — so we deliberately do
// NOT set OTEL_EXPORTER_OTLP_ENDPOINT; that keeps the function from blocking the invocation on the
// remote destination. The bundled collector reads its baked config and exports to the destination below
// asynchronously, off the invocation's critical path.
const (
	EnvServiceName       = "OTEL_SERVICE_NAME"
	EnvCollectorEndpoint = "ODIGOS_OTLP_ENDPOINT" // collector OTLP exporter target (host:port), read by the baked collector config
	EnvCollectorInsecure = "ODIGOS_OTLP_INSECURE" // collector OTLP exporter TLS insecure flag ("true"/"false")

	// EnvOtelLogLevel raises the in-function OTel log verbosity. When the debugLambdaTelemetry config toggle
	// is on the instrumentor sets it to debug so the layer/SDK + collector emit span and OTLP-export
	// diagnostics to the function's CloudWatch log group. Always stripped on rollback regardless of the toggle.
	EnvOtelLogLevel = "OTEL_LOG_LEVEL"

	// Legacy SDK-direct vars — no longer set, but stripped on rollback to clean up functions instrumented
	// by older connector versions (which pointed the SDK straight at the destination).
	envLegacyOTLPEndpoint = "OTEL_EXPORTER_OTLP_ENDPOINT"
	envLegacyOTLPHeaders  = "OTEL_EXPORTER_OTLP_HEADERS"

	// AWS_LAMBDA_EXEC_WRAPPER values provided by the OpenTelemetry Lambda layers. The wrapper script is
	// LANGUAGE-SPECIFIC: Python uses /opt/otel-instrument; Node.js and Ruby use /opt/otel-handler.
	// Java defaults to the full-agent bridge at /opt/otel-javaagent-handler.
	// Setting the wrong one (e.g. /opt/otel-instrument on a Java function) makes the layer bootstrap fail
	// at cold start with "/opt/otel-instrument: does not exist" and the function errors out. Use
	// execWrapperForRuntime, never a single hardcoded value.
	execWrapperInstrument = "/opt/otel-instrument" // python
	execWrapperAgent      = "/opt/otel-javaagent-handler"
	execWrapperHandler    = "/opt/otel-handler" // nodejs, java, ruby

	// The lightweight Java SDK ships four handler adapters. Picking the wrong one is not
	// a matter of richer or poorer spans. otel-handler installs TracingRequestWrapper, which implements
	// RequestHandler; a function implementing RequestStreamHandler has a different signature entirely, so
	// wrapping it that way breaks the invocation. otel-proxy-handler and otel-sqs-handler are the enriched
	// wrappers for API Gateway proxy and SQS events — a function that should use one of those still runs
	// under otel-handler, just with generic POJO spans.
	//
	// Nothing in GetFunctionConfiguration says which interface a handler implements: the handler string is a
	// class name either way. So the connector cannot detect this, and the customer selects it on the source
	// (see javaHandlerWrapperFor).
	execWrapperProxyHandler  = "/opt/otel-proxy-handler"  // java, API Gateway proxy events
	execWrapperSqsHandler    = "/opt/otel-sqs-handler"    // java, SQS events
	execWrapperStreamHandler = "/opt/otel-stream-handler" // java, RequestStreamHandler

	// Web-server mode (AWS Lambda Web Adapter) loader env vars + the value Odigos appends. These functions
	// run a normal web server, not a Lambda RequestHandler, so they cannot use the OTel handler exec
	// wrapper (the adapter already owns AWS_LAMBDA_EXEC_WRAPPER=/opt/bootstrap). Instead the language agent
	// is loaded via its native mechanism, alongside the adapter, and exports to the in-Lambda collector.
	EnvJavaToolOptions = "JAVA_TOOL_OPTIONS"
	EnvNodeOptions     = "NODE_OPTIONS"
	EnvPythonPath      = "PYTHONPATH"

	javaAgentOption  = "-javaagent:/opt/opentelemetry-javaagent.jar"
	nodeImportOption = "--import /opt/init.mjs"

	// EnvOtlpProtocol / otlpProtocolHTTP: the Python OTel layer bundles ONLY the HTTP OTLP exporter, but
	// the SDK defaults to gRPC (otlp_proto_grpc), which then fails to load ("Requested component
	// 'otlp_proto_grpc' not found"). The layer's own otel-instrument script sets http/protobuf for this
	// reason; web-server mode bypasses that script, so we set it ourselves for Python. The in-Lambda
	// collector serves HTTP on localhost:4318 (the SDK's http/protobuf default), so no endpoint is needed.
	EnvOtlpProtocol  = "OTEL_EXPORTER_OTLP_PROTOCOL"
	otlpProtocolHTTP = "http/protobuf"

	// Per-signal exporter selectors. Set to "otlp" to export a signal to the in-Lambda collector, or "none"
	// to disable it — so a signal the destination does not implement (e.g. metrics/logs to a traces-only
	// Jaeger, which returns "Unimplemented ... MetricsService") is never exported.
	EnvTracesExporter  = "OTEL_TRACES_EXPORTER"
	EnvMetricsExporter = "OTEL_METRICS_EXPORTER"
	EnvLogsExporter    = "OTEL_LOGS_EXPORTER"
	exporterOTLP       = "otlp"
	exporterNone       = "none"
	// pythonAutoInstrDir holds the OTel auto-instrumentation sitecustomize.py; on PYTHONPATH the Python
	// interpreter loads it at startup and auto-instruments.
	pythonAutoInstrDir = "/opt/python/opentelemetry/instrumentation/auto_instrumentation"
	// pythonInstrPath is prepended to PYTHONPATH: the layer's OTel packages + the sitecustomize dir, PLUS
	// the app's dependency locations. The last part is essential for web-server apps: the process is a
	// plain `python app.py` (not the Lambda Python runtime), so it gets NO automatic /var/task on sys.path,
	// and OTel auto-instrumentation SKIPS any library (e.g. Flask) not importable when sitecustomize runs.
	// Making the app's deps discoverable at startup is what lets Flask/requests/etc. actually be patched.
	// Non-existent path entries are harmless (Python ignores them), so both common layouts are covered.
	pythonInstrPath = "/opt/python:" + pythonAutoInstrDir + ":/var/task:/var/task/site-packages"

	// otelLogLevelDebug is the value set on EnvOtelLogLevel when debug telemetry is enabled.
	otelLogLevelDebug = "debug"
)

// ErrLayerUnavailable is returned when no layer ARN can be resolved for a function's region/runtime/arch.
var ErrLayerUnavailable = errors.New("no OpenTelemetry Lambda layer available for region/runtime/arch")

// LayerResolver resolves the OpenTelemetry Lambda layer ARN to attach to a function, given its region,
// runtime (e.g. "python3.12", "nodejs20.x") and architecture (e.g. "x86_64", "arm64"). The connector
// supports two strategies behind this seam: the public OTel layers today, and (later) an Odigos-owned
// layer the connector publishes into the customer account.
type LayerResolver interface {
	Resolve(ctx context.Context, region, runtime, arch string) (string, error)
}

// existingLayerDiscoverer is implemented by resolvers that can look up an already-published layer without
// publishing one (ManagedLayerResolver). Resolvers that never publish (the public-ARN table) don't need it.
type existingLayerDiscoverer interface {
	DiscoverExisting(ctx context.Context, region, runtime, arch string) (string, error)
}

// NonPublishingLayerARN resolves the layer ARN for a function WITHOUT publishing one into the account. It is
// the read-only counterpart to LayerResolver.Resolve, whose managed implementation publishes the bundled layer
// as a side effect of being asked.
//
// Two callers need that guarantee, for the same reason expressed differently:
//
//   - rollback, which is uninstrumenting a function and must not mutate the account on the way out;
//   - the read-only observer, which runs for MANUAL-mode types. There, publishing would be a write to a
//     customer's account performed by the one code path whose entire contract is that it performs none —
//     and it would happen on a schedule, for every function, in the mode where Odigos has explicitly been
//     told not to touch anything.
//
// A miss is returned as an error rather than an empty ARN, so a caller decides what an unresolvable layer
// means instead of silently treating "unknown" as "absent".
func NonPublishingLayerARN(ctx context.Context, layers LayerResolver, region, runtime, arch string) (string, error) {
	if d, ok := layers.(existingLayerDiscoverer); ok {
		return d.DiscoverExisting(ctx, region, runtime, arch)
	}
	// Resolvers without a discover-only mode don't publish on Resolve either (PublicLayerResolver reads a
	// configured ARN table), so calling Resolve is already non-mutating for them.
	return layers.Resolve(ctx, region, runtime, arch)
}

// AccountLayerResolver discovers a layer the connector ITSELF published on a previous run: it lists the
// layers in the function's region compatible with the function's runtime and architecture
// (lambda:ListLayers) and returns the one whose name is our published-layer signature
// (publishedLayerName). It deliberately does NOT reuse a customer's or a public OpenTelemetry/ADOT layer,
// even one whose name contains "otel"/"adot": the instrumentor wires env (e.g. AWS_LAMBDA_EXEC_WRAPPER=
// /opt/otel-instrument, the ODIGOS_OTLP_* collector vars) that assumes OUR layer's exact script layout and
// baked collector config. Attaching a foreign layer would break the function at cold start (missing
// wrapper script) or silently misroute telemetry (a foreign collector ignores ODIGOS_OTLP_*). Discovery
// exists only so the connector reuses its own layer instead of republishing it every run.
type AccountLayerResolver struct {
	cache *awsclient.Cache
}

// NewAccountLayerResolver builds the account-discovery resolver over the shared config cache.
func NewAccountLayerResolver(cache *awsclient.Cache) *AccountLayerResolver {
	return &AccountLayerResolver{cache: cache}
}

func (r *AccountLayerResolver) Resolve(ctx context.Context, region, runtime, arch string) (string, error) {
	found, err := r.latest(ctx, region, runtime, arch)
	if err != nil {
		return "", err
	}
	return found.arn, nil
}

// discoveredLayer is the newest version of our layer the account holds for a runtime/arch, with the
// Description it was published under. The description is what tells ManagedLayerResolver whether that
// version carries the bytes this connector build bundles (see bundledDescription).
type discoveredLayer struct {
	arn         string
	description string
}

func (r *AccountLayerResolver) latest(ctx context.Context, region, runtime, arch string) (discoveredLayer, error) {
	return r.latestArtifact(ctx, region, runtime, arch, LanguageFromRuntime(runtime))
}
func (r *AccountLayerResolver) latestArtifact(ctx context.Context, region, runtime, arch, lang string) (discoveredLayer, error) {
	if lang == "" {
		return discoveredLayer{}, fmt.Errorf("%w: unsupported runtime %q", ErrLayerUnavailable, runtime)
	}
	wantName := publishedLayerName(lang, arch)
	cfg, err := r.cache.Config(ctx, region)
	if err != nil {
		return discoveredLayer{}, err
	}
	in := &lambda.ListLayersInput{}
	if runtime != "" {
		in.CompatibleRuntime = lambdatypes.Runtime(runtime)
	}
	if arch != "" {
		in.CompatibleArchitecture = lambdatypes.Architecture(arch)
	}
	// ListLayers returns up to 50 layers per page. Our own published layer may sit past the first page in
	// an account with many layers, so paginate rather than risk a spurious republish (version churn).
	client := lambda.NewFromConfig(cfg)
	for {
		out, err := client.ListLayers(ctx, in)
		if err != nil {
			return discoveredLayer{}, fmt.Errorf("list layers (region=%s runtime=%s arch=%s): %w", region, runtime, arch, err)
		}
		for _, l := range out.Layers {
			if l.LatestMatchingVersion == nil {
				continue
			}
			// Match ONLY our own published layer name exactly — never a customer/public OTel layer.
			if aws.ToString(l.LayerName) != wantName {
				continue
			}
			if arn := aws.ToString(l.LatestMatchingVersion.LayerVersionArn); arn != "" {
				return discoveredLayer{arn: arn, description: aws.ToString(l.LatestMatchingVersion.Description)}, nil
			}
		}
		if out.NextMarker == nil {
			break
		}
		in.Marker = out.NextMarker
	}
	return discoveredLayer{}, fmt.Errorf("%w: region=%s runtime=%s arch=%s (no odigos-published layer %q found in account)", ErrLayerUnavailable, region, runtime, arch, wantName)
}

// MatchesInstrumentationLayerARN reports whether arn refers to a layer THIS connector published. It is
// name-signature based (isOdigosLayerName): a customer or public OTel/ADOT layer is not considered ours.
func MatchesInstrumentationLayerARN(arn string) bool {
	name, _, ok := store.ParseLayerVersionARN(arn)
	return ok && store.IsOdigosLayerName(name)
}

// normalizeInstrumentationLayers keeps at most one version of each Lambda layer name. When desiredARN
// is set, every other version of that layer name is removed and desiredARN is appended if missing.
// This repairs functions left with multiple versions of the same layer after a failed partial update.
func normalizeInstrumentationLayers(layers []string, desiredARN string) []string {
	desiredName, _, hasDesired := store.ParseLayerVersionARN(desiredARN)
	seen := map[string]bool{}
	out := make([]string, 0, len(layers)+1)
	for _, arn := range layers {
		name, _, ok := store.ParseLayerVersionARN(arn)
		if !ok {
			out = append(out, arn)
			continue
		}
		if hasDesired && (name == desiredName || (store.IsOdigosLayerName(name) && store.IsOdigosLayerName(desiredName))) {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, arn)
	}
	if hasDesired && !containsString(out, desiredARN) {
		out = append(out, desiredARN)
	}
	return out
}

func containsString(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// PublicLayerResolver resolves to the public open-telemetry/opentelemetry-lambda layers. Their ARNs are
// region-, language- and version-specific and rotate with releases, so they are NOT hardcoded: the ARN
// table is supplied via connector config (operators copy the values for their regions from
// https://github.com/open-telemetry/opentelemetry-lambda/releases). A missing entry fails the function
// with ErrLayerUnavailable rather than attaching a wrong layer. Keys are "<region>/<language>/<arch>"
// with progressively looser fallbacks ("<region>/<language>", "<language>").
type PublicLayerResolver struct {
	// ARNs maps a layer key to a full Lambda layer ARN. Empty until configured.
	ARNs map[string]string
}

// NewPublicLayerResolver builds the public-layer resolver over a configured ARN table.
func NewPublicLayerResolver(arns map[string]string) *PublicLayerResolver {
	return &PublicLayerResolver{ARNs: arns}
}

func (r *PublicLayerResolver) Resolve(ctx context.Context, region, runtime, arch string) (string, error) {
	return r.ResolveDistro(ctx, region, runtime, arch, "")
}
func (r *PublicLayerResolver) DiscoverDistro(ctx context.Context, region, runtime, arch, distro string) (string, error) {
	return r.ResolveDistro(ctx, region, runtime, arch, distro)
}
func (r *PublicLayerResolver) ResolveDistro(_ context.Context, region, runtime, arch, distro string) (string, error) {
	if err := ValidateLambdaDistroRuntime(runtime, ActivationHandler, distro); err != nil {
		return "", fmt.Errorf("%w: %v", ErrLayerUnavailable, err)
	}
	return r.resolveArtifact(region, lambdaArtifact(runtime, distro), arch)
}
func (r *PublicLayerResolver) ResolveCollector(_ context.Context, region, runtime, arch string, _ bool) (string, error) {
	return r.resolveArtifact(region, "adot-collector", arch)
}
func (r *PublicLayerResolver) resolveArtifact(region, lang, arch string) (string, error) {
	if lang == "" {
		return "", fmt.Errorf("%w: unsupported Lambda language %q", ErrLayerUnavailable, lang)
	}
	for _, key := range []string{
		layerKey(region, lang, arch),
		region + "/" + lang,
		lang,
	} {
		if arn, ok := r.ARNs[key]; ok && arn != "" {
			return arn, nil
		}
	}
	return "", fmt.Errorf("%w: region=%s language=%s arch=%s (configure connector layer ARNs)", ErrLayerUnavailable, region, lang, arch)
}

func layerKey(region, language, arch string) string {
	return region + "/" + language + "/" + arch
}

// JavaHandlerDistros are the values a source's distro override may carry to select a Java wrapper. They are
// the layer's own script names so the value a customer sets is the thing they can see in the layer, rather
// than an Odigos synonym for it.
var JavaHandlerDistros = map[string]string{
	"otel-handler":        execWrapperHandler,
	"otel-proxy-handler":  execWrapperProxyHandler,
	"otel-sqs-handler":    execWrapperSqsHandler,
	"otel-stream-handler": execWrapperStreamHandler,
}

// javaHandlerWrapperFor selects the full-agent lifecycle bridge by default, or one of the
// lightweight SDK's explicit handler adapters. Unknown choices are refused by the resolver.
func javaHandlerWrapperFor(distro string) string {
	if distro == "" || distro == JavaAgentDistro {
		return execWrapperAgent
	}
	for _, d := range lambdaRecipes() {
		if d.Language == "java" && d.Name == distro {
			return d.ExecWrapper
		}
	}
	return JavaHandlerDistros[distro]
}

// execWrapperForRuntime returns the AWS_LAMBDA_EXEC_WRAPPER value the OTel layer expects for a function's
// runtime, or "" when there is none — an unsupported runtime, or a Java distro override naming a wrapper
// the layer does not ship. The caller declines to instrument in both cases rather than setting a wrapper
// that fails at cold start.
//
// distro is the source's distro override and is only consulted for Java: every other language's layer ships
// exactly one wrapper, so an override there would be a choice between one option.
func execWrapperForRuntime(runtime, distro string) string {
	lang := LanguageFromRuntime(runtime)
	if d := recipeFor(lang, distro); d.Artifact != "" {
		return d.ExecWrapper
	}
	if lang == "java" {
		return javaHandlerWrapperFor(distro)
	}
	return execWrapperForLanguage(lang)
}

// execWrapperForLanguage is the same mapping keyed by the OTel layer language rather than by an AWS runtime
// identifier, for callers that already hold a language — the published instrumentation plan, which is per
// language and has no single runtime to speak of. Split out rather than duplicated so the two cannot disagree
// about which wrapper a language gets, which is the mistake that breaks a function at cold start.
func execWrapperForLanguage(lang string) string {
	switch lang {
	case "python":
		return execWrapperInstrument
	case "java":
		return execWrapperAgent
	case "nodejs", "ruby":
		return execWrapperHandler
	default:
		return ""
	}
}

// isOdigosExecWrapper reports whether an AWS_LAMBDA_EXEC_WRAPPER value is one our layers set (either
// language wrapper). Used when attributing ownership / capturing prior config so a function carrying our
// wrapper — including one a pre-fix connector set to the wrong language value — is recognized as ours and
// its wrapper is removed (not "restored") on rollback.
// Every Java variant counts: a function instrumented with otel-stream-handler must still be recognized as
// ours, or rollback would leave the wrapper behind and ownership detection would read it as a customer's.
func isOdigosExecWrapper(v string) bool {
	switch v {
	case lambdaWebServerWrapper, execWrapperInstrument, execWrapperAgent, execWrapperHandler, execWrapperProxyHandler, execWrapperSqsHandler, execWrapperStreamHandler:
		return true
	}
	for _, recipe := range lambdaRecipes() {
		if v == recipe.ExecWrapper {
			return true
		}
	}
	return false
}

// isJavaHandler reports whether a Lambda handler string is a standard Java RequestHandler reference the
// OpenTelemetry Java layer can wrap: a fully-qualified class ("com.example.App"), optionally with an
// explicit method ("com.example.App::handleRequest"). It rejects non-Java handlers such as the AWS Lambda
// Web Adapter's "run.sh"/"bootstrap" — the OTel Java wrapper would otherwise reject them at runtime
// ("X is not a valid handler name. Expected format: package.ClassName::methodName") and break the function.
func isJavaHandler(handler string) bool {
	if handler == "" {
		return false
	}
	// "class::method" form is unambiguously a Java handler.
	if strings.Contains(handler, "::") {
		return true
	}
	// Otherwise it must be a bare class reference; by Java convention the class name (the last dotted
	// segment) starts with an uppercase letter. This distinguishes "com.example.App" from "run.sh".
	seg := handler
	if i := strings.LastIndexByte(handler, '.'); i >= 0 {
		seg = handler[i+1:]
	}
	return seg != "" && seg[0] >= 'A' && seg[0] <= 'Z'
}

// usesLambdaWebAdapter reports whether any attached layer is the AWS Lambda Web Adapter. Such functions
// run a web server fronted by the adapter rather than a language handler, so handler/exec-wrapper-based
// OpenTelemetry instrumentation does not apply and would break the function.
func usesLambdaWebAdapter(layers []string) bool {
	for _, arn := range layers {
		name, _, ok := store.ParseLayerVersionARN(arn)
		if !ok {
			name = arn
		}
		if strings.Contains(strings.ToLower(name), "lambdaadapter") {
			return true
		}
	}
	return false
}

// unsupportedHandlerReason returns a reason a NON-web-adapter function cannot be instrumented in handler
// mode (empty if it can). Web-adapter functions are handled by web-server mode and are NOT routed here.
// Two cases are refused:
//   - the function already carries a non-Odigos AWS_LAMBDA_EXEC_WRAPPER (Datadog's /opt/datadog_wrapper, a
//     secrets/bootstrap wrapper, …). Handler-mode instrumentation would overwrite it — a function has only
//     one exec wrapper — silently disabling the customer's tooling or breaking the function. A wrapper that
//     is ours (a re-instrument of a function we already own) is fine.
//   - a Java function whose handler is not a standard class[::method] the OTel Java wrapper can wrap.
func unsupportedHandlerReason(runtime, handler, execWrapper string) string {
	if execWrapper != "" && !isOdigosExecWrapper(execWrapper) {
		return fmt.Sprintf("function sets a non-Odigos AWS_LAMBDA_EXEC_WRAPPER %q that handler-mode instrumentation would overwrite", execWrapper)
	}
	if LanguageFromRuntime(runtime) == "java" && !isJavaHandler(handler) {
		return fmt.Sprintf("Java handler %q is not a class[::method] handler the OpenTelemetry Java layer can wrap", handler)
	}
	return ""
}

// webServerLoader returns the env var, the value to add, and the join separator that loads the language
// OpenTelemetry agent/SDK for a web-server (AWS Lambda Web Adapter) function, plus whether web-server mode
// is supported for the language. The loader runs alongside the adapter's bootstrap (it does NOT use
// AWS_LAMBDA_EXEC_WRAPPER) and the instrumented server exports to the in-Lambda collector on localhost.
func webServerLoader(lang string) (envKey, value, sep string, ok bool) {
	switch lang {
	case "java":
		return EnvJavaToolOptions, javaAgentOption, " ", true
	case "nodejs":
		return EnvNodeOptions, nodeImportOption, " ", true
	case "python":
		return EnvPythonPath, pythonInstrPath, ":", true
	default:
		// ruby (and anything else) has no verified web-server loader yet.
		return "", "", "", false
	}
}

// webServerExtraEnv returns additional env vars a language needs in web-server mode beyond the loader.
// Python must be pinned to the HTTP OTLP exporter (the only one bundled in its layer); other languages
// export correctly with their defaults.
func webServerExtraEnv(lang string) map[string]string {
	if lang == "python" {
		return map[string]string{EnvOtlpProtocol: otlpProtocolHTTP}
	}
	return nil
}

// webServerLoaderApplied reports whether the function's env already carries our exact web-server loader
// value. Checking the full value (not just a substring) means a changed loader value — e.g. added
// PYTHONPATH dep dirs — fails the check and forces a converging re-instrument.
func webServerLoaderApplied(lang string, env map[string]string) bool {
	key, value, _, ok := webServerLoader(lang)
	if !ok {
		return false
	}
	return strings.Contains(env[key], value)
}

// addLoaderValue idempotently adds our loader value to an existing env value. PYTHONPATH is prepended (so
// the layer packages + sitecustomize are found first); the *_OPTIONS vars are appended.
func addLoaderValue(existing, value, sep string) string {
	if existing == "" {
		return value
	}
	if strings.Contains(existing, value) {
		return existing
	}
	if sep == ":" { // PYTHONPATH — prepend ours
		return value + sep + existing
	}
	return existing + sep + value // *_OPTIONS — append ours
}

// removeLoaderValue is the inverse of addLoaderValue: it removes our loader value from an env value,
// collapsing the adjacent separator, and returns the remaining (customer) portion — "" if only ours was
// present. Handles the prepend (PYTHONPATH), append (*_OPTIONS) and mid-string forms; if our value is not
// present as a clean separator-delimited segment the value is returned unchanged (it is the customer's).
func removeLoaderValue(existing, value, sep string) string {
	switch {
	case existing == "" || value == "":
		return existing
	case existing == value:
		return ""
	case strings.HasPrefix(existing, value+sep):
		return strings.TrimPrefix(existing, value+sep)
	case strings.HasSuffix(existing, sep+value):
		return strings.TrimSuffix(existing, sep+value)
	case strings.Contains(existing, sep+value+sep):
		return strings.Replace(existing, sep+value+sep, sep, 1)
	default:
		return existing
	}
}

// LanguageFromRuntime maps an AWS Lambda runtime identifier to the OTel layer language key, or "" when
// the runtime has no Odigos instrumentation. Only languages the connector bundles an OpenTelemetry Lambda
// layer for are mapped (python, nodejs, java, ruby); others (e.g. dotnet, go, provided/custom) return ""
// so the instrumentor reports the workload as an unsupported-runtime error rather than mis-instrumenting.
func LanguageFromRuntime(runtime string) string {
	switch {
	case strings.HasPrefix(runtime, "python"):
		return "python"
	case strings.HasPrefix(runtime, "nodejs"):
		return "nodejs"
	case strings.HasPrefix(runtime, "java"):
		return "java"
	case strings.HasPrefix(runtime, "ruby"):
		return "ruby"
	default:
		return ""
	}
}

// ManagedLayerResolver makes the connector self-sufficient: it first tries to discover a suitable OTel
// layer already in the account (including one it published on a previous run); if none exists it
// publishes the layer bundled in the connector image — <layerDir>/<language>/<arch>/layer.zip — into the
// account via lambda:PublishLayerVersion, then uses that. This is the "check if a suitable layer exists,
// else upload ours" path. Published/discovered ARNs are cached per region/language/arch for the process
// lifetime so publishing happens at most once.
//
// A discovered layer is reused only when it carries the bytes this connector build bundles. Discovery is by
// name, and every connector version publishes under the same name, so without that check an account a
// previous connector version instrumented would keep its old layer for good: the key is in use, so it is
// never an orphan, so it is never refreshed — and bumping ARTIFACTS_VERSION would change nothing for the
// customers it was meant for.
type ManagedLayerResolver struct {
	cache        *awsclient.Cache
	layerDir     string
	discover     *AccountLayerResolver
	mu           sync.Mutex
	arns         map[string]string // region/lang/arch -> arn (discovered or published)
	published    map[string]string // region/lang/arch -> arn, only layers WE published (GC candidates)
	descriptions map[string]string // lang/arch -> bundledDescription, memoized (hashing a ~45MB zip)
}

// NewManagedLayerResolver builds the discover-or-publish resolver. An empty layerDir uses DefaultLayerDir.
func NewManagedLayerResolver(cache *awsclient.Cache, layerDir string) *ManagedLayerResolver {
	if layerDir == "" {
		layerDir = DefaultLayerDir
	}
	return &ManagedLayerResolver{
		cache:        cache,
		layerDir:     layerDir,
		discover:     NewAccountLayerResolver(cache),
		arns:         make(map[string]string),
		published:    make(map[string]string),
		descriptions: make(map[string]string),
	}
}

func (r *ManagedLayerResolver) Resolve(ctx context.Context, region, runtime, arch string) (string, error) {
	return r.ResolveDistro(ctx, region, runtime, arch, "")
}
func (r *ManagedLayerResolver) DiscoverDistro(ctx context.Context, region, runtime, arch, distro string) (string, error) {
	found, err := r.discover.latestArtifact(ctx, region, runtime, arch, lambdaArtifact(runtime, distro))
	return found.arn, err
}
func (r *ManagedLayerResolver) ResolveDistro(ctx context.Context, region, runtime, arch, distro string) (string, error) {
	if err := ValidateLambdaDistroRuntime(runtime, ActivationHandler, distro); err != nil {
		return "", err
	}
	return r.resolveArtifact(ctx, region, runtime, arch, lambdaArtifact(runtime, distro))
}
func (r *ManagedLayerResolver) ResolveCollector(ctx context.Context, region, runtime, arch string, readOnly bool) (string, error) {
	if readOnly {
		found, err := r.discover.latestArtifact(ctx, region, runtime, arch, "adot-collector")
		return found.arn, err
	}
	return r.resolveArtifact(ctx, region, runtime, arch, "adot-collector")
}
func (r *ManagedLayerResolver) resolveArtifact(ctx context.Context, region, runtime, arch, lang string) (string, error) {
	if lang == "" {
		return "", fmt.Errorf("%w: unsupported runtime %q", ErrLayerUnavailable, runtime)
	}
	key := region + "/" + lang + "/" + arch

	r.mu.Lock()
	if arn, ok := r.arns[key]; ok {
		r.mu.Unlock()
		return arn, nil
	}
	r.mu.Unlock()

	// 1. Reuse OUR OWN layer already in the account — one this connector published on a previous run
	//    (AccountLayerResolver matches only our published-layer name, never a customer/public layer).
	//    Record it as a GC candidate so it is reclaimed when its last Lambda is uninstrumented (orphaned
	//    ones are deleted+republished earlier, in RefreshOrphan).
	//
	//    Only if it is the layer this build bundles, though. A version published by an earlier connector
	//    release — or under a CDK/SAR description — falls through to step 2, which publishes the current
	//    zip as a new version of the same layer name; instrument then swaps every function onto it
	//    (normalizeInstrumentationLayers keeps one version per name). The superseded version is left in
	//    place: functions still reference it until that swap lands.
	if found, err := r.discover.latestArtifact(ctx, region, runtime, arch, lang); err == nil {
		if reusableLayer(found, r.bundledDescription(lang, arch)) {
			r.mu.Lock()
			r.arns[key] = found.arn
			r.published[key] = found.arn
			r.mu.Unlock()
			return found.arn, nil
		}
		log.Info("odigos lambda layer in account is not the bundled version; publishing the current one",
			"arn", found.arn, "description", found.description, "key", key)
	}
	// 2. Publish our bundled layer into the account.
	arn, err := r.publish(ctx, region, lang, arch)
	if err != nil {
		return "", err
	}
	r.mu.Lock()
	r.arns[key] = arn
	r.published[key] = arn // ours — eligible for GC when the region stops using it
	r.mu.Unlock()
	return arn, nil
}

// DiscoverExisting returns an already-published Odigos layer ARN for region/runtime/arch WITHOUT
// publishing one if none exists (unlike Resolve). Used by the rollback ownership path so uninstrumenting a
// function never mutates the customer account by publishing a layer as a side effect.
func (r *ManagedLayerResolver) DiscoverExisting(ctx context.Context, region, runtime, arch string) (string, error) {
	return r.discover.Resolve(ctx, region, runtime, arch)
}

// GarbageCollect deletes layers WE published for a region/lang/arch that is no longer using them (no
// enabled or instrumented Lambda for that key). The layer is global per language+arch, so it is removed
// once the last such function drops it. Discovered non-Odigos (customer/public) layers are never deleted.
func (r *ManagedLayerResolver) GarbageCollect(ctx context.Context, eligibleKeys map[string]bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, arn := range r.published {
		if !eligibleKeys[key] {
			continue
		}
		region := regionFromLayerKey(key)
		name, version, ok := store.ParseLayerVersionARN(arn)
		if !ok {
			continue
		}
		if err := r.deleteLayerVersion(ctx, region, name, version); err != nil {
			log.Error("layer gc: delete failed", "arn", arn, "error", err)
			continue
		}
		log.Info("deleted unused odigos lambda layer", "arn", arn, "key", key)
		delete(r.published, key)
		delete(r.arns, key)
	}
	return nil
}

// RefreshOrphan deletes an orphaned Odigos-published layer for region/lang/arch — one left in the account
// (e.g. by a previous connector version or a failed cleanup) but no longer in use — and drops it from the
// caches so the next Resolve republishes the current bundled version. It only touches layers WE own (by
// publishedLayerName); a customer/public OTel layer is left untouched. The caller decides "orphaned" (no
// instrumented function uses the key) so a layer in active use is never disturbed.
func (r *ManagedLayerResolver) RefreshOrphan(ctx context.Context, region, runtime, arch string) error {
	lang := LanguageFromRuntime(runtime)
	if lang == "" {
		return nil
	}
	arn, err := r.discover.Resolve(ctx, region, runtime, arch)
	if err != nil {
		return nil // nothing in the account to refresh
	}
	name, version, ok := store.ParseLayerVersionARN(arn)
	if !ok || name != publishedLayerName(lang, arch) {
		return nil // not our layer — never delete a customer/public layer
	}
	if err := r.deleteLayerVersion(ctx, region, name, version); err != nil {
		return fmt.Errorf("delete orphaned odigos layer %s: %w", arn, err)
	}
	key := region + "/" + lang + "/" + arch
	r.mu.Lock()
	delete(r.arns, key)
	delete(r.published, key)
	r.mu.Unlock()
	log.Info("deleted orphaned odigos lambda layer; will republish current version", "arn", arn, "key", key)
	return nil
}

func (r *ManagedLayerResolver) deleteLayerVersion(ctx context.Context, region, name string, version int64) error {
	cfg, err := r.cache.Config(ctx, region)
	if err != nil {
		return err
	}
	_, err = lambda.NewFromConfig(cfg).DeleteLayerVersion(ctx, &lambda.DeleteLayerVersionInput{
		LayerName:     aws.String(name),
		VersionNumber: aws.Int64(version),
	})
	return err
}

// regionFromLayerKey extracts the region from a "region/lang/arch" layer key.
func regionFromLayerKey(key string) string {
	if i := strings.IndexByte(key, '/'); i >= 0 {
		return key[:i]
	}
	return key
}

// publishedLayerName is the layer name this connector publishes for a language/arch. It carries
// store.OdigosLayerNamePrefix so store.IsOdigosLayerName recognizes it, and we only ever delete layers we own.
func publishedLayerName(lang, arch string) string {
	return store.OdigosLayerNamePrefix + lang + "-" + arch
}

func (r *ManagedLayerResolver) publish(ctx context.Context, region, lang, arch string) (string, error) {
	zipPath := filepath.Join(r.layerDir, lang, arch, "layer.zip")
	content, err := os.ReadFile(zipPath)
	if err != nil {
		return "", fmt.Errorf("%w: no bundled layer for %s/%s at %s: %v", ErrLayerUnavailable, lang, arch, zipPath, err)
	}
	cfg, err := r.cache.Config(ctx, region)
	if err != nil {
		return "", err
	}
	name := publishedLayerName(lang, arch)
	description := bundledLayerDescription(lang, arch, content)
	r.mu.Lock()
	r.descriptions[lang+"/"+arch] = description
	r.mu.Unlock()
	out, err := lambda.NewFromConfig(cfg).PublishLayerVersion(ctx, &lambda.PublishLayerVersionInput{
		LayerName:               aws.String(name),
		Description:             aws.String(description),
		Content:                 &lambdatypes.LayerVersionContentInput{ZipFile: content},
		CompatibleRuntimes:      compatibleRuntimes(lang),
		CompatibleArchitectures: []lambdatypes.Architecture{lambdatypes.Architecture(arch)},
	})
	if err != nil {
		return "", fmt.Errorf("publish layer %s in %s: %w", name, region, err)
	}
	return aws.ToString(out.LayerVersionArn), nil
}

// bundledLayerDescription is the Description the connector publishes a bundled layer under: the layer's
// purpose, then the leading 16 hex digits of the zip's sha256. The digest is the point — it is how a later
// Resolve, from this or any other connector build, tells whether a discovered version carries the bytes it
// bundles. A version number would not do: LambdaLayerDir can point at any zips, and a rebuilt zip of the same
// release must still count as the same layer (aws-connector-artifacts builds them reproducibly for exactly
// that reason).
func bundledLayerDescription(lang, arch string, content []byte) string {
	sum := sha256.Sum256(content)
	return "Odigos OpenTelemetry layer for " + lang + "/" + arch + " sha256:" + hex.EncodeToString(sum[:])[:16]
}

// bundledDescription returns bundledLayerDescription for the zip staged at <layerDir>/<lang>/<arch>, or ""
// when there is none to read. Memoized: the zip does not change while the process runs.
func (r *ManagedLayerResolver) bundledDescription(lang, arch string) string {
	key := lang + "/" + arch
	r.mu.Lock()
	if d, ok := r.descriptions[key]; ok {
		r.mu.Unlock()
		return d
	}
	r.mu.Unlock()
	content, err := os.ReadFile(filepath.Join(r.layerDir, lang, arch, "layer.zip"))
	if err != nil {
		return ""
	}
	d := bundledLayerDescription(lang, arch, content)
	r.mu.Lock()
	r.descriptions[key] = d
	r.mu.Unlock()
	return d
}

// reusableLayer reports whether a discovered layer version can be attached as it is. It can when it was
// published from the zip this build bundles. It also can when there is no bundled zip to compare against
// (bundled == ""): publishing needs that zip too, so replacing the version is impossible, and reusing it is
// what every connector did before this check existed.
func reusableLayer(found discoveredLayer, bundled string) bool {
	return bundled == "" || found.description == bundled
}

// compatibleRuntimes is the runtime set the published layer advertises. Built from strings to avoid coupling
// to specific SDK enum constants, and the list is every non-retired version of each language AWS currently
// offers. That runs ahead of the pinned SDK's lambdatypes.Runtime: the Amazon Linux 2023 Java identifiers
// (java8.al2023, java11.al2023, java17.al2023) are newer than its enum, which is fine — the enum is a client
// convenience and Lambda validates the value server-side.
//
// A missing entry is not cosmetic. AWS does not enforce CompatibleRuntimes when a layer is attached, so
// instrumentation still works; what breaks is DISCOVERY. AccountLayerResolver finds a previously-published
// layer with lambda:ListLayers filtered by the function's runtime, so a layer that does not advertise
// python3.14 is invisible to a python3.14 function — and the resolver then publishes a second, identical
// layer version on every fresh region/language, quietly accumulating them.
//
// The lists run wider than LanguageFromRuntime needs on purpose: that function prefix-matches, so it already
// routes java8.al2 and ruby4.0 to a language, and the layer has to be discoverable for the runtimes the
// connector will in fact try to instrument.
func compatibleRuntimes(lang string) []lambdatypes.Runtime {
	if lang == "adot-collector" {
		seen := map[string]bool{}
		out := []lambdatypes.Runtime{}
		for _, d := range lambdaRecipes() {
			for _, runtime := range d.CompatibleRuntimes {
				if !seen[runtime] {
					out = append(out, lambdatypes.Runtime(runtime))
					seen[runtime] = true
				}
			}
		}
		return out
	}
	for _, d := range lambdaRecipes() {
		if d.Artifact == lang && len(d.CompatibleRuntimes) > 0 {
			out := []lambdatypes.Runtime{}
			for _, runtime := range d.CompatibleRuntimes {
				out = append(out, lambdatypes.Runtime(runtime))
			}
			return out
		}
	}
	var names []string
	switch lang {
	case "python":
		names = []string{"python3.9", "python3.10", "python3.11", "python3.12", "python3.13", "python3.14"}
	case "nodejs":
		names = []string{"nodejs18.x", "nodejs20.x", "nodejs22.x", "nodejs24.x"}
	case "java":
		names = []string{"java8.al2", "java8.al2023", "java11", "java11.al2023", "java17", "java17.al2023", "java21", "java25"}
	case "ruby":
		names = []string{"ruby3.2", "ruby3.3", "ruby3.4", "ruby4.0"}
	}
	out := make([]lambdatypes.Runtime, len(names))
	for i, n := range names {
		out[i] = lambdatypes.Runtime(n)
	}
	return out
}

// UsesLambdaWebAdapter exposes activation detection to the capability catalog.
func UsesLambdaWebAdapter(layers []string) bool { return usesLambdaWebAdapter(layers) }
