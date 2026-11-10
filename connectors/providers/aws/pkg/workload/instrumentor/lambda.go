package instrumentor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos/connectors/base/log"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/awsclient"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

// ErrUnsupportedRuntime is returned by instrument when a Lambda's runtime has no Odigos OpenTelemetry
// instrumentation (no bundled layer for its language). Its message is prefixed with
// baseapi.ErrPrefixUnsupportedRuntime so the workload report classifies it as an Unsupported state.
var ErrUnsupportedRuntime = errors.New(baseapi.ErrPrefixUnsupportedRuntime)

// ErrUnsupportedInstrumentation is returned by instrument when a function's runtime is supported but the
// function itself cannot be safely instrumented (e.g. the AWS Lambda Web Adapter, or a non-standard Java
// handler the OTel layer cannot wrap). Prefixed with baseapi.ErrPrefixUnsupported so the workload report
// classifies it as an Unsupported state rather than a retryable failure.
var ErrUnsupportedInstrumentation = errors.New(baseapi.ErrPrefixUnsupported)

// ErrMaxLayersReached is recorded as the workload's instrumentation error when a function already carries
// the maximum number of layers AWS allows, leaving no room to attach the Odigos instrumentation layer. It
// is intentionally NOT prefixed as unsupported: the workload report classifies it as an Error state (the
// operator can free a layer slot to resolve it), which is the reported behavior asked for.
var ErrMaxLayersReached = errors.New("max amount of layers was reached already")

// errLambdaSettling is internal control flow: Lambda accepted a previous configuration change, but its
// lifecycle has not settled yet. Reconcile must defer both route-state publication and another AWS update
// until the next pass; it is not an instrumentation failure.
var errLambdaSettling = errors.New("lambda configuration is still settling")

const lambdaRoutingErrorPrefix = "routing: "

var log = connlog.Subsystem("aws-instrumentor")

// maxConfigUpdateAttempts bounds read-modify-write retries when AWS reports a revision mismatch
// (another writer changed the function between our Get and Update).
const maxConfigUpdateAttempts = 3

// maxLambdaLayers is AWS's hard limit on the number of layers a single function may have. Attaching the
// Odigos instrumentation layer to a function already at this limit would be rejected by AWS with an opaque
// InvalidParameterValueException; instrument detects it up front and records ErrMaxLayersReached instead of
// retrying that rejection every reconcile cycle.
const maxLambdaLayers = 5

// LambdaInstrumentor reconciles Lambda instrumentation against source intent. Each cycle it instruments
// enabled Lambda sources (attach the OTel layer + OTEL_* env so the function-local Collector exports to
// either the applicable regional Collector or the global destination) and rolls back functions that are
// instrumented but no longer have an enabled source. It is idempotent: live AWS config is the source of
// truth, so re-running is a no-op once converged.
type LambdaInstrumentor struct {
	store     *store.Store
	snapshots lambdaSnapshotStore
	cache     *awsclient.Cache
	layers    LayerResolver
	// routes resolves the effective destination for every enabled function in one pass. AWS supplies the
	// regional implementation; nil preserves the connector-wide direct destination for tests and callers
	// that do not expose managed Collectors.
	routes LambdaRouteResolver
	// debug returns the current debug-telemetry toggle (DB-backed live config): when true, instrumented
	// functions get OTEL_LOG_LEVEL=debug. A getter so a config change applies without a restart.
	debug func() bool
}

// NewLambda builds the Lambda instrumentor over the AWS store, config cache, layer resolver and a getter
// for the live debug-telemetry toggle.
// LambdaRouteResolver resolves a complete reconcile pass rather than one function at a time. The pass
// boundary lets the provider apply one bounded regional cutover batch and keeps observer and mutator intent
// identical. The returned map is keyed by function ARN; omitted functions are never mutated.
type LambdaRouteResolver interface {
	ResolveLambdaRoutes(context.Context, []store.LambdaFunction) (map[string]domaindest.Routing, error)
}

func NewLambda(st *store.Store, cache *awsclient.Cache, layers LayerResolver, debug func() bool, routes ...LambdaRouteResolver) *LambdaInstrumentor {
	var routeResolver LambdaRouteResolver
	if len(routes) > 0 {
		routeResolver = routes[0]
	}
	return &LambdaInstrumentor{store: st, snapshots: st, cache: cache, layers: layers, debug: debug, routes: routeResolver}
}

