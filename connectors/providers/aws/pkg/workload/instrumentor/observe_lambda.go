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
// beyond a read, resolves layers exclusively through NonPublishingLayerARN, and every store method it calls
// writes observation keys only. The reviewable invariant is that this file contains no AWS call other than
// ListFunctions.
type LambdaObserver struct {
	store  *store.Store
	cache  *awsclient.Cache
	layers LayerResolver
	debug  func() bool
	// now is the clock for the drift since-stamp, injectable so a test can assert the stamp is CARRIED
	// FORWARD across passes rather than refreshed — the property that makes "drifted for 3 days" true.
	now func() time.Time
}

// NewLambdaObserver builds the read-only Lambda observer over the same store, config cache, layer resolver and
// live debug toggle the instrumentor uses, so both judge conformance against identical desired inputs.
func NewLambdaObserver(st *store.Store, cache *awsclient.Cache, layers LayerResolver, debug func() bool) *LambdaObserver {
	return &LambdaObserver{store: st, cache: cache, layers: layers, debug: debug, now: time.Now}
}

// Observe implements baseapi.InstrumentationObserver. One pass:
//
//  1. read intent (which functions the customer marked) and the OTLP destination;
//  2. list the functions in each region that has marked functions — one ListFunctions per region, the same
//     call discovery makes, which returns env, layers, State and LastUpdateStatus together;
//  3. diff each against the desired instrumentation and persist the verdict.
//
// Scope is deliberately the MARKED functions only. An unmarked function has no desired instrumentation to be
// conformant with, so observing it would produce a verdict about nothing; marked-but-not-instrumented ones are
// included because that is what lets a verdict be cleared when a customer removes instrumentation, rather than
// leaving the last drift verdict standing forever.
func (o *LambdaObserver) Observe(ctx context.Context) error {
	enabled, reportedNames, err := o.enabledLambdaSources(ctx)
	if err != nil {
		return err
	}
	if len(enabled) == 0 {
		return nil // nothing marked — nothing to be conformant with
	}
	dest, haveDest, err := o.store.OTLPExportTarget(ctx)
	if err != nil {
		return fmt.Errorf("resolve otlp export target: %w", err)
	}
	if !haveDest {
		// Without a destination there is no desired instrumentation to compare against — the endpoint is part
		// of it — and the report already says so (WaitingForDestination). Recording an observation error on top
		// would blame the read for a configuration gap the customer has already been told about.
		observeLog.Debug("skipping lambda observation: no OTLP destination configured")
		return nil
	}
	functions, err := o.store.ListLambdaFunctions(ctx)
	if err != nil {
		return err
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
			cur, ok := live[fn.FunctionARN]
			if !ok {
				// Listed by a previous discovery cycle but gone from this listing: deleted, or moved out of the
				// tag filter. Discovery owns that transition (it prunes the row); the observer must not turn a
				// disappearance into a drift verdict.
				continue
			}
			if obs, ok := o.observation(ctx, fn, cur, dest, reportedNames[fn.FunctionARN]); ok {
				o.persist(ctx, fn, obs)
			}
		}
	}
	return errors.Join(errs...)
}

// observation computes one function's verdict from its live configuration, or reports that no verdict should be
// recorded this pass. Pure apart from the layer lookup, which is read-only by construction
// (NonPublishingLayerARN).
func (o *LambdaObserver) observation(ctx context.Context, fn store.LambdaFunction, cur lambdatypes.FunctionConfiguration, dest domaindest.OTLPTarget, reportedServiceName string) (store.LambdaObservation, bool) {
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
	obs := store.LambdaObservation{ObservedPrint: observedPrint(env, layers)}

	// No ownership marker means nothing on this function is attributable to Odigos — including a customer's
	// own unrelated OpenTelemetry setup, which must never be reported as broken Odigos instrumentation. It is
	// Absent, not Partial, and no desired state needs resolving to say so.
	if !store.OdigosInstrumented(env) {
		obs.Conformance = baseapi.ConformanceAbsent
		return obs, true
	}

	webServer := usesLambdaWebAdapter(layers)
	layerARN, layerErr := o.desiredLayerARN(ctx, fn, env)
	debug := o.debug()
	obs.DesiredPrint = desiredPrint(webServer, fn, layerARN, dest, debug, reportedServiceName)

	diff := instrumentationDiff(webServer, fn, env, layers, layerARN, dest, debug, reportedServiceName)
	if layerErr != nil {
		// The layer is the one component that could not be checked (see desiredLayerARN). Report the verdict
		// over everything that COULD be checked and say the check was incomplete, rather than either inventing
		// a missing layer or quietly calling a partially-verified function conformant.
		obs.Error = fmt.Sprintf("could not determine the expected instrumentation layer: %v", layerErr)
	}
	if len(diff) == 0 {
		obs.Conformance = baseapi.ConformanceConformant
		return obs, true
	}
	obs.Conformance = baseapi.ConformancePartial
	obs.Missing = diff
	return obs, true
}

