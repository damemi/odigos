package instrumentor

import (
	"context"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
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
	store  *store.Store
	layers LayerResolver
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
	pl, err := DesiredLambdaPlan(lang, activation)
	if err != nil {
		return nil, err
	}
	resolved := p.resolveLayer(ctx, fn)
	if resolved == nil {
		resolved = map[string]string{}
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
	if arn := store.OdigosLayerARN(fn.Layers); arn != "" {
		return map[string]string{PlaceholderLayerARN: arn}
	}
	arn, err := NonPublishingLayerARN(ctx, p.layers, fn.Region, fn.Runtime, firstArch(fn.Architecture))
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