// EnsurePrerequisite implements base.Prerequisite: Lambda instrumentation needs an OTel layer, so this
// ensures one is available (discovered in the account, or our bundled layer published into it) for every
// enabled Lambda function before Reconcile attaches it. Resolved ARNs are cached in the resolver, so
// Reconcile's per-function Resolve is a cache hit. It gates the step only when nothing can be ensured
// (no enabled function got a layer); partial gaps are left to per-function handling in Reconcile.
func (i *LambdaInstrumentor) EnsurePrerequisite(ctx context.Context) error {
	enabled, err := i.enabledLambdaSources(ctx)
	if err != nil {
		return err
	}
	if len(enabled) == 0 {
		return nil // nothing marked for instrumentation — trivially satisfied
	}
	functions, err := i.store.ListLambdaFunctions(ctx)
	if err != nil {
		return err
	}
	_, haveGlobalDestination, err := i.store.OTLPExportTarget(ctx)
	if err != nil {
		return err
	}
	// Without a global terminal, only functions with a proven regional Collector route
	// have somewhere to export. Do not publish layers for unrouteable functions.
	var collectorRoutes map[string]domaindest.Routing
	if !haveGlobalDestination {
		enabledFunctions := make([]store.LambdaFunction, 0, len(functions))
		for _, fn := range functions {
			if _, want := enabled[fn.FunctionARN]; want {
				enabledFunctions = append(enabledFunctions, fn)
			}
		}
		collectorRoutes, err = i.resolveLambdaRoutes(ctx, enabledFunctions)
		if err != nil {
			log.Warn("lambda prerequisite could not resolve regional Collector routes", "error", err)
			return nil
		}
	}
	// A layer key is "in use" when some function is already instrumented with it; an existing Odigos layer
	// for a key with no in-use function is an orphaned leftover (e.g. from a previous connector version or
	// a failed cleanup). Refresh those once per key before resolving, so we attach a freshly-published
	// current layer rather than reusing a stale orphan — without churning layers that are actively in use.
	inUse := instrumentedLayerKeys(functions)
	refresher, canRefresh := i.layers.(orphanRefresher)
	refreshed := map[string]bool{}

	var needed, ensured int
	for _, fn := range functions {
		src, want := enabled[fn.FunctionARN]
		if !want {
			continue
		}
		if !haveGlobalDestination {
			route := collectorRoutes[fn.FunctionARN]
			if !route.Found || route.Outcome == domaindest.RoutingError {
				continue
			}
		}
		needed++
		if key := fnLayerKey(fn); canRefresh && recipeFor(LanguageFromRuntime(fn.Runtime), src.distro).Artifact == "" && key != "" && !inUse[key] && !refreshed[key] {
			refreshed[key] = true
			if err := refresher.RefreshOrphan(ctx, fn.Region, fn.Runtime, firstArch(fn.Architecture)); err != nil {
				log.Warn("orphaned odigos layer refresh failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
			}
		}
		if _, err := resolveLambdaLayer(ctx, i.layers, fn.Region, fn.Runtime, firstArch(fn.Architecture), src.distro, false); err != nil {
			log.Warn("lambda layer prerequisite unresolved", "function", fn.FunctionName, "region", fn.Region, "error", err)
			continue
		}
		ensured++
	}
	if needed > 0 && ensured == 0 {
		// Deliberately NOT an error: the coordinator skips the entire reconcile pass when a prerequisite
		// errors, which would also skip rolling back now-disabled sources — leaving customer functions
		// instrumented. instrument() handles the per-function "no layer" case itself and records the error,
		// so it is safe (and more important) to let the reconcile run and perform rollbacks.
		log.Warn("no OTel layer available for any enabled Lambda function; instrumentation will retry, rollbacks still run", "enabled", needed)
	}
	return nil
}

func (i *LambdaInstrumentor) Reconcile(ctx context.Context) error {
	enabled, err := i.enabledLambdaSources(ctx)
	if err != nil {
		return err
	}
	functions, err := i.store.ListLambdaFunctions(ctx)
	if err != nil {
		return err
	}

	// Resolve all enabled functions together. A regional implementation selects the same-region managed
	// Collector only when its marker, readiness, and network reachability gates pass; otherwise it returns
	// the global target with a workload note. A routing error is recorded on that function and never blocks
	// other functions in the account.
	enabledFunctions := make([]store.LambdaFunction, 0, len(functions))
	for _, fn := range functions {
		if _, want := enabled[fn.FunctionARN]; want {
			enabledFunctions = append(enabledFunctions, fn)
		}
	}
	routes, routeErr := i.resolveLambdaRoutes(ctx, enabledFunctions)
	if routeErr != nil {
		log.Error("resolve lambda OTLP routes failed", "error", routeErr)
	}

	// Accumulate per-function failures and return them joined. The steady-state reconcile loop only logs
	// the returned error and carries on, but the shutdown drain propagates it so a function that failed to
	// roll back is never reported as cleanly drained (see coordinator.Shutdown).
	var errs []error
	for _, fn := range functions {
		_, want := enabled[fn.FunctionARN]
		if want {
			routing, ok := routes[fn.FunctionARN]
			if routeErr != nil || !ok {
				err := routeErr
				if err == nil {
					err = fmt.Errorf("no routing decision was returned for %s", fn.FunctionARN)
				}
				if serr := i.store.SetLambdaRouting(ctx, fn.FunctionARN, fn.RoutingVia, &baseapi.WorkloadRoutingNote{
					Code: "routing-error", Message: err.Error(), Transient: true,
				}); serr != nil {
					log.Warn("set lambda routing error note failed", "function", fn.FunctionName, "error", serr)
				}
				_ = i.store.SetLambdaInstrumentationError(ctx, fn.FunctionARN, lambdaRoutingErrorMessage(err.Error()))
				errs = append(errs, fmt.Errorf("resolve route for %s: %w", fn.FunctionName, err))
				continue
			}
			if routing.Outcome == domaindest.RoutingError || !routing.Found {
				if serr := i.store.SetLambdaRouting(ctx, fn.FunctionARN, fn.RoutingVia, lambdaRoutingNote(routing)); serr != nil {
					log.Warn("set lambda routing error note failed", "function", fn.FunctionName, "error", serr)
				}
				err := errors.New(routing.Note)
				if err.Error() == "" {
					err = errors.New(routing.Describe())
				}
				_ = i.store.SetLambdaInstrumentationError(ctx, fn.FunctionARN, lambdaRoutingErrorMessage(err.Error()))
				errs = append(errs, fmt.Errorf("instrument %s: %w", fn.FunctionName, err))
				continue
			}
			// Publish or clear the fallback explanation immediately, but retain the last applied Via until the
			// configuration is confirmed or AWS accepts the update. A failed/deferred update must not claim a
			// route that is not yet live.
			if serr := i.store.SetLambdaRouting(ctx, fn.FunctionARN, fn.RoutingVia, lambdaSignalRoutingNote(routing, fn.Runtime)); serr != nil {
				log.Warn("set lambda routing note failed", "function", fn.FunctionName, "error", serr)
			}
			if err := i.instrument(ctx, fn, routing.Target, routing.Headers, enabled[fn.FunctionARN]); err != nil {
				if errors.Is(err, errLambdaSettling) {
					continue
				}
				log.Error("instrument lambda failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
				_ = i.store.SetLambdaInstrumentationError(ctx, fn.FunctionARN, err.Error())
				errs = append(errs, fmt.Errorf("instrument %s: %w", fn.FunctionName, err))
				i.recordCutover(ctx, fn, routing, err)
			} else {
				_ = i.store.ClearLambdaInstrumentationError(ctx, fn.FunctionARN)
				if serr := i.store.SetLambdaRouting(ctx, fn.FunctionARN, string(routing.Via), lambdaSignalRoutingNote(routing, fn.Runtime)); serr != nil {
					log.Warn("set lambda routing after converge failed", "function", fn.FunctionName, "error", serr)
				}
				i.recordCutover(ctx, fn, routing, nil)
			}
			continue
		}
		// Not enabled: applied instrumentation or a pending snapshot can need rollback. A pending
		// snapshot may precede a failed write, or an accepted write interrupted before tag promotion.
		// A never-instrumented function
		// needs no per-cycle GetFunctionConfiguration — skipping it avoids an N+1 control-plane GET across
		// the whole account every reconcile, which shares (and can throttle) the customer's own Lambda
		// control-plane quota. rollback does its own GET + ownership gate, so we don't pre-check live state.
		if !fn.Instrumented && !store.LambdaHasPendingInstrumentation(fn) {
			continue
		}
		if err := i.rollback(ctx, fn); err != nil {
			if errors.Is(err, errLambdaSettling) {
				continue
			}
			log.Error("rollback lambda failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
			_ = i.store.SetLambdaInstrumentationError(ctx, fn.FunctionARN, err.Error())
			errs = append(errs, fmt.Errorf("rollback %s: %w", fn.FunctionName, err))
		} else {
			_ = i.store.ClearLambdaInstrumentationError(ctx, fn.FunctionARN)
		}
	}

	// Reclaim our per-language layer once its last Lambda in a region is uninstrumented (the rollbacks
	// above already detached it), so deleting the now-unused layer version is safe.
	if gc, ok := i.layers.(layerGarbageCollector); ok {
		if err := gc.GarbageCollect(ctx, eligibleLayerKeys(enabled, functions)); err != nil {
			log.Error("lambda layer garbage-collect failed", "error", err)
		}
	}
	return errors.Join(errs...)
}

// layerGarbageCollector is implemented by resolvers that publish layers into the account and can delete
// them when unused (ManagedLayerResolver). Other resolvers (e.g. config-driven public ARNs) don't. The
// eligible set is keyed by layer identity "region/lang/arch" so a language's layer is reclaimed as soon as
// its last Lambda is uninstrumented, independent of other languages in the same region.
type layerGarbageCollector interface {
	GarbageCollect(ctx context.Context, eligibleKeys map[string]bool) error
}

// orphanRefresher is implemented by resolvers that can delete an orphaned (no longer in use) Odigos-owned
// layer so the next Resolve republishes the current bundled version. Called when instrumentation of a
// region/lang/arch starts fresh (no function instrumented yet) so a stale leftover is replaced rather than
// reused. Resolvers that don't publish layers don't implement it.
type orphanRefresher interface {
	RefreshOrphan(ctx context.Context, region, runtime, arch string) error
}

// fnLayerKey is the per-language, per-arch identity of the Odigos layer a function uses. Empty for an
// unsupported runtime (which gets no layer).
func fnLayerKey(fn store.LambdaFunction) string {
	lang := LanguageFromRuntime(fn.Runtime)
	if lang == "" {
		return ""
	}
	return layerKey(fn.Region, lang, firstArch(fn.Architecture))
}

// eligibleLayerKeys returns the layer keys (region/lang/arch) that have at least one discovered Lambda but
// no enabled source and no instrumented function — the shared per-language layer is unused there and can
// be reclaimed. Granularity is per language+arch so one language's layer is freed even while another
// language is still instrumented in the same region.
func eligibleLayerKeys(enabled map[string]enabledLambdaSource, functions []store.LambdaFunction) map[string]bool {
	all, used := map[string]bool{}, map[string]bool{}
	for _, fn := range functions {
		language := LanguageFromRuntime(fn.Runtime)
		if language == "" {
			continue
		}
		arch := firstArch(fn.Architecture)
		families := []string{language}
		if language == "java" || language == "python" || language == "nodejs" {
			families = append(families, "adot-"+language, "adot-collector")
		}
		for _, family := range families {
			all[layerKey(fn.Region, family, arch)] = true
		}
		if src, want := enabled[fn.FunctionARN]; want {
			family := lambdaArtifact(fn.Runtime, src.distro)
			used[layerKey(fn.Region, family, arch)] = true
			if family != language {
				used[layerKey(fn.Region, "adot-collector", arch)] = true
			}
		}
		if fn.Instrumented {
			// Inventory can lag a successful update: retain the upstream family too until
			// discovery has recorded the exact SDK and collector layers now in use.
			used[layerKey(fn.Region, language, arch)] = true
			for _, arn := range fn.Layers {
				name, _, ok := store.ParseLayerVersionARN(arn)
				for _, family := range families {
					if ok && name == publishedLayerName(family, arch) {
						used[layerKey(fn.Region, family, arch)] = true
					}
				}
			}
		}
	}
	out := map[string]bool{}
	for key := range all {
		if !used[key] {
			out[key] = true
		}
	}
	return out
}

// instrumentedLayerKeys returns the set of layer keys with at least one currently-instrumented function —
// i.e. the layer is in use and must not be treated as an orphan.
func instrumentedLayerKeys(functions []store.LambdaFunction) map[string]bool {
	out := map[string]bool{}
	for _, fn := range functions {
		if fn.Instrumented {
			if k := fnLayerKey(fn); k != "" {
				out[k] = true
			}
		}
	}
	return out
}

// enabledLambdaSources returns enabled Lambda function ARNs and their OTel service.name values
// (source override when set, otherwise the source name / discovered workload identity).
func (i *LambdaInstrumentor) enabledLambdaSources(ctx context.Context) (map[string]enabledLambdaSource, error) {
	sources, err := i.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]enabledLambdaSource)
	for _, s := range sources {
		if s.Kind == store.KindLambda && s.Enabled && s.ExternalResourceID != "" {
			enabled[s.ExternalResourceID] = enabledLambdaSource{
				serviceName: s.ReportedServiceName(),
				distro:      s.Config.DistroOverride.OtelDistroName,
			}
		}
	}
	return enabled, nil
}

func (i *LambdaInstrumentor) resolveLambdaRoutes(ctx context.Context, functions []store.LambdaFunction) (map[string]domaindest.Routing, error) {
	if i.routes != nil {
		return i.routes.ResolveLambdaRoutes(ctx, functions)
	}
	routing, err := i.store.OTLPRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]domaindest.Routing, len(functions))
	for _, fn := range functions {
		out[fn.FunctionARN] = routing
	}
	return out, nil
}

