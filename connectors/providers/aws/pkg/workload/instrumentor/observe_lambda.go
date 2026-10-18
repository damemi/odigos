package instrumentor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// observeLog is a subsystem of its own so an operator can tell a read-only observation apart from a mutating
// instrumentation pass in the logs — the distinction that matters most in manual mode.
var observeLog = connlog.Subsystem("aws-observer")

// LambdaObserver is the READ-ONLY conformance observer for Lambda: it answers "does the instrumentation
// actually on this function match what Odigos wants for it", and it is the only instrumentation code path the
// base runs for a manual (IaC) mode type.
//
// It lives in the instrumentor package on purpose, despite never instrumenting anything. Every comparison it
// needs — instrumentationDiff, the web-adapter and loader predicates, the marker codec — is the instrumentor's
// own logic, and the whole point of §7.2 is that observation and mutation share ONE comparator rather than
// drifting into two definitions of "correct". A sibling package would have forced eight internals to be
// exported, widening the mutating package's surface to everyone for no gain.
//
// What keeps it honest is not its location but its dependencies: it holds no lambda client factory of its own
// beyond a read, it resolves no layer at all (see the layer check in observation), and every store method it
// calls writes observation keys only. The reviewable invariant is that this file contains no AWS call other
// than ListFunctions — which is also what makes manual mode's permission set sufficient, since a layer lookup
// is an instrumentation permission manual mode deliberately does not grant.
type LambdaObserver struct {
	store *store.Store
	cache *awsclient.Cache
	// routes is the same complete-pass resolver used by the mutating Lambda instrumentor. A nil resolver
	// keeps the observer's legacy direct-destination behavior for callers without managed Collectors.
	routes LambdaRouteResolver
	debug  func() bool
	// now is the clock for the drift since-stamp, injectable so a test can assert the stamp is CARRIED
	// FORWARD across passes rather than refreshed — the property that makes "drifted for 3 days" true.
	now func() time.Time
}

// NewLambdaObserver builds the read-only Lambda observer over the same store, config cache and live debug
// toggle the instrumentor uses, so both judge conformance against identical desired inputs.
func NewLambdaObserver(st *store.Store, cache *awsclient.Cache, debug func() bool, routes ...LambdaRouteResolver) *LambdaObserver {
	var routeResolver LambdaRouteResolver
	if len(routes) > 0 {
		routeResolver = routes[0]
	}
	return &LambdaObserver{store: st, cache: cache, routes: routeResolver, debug: debug, now: time.Now}
}

