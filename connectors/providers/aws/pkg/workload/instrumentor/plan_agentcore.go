package instrumentor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	dest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/plan"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/agentcore"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/config"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/workload/store"
)

// AgentCore manual-mode plan: the same environment recipe automatic mode applies and the observer compares, plus the
// IaC marker and the package contract the observer needs to identify the runtime's language. Values that depend on
// the distro, the destination or the runtime are tokens, so both distros of a language share one set of published
// documents; PlanFor resolves them for the runtime at hand from the recipe itself.
const (
	agentCoreTokenDistro        = "<AGENTCORE_DISTRO>"
	agentCoreTokenServiceName   = "<AGENTCORE_SERVICE_NAME>"
	agentCoreTokenEndpoint      = "<AGENTCORE_OTLP_ENDPOINT>"
	agentCoreTokenHeaders       = "<OTLP_AUTH_HEADERS>"
	agentCoreTokenPythonDistro  = "<AGENTCORE_PYTHON_DISTRO>"
	agentCoreTokenPythonConfig  = "<AGENTCORE_PYTHON_CONFIGURATOR>"
	agentCoreTokenPythonDisable = "<AGENTCORE_PYTHON_DISABLED_INSTRUMENTATIONS>"
	agentCoreTokenImage         = "<AGENTCORE_AGENTS_IMAGE>"
	agentCoreTokenRuntimeID     = "<AGENTCORE_RUNTIME_ID>"
	agentCoreTokenRuntimeName   = "<AGENTCORE_RUNTIME_NAME>"
	agentCoreTokenCDKDistro     = "<AGENTCORE_CDK_DISTRIBUTION>"
)

var agentCoreSignals = []string{"TRACES", "METRICS", "LOGS"}

func agentCoreExporterToken(signal string) string { return "<AGENTCORE_" + signal + "_EXPORTER>" }
func agentCoreSignalToken(signal string) string   { return "<AGENTCORE_" + signal + "_ENABLED>" }

// agentCoreAgentsImage is the SDK carrier manual preparation copies from. The connector never pulls it.
func agentCoreAgentsImage() string {
	if image := os.Getenv("ODIGOS_AGENTCORE_AGENTS_IMAGE"); image != "" {
		return image
	}
	return "registry.odigos.io/odigos-agentcore-agents:" + config.ArtifactsVersion
}

// agentCoreIaCMarkerValue marks customer-owned instrumentation. The observer only checks its presence; the distro and
// recipe say what was applied.
func agentCoreIaCMarkerValue(distro string) string {
	return fmt.Sprintf(`{"distro":%q,"recipe":%q,"v":%d}`, distro, agentCoreRecipeVersion, baseapi.IaCMarkerVersion)
}

// agentCorePackageValue is the preparation contract (agentcore.Package) for an artifact carrying one distro. Written
// out rather than marshalled: encoding/json would escape the token's angle brackets.
func agentCorePackageValue(language, distro string) string {
	if language == "nodejs" {
		return fmt.Sprintf(`{"v":1,"language":%q,"distros":[%q],"moduleFormat":"commonjs"}`, language, distro)
	}
	return fmt.Sprintf(`{"v":1,"language":%q,"distros":[%q]}`, language, distro)
}