func lambdaRoutingErrorMessage(message string) string {
	return lambdaRoutingErrorPrefix + message
}

func isLambdaRoutingError(message string) bool {
	return strings.HasPrefix(message, lambdaRoutingErrorPrefix)
}

func lambdaRoutingNote(routing domaindest.Routing) *baseapi.WorkloadRoutingNote {
	if routing.Note == "" {
		return &baseapi.WorkloadRoutingNote{Code: "routing-error", Message: routing.Describe(), Transient: true}
	}
	return &baseapi.WorkloadRoutingNote{Code: string(routing.Reason), Message: routing.Note, Transient: true}
}

func isLambdaRoutingFailureNote(note *baseapi.WorkloadRoutingNote) bool {
	if note == nil {
		return false
	}
	return note.Code == "routing-error" || note.Code == string(domaindest.ReasonNoDestination)
}

// enabledLambdaSource is the per-source intent the instrumentor and the observer both read. It mirrors
// enabledFargateSource: one struct rather than parallel maps, so a new piece of intent cannot be threaded
// into one caller and forgotten in the other.
type enabledLambdaSource struct {
	serviceName string // reported OTel service.name
	// distro is the source's distro override, reused here to select WHICH Java wrapper the layer should
	// install (see javaHandlerWrapperFor). It rides on the existing override rather than a new field
	// because the source model is generated upstream; ignored for every other language, whose layer ships
	// exactly one wrapper.
	distro string
}

// exporterFor returns the OTel SDK exporter env value for a signal: "otlp" to export it to the in-Lambda
// collector, or "none" to disable it so it is never sent to a destination that does not accept it. The
// per-signal support decision itself is connector-wide — resolved by the base from the destination's
// signals (see base/domain/destination.ResolveOTLPTarget, exposed via store.OTLPExportTarget).
func exporterFor(supported bool) string {
	return domaindest.ExporterFor(supported)
}

