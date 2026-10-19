package instrumentor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// LambdaPlanner serves the Lambda instrumentation plan for one discovered function.
//
// It reads the connector's own inventory and resolves the layer ARN read-only. Neither is a mutating call, and
// neither publishes a layer: this runs on the GraphQL read path, and NonPublishingLayerARN exists precisely so
// a read path can answer "which layer would you attach" without attaching one.
type LambdaPlanner struct {
	store  lambdaPlanStore
	layers LayerResolver
}

type lambdaPlanStore interface {
	GetLambdaFunction(context.Context, string) (*store.LambdaFunction, bool, error)
	OTLPExportTarget(context.Context) (domaindest.OTLPTarget, bool, error)
	OTLPExportHeaders(context.Context) (map[string]string, error)
}

// NewLambdaPlanner builds the planner.
func NewLambdaPlanner(st *store.Store, layers LayerResolver) *LambdaPlanner {
	return &LambdaPlanner{store: st, layers: layers}
}

// PlanFor implements baseapi.InstrumentationPlanner.
//
// The inventory read is what makes the plan safe. Lambda has two activations — the layer's exec wrapper on a
// RequestHandler function, the language's own loader variable on one running a web server behind the AWS
// Lambda Web Adapter — and applying the wrong one BREAKS the function: the adapter already owns
// AWS_LAMBDA_EXEC_WRAPPER. The activation is derived from the function's attached layers, which discovery
// records, so it is resolved here rather than offered as a choice a client could get wrong.
func (p *LambdaPlanner) PlanFor(ctx context.Context, w baseapi.DiscoveredWorkload) (*baseapi.PlannedInstrumentation, error) {
	fn, ok, err := p.store.GetLambdaFunction(ctx, w.ExternalID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	lang := LanguageFromRuntime(fn.Runtime)
	if lang == "" {
		// No OpenTelemetry Lambda layer for this runtime (Go, .NET, a custom runtime). The report already
		// reports it as an unsupported runtime; a plan would be instructions that cannot work.
		return nil, nil
	}
	activation := ActivationHandler
	if usesLambdaWebAdapter(fn.Layers) {
		activation = ActivationWebServer
	}
	if err := ValidateLambdaDistroRuntime(fn.Runtime, activation, w.Overrides.OtelDistroName); err != nil {
		return nil, err
	}
	d, err := ResolveLambdaDistro(lang, activation, w.Overrides.OtelDistroName)
	if err != nil {
		return nil, err
	}
	distro := d.Name
	pl, err := DesiredLambdaPlanForDistro(lang, activation, distro)
	if err != nil {
		return nil, err
	}
	resolved := p.resolveLayerForDistro(ctx, fn, distro)
	if resolved == nil {
		resolved = map[string]string{}
	}
	if arn, err := resolveLambdaCollector(ctx, p.layers, *fn, distro, true); err == nil && arn != "" {
		resolved["<OTEL_COLLECTOR_LAYER_ARN>"] = arn
	}
	for k, v := range lambdaSnippetReplacements(lang, distro) {
		resolved[k] = v
	}
	for k, v := range lambdaLayerSnippetReplacements(lang, distro, resolved[PlaceholderLayerARN], resolved["<OTEL_COLLECTOR_LAYER_ARN>"]) {
		resolved[k] = v
	}
	if target, ok, err := p.store.OTLPExportTarget(ctx); err != nil {
		return nil, err
	} else if ok {
		if err := validateLambdaApplicationSignalsTarget(lang, distro, target); err != nil {
			return nil, err
		}
		headers, err := p.store.OTLPExportHeaders(ctx)
		if err != nil {
			return nil, err
		}
		service := w.ReportedServiceName()
		if service == "" {
			service = fn.FunctionName
		}
		c := lambdaBootstrapConfig{Version: lambdaConfigVersion, Service: service, Signals: signalMask(target), Endpoint: target.Endpoint, Insecure: target.Insecure, Headers: headers, ApplicationSignals: recipeFor(lang, distro).ApplicationSignals}
		c.FunctionLogs = target.Logs && activation != ActivationWebServer && lambdaFunctionLogs(lang, recipeFor(lang, distro).ExecWrapper)
		if c.Endpoint == "" {
			c.Endpoint = target.HTTPEndpoint
			c.HTTP = true
			c.Insecure = false
		}
		if activation == ActivationWebServer {
			wrapper := "<ORIGINAL_EXEC_WRAPPER>"
			c.WebWrapper = &wrapper
		}
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		for k, v := range lambdaConfigSnippetValues(string(raw)) {
			resolved[k] = v
		}
		resolved["<LAMBDA_CDK_DESTINATION>"] = lambdaCDKDestination(target, headers)
	}
	if activation == ActivationWebServer {
		resolved["<LAMBDA_EXEC_WRAPPER>"] = lambdaWebServerWrapper
		resolved["<JAVA_LAMBDA_EXEC_WRAPPER>"] = lambdaWebServerWrapper
		resolved["<LAMBDA_CDK_MODE>"] = "mode: OdigosInstrumentationMode.WEB_SERVER,"
		resolved["<LAMBDA_CDK_IMPORT>"] = "OdigosLambdaInstrumentation, OdigosInstrumentationMode"
		resolved["<LAMBDA_SDK_INSTRUCTIONS>"] = "The selected SDK is loaded by /opt/odigos-webserver. Configuration field webWrapper preserves the function's original execution wrapper."
	}
	resolved[plan.PlaceholderPackageVersion] = config.CDKPackageVersion()
	return &baseapi.PlannedInstrumentation{Plan: pl, Resolved: resolved}, nil
}

// resolveLayer fills <OTEL_LAYER_ARN> with the version the connector would actually attach.
//
// The MARKER outranks the resolver, matching the observer's desiredLayerARN, and the order is not incidental:
// the marker records what the customer actually pinned from a previous set of instructions, so echoing it back
// keeps the plan consistent with the version their function is judged against. Resolving fresh would hand them
// a newer layer to apply and then report the change as drift against the recorded one.
//
// An unresolvable layer leaves the placeholder unresolved rather than erroring: no Odigos layer is published
// yet for any region, so this is the common case, and the artifacts' notes already say where to get the value.
func (p *LambdaPlanner) resolveLayer(ctx context.Context, fn *store.LambdaFunction) map[string]string {
	return p.resolveLayerForDistro(ctx, fn, "")
}
func (p *LambdaPlanner) resolveLayerForDistro(ctx context.Context, fn *store.LambdaFunction, distro string) map[string]string {
	if arn := store.OdigosLayerARN(fn.Layers); arn != "" && layerMatchesDistro(arn, fn.Runtime, firstArch(fn.Architecture), distro) {
		return map[string]string{PlaceholderLayerARN: arn}
	}
	arn, err := resolveLambdaLayer(ctx, p.layers, fn.Region, fn.Runtime, firstArch(fn.Architecture), distro, true)
	if err != nil || arn == "" {
		return nil
	}
	return map[string]string{PlaceholderLayerARN: arn}
}

// ECSFargatePlanner serves the Fargate instrumentation plan for one discovered service.
//
// No AWS calls: the resolved language, the runtime version and the container inventory are all written by
// discovery and the instrumentor, and this runs on the GraphQL read path.
type ECSFargatePlanner struct {
	// image is the odigos-fargate-agents image the staging container copies from, so a connector configured
	// with an override publishes instructions naming the image it would actually use.
	image string
}

// NewECSFargatePlanner builds the planner. image is config.FargateAgentsImage.
func NewECSFargatePlanner(image string) *ECSFargatePlanner { return &ECSFargatePlanner{image: image} }

// PlanFor implements baseapi.InstrumentationPlanner.
func (p *ECSFargatePlanner) PlanFor(_ context.Context, w baseapi.DiscoveredWorkload) (*baseapi.PlannedInstrumentation, error) {
	lang, version := fargatePlanRuntime(w)
	if lang == "" || !CoverableLanguage(lang) {
		// Go and every other eBPF-only runtime lands here, and so does a workload whose language could not be
		// determined. Both are already reported with the reason and the remedy (a runtime override), and
		// neither has instructions that would work.
		return nil, nil
	}
	// The version-templated agents (php, ruby) need a major.minor. Without one the plan keeps
	// <RUNTIME_VERSION> as a placeholder — which is honest and applicable — rather than refusing outright.
	if version == "" {
		version = plan.PlaceholderRuntimeVersion
	}

	pl, err := DesiredFargatePlan(lang, p.image, version)
	if err != nil {
		return nil, err
	}
	resolved := map[string]string{
		// The CDK artifacts name the package to install, and its version is this connector build's release
		// rather than anything in the customer's account — so it is resolved here instead of being left as a
		// blank they would have to guess, and guess wrong.
		plan.PlaceholderPackageVersion: config.CDKPackageVersion(),
	}
	if name := soleInstrumentableContainer(w); name != "" {
		resolved[plan.PlaceholderContainer] = name
	}
	return &baseapi.PlannedInstrumentation{Plan: pl, Resolved: resolved}, nil
}

// fargatePlanRuntime resolves the language and version to plan for, override first — matching
// resolveECSRuntime's precedence, so the plan names the agent the reconcile would inject rather than the one
// detection guessed and the customer overrode.
//
// The container fallback is what makes this work in MANUAL mode at all, and without it Fargate published no
// instructions there — the one mode they exist for. w.RuntimeLanguage is the service row's aggregate, and it
// used to have exactly one writer: instrumentService (fargate-task.go), which runs from the reconciler the
// coordinator never wires for a manual-mode type (activeReconcilers), leaving the field permanently empty for
// precisely the workloads that need a plan. Lambda never hit this because a function's runtime comes from the
// function itself. The observer now writes the aggregate too (recordObservedRuntime), so the first branch below
// usually answers; this one still stands behind it, for a row written by an older connector and for a pass that
// resolved per-container runtimes without agreeing on a workload-level one.
func fargatePlanRuntime(w baseapi.DiscoveredWorkload) (lang, version string) {
	if l := normalizeLanguage(w.Overrides.RuntimeLanguage); l != "" {
		return l, w.Overrides.RuntimeVersion
	}
	if l := normalizeLanguage(w.RuntimeLanguage); l != "" {
		return l, w.RuntimeVersion
	}
	return detectedContainerRuntime(w)
}

// detectedContainerRuntime is the language every instrumentable container in the task definition agrees on,
// and "" when they do not agree or none resolved.
//
// UNANIMITY is what makes consulting per-container values safe here, and it is the distinction from the
// per-container override this function used to refuse outright. Refusing was right against "pick one
// container's language and present it as the task's": the plan is per workload and a task definition can pack
// several applications, so choosing whichever came first would publish instructions that instrument one of
// them and silently leave the rest. Requiring every eligible container to resolve the same way cannot make
// that mistake — there is nothing to choose between — while a genuinely mixed task still returns "" and gets
// no plan, matching aggregateRuntime's "multiple".
//
// Each container is read with the same precedence resolveECSRuntime uses (its own override, else what was
// detected for it), because that is the agent the connector would judge that container against.
func detectedContainerRuntime(w baseapi.DiscoveredWorkload) (lang, version string) {
	if w.Details == nil {
		return "", ""
	}
	for _, c := range w.Details.Containers {
		if !c.Eligible {
			continue
		}
		l, v := normalizeLanguage(c.OverrideLanguage), c.OverrideVersion
		if l == "" {
			l, v = normalizeLanguage(c.RuntimeLanguage), c.RuntimeVersion
		}
		if l == "" {
			continue
		}
		if lang == "" {
			lang, version = l, v
			continue
		}
		if l != lang {
			return "", "" // several languages in one task: no single plan describes it
		}
		if v != version {
			// Same language, different versions. The language still has one plan; the version-templated
			// agents (php, ruby) fall back to their placeholder rather than naming one container's version.
			version = ""
		}
	}
	return lang, version
}

// soleInstrumentableContainer returns the container name when the workload has exactly one Odigos could
// instrument, and "" otherwise.
//
// Exactly one, deliberately. A task definition can pack several independently instrumentable applications, and
// resolving the placeholder would pick one of them for the customer — the container-scoped fragments would
// then instrument that one and silently leave the rest, which reads as success. Leaving it unresolved makes
// the choice visible, which is correct when there is one to make.
func soleInstrumentableContainer(w baseapi.DiscoveredWorkload) string {
	if w.Details == nil {
		return ""
	}
	var found string
	for _, c := range w.Details.Containers {
		if !c.Eligible {
			continue
		}
		if found != "" {
			return ""
		}
		found = c.Name
	}
	return found
}

func lambdaSnippetReplacements(lang, distro string) map[string]string {
	resolved := map[string]string{}
	d := recipeFor(lang, distro)
	resolved["<LAMBDA_EXEC_WRAPPER>"] = d.ExecWrapper
	resolved["<JAVA_LAMBDA_EXEC_WRAPPER>"] = d.ExecWrapper
	resolved["<LAMBDA_OTEL_DISTRO>"] = d.Name
	resolved["<LAMBDA_DISTRO_DESCRIPTION>"] = fmt.Sprintf("%s (%s). Collector pipelines and SDK defaults are expanded from compact configuration at startup.", d.DisplayName, d.Version)
	resolved["<LAMBDA_SDK_INSTRUCTIONS>"] = "The selected SDK is loaded by " + d.ExecWrapper + "."
	resolved["<LAMBDA_CDK_MODE>"] = "// Handler mode"
	resolved["<LAMBDA_CDK_IMPORT>"] = "OdigosLambdaInstrumentation"
	resolved["<LAMBDA_CDK_DESTINATION>"] = "{ endpoint: '<OTLP_ENDPOINT>', insecure: <OTLP_INSECURE>, traces: true, metrics: true, logs: true }"
	resolved["<LAMBDA_DISTRO_PREREQUISITES>"] = "Use the layer matching the selected upstream SDK, region and architecture."
	if d.Artifact != "" {
		resolved["<LAMBDA_DISTRO_PREREQUISITES>"] = "ADOT requires two layer slots: attach the matching ADOT SDK and collector layers, replacing previous Odigos SDK/collector layers. Handler mode only."
		if d.ApplicationSignals {
			resolved["<LAMBDA_DISTRO_PREREQUISITES>"] += " Enable destination traces and metrics. Automatic mode attaches the existing CloudWatchLambdaApplicationSignalsExecutionRolePolicy and enables Application Signals discovery. For manual deployment, configure that policy and discovery first. AWS also receives service telemetry."
		}
	}
	c := lambdaBootstrapConfig{Version: lambdaConfigVersion, Service: plan.PlaceholderServiceName, Signals: 7, Endpoint: plan.PlaceholderOTLPEndpoint, ApplicationSignals: d.ApplicationSignals}
	c.FunctionLogs = lambdaFunctionLogs(lang, d.ExecWrapper)
	raw, _ := json.Marshal(c)
	text := strings.TrimSuffix(string(raw), "}") + `,"insecure":<OTLP_INSECURE>}`
	for k, v := range lambdaConfigSnippetValues(text) {
		resolved[k] = v
	}
	return resolved
}

func lambdaCDKDestination(target domaindest.OTLPTarget, headers map[string]string) string {
	destination := map[string]any{"traces": target.Traces, "metrics": target.Metrics, "logs": target.Logs}
	if target.Endpoint != "" {
		destination["endpoint"] = target.Endpoint
		destination["insecure"] = target.Insecure
	} else {
		destination["httpEndpoint"] = target.HTTPEndpoint
	}
	if len(headers) != 0 {
		destination["headers"] = headers
	}
	return snippetJSON(destination)
}

func lambdaLayerSnippetReplacements(language, distro, layer, collector string) map[string]string {
	if layer == "" {
		layer = PlaceholderLayerARN
	}
	arns := []string{layer}
	marker := baseapi.IaCMarker{Version: baseapi.IaCMarkerVersion, Layer: layer}
	if recipeFor(language, distro).Artifact != "" {
		if collector == "" {
			collector = "<OTEL_COLLECTOR_LAYER_ARN>"
		}
		arns = append(arns, collector)
		marker.CollectorLayer = collector
	}
	encoded := snippetJSON(marker)
	quoted := snippetJSON(encoded)
	values := []string{}
	for _, arn := range arns {
		values = append(values, snippetJSON(arn))
	}
	tags := map[string]string{store.TagLambdaIaC: "v1", store.TagLambdaSDK: layer}
	tf := []string{fmt.Sprintf("%q = %q", store.TagLambdaIaC, "v1"), fmt.Sprintf("%q = %s", store.TagLambdaSDK, snippetJSON(layer))}
	cf := []string{fmt.Sprintf("- { Key: %s, Value: %s }", snippetJSON(store.TagLambdaIaC), snippetJSON("v1")), fmt.Sprintf("- { Key: %s, Value: %s }", snippetJSON(store.TagLambdaSDK), snippetJSON(layer))}
	if len(arns) > 1 {
		tags[store.TagLambdaCollector] = collector
		tf = append(tf, fmt.Sprintf("%q = %s", store.TagLambdaCollector, snippetJSON(collector)))
		cf = append(cf, fmt.Sprintf("- { Key: %s, Value: %s }", snippetJSON(store.TagLambdaCollector), snippetJSON(collector)))
	}
	return map[string]string{"<LAMBDA_LAYER_ARNS_JSON>": strings.Join(values, ", "), "<LAMBDA_LAYER_ARNS_SHELL>": strings.Join(values, " "), "<LAMBDA_IAC_MARKER>": string(encoded), "<LAMBDA_IAC_MARKER_QUOTED>": string(quoted), "<LAMBDA_TAGS_TERRAFORM>": strings.Join(tf, "\n            "), "<LAMBDA_TAGS_CLOUDFORMATION>": strings.Join(cf, "\n          "), "<LAMBDA_TAGS_JSON>": snippetJSON(tags)}
}

// Keep angle-bracket placeholders readable for the UI renderer; json.Marshal's
// HTML escaping otherwise hides unresolved ARNs as literal Unicode escapes.
func snippetJSON(value any) string {
	var b bytes.Buffer
	encoder := json.NewEncoder(&b)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(b.String(), "\n")
}

func lambdaConfigSnippetValues(raw string) map[string]string {
	quoted, _ := json.Marshal(raw)
	return map[string]string{"<ODIGOS_LAMBDA_CONFIG>": raw, "<LAMBDA_CONFIG_QUOTED>": string(quoted)}
}