// agentCoreAdoptPackageValue is the preparation contract for an artifact carrying ADOT only (the ADOT route). It
// carries no token, so it is marshalled. Manual preparation keeps the contract form that names ADOT through Mode,
// which agentCorePackageSDK reads the same as automatic installation's SDK field.
func agentCoreAdoptPackageValue(language string) string {
	p := agentcore.Package{Version: 1, Language: language, Distros: []string{language + "-agentcore-adot"}, Mode: agentCoreAdopt}
	if language == "nodejs" {
		p.ModuleFormat = "commonjs"
	}
	raw, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

// AgentCoreManualPlan is the manual-mode plan for one distro, with the tokens above in place of resolved values. It
// describes route A: the artifact carries the Odigos package (or the carrier bundle) and this environment switches
// its bootstrap on.
func AgentCoreManualPlan(d baseapi.InstrumentationDistro) (plan.Plan, error) {
	env := agentCoreOdigosEnv(d, agentCoreTokenServiceName, agentCoreTokenTarget(), nil)
	agentCoreTokenizeExport(env)
	env[agentcore.DistroEnv] = agentCoreTokenDistro
	if d.Language == "python" {
		env["OTEL_PYTHON_DISTRO"] = agentCoreTokenPythonDistro
		env["OTEL_PYTHON_CONFIGURATOR"] = agentCoreTokenPythonConfig
		env["OTEL_PYTHON_DISABLED_INSTRUMENTATIONS"] = agentCoreTokenPythonDisable
	}
	env[agentcore.PackageEnv] = agentCorePackageValue(d.Language, agentCoreTokenDistro)
	env[store.EnvOdigosIaC] = agentCoreIaCMarkerValue(agentCoreTokenDistro)
	return agentCorePlanFrom(env, d.Language, d.Name, agentCoreManualNotes)
}

// AgentCoreAdoptPlan is the manual-mode plan for route B: the artifact carries ADOT only, started through its own
// launcher, and this environment configures it. The distro is fixed and the distro-dependent values are literals;
// the destination and service tokens are route A's, so one set of resolved values serves both routes. The
// environment is agentCoreADOTEnv, which is what the observer compares a
// runtime declaring mode adopt against.
func AgentCoreAdoptPlan(language string) (plan.Plan, error) {
	d, err := ResolveAgentCoreDistro(language, language+"-agentcore-adot")
	if err != nil {
		return plan.Plan{}, err
	}
	env := agentCoreADOTEnv(d, agentCoreTokenServiceName, agentCoreTokenTarget(), nil)
	agentCoreTokenizeExport(env)
	env[agentcore.PackageEnv] = agentCoreAdoptPackageValue(language)
	env[store.EnvOdigosIaC] = agentCoreIaCMarkerValue(d.Name)
	return agentCorePlanFrom(env, language, string(agentCoreSDKADOT), agentCoreAdoptNotes(language))
}

func agentCoreTokenTarget() dest.OTLPTarget {
	return dest.OTLPTarget{HTTPEndpoint: agentCoreTokenEndpoint, Traces: true, Metrics: true, Logs: true}
}

// agentCoreTokenizeExport replaces the destination-dependent values with their tokens.
func agentCoreTokenizeExport(env map[string]string) {
	for _, signal := range agentCoreSignals {
		env["OTEL_"+signal+"_EXPORTER"] = agentCoreExporterToken(signal)
	}
	env["OTEL_EXPORTER_OTLP_HEADERS"] = agentCoreTokenHeaders
}

func agentCorePlanFrom(env map[string]string, language, activation string, notes []string) (plan.Plan, error) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vars := make([]plan.EnvVar, 0, len(keys))
	for _, k := range keys {
		merge := plan.MergeSet
		if env[k] == "" {
			merge = plan.MergeRemove
		}
		vars = append(vars, plan.EnvVar{Name: k, Value: env[k], Merge: merge})
	}
	p := plan.Plan{
		Type: agentcore.Kind, Language: language, Mode: plan.ModePackagedSDK, Activation: activation,
		Fragments: []plan.Fragment{{Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldEnv}, Kind: plan.KindEnv, Env: vars}},
		Notes:     notes,
	}
	// Declared from what the plan carries: the adopt plan uses a subset of the package plan's tokens.
	for _, ph := range agentCorePlaceholders(language) {
		if plan.UsesPlaceholder(p, ph.Token) {
			p.Placeholders = append(p.Placeholders, ph)
		}
	}
	return p, p.Validate()
}