// desiredLayerARN resolves the layer version this function is supposed to carry, without publishing one.
//
// The ownership marker comes FIRST, and that ordering is the whole semantics of manual-mode drift. The marker
// records the layer that was applied, which in manual mode is the layer the CUSTOMER pinned from the
// instrumentation Odigos gave them. Drift means "what is on the function no longer matches what was applied" —
// so the marker is the correct reference, and the case this pass exists to catch is exactly the one it
// catches: the layer was detached (or replaced) while the marker survived.
//
// Asking the resolver first would instead compare against whatever layer version Odigos currently has, and
// then every layer release would report every conformant function as drifted — an available upgrade announced
// as an error, on every function, until the customer re-applied. That is a false alarm, not drift.
//
// The resolver is the fallback for a marker that records no layer, which a customer's own IaC can produce.
// Under a manual-mode policy that call is not necessarily granted (lambda:ListLayers is declared on the
// instrumentation type, and manual mode deliberately grants no instrumentation permissions — see
// spec.manualObservationReads, which asserts Lambda observation needs nothing beyond ListFunctions). A denial
// there surfaces as an incomplete check, which is the honest outcome: Odigos genuinely cannot tell which layer
// was meant.
func (o *LambdaObserver) desiredLayerARN(ctx context.Context, fn store.LambdaFunction, env map[string]string) (string, error) {
	if m, ok := parseManifest(env[store.EnvOdigosManaged]); ok && m.Layer != "" {
		return m.Layer, nil
	}
	arn, err := NonPublishingLayerARN(ctx, o.layers, fn.Region, fn.Runtime, firstArch(fn.Architecture))
	switch {
	case err != nil:
		return "", err
	case arn == "":
		return "", ErrLayerUnavailable
	default:
		return arn, nil
	}
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
// the record to write and whether writing is worth it at all.
//
// The since-stamp is the point. It is carried forward unchanged while the verdict holds and re-stamped only
// when the verdict actually flips, because "this has been drifted for three days" and "this is drifted right
// now" are different reports and only the first is actionable. Re-stamping every pass — the obvious
// implementation — would silently produce the second.
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
	obs.Since = prev.Since
	if obs.Conformance != prev.Conformance || obs.Since.IsZero() {
		obs.Since = now.UTC()
	}
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
func (o *LambdaObserver) enabledLambdaSources(ctx context.Context) (map[string]struct{}, map[string]string, error) {
	sources, err := o.store.ListSources(ctx)
	if err != nil {
		return nil, nil, err
	}
	enabled := make(map[string]struct{})
	reported := make(map[string]string)
	for _, s := range sources {
		if s.Kind == store.KindLambda && s.Enabled && s.ExternalResourceID != "" {
			enabled[s.ExternalResourceID] = struct{}{}
			reported[s.ExternalResourceID] = s.ReportedServiceName()
		}
	}
	return enabled, reported, nil
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
	// The marker's own value carries a snapshot of the customer's pre-Odigos env, which changes for reasons
	// that are not instrumentation changes. Only the layer it records participates.
	marker, _ := parseManifest(env[store.EnvOdigosManaged])
	parts = append(parts, "marker="+marker.Layer)
	for _, k := range sortedCopy(odigosEnvKeys) {
		parts = append(parts, k+"="+env[k])
	}
	parts = append(parts, sortedCopy(layers)...)
	return fingerprint(parts)
}

func desiredPrint(webServer bool, fn store.LambdaFunction, layerARN string, dest domaindest.OTLPTarget, debug bool, reportedServiceName string) string {
	if reportedServiceName == "" {
		reportedServiceName = fn.FunctionName
	}
	return fingerprint([]string{
		"webServer=" + strconv.FormatBool(webServer),
		"runtime=" + fn.Runtime,
		"layer=" + layerARN,
		"service=" + reportedServiceName,
		"endpoint=" + dest.Endpoint,
		"insecure=" + strconv.FormatBool(dest.Insecure),
		"traces=" + exporterFor(dest.Traces),
		"metrics=" + exporterFor(dest.Metrics),
		"logs=" + exporterFor(dest.Logs),
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
