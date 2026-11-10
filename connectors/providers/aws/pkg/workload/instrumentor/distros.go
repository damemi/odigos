package instrumentor

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	baseapi "github.com/odigos-io/odigos/connectors/base/api"
	domaindest "github.com/odigos-io/odigos/connectors/base/domain/destination"
	"github.com/odigos-io/odigos/connectors/base/domain/plan"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/workload/store"
)

const JavaAgentDistro = "java-lambda-otel-agent"
const JavaWrapperDistro = "java-lambda-otel-wrapper"

//go:embed data/lambda_distros.json
var lambdaDistroJSON []byte

type lambdaRecipe struct {
	baseapi.InstrumentationDistro
	ExecWrapper          string            `json:"execWrapper"`
	Artifact             string            `json:"artifact,omitempty"`
	ApplicationSignals   bool              `json:"applicationSignals,omitempty"`
	CompatibleRuntimes   []string          `json:"compatibleRuntimes,omitempty"`
	Environment          map[string]string `json:"environment,omitempty"`
	BootstrapEnvironment map[string]string `json:"bootstrapEnvironment,omitempty"`
}

func lambdaRecipes() []lambdaRecipe {
	var all []lambdaRecipe
	if err := json.Unmarshal(lambdaDistroJSON, &all); err != nil {
		panic(err)
	}
	return all
}

// LambdaDistros advertises one concrete activation recipe per supported SDK selection.
func LambdaDistros(language, activation string) []baseapi.InstrumentationDistro {
	out := []baseapi.InstrumentationDistro{}
	for _, d := range lambdaRecipes() {
		if d.Language != language {
			continue
		}
		if activation == ActivationWebServer && (d.Artifact != "" || language == "ruby" || (language == "java" && d.Name != JavaAgentDistro)) {
			continue
		}
		out = append(out, d.InstrumentationDistro)
	}
	return out
}

func ResolveLambdaDistro(language, activation, name string) (baseapi.InstrumentationDistro, error) {
	// Former script-name overrides retain their meaning, but are not advertised as SDKs.
	if language == "java" {
		for _, d := range lambdaRecipes() {
			if "/opt/"+name == d.ExecWrapper {
				name = d.Name
				break
			}
		}
	}
	for _, d := range LambdaDistros(language, activation) {
		if name == d.Name || (name == "" && d.IsDefault) {
			return d, nil
		}
	}
	return baseapi.InstrumentationDistro{}, fmt.Errorf("unsupported instrumentation distro %q for Lambda %s (%s)", name, language, activation)
}

func DesiredLambdaPlanForDistro(lang, activation, name string) (plan.Plan, error) {
	d, err := ResolveLambdaDistro(lang, activation, name)
	if err != nil {
		return plan.Plan{}, err
	}
	p, err := DesiredLambdaPlan(lang, activation)
	if err != nil {
		return p, err
	}
	if recipe := recipeFor(lang, d.Name); recipe.Artifact != "" {
		for i := range p.Fragments {
			if p.Fragments[i].Kind == plan.KindLayer {
				p.Fragments[i].Layer.Placeholder = "<OTEL_LAYER_ARN>"
			}
		}
		p.Fragments = append(p.Fragments, plan.Fragment{Anchor: plan.Anchor{Scope: plan.ScopeWorkload, Field: plan.FieldLayers}, Kind: plan.KindLayer, Layer: &plan.Layer{Placeholder: "<OTEL_COLLECTOR_LAYER_ARN>"}})
		p.Placeholders = append(p.Placeholders, plan.Placeholder{Token: "<OTEL_COLLECTOR_LAYER_ARN>", Description: "The Odigos ADOT collector layer version ARN for the same region and architecture. Attach it alongside the ADOT SDK layer."})
		p.Notes = append(p.Notes, "Use the matching ADOT layer, replacing any previous Odigos SDK layer. ADOT supports handler mode only.")
		if recipe.ApplicationSignals {
			p.Notes = append(p.Notes, "Application Signals additionally sends service telemetry to AWS. For this manual deployment, enable Application Signals service discovery and grant the existing CloudWatchLambdaApplicationSignalsExecutionRolePolicy to the function execution role. The selected destination must enable traces and metrics.")
		}
	}
	wrapper := recipeFor(lang, d.Name).ExecWrapper
	if activation == ActivationWebServer {
		wrapper = lambdaWebServerWrapper
	}
	for i := range p.Fragments {
		if p.Fragments[i].Kind == plan.KindEnv {
			p.Fragments[i].Env = []plan.EnvVar{
				{Name: EnvLambdaConfig, Value: "<ODIGOS_LAMBDA_CONFIG>", Merge: plan.MergeSet},
				{Name: store.EnvExecWrapper, Value: wrapper, Merge: plan.MergeSet},
			}
		}
	}
	filteredPlaceholders := p.Placeholders[:0]
	for _, placeholder := range p.Placeholders {
		if placeholder.Token != plan.PlaceholderServiceName && placeholder.Token != plan.PlaceholderOTLPEndpoint && placeholder.Token != plan.PlaceholderOTLPInsecure {
			filteredPlaceholders = append(filteredPlaceholders, placeholder)
		}
	}
	p.Placeholders = filteredPlaceholders
	p.Placeholders = append(p.Placeholders, plan.Placeholder{Token: "<ODIGOS_LAMBDA_CONFIG>", Description: "Compact destination and SDK settings. The layer builds collector pipelines and process environment at startup."})
	p.Notes = append(p.Notes, "Add Lambda tags odigos:iac=v1 and odigos:sdk-layer=<OTEL_LAYER_ARN>. ADOT also requires an odigos:collector-layer tag matching the attached collector ARN. Keep customer SDK environment variables unchanged. The Odigos layer applies its SDK settings only at process startup. For Web Adapter activation, use the Odigos web-server wrapper and record the original execution wrapper in configuration field webWrapper.")
	p.Sort()
	return p, p.Validate()
}