// Observe implements baseapi.InstrumentationObserver. One pass:
//
//  1. read intent (which functions the customer marked) and resolve each function's effective route;
//  2. list the functions in each region that has marked functions — one ListFunctions per region, the same
//     call discovery makes, which returns env, layers, State and LastUpdateStatus together;
//  3. diff each against the desired instrumentation and persist the verdict.
//
// Scope is deliberately the MARKED functions only. An unmarked function has no desired instrumentation to be
// conformant with, so observing it would produce a verdict about nothing; marked-but-not-instrumented ones are
// included because that is what lets a verdict be cleared when a customer removes instrumentation, rather than
// leaving the last drift verdict standing forever.
func (o *LambdaObserver) Observe(ctx context.Context) error {
	enabled, err := o.enabledLambdaSources(ctx)
	if err != nil {
		return err
	}
	if len(enabled) == 0 {
		return nil // nothing marked — nothing to be conformant with
	}
	functions, err := o.store.ListLambdaFunctions(ctx)
	if err != nil {
		return err
	}
	enabledFunctions := make([]store.LambdaFunction, 0, len(functions))
	for _, fn := range functions {
		if _, want := enabled[fn.FunctionARN]; want {
			enabledFunctions = append(enabledFunctions, fn)
		}
	}
	routes, routeErr := o.resolveLambdaRoutes(ctx, enabledFunctions)
	if routeErr != nil {
		return fmt.Errorf("resolve lambda OTLP routes: %w", routeErr)
	}

	byRegion := map[string][]store.LambdaFunction{}
	for _, fn := range functions {
		if _, want := enabled[fn.FunctionARN]; !want {
			continue
		}
		byRegion[fn.Region] = append(byRegion[fn.Region], fn)
	}

	var errs []error
	for region, fns := range byRegion {
		live, err := o.liveFunctions(ctx, region)
		if err != nil {
			// A failed read is NOT drift. Record it against each function in the region so the report raises a
			// transient issue while continuing to show the last confirmed verdict, and keep going with the
			// other regions.
			observeLog.Warn("lambda observation read failed", "region", region, "error", err)
			for _, fn := range fns {
				o.persist(ctx, fn, store.LambdaObservation{
					Conformance:   fn.Observation.Conformance,
					Missing:       fn.Observation.Missing,
					Since:         fn.Observation.Since,
					ObservedPrint: fn.Observation.ObservedPrint,
					DesiredPrint:  fn.Observation.DesiredPrint,
					Error:         err.Error(),
				})
			}
			errs = append(errs, fmt.Errorf("observe region %s: %w", region, err))
			continue
		}
		for _, fn := range fns {
			routing, haveRoute := routes[fn.FunctionARN]
			if !haveRoute {
				err := fmt.Errorf("no routing decision was returned for %s", fn.FunctionARN)
				if serr := o.store.SetLambdaRouting(ctx, fn.FunctionARN, fn.RoutingVia, &baseapi.WorkloadRoutingNote{
					Code: "routing-error", Message: err.Error(), Transient: true,
				}); serr != nil {
					observeLog.Warn("set lambda routing error note failed", "function", fn.FunctionName, "error", serr)
				}
				_ = o.store.SetLambdaInstrumentationError(ctx, fn.FunctionARN, lambdaRoutingErrorMessage(err.Error()))
				errs = append(errs, fmt.Errorf("resolve route for %s: %w", fn.FunctionName, err))
				continue
			}
			if routing.Outcome == domaindest.RoutingError || !routing.Found {
				note := routing.Note
				if note == "" {
					note = routing.Describe()
				}
				if serr := o.store.SetLambdaRouting(ctx, fn.FunctionARN, fn.RoutingVia, lambdaRoutingNote(routing)); serr != nil {
					observeLog.Warn("set lambda routing error note failed", "function", fn.FunctionName, "error", serr)
				}
				if serr := o.store.SetLambdaInstrumentationError(ctx, fn.FunctionARN, lambdaRoutingErrorMessage(note)); serr != nil {
					observeLog.Warn("set lambda routing error failed", "function", fn.FunctionName, "error", serr)
				}
				continue
			}
			// Publish or clear the fallback explanation while keeping the last applied route. The route is
			// advanced only after a settled live configuration conforms to the desired target below.
			if serr := o.store.SetLambdaRouting(ctx, fn.FunctionARN, fn.RoutingVia, lambdaSignalRoutingNote(routing, fn.Runtime)); serr != nil {
				observeLog.Warn("set lambda routing note failed", "function", fn.FunctionName, "error", serr)
			}
			cur, ok := live[fn.FunctionARN]
			if !ok {
				// Listed by a previous discovery cycle but gone from this listing: deleted, or moved out of the
				// tag filter. Discovery owns that transition (it prunes the row); the observer must not turn a
				// disappearance into a drift verdict.
				continue
			}
			if obs, ok := o.observation(ctx, fn, cur, routing.Target, routing.Headers, enabled[fn.FunctionARN]); ok {
				o.persist(ctx, fn, obs)
				if isLambdaRoutingFailureNote(fn.RoutingNote) {
					if serr := o.store.ClearLambdaInstrumentationError(ctx, fn.FunctionARN); serr != nil {
						observeLog.Warn("clear recovered lambda routing error failed", "function", fn.FunctionName, "error", serr)
					}
				}
				if obs.Conformance == baseapi.ConformanceConformant {
					if serr := o.store.SetLambdaRouting(ctx, fn.FunctionARN, string(routing.Via), lambdaSignalRoutingNote(routing, fn.Runtime)); serr != nil {
						observeLog.Warn("set lambda routing after conformance failed", "function", fn.FunctionName, "error", serr)
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

// observation computes one function's verdict from its live configuration, or reports that no verdict should be
// recorded this pass. Pure apart from the layer lookup, which is read-only by construction
// (NonPublishingLayerARN).
func (o *LambdaObserver) observation(ctx context.Context, fn store.LambdaFunction, cur lambdatypes.FunctionConfiguration, dest domaindest.OTLPTarget, destHeaders map[string]string, src enabledLambdaSource) (store.LambdaObservation, bool) {
	reportedServiceName := src.serviceName
	// A configuration change still being applied must not be judged at all. Mid-flight state is
	// indistinguishable from a half-applied instrumentation, so recording a verdict here would both report a
	// drift that is about to resolve itself and — worse — reset the drift clock of a workload that really is
	// broken, every time the customer touched it. The last confirmed verdict stands; the report meanwhile says
	// "verifying your changes", derived from the lifecycle fields discovery captured.
	//
	// This is §7.3's rule at its source: drift is only ever declared after a successful, SETTLED read.
	if lifecycleSettling(cur.State, cur.LastUpdateStatus) {
		observeLog.Debug("skipping observation: a configuration change is still settling",
			"function", fn.FunctionName, "region", fn.Region)
		return store.LambdaObservation{}, false
	}

	env := currentEnv(cur.Environment)
	layers := layerARNs(cur.Layers)
	if o.cache != nil && o.store != nil {
		cfg, err := o.cache.Config(ctx, fn.Region)
		if err == nil {
			reader := &LambdaInstrumentor{snapshots: o.store}
			_, err = reader.readLambdaTags(ctx, lambda.NewFromConfig(cfg), fn, env, layers)
		}
		if err != nil {
			return store.LambdaObservation{Conformance: fn.Observation.Conformance, Missing: fn.Observation.Missing, Since: fn.Observation.Since, Error: fmt.Sprintf("read Lambda attribution: %v", err)}, true
		}
	}
	obs := store.LambdaObservation{ObservedPrint: observedPrint(env, layers)}

	// No ownership marker means nothing on this function is attributable to Odigos — including a customer's
	// own unrelated OpenTelemetry setup, which must never be reported as broken Odigos instrumentation. It is
	// Absent, not Partial, and no desired state needs resolving to say so.
	if !store.OdigosInstrumented(env) {
		obs.Conformance = baseapi.ConformanceAbsent
		return obs, true
	}

	webServer := usesLambdaWebAdapter(layers)
	// The layer the APPLIED instrumentation recorded, and the only version this function is held to. Empty when
	// neither marker recorded one, which is the unpinned case the layer check below handles by name instead.
	pinnedLayerARN := recordedLayer(env)
	debug := o.debug()

	// A destination the collector cannot be configured for is a configuration problem, not function drift:
	// there is no desired state to judge this function against, so say that rather than reporting every
	// marked function as Partial because the connector cannot describe what it wants.
	collectorCfg, cfgErr := renderCollectorConfig(dest, destHeaders)
	if cfgErr != nil {
		obs.Conformance = fn.Observation.Conformance
		obs.Missing = fn.Observation.Missing
		obs.Since = fn.Observation.Since
		obs.Error = fmt.Sprintf("the configured destination cannot be exported to from Lambda: %v", cfgErr)
		return obs, true
	}
	obs.DesiredPrint = desiredPrint(webServer, fn, pinnedLayerARN, debug, reportedServiceName, collectorCfg, src.distro)

	diff := instrumentationDiff(webServer, fn, env, layers, pinnedLayerARN, dest, debug, reportedServiceName, collectorCfg, src.distro)
	// An unpinned marker (a customer's own IaC, or an Odigos artifact predating the recorded layer) leaves NO
	// version to hold the function to, so the check falls back from identity to signature: an Odigos layer is
	// attached, or none is.
	//
	// Resolving one to compare against instead is the false alarm this exists to avoid. The resolver answers
	// "which layer does Odigos ship TODAY", so every layer release would report every conformant function in the
	// account as drifted — an available upgrade announced as an error, on every function, until the customer
	// re-applied. Manual mode also grants no permission to ask (a layer lookup is an instrumentation
	// permission), so the lookup that produced that wrong verdict was not even one this path is entitled to make.
	if pinnedLayerARN == "" && store.OdigosLayerARN(layers) == "" {
		diff = append(diff, componentInstrumentationLayer)
	}
	if len(diff) == 0 {
		obs.Conformance = baseapi.ConformanceConformant
		return obs, true
	}
	obs.Conformance = baseapi.ConformancePartial
	obs.Missing = diff
	return obs, true
}

// persist writes a verdict only when something actually moved. The decision itself is nextObservation's, kept
// pure so the since-clock can be tested across passes rather than only within one.
func (o *LambdaObserver) persist(ctx context.Context, fn store.LambdaFunction, obs store.LambdaObservation) {
	next, write := nextObservation(fn.Observation, obs, o.now())
	if !write {
		return
	}
	if err := o.store.SetLambdaObservation(ctx, fn.FunctionARN, next); err != nil {
		observeLog.Warn("persist lambda observation failed", "function", fn.FunctionName, "region", fn.Region, "error", err)
	}
}

// nextObservation decides what to store for a freshly computed observation, given the previously stored one:
// the record to write and whether writing is worth it at all. The since-stamp rule itself is the base's
// (api.ConformanceSince) — it is the part that must not be re-derived per provider.
//
// The fingerprints are what make skipping safe, and are the reason they are stored at all: identical observed
// and desired prints mean neither the function nor the desired instrumentation moved since the last pass, so
// the row does not need rewriting on every cycle of every function in the account.
func nextObservation(prev, obs store.LambdaObservation, now time.Time) (store.LambdaObservation, bool) {
	unchanged := obs.ObservedPrint == prev.ObservedPrint &&
		obs.DesiredPrint == prev.DesiredPrint &&
		obs.Conformance == prev.Conformance &&
		obs.Error == prev.Error
	// A stored record with no stamp is one written before this pass could date it (or by an older connector),
	// so it is rewritten once to acquire one rather than staying permanently undateable.
	if unchanged && !prev.Since.IsZero() {
		return prev, false
	}
	obs.Since = baseapi.ConformanceSince(prev.Conformance, prev.Since, obs.Conformance, now)
	return obs, true
}

// liveFunctions lists a region's functions keyed by ARN. This is the observer's ONLY AWS call: ListFunctions
// returns each function's environment, layers, State and LastUpdateStatus in one paginated read, which is why
// Lambda needs no additional permission for manual mode (see the aws spec — nothing was added for
// aws.lambda observation).
func (o *LambdaObserver) liveFunctions(ctx context.Context, region string) (map[string]lambdatypes.FunctionConfiguration, error) {
	cfg, err := o.cache.Config(ctx, region)
	if err != nil {
		return nil, err
	}
	client := lambda.NewFromConfig(cfg)
	out := map[string]lambdatypes.FunctionConfiguration{}
	paginator := lambda.NewListFunctionsPaginator(client, &lambda.ListFunctionsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, fn := range page.Functions {
			out[aws.ToString(fn.FunctionArn)] = fn
		}
	}
	return out, nil
}

// enabledLambdaSources mirrors the instrumentor's intent read: enabled Lambda source ARNs and the OTel
// service.name each should report. Both must resolve the reported name identically or the observer would call
// a correctly-instrumented function drifted over a name only it disagrees with.
func (o *LambdaObserver) enabledLambdaSources(ctx context.Context) (map[string]enabledLambdaSource, error) {
	sources, err := o.store.ListSources(ctx)
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

func (o *LambdaObserver) resolveLambdaRoutes(ctx context.Context, functions []store.LambdaFunction) (map[string]domaindest.Routing, error) {
	if o.routes != nil {
		return o.routes.ResolveLambdaRoutes(ctx, functions)
	}
	routing, err := o.store.OTLPRouting(ctx, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]domaindest.Routing, len(functions))
	for _, fn := range functions {
		out[fn.FunctionARN] = routing
	}
	return out, nil
}

// observedPrint / desiredPrint fingerprint the live and wanted instrumentation.
//
// They are CHANGE DETECTORS, not the conformance test — instrumentationDiff is that, and it applies rules a
// hash cannot (a loader value that must be present as a substring, a legacy key whose absence is what matters).
// Equal prints across two passes mean nothing moved; they do not mean observed == desired. Keeping that
// distinction explicit is why the verdict is stored separately instead of being inferred from
// env_hash = config_hash.
func observedPrint(env map[string]string, layers []string) string {
	parts := make([]string, 0, len(odigosEnvKeys)+len(layers)+1)
	// A marker's own value carries things that change for reasons that are not instrumentation changes — the
	// ownership marker's snapshot of the customer's pre-Odigos env, the IaC marker's artifact release. Only
	// the layer they record participates, from whichever marker recorded it.
	parts = append(parts, "marker="+recordedLayer(env))
	for _, k := range sortedCopy(odigosEnvKeys) {
		parts = append(parts, k+"="+env[k])
	}
	parts = append(parts, sortedCopy(layers)...)
	return fingerprint(parts)
}

// desiredPrint fingerprints the instrumentation the connector wants on this function, so a change in intent
// is visible as a changed print even when the function itself did not move.
//
// The collector config stands in for what used to be four separate parts (endpoint, insecure, and the three
// exporter selectors): it is rendered from exactly those inputs plus the ones they could not express — the
// transport, the destination's credential, and which pipelines exist — so hashing it tracks strictly more of
// the desired state than enumerating fields did, and cannot drift from what is actually applied.
func desiredPrint(webServer bool, fn store.LambdaFunction, layerARN string, debug bool, reportedServiceName, collectorCfg, distro string) string {
	if reportedServiceName == "" {
		reportedServiceName = fn.FunctionName
	}
	return fingerprint([]string{
		"webServer=" + strconv.FormatBool(webServer),
		"runtime=" + fn.Runtime,
		"layer=" + layerARN,
		"service=" + reportedServiceName,
		"collector=" + collectorCfg,
		// The Java wrapper the source selected is desired state too: a source switched from otel-handler to
		// otel-stream-handler changes what the function must carry, and a print that ignored it would report
		// "nothing has moved" on exactly the change the customer just made.
		"wrapper=" + execWrapperForRuntime(fn.Runtime, distro),
		"distro=" + recipeFor(LanguageFromRuntime(fn.Runtime), distro).Name,
		"debug=" + strconv.FormatBool(debug),
	})
}

// fingerprint hashes canonical parts with a separator, so ["a","bc"] and ["ab","c"] cannot collide. Truncated
// to 128 bits: this is a change detector, not a security boundary, and it has to fit a TEXT column readably.
func fingerprint(parts []string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func sortedCopy(xs []string) []string {
	out := append([]string(nil), xs...)
	sort.Strings(out)
	return out
}