// DesiredAgentCorePlans is every manual plan the connector can serve: per language, route A (the Odigos package)
// for each distro and route B (ADOT).
func DesiredAgentCorePlans() ([]plan.Plan, error) {
	var out []plan.Plan
	for _, language := range []string{"python", "nodejs"} {
		for _, d := range AgentCoreDistros(language) {
			p, err := AgentCoreManualPlan(d)
			if err != nil {
				return nil, fmt.Errorf("AgentCore plan %s: %w", d.Name, err)
			}
			out = append(out, p)
		}
		p, err := AgentCoreAdoptPlan(language)
		if err != nil {
			return nil, fmt.Errorf("AgentCore adopt plan %s: %w", language, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// agentCoreResolved maps every token to its value for one runtime, read from the recipe the observer compares
// against, so the instructions and the conformance check cannot disagree. The header token always stays a
// placeholder: credentials never appear in a generated document, and an empty value cannot be substituted.
func agentCoreResolved(d baseapi.InstrumentationDistro, service string, t dest.OTLPTarget) map[string]string {
	env := agentCoreOdigosEnv(d, service, t, nil)
	out := map[string]string{
		agentCoreTokenDistro:           d.Name,
		agentCoreTokenServiceName:      service,
		agentCoreTokenEndpoint:         env["OTEL_EXPORTER_OTLP_ENDPOINT"],
		agentCoreTokenImage:            agentCoreAgentsImage(),
		agentCoreTokenCDKDistro:        strings.ToUpper(strings.TrimPrefix(d.Name, d.Language+"-agentcore-")),
		plan.PlaceholderPackageVersion: config.CDKPackageVersion(),
	}
	for _, signal := range agentCoreSignals {
		out[agentCoreExporterToken(signal)] = env["OTEL_"+signal+"_EXPORTER"]
		out[agentCoreSignalToken(signal)] = fmt.Sprint(env["OTEL_"+signal+"_EXPORTER"] == "otlp")
	}
	if d.Language == "python" {
		out[agentCoreTokenPythonDistro] = env["OTEL_PYTHON_DISTRO"]
		out[agentCoreTokenPythonConfig] = env["OTEL_PYTHON_CONFIGURATOR"]
		out[agentCoreTokenPythonDisable] = env["OTEL_PYTHON_DISABLED_INSTRUMENTATIONS"]
	}
	return out
}

var agentCoreManualNotes = []string{
	"Prepare the artifact with the Odigos package (or the carrier bundle) first; environment settings alone install nothing. The bootstrap stays dormant until this environment enables it.",
	"Apply these changes through the runtime's deployment owner and keep its other settings. Never set ODIGOS_MANAGED: that marker belongs to connector-managed runtimes.",
	"Prompts, responses and tool contents stay disabled. Export goes directly to the global OTLP/HTTP destination.",
	"Deploy a new version, wait for DEFAULT to serve it, and invoke with a new session. Endpoints pinned to older versions keep them until updated.",
	"The observer verifies configuration, not delivery. To remove, delete these settings and restore the original artifact through the same deployment owner.",
}

// agentCoreAdoptNotes are route B's caveats. The preparation and the missing service span differ from route A;
// the deployment notes are shared.
func agentCoreAdoptNotes(language string) []string {
	start := "Add aws-opentelemetry-distro to the agent's dependencies and start through its launcher: a direct-code ZIP's entry point becomes [\"opentelemetry-instrument\", <your entry point>], a container's CMD is prefixed with opentelemetry-instrument."
	if language == "nodejs" {
		start = "Add @aws/aws-distro-opentelemetry-node-autoinstrumentation to the agent's dependencies and load it before the application: append --require @aws/aws-distro-opentelemetry-node-autoinstrumentation/register to NODE_OPTIONS (the runtime's environment, or the image's)."
	}
	return append([]string{
		start + " No Odigos package is needed, and only the ADOT distribution can be adopted.",
		"ADOT cannot root an invocation at the agent, so AgentCore's own service span stays missing from the trace. Route A (the Odigos package or carrier bundle) adds it.",
	}, agentCoreManualNotes[1:]...)
}

func agentCorePlaceholders(language string) []plan.Placeholder {
	out := []plan.Placeholder{
		{Token: agentCoreTokenDistro, Description: "The selected Odigos AgentCore distribution, for example " + language + "-agentcore-adot."},
		{Token: agentCoreTokenServiceName, Description: "The service name the runtime reports: the source's reported service name, or the runtime name."},
		{Token: agentCoreTokenEndpoint, Description: "The global OTLP/HTTP destination's base URL, without a per-signal /v1 path."},
		{Token: agentCoreTokenHeaders, Description: "Destination authentication as percent-encoded OTLP header key=value pairs. Supply it through your deployment's secret mechanism; leave it empty when the destination needs none."},
	}
	for _, signal := range agentCoreSignals {
		out = append(out, plan.Placeholder{Token: agentCoreExporterToken(signal), Description: "otlp when the destination receives " + strings.ToLower(signal) + ", otherwise none."})
	}
	if language == "python" {
		out = append(out,
			plan.Placeholder{Token: agentCoreTokenPythonDistro, Description: "aws_distro for ADOT, distro for OpenLLMetry."},
			plan.Placeholder{Token: agentCoreTokenPythonConfig, Description: "aws_configurator for ADOT, configurator for OpenLLMetry."},
			plan.Placeholder{Token: agentCoreTokenPythonDisable, Description: "The instrumentations the selected distribution disables to avoid duplicate spans."},
		)
	}
	return out
}

type AgentCorePlanner struct{ store agentCoreStore }

func NewAgentCorePlanner(st agentCoreStore) *AgentCorePlanner { return &AgentCorePlanner{store: st} }

func (p *AgentCorePlanner) PlanFor(ctx context.Context, w baseapi.DiscoveredWorkload) (*baseapi.PlannedInstrumentation, error) {
	runtimes, err := p.store.ListAgentCore(ctx)
	if err != nil {
		return nil, err
	}
	var runtime *agentcore.Runtime
	for i := range runtimes {
		if runtimes[i].ARN == w.ExternalID {
			runtime = &runtimes[i]
			break
		}
	}
	if runtime == nil {
		return nil, nil
	}
	language := runtime.Language
	if w.Overrides.RuntimeLanguage != "" {
		language = w.Overrides.RuntimeLanguage
	}
	distro, err := ResolveAgentCoreDistro(language, w.Overrides.OtelDistroName)
	if err != nil {
		return nil, err
	}
	target, found, err := p.store.OTLPExportTarget(ctx)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("configure a global OTLP/HTTP destination for AgentCore instructions")
	}
	if err := agentCoreTarget(target); err != nil {
		return nil, err
	}
	// Destination credentials are never read here: the header token stays a placeholder the customer fills through
	// their own deployment secret mechanism.
	// Route A (the Odigos package) is the primary instruction set. A runtime whose preparation contract already
	// declares ADOT is judged against the ADOT recipe, so it is described by route B's plan; the resolved values
	// below serve either, and the published documents carry both routes.
	var pl plan.Plan
	if agentCorePackageSDK(runtime.Package) == agentCoreSDKADOT && distro.Name == language+"-agentcore-adot" {
		pl, err = AgentCoreAdoptPlan(language)
	} else {
		pl, err = AgentCoreManualPlan(distro)
	}
	if err != nil {
		return nil, err
	}
	// The observer compares against the same name: the source's reported service name, else the runtime name.
	service := w.ReportedServiceName()
	if service == "" {
		service = runtime.Name
	}
	resolved := agentCoreResolved(distro, service, target)
	resolved[agentCoreTokenRuntimeID] = runtime.ID
	resolved[agentCoreTokenRuntimeName] = runtime.Name
	return &baseapi.PlannedInstrumentation{Plan: pl, Resolved: resolved}, nil
}