//go:embed data/fargate_distros.json
var fargateDistroJSON []byte

// FargateDistros advertises only recipes staged by the connector, never the global K8s/eBPF list.
func FargateDistros() []baseapi.InstrumentationDistro {
	var all []baseapi.InstrumentationDistro
	if err := json.Unmarshal(fargateDistroJSON, &all); err != nil {
		panic(err)
	}
	return all
}

// FargateDistroMatchesLanguage accepts the same runtime aliases as the injection recipes.
func FargateDistroMatchesLanguage(name, language string) bool {
	lang := normalizeLanguage(language)
	for _, d := range FargateDistros() {
		if d.Name == name && d.Language == lang {
			return true
		}
	}
	return false
}

func recipeFor(language, name string) lambdaRecipe {
	for _, d := range lambdaRecipes() {
		if d.Language == language && (d.Name == name || (name == "" && d.IsDefault)) {
			return d
		}
	}
	return lambdaRecipe{}
}

// ValidateLambdaDistroRuntime prevents attaching an SDK outside its supported runtime matrix.
func ValidateLambdaDistroRuntime(runtime, activation, name string) error {
	d, err := ResolveLambdaDistro(LanguageFromRuntime(runtime), activation, name)
	if err != nil {
		return err
	}
	recipe := recipeFor(d.Language, d.Name)
	if len(recipe.CompatibleRuntimes) > 0 && !slices.Contains(recipe.CompatibleRuntimes, runtime) {
		return fmt.Errorf("Lambda distro %s does not support runtime %s", d.Name, runtime)
	}
	return nil
}

func lambdaArtifact(runtime, distro string) string {
	language := LanguageFromRuntime(runtime)
	if d := recipeFor(language, distro); d.Artifact != "" {
		return d.Artifact
	}
	return language
}

// Per-signal endpoints are explicit for ADOT: its Lambda configurator otherwise redirects
// traces to X-Ray even when the generic OTLP endpoint is set.
func lambdaDistributionEnv(language, distro string) map[string]string {
	env := map[string]string{}
	for k, v := range recipeFor(language, distro).Environment {
		env[k] = v
	}
	return env
}