func (i *LambdaInstrumentor) instrument(ctx context.Context, fn store.LambdaFunction, dest domaindest.OTLPTarget, destHeaders map[string]string, src enabledLambdaSource) error {
	reportedServiceName := src.serviceName
	if reportedServiceName == "" {
		reportedServiceName = fn.FunctionName
	}
	// Language matching first: a runtime Odigos has no layer for (dotnet, go, provided/custom, …) is
	// reported as an unsupported-runtime error rather than attempting — and failing — to resolve a layer.
	lang := LanguageFromRuntime(fn.Runtime)
	if lang == "" {
		return fmt.Errorf("%w %q: Odigos has no OpenTelemetry instrumentation for this Lambda language", ErrUnsupportedRuntime, fn.Runtime)
	}
	activation := ActivationHandler
	if usesLambdaWebAdapter(fn.Layers) {
		activation = ActivationWebServer
	}
	if err := ValidateLambdaDistroRuntime(fn.Runtime, activation, src.distro); err != nil {
		return fmt.Errorf("%w: %v", ErrUnsupportedInstrumentation, err)
	}
	if err := validateLambdaApplicationSignalsTarget(lang, src.distro, dest); err != nil {
		return err
	}
	debug := i.debug()
	// Render the collector's configuration before touching AWS. A destination the collector cannot be
	// configured for — no usable endpoint, or no accepted signal — is an instrumentation error reported
	// against the workload, NOT something to discover after the function has been mutated: the previous
	// behavior wrote an empty endpoint into the env and produced a collector that exited on startup while
	// the function was still reported as conformant.
	collectorCfg, err := renderCollectorConfig(dest, destHeaders)
	if err != nil {
		return err
	}
	// Live progress phases: only written while the function is not yet instrumented, so a steady-state
	// instrumented function does not flip phases every reconcile (the as-desired path below records the
	// terminal Instrumented phase via SetLambdaInstrumented).
	if !fn.Instrumented {
		i.setPhase(ctx, fn, store.PhaseVerifying)
	}
	layerARN, err := resolveLambdaLayer(ctx, i.layers, fn.Region, fn.Runtime, firstArch(fn.Architecture), src.distro, false)
	if err != nil {
		return err
	}
	collectorLayerARN, err := resolveLambdaCollector(ctx, i.layers, fn, src.distro, false)
	if err != nil {
		return err
	}
	if !fn.Instrumented {
		i.setPhase(ctx, fn, store.PhaseLayerReady)
	}
	client, err := i.lambdaClient(ctx, fn.Region)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < maxConfigUpdateAttempts; attempt++ {
		cur, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(fn.FunctionARN)})
		if err != nil {
			return err
		}
		// Lambda applies config changes asynchronously; a second UpdateFunctionConfiguration while one is
		// still settling returns ResourceConflictException (409). Defer to the next reconcile cycle.
		if updateInProgress(cur) {
			log.Debug("lambda update in progress; deferring instrumentation", "function", fn.FunctionName, "region", fn.Region)
			return errLambdaSettling
		}

		env := currentEnv(cur.Environment)
		layers := layerARNs(cur.Layers)
		tags, err := i.readLambdaTags(ctx, client, fn, env, layers)
		if err != nil {
			return err
		}

		if raw := env[store.EnvOdigosManaged]; raw != "" {
			m, ok := parseManifest(raw)
			if !ok {
				return fmt.Errorf("invalid Lambda ownership marker; refusing to modify the function")
			}
			if m.SnapshotID != "" {
				if _, err := i.loadManifest(ctx, fn.FunctionARN, m); err != nil {
					return err
				}
			}
		}

		// Two instrumentation modes:
		//   web-server: the function runs behind the AWS Lambda Web Adapter (a web server, not a Lambda
		//               handler). Instrument the server via the language agent loader, leaving the adapter's
		//               exec wrapper intact; the in-Lambda collector extension still exports telemetry.
		//   handler:    a standard RequestHandler function — attach the OTel handler exec wrapper.
		webServer := usesLambdaWebAdapter(layers)
		if webServer {
			if _, _, _, ok := webServerLoader(lang); !ok {
				return fmt.Errorf("%w: AWS Lambda Web Adapter instrumentation is not supported for %s", ErrUnsupportedInstrumentation, lang)
			}
		} else if reason := unsupportedHandlerReason(fn.Runtime, aws.ToString(cur.Handler), env[store.EnvExecWrapper]); reason != "" {
			// A non-web-adapter function the OTel layer cannot wrap, or one carrying a foreign exec wrapper we
			// would clobber (Datadog, a secrets/bootstrap wrapper) — never mutate it (would break it or silently
			// disable the customer's tooling). Roll back any instrumentation a prior version applied, then
			// report it unsupported.
			if store.OdigosInstrumented(env) {
				log.Info("rolling back un-instrumentable lambda to restore it", "function", fn.FunctionName, "region", fn.Region, "reason", reason)
				if err := i.rollback(ctx, fn); err != nil {
					return fmt.Errorf("restore un-instrumentable lambda %s: %w", fn.FunctionName, err)
				}
			}
			return fmt.Errorf("%w: %s", ErrUnsupportedInstrumentation, reason)
		}

		// Recheck AWS prerequisites even when the function's layers and environment
		// already match, so a removed policy attachment is repaired automatically.
		if recipeFor(lang, src.distro).ApplicationSignals {
			if err := i.ensureApplicationSignals(ctx, fn.Region, aws.ToString(cur.Role)); err != nil {
				return err
			}
		}
		// Already instrumented and settled: mark converged without churning a version.
		if (tags[store.TagLambdaManaged] != "" || tags[store.TagLambdaPending] != "") && env[store.EnvOdigosManaged] != "" && lambdaConfigFromEnvironment(env) != "" && recordedCollectorLayer(env) == collectorLayerARN && i.instrumentedAsDesired(webServer, fn, env, layers, layerARN, dest, debug, reportedServiceName, collectorCfg, src.distro) {
			if reference, ok := parseManifest(env[store.EnvOdigosManaged]); ok && reference.SnapshotID != "" && (tags[store.TagLambdaPending] != "" || tags[store.TagLambdaSDK] != reference.Layer || tags[store.TagLambdaCollector] != reference.CollectorLayer) {
				if err := i.promoteLambdaTags(ctx, client, fn.FunctionARN, reference); err != nil {
					return err
				}
			}
			if err := i.store.SetLambdaInstrumented(ctx, fn.FunctionARN, true); err != nil {
				log.Warn("set lambda instrumented flag failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
			}
			i.clearPhase(ctx, fn)
			return validateLambdaAliasCoverage(ctx, client, fn.FunctionARN, env, layers)
		}

		// Determine the final layer set up front and refuse BEFORE mutating anything if attaching our layer
		// would exceed AWS's hard per-function layer limit — otherwise AWS rejects the UpdateFunctionConfiguration
		// with an opaque InvalidParameterValueException we would retry forever. Reported as an Error workload
		// state (see ErrMaxLayersReached) so the operator knows to free a layer slot.
		normalized := normalizeInstrumentationLayers(layers, layerARN)
		if collectorLayerARN != "" {
			normalized = append(normalized, collectorLayerARN)
		}
		if len(normalized) > maxLambdaLayers {
			return fmt.Errorf("%w: attaching the Odigos instrumentation layer would give %q %d layers, exceeding the AWS maximum of %d (remove an unused layer to enable instrumentation)",
				ErrMaxLayersReached, fn.FunctionName, len(normalized), maxLambdaLayers)
		}

		// Snapshot what we are about to change BEFORE mutating env, so rollback can restore the function to its
		// pre-Odigos state. A re-instrument reuses the snapshot recorded in the marker so it is never clobbered.
		snapshotEnv := cloneLambdaEnv(env)
		if m, ok := parseManifest(env[store.EnvOdigosManaged]); ok && m.SnapshotID != "" {
			hydrated, err := i.loadManifest(ctx, fn.FunctionARN, m)
			if err != nil {
				return err
			}
			snapshotEnv[store.EnvOdigosManaged], err = marshalManifest(hydrated)
			if err != nil {
				return err
			}
		}
		manifest := buildManifest(snapshotEnv, layerARN)
		if manifest.PrevTags == nil {
			manifest.PrevTags = snapshotPriorTags(tags)
			preserveInitialLambdaWrapper(&manifest, env)
			preserveIaCOriginal(&manifest, env, tags, layers)
		}
		manifest.CollectorLayer = collectorLayerARN
		// Capture any customer edit to a shared, instrumentor-owned env key made AFTER the snapshot was first
		// taken, so a value the customer set post-instrument is restored (not deleted) on rollback.
		reconcileCustomerEdits(&manifest, env, desiredLambdaSharedEnv(i, webServer, fn.Runtime, src.distro, reportedServiceName, dest, debug, collectorCfg))
		if lambdaConfigFromEnvironment(env) != "" {
			// Compact instrumentation owns only its configuration and exec wrapper. Refresh all
			// other saved values from AWS so customer edits remain untouched during reconciliation.
			for _, key := range odigosEnvKeys {
				if key != store.EnvExecWrapper && key != EnvLambdaConfig {
					manifest.PrevEnv[key] = priorValue(key, env)
				}
			}
		}

		if webServer {
			applyWebServerEnv(env, manifest, lang, reportedServiceName, dest, collectorCfg)
		} else {
			applyHandlerEnv(env, fn.Runtime, src.distro, reportedServiceName, dest, debug, collectorCfg)
		}
		if err := compactLambdaEnvironment(env, manifest, webServer, fn.Runtime, src.distro, reportedServiceName, dest, destHeaders, debug); err != nil {
			return err
		}
		layers = normalized

		// Stamp the ownership marker + change snapshot last, over the now-mutated env.
		manifest.Config = nil
		delete(env, store.EnvOdigosManaged)
		delete(env, store.EnvOdigosIaC)
		manifest.AppliedFingerprint = lambdaAppliedFingerprint(env, layers)
		marker, err := i.persistManifest(ctx, fn.FunctionARN, manifest)
		if err != nil {
			return err
		}
		reference, ok := parseManifest(marker)
		if !ok {
			return fmt.Errorf("invalid persisted Lambda snapshot reference")
		}
		if err := validateLambdaEnvSize(env); err != nil {
			return err
		}

		if err := validateLambdaTagCapacity(tags, collectorLayerARN != ""); err != nil {
			return err
		}
		i.setPhase(ctx, fn, store.PhaseInstrumenting)
		applied, err := i.updateTaggedLambdaConfiguration(ctx, client, lambdaConfigUpdateInput(fn.FunctionARN, layers, env, cur.RevisionId), tags, reference.SnapshotID)
		if err != nil {
			if revisionConflict(err) && attempt+1 < maxConfigUpdateAttempts {
				log.Debug("lambda config changed since read; retrying instrumentation", "function", fn.FunctionName, "region", fn.Region, "attempt", attempt+1)
				continue
			}
			if revisionConflict(err) {
				log.Debug("lambda config changed since read; deferring instrumentation to next reconcile", "function", fn.FunctionName, "region", fn.Region)
				return errLambdaSettling
			}
			return err
		}
		if !applied {
			// TagResource changes RevisionId. Re-read the entire configuration and rebuild the
			// merge, rather than attaching a fresh revision to potentially stale customer values.
			continue
		}
		log.Info("instrumented lambda; waiting for configuration to settle", "function", fn.FunctionName, "region", fn.Region, "layer", layerARN, "mode", instrumentMode(webServer))
		// Promotion waits until the next settled read confirms AWS applied this configuration.
		// Write the live flag through immediately so a reconcile before the next discovery cycle sees the
		// function as instrumented (keeps rollback-on-disable prompt). The phase is left at "Instrumenting":
		// the config change is still settling asynchronously in Lambda, and leaving the phase set makes that
		// observable in the WorkloadReport — the next reconcile that finds it converged clears it.
		if err := i.store.SetLambdaInstrumented(ctx, fn.FunctionARN, true); err != nil {
			log.Warn("set lambda instrumented flag failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
		}
		return nil
	}
	return errLambdaSettling
}

// setPhase records an in-flight instrumentation phase for live WorkloadReport progress. Best-effort: a
// failed phase write only delays a status update and never affects instrumentation, so it is logged and
// ignored. The terminal Instrumented phase is set by SetLambdaInstrumented, not here.
func (i *LambdaInstrumentor) setPhase(ctx context.Context, fn store.LambdaFunction, phase string) {
	if err := i.store.SetLambdaInstrumentationPhase(ctx, fn.FunctionARN, phase); err != nil {
		log.Warn("set lambda instrumentation phase failed", "function", fn.FunctionName, "region", fn.Region, "phase", phase, "error", err)
	}
}

// clearPhase drops the in-flight phase once a function has fully converged. Best-effort like setPhase.
func (i *LambdaInstrumentor) clearPhase(ctx context.Context, fn store.LambdaFunction) {
	if err := i.store.ClearLambdaInstrumentationPhase(ctx, fn.FunctionARN); err != nil {
		log.Warn("clear lambda instrumentation phase failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
	}
}

func instrumentMode(webServer bool) string {
	if webServer {
		return "web-server"
	}
	return "handler"
}

// applyHandlerEnv sets the env for a standard RequestHandler function: the OTel handler exec wrapper +
// collector export vars. The auto-instrumentation exports to the in-Lambda collector on localhost (never
// blocking the invocation on the remote destination), which forwards to the destination.
func applyHandlerEnv(env map[string]string, runtime, distro, serviceName string, dest domaindest.OTLPTarget, debug bool, collectorCfg string) {
	env[store.EnvExecWrapper] = execWrapperForRuntime(runtime, distro)
	env[EnvServiceName] = serviceName
	applyCollectorExport(env, dest, collectorCfg)
	applySignalActivation(env, dest, LanguageFromRuntime(runtime))
	applyLambdaDistributionEnv(env, LanguageFromRuntime(runtime), distro)
	if debug {
		env[EnvOtelLogLevel] = otelLogLevelDebug
	} else {
		delete(env, EnvOtelLogLevel)
	}
	delete(env, envLegacyOTLPEndpoint)
	delete(env, envLegacyOTLPHeaders)
}

// applyWebServerEnv instruments a web-adapter function's web server via the language agent loader WITHOUT
// touching AWS_LAMBDA_EXEC_WRAPPER (the adapter owns it). It also restores the exec wrapper to the
// function's pre-Odigos value from the snapshot, undoing an OTel handler wrapper a prior mis-instrumentation
// may have set (which is what broke these functions). The agent/SDK exports to the in-Lambda collector on
// localhost by default; the collector forwards to the destination via the ODIGOS_OTLP_* vars.
func applyWebServerEnv(env map[string]string, manifest managedManifest, lang, serviceName string, dest domaindest.OTLPTarget, collectorCfg string) {
	if prev, ok := manifest.PrevEnv[store.EnvExecWrapper]; ok {
		if prev == nil {
			delete(env, store.EnvExecWrapper)
		} else {
			env[store.EnvExecWrapper] = *prev
		}
	}
	if key, value, sep, ok := webServerLoader(lang); ok {
		// Rebuild from the ORIGINAL (pre-Odigos) value + our value, not the current env value, so a
		// re-instrument (e.g. after we change the loader value) never accumulates our previous additions.
		env[key] = addLoaderValue(priorEnv(manifest, key), value, sep)
	}
	for k, v := range webServerExtraEnv(lang) {
		env[k] = v
	}
	env[EnvServiceName] = serviceName
	applyCollectorExport(env, dest, collectorCfg)
	applySignalActivation(env, dest, lang)
	applyLambdaDistributionEnv(env, lang, "")
	delete(env, envLegacyOTLPEndpoint)
	delete(env, envLegacyOTLPHeaders)
	delete(env, EnvOtelLogLevel)
}

// applyCollectorExport configures the in-Lambda collector and tells the SDK which signals to produce.
//
// The collector's entire configuration is rendered per function and passed inline through the layer's own
// OPENTELEMETRY_COLLECTOR_CONFIG_URI (see collector_config.go), which is what replaced the pair of
// odigos-namespaced ODIGOS_OTLP_* variables. Those are deleted here so a function instrumented by an earlier
// version converges instead of carrying two competing descriptions of the same destination.
func applyCollectorExport(env map[string]string, dest domaindest.OTLPTarget, collectorCfg string) {
	env[EnvCollectorConfigURI] = collectorCfg
	applySignalExporters(env, dest)
	for _, key := range domaindest.SignalTransportOverrides() {
		delete(env, key)
	}
	delete(env, EnvCollectorEndpoint)
	delete(env, EnvCollectorInsecure)
}

// applySignalExporters applies the base connector's explicit signal policy.
func applySignalExporters(env map[string]string, dest domaindest.OTLPTarget) {
	for key, value := range dest.SignalActivation("") {
		env[key] = value
	}
}

// collectorExportAsDesired reports whether the rendered collector config and the per-signal exporter
// overrides already match the destination, so a change — the destination moving, gaining or losing a signal,
// or gaining a credential — forces a converging re-instrument.
//
// A leftover ODIGOS_OTLP_* pair counts as not-as-desired on purpose: it is how a function instrumented before
// the inline config existed gets migrated exactly once, rather than being left carrying stale variables that
// nothing reads.
func collectorExportAsDesired(env map[string]string, dest domaindest.OTLPTarget, collectorCfg string) bool {
	if _, ok := env[EnvCollectorEndpoint]; ok {
		return false
	}
	if _, ok := env[EnvCollectorInsecure]; ok {
		return false
	}
	return env[EnvCollectorConfigURI] == collectorCfg &&
		env[EnvTracesExporter] == exporterFor(dest.Traces) &&
		env[EnvMetricsExporter] == exporterFor(dest.Metrics) &&
		env[EnvLogsExporter] == exporterFor(dest.Logs)
}

// Local names for the components the diffs below report. They are ALIASES of the base's vocabulary
// (baseapi.Component*), never their own strings: the base is what renders a component into the prose a customer
// reads, so a component defined here would be one the report could only print as a raw identifier. Aliasing
// keeps the call sites short while making an unlabelled component impossible — see api.InstrumentationComponents.
const (
	componentOwnershipMarker      = baseapi.ComponentOwnershipMarker
	componentInstrumentationLayer = baseapi.ComponentInstrumentationLayer
	componentLayerVersionConflict = baseapi.ComponentLayerVersionConflict
	componentServiceName          = baseapi.ComponentServiceName
	componentCollectorExport      = baseapi.ComponentCollectorExport
	componentLegacyExportVars     = baseapi.ComponentLegacyExportVars
	componentAgentLoader          = baseapi.ComponentAgentLoader
	componentAgentLoaderConfig    = baseapi.ComponentAgentLoaderConfig
	componentExecWrapper          = baseapi.ComponentExecWrapper
	componentLogLevel             = baseapi.ComponentLogLevel
)

// instrumentationDiff returns the components of the desired instrumentation that are absent or stale on the
// function — empty when it is instrumented exactly as desired.
//
// It replaces what used to be a bare bool, and the reason is the difference between the two modes. As a bool
// its only consumer was "may I skip this UpdateFunctionConfiguration"; the answer's DETAIL was worthless
// because the instrumentor was about to converge whatever was wrong anyway. In manual (IaC) mode nothing
// converges it: the customer has to go edit their own deployment definition, and the component list is the
// only thing that tells them where to look. Same predicate, same call site for the mutating path
// (instrumentedAsDesired below is now len(diff) == 0), one new consumer that needs the facts rather than the
// verdict.
//
// It is a pure function of (observed env, observed layers, desired inputs) so the observer can call it on a
// live read without touching the instrumentor's state or making a single mutating call.
func instrumentationDiff(webServer bool, fn store.LambdaFunction, env map[string]string, layers []string, layerARN string, dest domaindest.OTLPTarget, debug bool, reportedServiceName, collectorCfg, distro string) []string {
	// No marker of either kind: nothing here is attributable to Odigos, so there is nothing to compare
	// against. Reporting the individual components as missing would describe an uninstrumented function as a
	// broken one — including a customer's own unrelated OpenTelemetry setup.
	//
	// Both markers qualify because both mean "Odigos instrumentation was applied here", which is the only
	// question this gate asks. The mutating path reaches this function through instrumentedAsDesired, where a
	// diff of zero means "skip the update" — never "this function is mine to roll back", which ownership()
	// decides on the ownership marker alone.
	if !store.OdigosInstrumented(env) {
		return []string{componentOwnershipMarker}
	}

	if raw := lambdaConfigFromEnvironment(env); raw != "" {
		c, err := parseLambdaConfig(raw)
		expectedService := reportedServiceName
		if expectedService == "" {
			expectedService = fn.FunctionName
		}
		if err != nil || c.Service != expectedService || c.Signals != signalMask(dest) || c.Debug != (debug && !webServer) || c.ApplicationSignals != recipeFor(LanguageFromRuntime(fn.Runtime), distro).ApplicationSignals || c.FunctionLogs != (dest.Logs && !webServer && lambdaFunctionLogs(LanguageFromRuntime(fn.Runtime), execWrapperForRuntime(fn.Runtime, distro))) || (c.WebWrapper != nil) != webServer {
			return []string{componentCollectorExport}
		}
		cfg, err := renderCollectorConfig(c.target(), c.Headers)
		if err != nil || cfg != collectorCfg {
			return []string{componentCollectorExport}
		}
		expectedWrapper := execWrapperForRuntime(fn.Runtime, distro)
		if webServer {
			expectedWrapper = lambdaWebServerWrapper
		}
		if env[store.EnvExecWrapper] != expectedWrapper {
			return []string{componentExecWrapper}
		}
		virtual, err := expandedLambdaEnvironment(env, fn.Runtime, distro)
		if err != nil {
			return []string{componentCollectorExport}
		}
		return instrumentationDiff(webServer, fn, virtual, layers, layerARN, dest, debug, reportedServiceName, collectorCfg, distro)
	}
	var diff []string
	if reportedServiceName == "" {
		reportedServiceName = fn.FunctionName
	}
	if env[EnvServiceName] != reportedServiceName {
		diff = append(diff, componentServiceName)
	}
	if !collectorExportAsDesired(env, dest, collectorCfg) || !lambdaSignalConfigurationMatches(env, dest, LanguageFromRuntime(fn.Runtime), distro) {
		diff = append(diff, componentCollectorExport)
	}
	// An empty desired ARN means the layer could not be resolved read-only. The caller decides what that
	// means (the observer records it as an observation limitation); claiming the layer is missing on the
	// strength of not having looked it up would be manufactured drift.
	if layerARN != "" && (!contains(layers, layerARN) || !layerFamilyMatches(layerARN, fn.Runtime, firstArch(fn.Architecture), distro)) {
		diff = append(diff, componentInstrumentationLayer)
	}
	if recipeFor(LanguageFromRuntime(fn.Runtime), distro).Artifact != "" {
		if collector := recordedCollectorLayer(env); collector != "" {
			if !contains(layers, collector) || !isCollectorLayer(collector, firstArch(fn.Architecture)) {
				diff = append(diff, componentInstrumentationLayer)
			}
		} else if !hasCollectorLayer(layers, firstArch(fn.Architecture)) {
			diff = append(diff, componentInstrumentationLayer)
		}
	}
	if hasDuplicateLayerVersions(layers) {
		diff = append(diff, componentLayerVersionConflict)
	}
	if _, ok := env[envLegacyOTLPEndpoint]; ok {
		diff = append(diff, componentLegacyExportVars)
	} else if _, ok := env[envLegacyOTLPHeaders]; ok {
		diff = append(diff, componentLegacyExportVars)
	}
	if webServer {
		lang := LanguageFromRuntime(fn.Runtime)
		if !webServerLoaderApplied(lang, env) {
			diff = append(diff, componentAgentLoader)
		}
		return diff
	}
	if env[store.EnvExecWrapper] != execWrapperForRuntime(fn.Runtime, distro) {
		diff = append(diff, componentExecWrapper)
	}
	desiredLogLevel := ""
	if debug {
		desiredLogLevel = otelLogLevelDebug
	}
	if env[EnvOtelLogLevel] != desiredLogLevel {
		diff = append(diff, componentLogLevel)
	}
	return diff
}

// instrumentedAsDesired reports whether the function already carries the exact instrumentation for its
// mode, so the update can be skipped (avoiding a new function version every reconcile). A missing marker,
// stale collector/service vars, a missing/duplicate layer, leftover legacy vars, or (per mode) a missing
// loader / wrong-or-stale handler wrapper all force a re-instrument to converge.
func (i *LambdaInstrumentor) instrumentedAsDesired(webServer bool, fn store.LambdaFunction, env map[string]string, layers []string, layerARN string, dest domaindest.OTLPTarget, debug bool, reportedServiceName, collectorCfg, distro string) bool {
	return len(instrumentationDiff(webServer, fn, env, layers, layerARN, dest, debug, reportedServiceName, collectorCfg, distro)) == 0
}

func (i *LambdaInstrumentor) rollback(ctx context.Context, fn store.LambdaFunction) error {
	client, err := i.lambdaClient(ctx, fn.Region)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < maxConfigUpdateAttempts; attempt++ {
		cur, err := client.GetFunctionConfiguration(ctx, &lambda.GetFunctionConfigurationInput{FunctionName: aws.String(fn.FunctionARN)})
		if err != nil {
			return err
		}
		if updateInProgress(cur) {
			log.Debug("lambda update in progress; deferring rollback", "function", fn.FunctionName, "region", fn.Region)
			return errLambdaSettling
		}

		env := currentEnv(cur.Environment)
		layers := layerARNs(cur.Layers)
		tags, err := i.readLambdaTags(ctx, client, fn, env, layers)
		if err != nil {
			return err
		}

		// Ownership gate: only roll back instrumentation we can attribute to ourselves. A function we never
		// instrumented (e.g. a customer's own OpenTelemetry/ADOT setup) is left completely untouched — we never
		// strip a layer or env var by name-matching. Still correct a possibly-stale inventory flag.
		if raw := env[store.EnvOdigosManaged]; raw != "" {
			if _, ok := parseManifest(raw); !ok {
				return fmt.Errorf("invalid Lambda ownership marker; refusing rollback")
			}
		}
		manifest, owned := i.ownership(ctx, fn, env)
		if !owned {
			if tags[store.TagLambdaPending] != "" {
				if _, err := client.UntagResource(ctx, &lambda.UntagResourceInput{Resource: aws.String(fn.FunctionARN), TagKeys: []string{store.TagLambdaPending}}); err != nil {
					return err
				}
			}
			// Sync the flag to what is actually on the function rather than to false. A function carrying only
			// the IaC marker IS instrumented — by the customer's own deployment — and is simply not ours to
			// roll back; writing false there would contradict discovery and be flipped straight back on its
			// next pass.
			if err := i.store.SetLambdaInstrumented(ctx, fn.FunctionARN, store.OdigosInstrumented(env)); err != nil {
				log.Warn("set lambda instrumented flag failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
			}
			return nil
		}

		if manifest.SnapshotID != "" {
			manifest, err = i.loadManifest(ctx, fn.FunctionARN, manifest)
			if err != nil {
				return err
			}
		}
		// Restore the function to its pre-Odigos state from the recorded snapshot: delete keys we added (prior
		// value absent) and restore the exact customer values we overwrote.
		restoreLambdaEnvironment(env, manifest)

		// Remove only the exact layer we attached — never an unrelated (customer) layer.
		if manifest.Layer != "" {
			layers = remove(layers, manifest.Layer)
			if manifest.CollectorLayer != "" {
				layers = remove(layers, manifest.CollectorLayer)
			}
		}

		for _, arn := range manifest.PriorLayers {
			if !contains(layers, arn) {
				layers = append(layers, arn)
			}
		}
		if len(layers) > maxLambdaLayers {
			return fmt.Errorf("restoring original Lambda layers exceeds AWS's %d-layer limit", maxLambdaLayers)
		}

		if err := validateLambdaEnvSize(env); err != nil {
			return err
		}
		log.Info("rolling back lambda instrumentation", "function", fn.FunctionName, "region", fn.Region)
		if _, err = client.UpdateFunctionConfiguration(ctx, lambdaConfigUpdateInput(fn.FunctionARN, layers, env, cur.RevisionId)); err != nil {
			if revisionConflict(err) && attempt+1 < maxConfigUpdateAttempts {
				log.Debug("lambda config changed since read; retrying rollback", "function", fn.FunctionName, "region", fn.Region, "attempt", attempt+1)
				continue
			}
			if revisionConflict(err) {
				log.Debug("lambda config changed since read; deferring rollback to next reconcile", "function", fn.FunctionName, "region", fn.Region)
				return errLambdaSettling
			}
			return err
		}
		if err := restoreLambdaTags(ctx, client, fn.FunctionARN, manifest.PrevTags); err != nil {
			return fmt.Errorf("restore Lambda ownership tags: %w", err)
		}
		// Write the live flag through immediately (mirror of instrument): the next reconcile sees the
		// function as uninstrumented without waiting for the next discovery cycle.
		if err := i.store.SetLambdaInstrumented(ctx, fn.FunctionARN, manifest.PrevTags[store.TagLambdaIaC] != nil); err != nil {
			log.Warn("set lambda instrumented flag failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
		}
		if err := i.store.SetLambdaRouting(ctx, fn.FunctionARN, "", nil); err != nil {
			log.Warn("clear lambda routing state failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
		}
		return nil
	}
	return nil
}

// updateInProgress reports whether the function has a configuration change still settling. Lambda
// applies updates asynchronously (State=Pending / LastUpdateStatus=InProgress); mutating again before it
// finishes returns ResourceConflictException (409), so the connector waits for the next reconcile cycle.
func updateInProgress(cur *lambda.GetFunctionConfigurationOutput) bool {
	return lifecycleSettling(cur.State, cur.LastUpdateStatus)
}

// lifecycleSettling is the same question for either shape AWS returns the lifecycle in — the instrumentor reads
// it from GetFunctionConfiguration, the observer from ListFunctions, and they are distinct SDK types over
// identical fields.
//
// The two callers act on it for different reasons that happen to coincide: the instrumentor must not mutate a
// function mid-settle (AWS rejects it with a 409), and the observer must not JUDGE one mid-settle, because a
// change still landing is indistinguishable from a change that landed half-way.
func lifecycleSettling(state lambdatypes.State, lastUpdate lambdatypes.LastUpdateStatus) bool {
	return state == lambdatypes.StatePending || lastUpdate == lambdatypes.LastUpdateStatusInProgress
}

// revisionConflict reports whether AWS rejected an UpdateFunctionConfiguration because the function's
// RevisionId changed since our read (another deploy landed in the window). Passing RevisionId on update
// turns a silent lost-update into this error so we re-read and merge on top of the customer's changes.
func revisionConflict(err error) bool {
	var e *lambdatypes.PreconditionFailedException
	return errors.As(err, &e)
}

func (i *LambdaInstrumentor) lambdaClient(ctx context.Context, region string) (*lambda.Client, error) {
	cfg, err := i.cache.Config(ctx, region)
	if err != nil {
		return nil, err
	}
	return lambda.NewFromConfig(cfg), nil
}

// lambdaConfigUpdateInput centralizes the only Lambda configuration mutation. VpcConfig is deliberately
// absent: routing consumes the function's existing VpcConfig for reachability, but never attaches a function
// to a VPC or changes its subnets/security groups as a side effect of instrumentation.
func lambdaConfigUpdateInput(functionARN string, layers []string, env map[string]string, revisionID *string) *lambda.UpdateFunctionConfigurationInput {
	return &lambda.UpdateFunctionConfigurationInput{
		FunctionName: aws.String(functionARN),
		Layers:       layers,
		Environment:  &lambdatypes.Environment{Variables: env},
		RevisionId:   revisionID,
	}
}

func currentEnv(env *lambdatypes.EnvironmentResponse) map[string]string {
	out := map[string]string{}
	if env != nil {
		for k, v := range env.Variables {
			out[k] = v
		}
	}
	return out
}

func layerARNs(layers []lambdatypes.Layer) []string {
	out := make([]string, 0, len(layers))
	for _, l := range layers {
		if a := aws.ToString(l.Arn); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// firstArch returns the first architecture from a possibly comma-joined value (defaults to x86_64).
func firstArch(arch string) string {
	if arch == "" {
		return "x86_64"
	}
	if i := strings.IndexByte(arch, ','); i >= 0 {
		return arch[:i]
	}
	return arch
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func remove(xs []string, x string) []string {
	out := xs[:0]
	for _, v := range xs {
		if v != x {
			out = append(out, v)
		}
	}
	return out
}

// managedManifest is the private rollback payload (legacy v1 markers stored it inline).
// A v2 Lambda marker stores only SnapshotID and layer ownership. Layer is the exact
// layer-version ARN we attached (empty if none). PrevEnv maps every env key we set or deleted to the
// value it had before we touched it: a nil entry means the key was absent (rollback deletes it), a
// non-nil entry is the customer's prior value (rollback restores it verbatim). This is the persisted
// "previous config" that lets rollback be non-destructive.
type managedManifest struct {
	Version            int                    `json:"v"`
	SnapshotID         string                 `json:"snapshot,omitempty"`
	Config             *lambdaBootstrapConfig `json:"c,omitempty"`
	AppliedFingerprint string                 `json:"appliedFingerprint,omitempty"`
	PrevTags           map[string]*string     `json:"prevTags,omitempty"`
	PriorLayers        []string               `json:"priorLayers,omitempty"`
	Layer              string                 `json:"layer,omitempty"`
	CollectorLayer     string                 `json:"collectorLayer,omitempty"`
	PrevEnv            map[string]*string     `json:"prevEnv,omitempty"`
	SignalEnv          map[string]string      `json:"signalEnv,omitempty"`
}

const managedManifestVersion = 1

// odigosEnvKeys are the env vars the instrumentor sets, appends to, or deletes (across both handler and
// web-server modes). Their prior values are snapshotted on first instrument so rollback restores the
// function exactly — including append-style vars (JAVA_TOOL_OPTIONS/NODE_OPTIONS/PYTHONPATH), where the
// snapshot is the customer's original value that rollback puts back verbatim.
var odigosEnvKeys = append([]string{
	store.EnvExecWrapper, EnvLambdaConfig, EnvServiceName, EnvCollectorConfigURI, EnvOtelLogLevel,
	// No longer set (the inline collector config replaced them) but still snapshotted and stripped, so a
	// function instrumented by an earlier version is restored cleanly rather than left carrying them.
	EnvCollectorEndpoint, EnvCollectorInsecure,
	envLegacyOTLPEndpoint, envLegacyOTLPHeaders,
	EnvJavaToolOptions, EnvNodeOptions, EnvPythonPath, EnvOtlpProtocol,
	EnvTracesExporter, EnvMetricsExporter, EnvLogsExporter,
	"OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED",
	"OTEL_SDK_DISABLED",
	"OTEL_NODE_ENABLED_INSTRUMENTATIONS",
}, append(domaindest.SignalTransportOverrides(), lambdaDistributionKeys()...)...)

// buildManifest snapshots the pre-Odigos config for the keys we change and records the layer we attach.
// A re-instrument reuses the snapshot already stored in the marker so our own values are never recorded
// as if they were the customer's; only the layer ARN is refreshed.
func buildManifest(env map[string]string, layerARN string) managedManifest {
	if existing, ok := parseManifest(env[store.EnvOdigosManaged]); ok {
		existing.Layer = layerARN
		if existing.PrevEnv == nil {
			existing.PrevEnv = map[string]*string{}
		}
		for _, key := range odigosEnvKeys {
			if _, tracked := existing.PrevEnv[key]; !tracked {
				existing.PrevEnv[key] = priorValue(key, env)
			}
		}
		return existing
	}
	prev := make(map[string]*string, len(odigosEnvKeys))
	for _, k := range odigosEnvKeys {
		prev[k] = priorValue(k, env)
	}
	return managedManifest{Version: managedManifestVersion, Layer: layerARN, PrevEnv: prev}
}

// priorValue is the value rollback should restore for an env key: the customer's current value, except
// for keys that are unmistakably ours — the odigos-namespaced collector vars, and our exec-wrapper
// sentinel — which were never the customer's and so are recorded as absent (deleted on rollback). This
// also makes a first-time snapshot of a pre-marker (legacy) function attribute those keys to us rather
// than mistaking our own values for the customer's.
func priorValue(key string, env map[string]string) *string {
	switch key {
	case EnvCollectorEndpoint, EnvCollectorInsecure:
		return nil // odigos-namespaced — always ours
	case store.EnvExecWrapper:
		if isOdigosExecWrapper(env[key]) {
			return nil // our wrapper sentinel (either language value), not a customer value
		}
	}
	if v, ok := env[key]; ok {
		vv := v
		return &vv
	}
	return nil
}

// priorEnv returns the customer's original value for an env key from the snapshot ("" if it was absent).
// Used to rebuild append-style vars (PYTHONPATH/NODE_OPTIONS/JAVA_TOOL_OPTIONS) from the original rather
// than from the current (already-modified) value, so re-instrument does not accumulate our additions.
func priorEnv(m managedManifest, key string) string {
	if prev, ok := m.PrevEnv[key]; ok && prev != nil {
		return *prev
	}
	return ""
}

// desiredSharedEnv returns the values instrument is about to SET for the shared, instrumentor-owned keys —
// those a customer might also set and that we overwrite (as opposed to the odigos-namespaced collector
// vars / exec-wrapper sentinel, which are unambiguously ours, and the append-style loader vars, which are
// rebuilt from the original snapshot each cycle). Used by reconcileCustomerEdits to tell our own live value
// apart from a customer edit.
func (i *LambdaInstrumentor) desiredSharedEnv(webServer bool, runtime, serviceName string, dest domaindest.OTLPTarget, debug bool, collectorCfg string) map[string]string {
	d := map[string]string{
		EnvServiceName: serviceName,
		// The collector's config URI is upstream's variable name, not an odigos-namespaced one, so a customer
		// running their own collector layer could legitimately have set it. Listing it here is what lets
		// reconcileCustomerEdits tell their value apart from ours and restore it on rollback.
		EnvCollectorConfigURI: collectorCfg,
	}
	for key, value := range dest.LambdaSignalActivation(LanguageFromRuntime(runtime)) {
		d[key] = value
	}
	// Handler mode is the only mode that sets a log level; applyWebServerEnv always deletes it.
	if debug && !webServer {
		d[EnvOtelLogLevel] = otelLogLevelDebug
	}
	if webServer && LanguageFromRuntime(runtime) == "python" {
		d[EnvOtlpProtocol] = otlpProtocolHTTP
	}
	return d
}

// reconcileCustomerEdits updates a reused snapshot's PrevEnv to capture a customer edit made to a shared,
// instrumentor-owned key AFTER first instrument. Without it, a value the customer set post-instrument (e.g.
// their own OTEL_SERVICE_NAME) is overwritten by re-instrument and then DELETED on rollback, because the
// original snapshot recorded the key as absent. For each reconcilable key, a live value that differs from
// both what we are about to set (desired) and the already-recorded prior is treated as a customer edit and
// captured. Trade-off: if OUR OWN desired value for a key changed since the previous cycle (e.g. an admin
// renamed the service), the previous Odigos value is captured instead — but that only leaves a harmless
// stale value on an uninstrumented function (never customer data loss) and self-corrects on the next
// genuine customer edit. Called on every (re)instrument; it is a no-op on the first-instrument snapshot,
// whose PrevEnv already holds the customer's values via priorValue.
func reconcileCustomerEdits(m *managedManifest, liveEnv, desired map[string]string) {
	if m.PrevEnv == nil {
		return
	}
	for key, desiredVal := range desired {
		live, ok := liveEnv[key]
		if !ok || live == desiredVal {
			continue // nothing pre-existing, or the live value is already ours
		}
		if prev, ok := m.PrevEnv[key]; ok && prev != nil && *prev == live {
			continue // already recorded as the customer's value
		}
		if m.SignalEnv[key] == live || m.SignalEnv[key] == managedEnvValue(live) {
			continue
		}
		// Legacy inline markers did not record the applied collector config hash. Preserve
		// their original snapshot when migrating a connector-rendered collector document.
		if key == EnvCollectorConfigURI && m.SignalEnv[key] == "" && strings.HasPrefix(live, "yaml:{receivers: {otlp: {protocols: ") && strings.Contains(live, collectorProcessors) {
			continue
		}
		// Older manifests did not record the previous selectors. Both standardized
		// values were connector-owned, so do not turn a signal transition into a
		// customer override in the rollback snapshot.
		if len(m.SignalEnv) == 0 && (key == EnvTracesExporter || key == EnvMetricsExporter || key == EnvLogsExporter) && (live == exporterOTLP || live == exporterNone) {
			continue
		}
		v := live
		m.PrevEnv[key] = &v
	}
	m.SignalEnv = map[string]string{}
	for key, value := range desired {
		m.SignalEnv[key] = managedEnvValue(value)
	}
}

// stripLegacyLoaderValue records, for a legacy (pre-marker) append-style loader var, the customer's
// ORIGINAL value — the current value minus our loader substring — so rollback restores it. If only our
// value was present the key is deleted (nil); if the key is absent it is dropped from the map so rollback
// leaves it alone. Handler-mode legacy functions never carry these keys, so they are dropped there.
func stripLegacyLoaderValue(prev map[string]*string, env map[string]string, key, ourValue, sep string) {
	cur, ok := env[key]
	if !ok {
		delete(prev, key)
		return
	}
	stripped := removeLoaderValue(cur, ourValue, sep)
	if stripped == "" {
		prev[key] = nil
		return
	}
	prev[key] = &stripped
}

func marshalManifest(m managedManifest) (string, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal odigos lambda manifest: %w", err)
	}
	return string(b), nil
}

func parseManifest(s string) (managedManifest, bool) {
	if s == "" {
		return managedManifest{}, false
	}
	var m managedManifest
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return managedManifest{}, false
	}
	if m.Version != 1 && m.Version != 2 {
		return managedManifest{}, false
	}
	if m.Version == 2 && m.SnapshotID == "" {
		return managedManifest{}, false
	}
	return m, true
}

// recordedLayer is the layer-version ARN the applied instrumentation says this function carries, from
// whichever marker recorded it — empty when neither did.
//
// The ownership marker wins when both are present, and that ordering is not arbitrary: both markers coexist
// only on a function an IaC artifact instrumented and the customer then handed to the connector, and there
// the connector's own record is the one describing what is actually applied now.
//
// One definition rather than two because its two callers must not disagree. desiredLayerARN answers "which
// layer is this function supposed to carry" and observedPrint answers "has that changed since the last
// pass"; a print built from a narrower notion of "recorded" than the verdict uses would go on reporting
// nothing-moved across exactly the change the verdict cares about.
func recordedLayer(env map[string]string) string {
	if m, ok := parseManifest(env[store.EnvOdigosManaged]); ok && m.Layer != "" {
		return m.Layer
	}
	if m, ok := baseapi.ParseIaCMarker(env[store.EnvOdigosIaC]); ok {
		return m.Layer
	}
	return ""
}

// ownership decides whether a function's live state is Odigos-owned and returns the manifest describing
// what to undo. It returns (manifest, true) only when ownership is provable: the marker (preferred), or —
// for functions instrumented before the marker existed — the odigos-namespaced collector vars, which no
// customer setup would set. A synthesized legacy manifest removes our env keys and the layer we resolve
// for this function; it never name-matches a layer, so a customer's OTel/ADOT layer is never stripped.
func (i *LambdaInstrumentor) ownership(ctx context.Context, fn store.LambdaFunction, env map[string]string) (managedManifest, bool) {
	if m, ok := parseManifest(env[store.EnvOdigosManaged]); ok {
		return m, true
	}
	if env[EnvCollectorEndpoint] == "" && env[EnvCollectorInsecure] == "" {
		return managedManifest{}, false // no provable Odigos signal — not ours
	}
	// Legacy (pre-marker) function: there is no snapshot to recover the customer's pre-instrument values, so
	// every Odigos-managed key present was set by us and is removed on rollback. This covers ALL keys the
	// current instrument path sets — not just the four collector vars the earlier version handled — so a
	// legacy uninstrument does not strand the web-server loader / OTEL_* vars. Left behind after the layer is
	// stripped, those (e.g. NODE_OPTIONS=--import /opt/init.mjs, a PYTHONPATH into /opt) point at files that
	// no longer exist and can break the function at the next cold start.
	prev := make(map[string]*string, len(odigosEnvKeys))
	for _, k := range odigosEnvKeys {
		prev[k] = nil // delete on rollback
	}
	// Preserve a customer-set exec wrapper (only OUR wrapper sentinel is removed); a foreign wrapper survives.
	if !isOdigosExecWrapper(env[store.EnvExecWrapper]) {
		delete(prev, store.EnvExecWrapper)
	}
	// Append-style loader vars may still carry a customer portion alongside ours (we append/prepend, never
	// overwrite): strip only our loader substring so the customer's original value is restored, not deleted.
	stripLegacyLoaderValue(prev, env, EnvNodeOptions, nodeImportOption, " ")
	stripLegacyLoaderValue(prev, env, EnvJavaToolOptions, javaAgentOption, " ")
	stripLegacyLoaderValue(prev, env, EnvPythonPath, pythonInstrPath, ":")
	m := managedManifest{Version: managedManifestVersion, PrevEnv: prev}
	// Resolve the layer we would have attached and strip exactly that ARN — via a DISCOVER-ONLY lookup that
	// never publishes (this is an uninstrument path; it must not mutate the account by publishing a layer).
	// A discovery miss leaves the layer in place rather than guessing.
	if arn, err := i.resolveExistingLayer(ctx, fn); err == nil {
		m.Layer = arn
	}
	return m, true
}

// resolveExistingLayer finds the layer ARN to strip on rollback without publishing one as a side effect —
// this is an uninstrument path, so it must not mutate the account.
func (i *LambdaInstrumentor) resolveExistingLayer(ctx context.Context, fn store.LambdaFunction) (string, error) {
	return NonPublishingLayerARN(ctx, i.layers, fn.Region, fn.Runtime, firstArch(fn.Architecture))
}

func hasDuplicateLayerVersions(layers []string) bool {
	seen := map[string]bool{}
	for _, arn := range layers {
		name, _, ok := store.ParseLayerVersionARN(arn)
		if !ok {
			continue
		}
		if seen[name] {
			return true
		}
		seen[name] = true
	}
	return false
}

func desiredLambdaSharedEnv(i *LambdaInstrumentor, webServer bool, runtime, distro, serviceName string, dest domaindest.OTLPTarget, debug bool, collectorCfg string) map[string]string {
	env := i.desiredSharedEnv(webServer, runtime, serviceName, dest, debug, collectorCfg)
	if !webServer {
		applyLambdaDistributionEnv(env, LanguageFromRuntime(runtime), distro)
	}
	return env
}

// Ownership needs equality, not a second copy of long instrumentation lists in
// Lambda's 4 KiB environment. Existing literal values remain readable on upgrade.
func managedEnvValue(value string) string {
	if len(value) <= 128 {
		return value
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value)))
}