func lambdaDistributionKeys() []string {
	keys := []string{}
	for _, d := range lambdaRecipes() {
		for k := range mergedRecipeEnvironment(d) {
			if !slices.Contains(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

func applyLambdaDistributionEnv(env map[string]string, language, distro string) {
	for _, key := range lambdaDistributionKeys() {
		// Transport keys have already been cleared by the shared destination policy.
		if (key != "OTEL_NODE_ENABLED_INSTRUMENTATIONS" || language != "nodejs") && key != "OTEL_SDK_DISABLED" && key != "OTEL_EXPORTER_OTLP_PROTOCOL" && (key != "OTEL_PYTHON_LOGGING_AUTO_INSTRUMENTATION_ENABLED" || language != "python") {
			delete(env, key)
		}
	}
	for key := range recipeFor(language, distro).BootstrapEnvironment {
		delete(env, key)
	}
	for key, value := range lambdaDistributionEnv(language, distro) {
		env[key] = value
	}
}

func lambdaSignalConfigurationMatches(env map[string]string, dest domaindest.OTLPTarget, language, distro string) bool {
	expected := dest.LambdaSignalActivation(language)
	for key := range recipeFor(language, distro).BootstrapEnvironment {
		delete(expected, key)
	}
	overrides := lambdaDistributionEnv(language, distro)
	for k, v := range overrides {
		expected[k] = v
	}
	for k, v := range expected {
		if env[k] != v {
			return false
		}
	}
	for _, k := range domaindest.SignalTransportOverrides() {
		if _, overridden := overrides[k]; !overridden {
			if _, exists := env[k]; exists {
				return false
			}
		}
	}
	for _, k := range lambdaDistributionKeys() {
		if _, managed := expected[k]; !managed {
			if _, exists := env[k]; exists {
				return false
			}
		}
	}
	return true
}

type distroLayerResolver interface {
	ResolveDistro(context.Context, string, string, string, string) (string, error)
	DiscoverDistro(context.Context, string, string, string, string) (string, error)
}

func resolveLambdaLayer(ctx context.Context, layers LayerResolver, region, runtime, arch, distro string, readOnly bool) (string, error) {
	if recipeFor(LanguageFromRuntime(runtime), distro).Artifact == "" {
		if readOnly {
			return NonPublishingLayerARN(ctx, layers, region, runtime, arch)
		}
		return layers.Resolve(ctx, region, runtime, arch)
	}
	if resolver, ok := layers.(distroLayerResolver); ok {
		if readOnly {
			return resolver.DiscoverDistro(ctx, region, runtime, arch, distro)
		}
		return resolver.ResolveDistro(ctx, region, runtime, arch, distro)
	}
	return "", fmt.Errorf("%w: resolver does not support Lambda distro %s", ErrLayerUnavailable, distro)
}

func layerMatchesDistro(arn, runtime, arch, distro string) bool {
	name, _, ok := store.ParseLayerVersionARN(arn)
	return ok && name == publishedLayerName(lambdaArtifact(runtime, distro), arch)
}

type collectorLayerResolver interface {
	ResolveCollector(context.Context, string, string, string, bool) (string, error)
}

func resolveLambdaCollector(ctx context.Context, layers LayerResolver, fn store.LambdaFunction, distro string, readOnly bool) (string, error) {
	if recipeFor(LanguageFromRuntime(fn.Runtime), distro).Artifact == "" {
		return "", nil
	}
	if r, ok := layers.(collectorLayerResolver); ok {
		return r.ResolveCollector(ctx, fn.Region, fn.Runtime, firstArch(fn.Architecture), readOnly)
	}
	return "", fmt.Errorf("%w: resolver does not support the ADOT collector layer", ErrLayerUnavailable)
}
func isCollectorLayer(arn, arch string) bool {
	name, _, ok := store.ParseLayerVersionARN(arn)
	return ok && name == publishedLayerName("adot-collector", arch)
}
func hasCollectorLayer(layers []string, arch string) bool {
	for _, arn := range layers {
		if isCollectorLayer(arn, arch) {
			return true
		}
	}
	return false
}
func recordedCollectorLayer(env map[string]string) string {
	for _, key := range []string{store.EnvOdigosManaged, store.EnvOdigosIaC} {
		var marker struct {
			CollectorLayer string `json:"collectorLayer"`
		}
		if json.Unmarshal([]byte(env[key]), &marker) == nil && marker.CollectorLayer != "" {
			return marker.CollectorLayer
		}
	}
	return ""
}
func layerFamilyMatches(arn, runtime, arch, distro string) bool {
	if recipeFor(LanguageFromRuntime(runtime), distro).Artifact != "" {
		return layerMatchesDistro(arn, runtime, arch, distro)
	}
	name, _, ok := store.ParseLayerVersionARN(arn)
	return !ok || !strings.HasPrefix(name, store.OdigosLayerNamePrefix+"adot-")
}

func mergedRecipeEnvironment(d lambdaRecipe) map[string]string {
	env := map[string]string{}
	for k, v := range d.Environment {
		env[k] = v
	}
	for k, v := range d.BootstrapEnvironment {
		env[k] = v
	}
	return env
}
