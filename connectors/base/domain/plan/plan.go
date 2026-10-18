// Package plan describes the instrumentation a workload needs as an ordered set of ANCHORED FRAGMENTS —
// the model behind the instructions a manual (IaC) mode customer applies in their own deployment
// definition.
//
// It exists because the obvious alternative does not work. Handing the customer a generated task
// definition / service object would make Odigos a second owner of a document their pipeline already owns,
// and for the whole-object declarative platforms it is not even expressible: Cloud Run
// (google_cloud_run_v2_service, `gcloud run services replace`) and ARM/Bicep admit no per-container or
// per-env-var child resource to append to, so there is nothing to hand over but the entire object. What a
// customer can always act on is "add THIS at THAT path", which is what a Fragment is.
//
// The package is deliberately pure: no I/O, no provider SDK types, no cloud calls. A provider builds a Plan
// from the same constants and recipes its injector uses (see each provider's DesiredPlans), and the base
// owns what must not be re-derived four times:
//
//   - LOADER-VARIABLE MERGE SEMANTICS. JAVA_TOOL_OPTIONS, PYTHONPATH, NODE_OPTIONS and friends already carry
//     customer values. Rendering ours as a literal silently discards theirs, and rendering it on the wrong
//     SIDE is just as wrong in a way that is much harder to see — Lambda PREPENDS to PYTHONPATH so the
//     layer's OTel packages and sitecustomize win over the app's own site-packages, while the staged-agent
//     platforms APPEND so the customer's own settings keep precedence. A Fragment therefore records the
//     direction explicitly (Merge + Separator) instead of leaving each provider's instructions to imply it.
//   - The vocabulary and ordering a customer reads, so four providers do not grow four ways of saying
//     "mount the agent volume".
//
// Rendering: Steps and Prose are provider-free and complete here. Per-variant artifacts (Terraform,
// CloudFormation, Bicep, CLI) are declared as templates in each provider's spec YAML — that is what the
// existing provider catalog already surfaces to the create-connector UI with no schema change — and
// RequiredLiterals is what a provider's test uses to prove those templates still say what this plan means.
package plan

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Merge is how a fragment's value combines with a value the customer's deployment definition may already
// carry at the same key.
//
// MergeAppend / MergePrepend are the reason this type exists. They must be rendered as an INSTRUCTION
// ("append this to whatever you already have"), never as a finished literal: a literal is correct only for
// the customer who happens to have nothing set, and silently destroys the value of every customer who does.
// Validate rejects a fragment that asks for either without a Separator, because "join them" with no joiner
// is not a renderable instruction.
type Merge string

const (
	// MergeSet: the key is ours alone; whatever is there is replaced. Correct for OTEL_SERVICE_NAME,
	// exporter selectors, the ownership marker — keys a customer has no reason to be setting, and where
	// preserving a stale value would misreport the workload.
	MergeSet Merge = "set"
	// MergeAppend: the customer's existing value first, then ours, joined by Separator. Their settings win.
	MergeAppend Merge = "append"
	// MergePrepend: ours first, then the customer's existing value. Ours wins — used where load ORDER is the
	// point (PYTHONPATH: the agent's packages and sitecustomize must resolve before the app's own).
	MergePrepend Merge = "prepend"
	// MergeRemove: the variable must NOT be set. It is a first-class instruction rather than a footnote
	// because the conformance check treats it as one: a variable the connector deletes is compared against
	// empty, so a workload that still carries it is reported as drifted. Leaving "and remove these" to prose
	// would produce a customer who applied every fragment correctly and is told their deployment is broken.
	//
	// A MergeRemove entry carries no Value, which is the one exception to the empty-value rule.
	MergeRemove Merge = "remove"
)

// mergeVerbs is the customer-facing phrasing for each direction, and the reason Merge is a closed set: the
// prose a customer follows has to be unambiguous about which side our value goes on.
var mergeVerbs = map[Merge]string{
	MergeSet:     "set",
	MergeAppend:  "append to",
	MergePrepend: "prepend to",
	MergeRemove:  "remove",
}

// Scope is WHAT a fragment's anchor is relative to.
type Scope string

const (
	// ScopeWorkload anchors at the deployment definition itself — the task definition, the service
	// template, the function. Shared things live here: the agent volume, the helper container, the layer.
	ScopeWorkload Scope = "workload"
	// ScopeContainer anchors inside one named container. Per-application things live here: activation env,
	// the volume mount, the startup dependency. Validate requires a container name, because a
	// container-scoped fragment with no container is an instruction the customer cannot follow — and on a
	// multi-container task definition (six applications in six languages is a real shape) guessing is worse
	// than refusing.
	ScopeContainer Scope = "container"
)

// Field is the collection an anchor points at. It is provider-NEUTRAL: each provider's spec artifact
// templates map it to that platform's own attribute name (`volumes` / `Volumes`, `env` / `Environment`,
// `dependsOn` / `DependsOn`), which is exactly the part that differs between four platforms and must not
// leak into the model.
type Field string

const (
	FieldEnv          Field = "env"          // the container's environment variables
	FieldVolumes      Field = "volumes"      // the shared agent volume, at workload scope
	FieldVolumeMounts Field = "volumeMounts" // the mount of that volume into a container
	FieldContainers   Field = "containers"   // the agent-staging helper container, at workload scope
	FieldDependsOn    Field = "dependsOn"    // start ordering between a container and the helper
	FieldLayers       Field = "layers"       // Lambda layer attachment, at workload scope
)

// Anchor names the path a fragment belongs at.
type Anchor struct {
	Scope Scope
	Field Field
	// Container is the container name for ScopeContainer anchors, and must be empty otherwise. It is the
	// APPLICATION container's name — the one the customer recognizes — not the helper's.
	Container string
}

// String renders the anchor the way §8.1 writes them, e.g. `template.containers[api].env`. It is display
// only: nothing parses it back, so no caller depends on the syntax.
func (a Anchor) String() string {
	if a.Scope == ScopeContainer {
		return fmt.Sprintf("containers[%s].%s", a.Container, a.Field)
	}
	return string(a.Field)
}

// Kind discriminates a Fragment's payload. Exactly one payload field may be set, and it must be the one
// this Kind names — enforced by Validate, so a renderer's switch can never fall through to a nil payload.
type Kind string

const (
	KindEnv               Kind = "env"
	KindVolume            Kind = "volume"
	KindVolumeMount       Kind = "volumeMount"
	KindHelperContainer   Kind = "helperContainer"
	KindStartupDependency Kind = "startupDependency"
	KindLayer             Kind = "layer"
)

// kindOrder is the order fragments are applied in, and it is a dependency order rather than a cosmetic
// one: a mount that names a volume the customer has not declared yet, or a startup dependency on a
// container that does not exist, is a deployment that fails to validate. Sort puts them in an order that
// applies cleanly top to bottom, which is how a customer reads and pastes them.
var kindOrder = map[Kind]int{
	KindVolume:            0,
	KindHelperContainer:   1,
	KindVolumeMount:       2,
	KindStartupDependency: 3,
	KindLayer:             4,
	KindEnv:               5,
}

// EnvVar is one environment variable the instrumentation needs, with the merge direction that makes it
// safe to apply to a definition that may already set it.
type EnvVar struct {
	Name  string
	Value string
	Merge Merge
	// Separator joins our value with the customer's for MergeAppend / MergePrepend (" " for the *_OPTIONS
	// vars, ":" for PYTHONPATH). Required for those, and must be empty for MergeSet.
	Separator string
	// Secret marks a value that must never be rendered into a copy-paste artifact. Nothing sets it today —
	// no injected variable carries a credential, and the ownership marker is a public provenance record —
	// but the instrumentation env is exactly where one would first appear (an export header, a token), and
	// the artifacts are documents customers paste into shared repositories and support tickets. Validate
	// rejects it, so such a variable fails the build that introduces it rather than leaking at render time.
	Secret bool
}

// Volume is the shared scratch area the helper container stages the agent into and the application
// container reads it from.
type Volume struct {
	Name string
	// Medium is "memory" for a tmpfs-backed volume, "ephemeral" for disk. Advisory: it maps to a platform's
	// own spelling (emptyDir medium MEMORY, StorageType EmptyDir, a bare-name ECS task volume).
	Medium string
	// SizeLimit bounds an in-memory volume (e.g. "500Mi"); empty when the platform does not accept one.
	SizeLimit string
}

// Mount is the application container's view of the agent volume.
type Mount struct {
	Volume   string
	Path     string
	ReadOnly bool
}

// HelperContainer stages the language agent into the shared volume so the application container can load
// it. It is a sidecar or an init container depending on what the platform offers; Init records which,
// because it changes where the customer declares it (`initContainers` vs `containers`) and whether a
// startup dependency is needed at all.
type HelperContainer struct {
	Name  string
	Image string
	// Command is the full argv, already resolved. It is rendered verbatim: the staging command is the one
	// thing in a plan a customer must not have to reconstruct.
	Command []string
	Mount   Mount
	// Init is true for a run-to-completion init container (ECS non-essential container, Container Apps
	// initContainers). False for a long-lived sidecar that must be ordered against explicitly — Cloud Run
	// has no run-to-completion containers, so its stager holds a port and the application container gates
	// its own start on a startup probe.
	Init bool
	// StartupProbePort is the TCP port a non-init stager holds open once staging completes, so the platform
	// can gate the application container's start on it. Zero for init containers.
	StartupProbePort int
}

// StartupDependency orders a container's start after the helper has finished staging.
type StartupDependency struct {
	// On is the container that must run first.
	On string
	// Condition is the platform-neutral requirement: "success" (the helper exited 0) or "started".
	Condition string
}

// Layer is a Lambda layer attachment. It has no analogue on the staged-agent platforms; Lambda has no
// container to stage from, so the agent arrives as a layer instead of a volume.
type Layer struct {
	// ARN is the layer version ARN to attach. It may be empty when the plan is built without a resolved
	// region/runtime/architecture — Placeholder then says what the customer must substitute.
	ARN string
	// Placeholder is the literal to render when ARN is empty (e.g. "<OTEL_LAYER_ARN>"). Validate requires
	// one of the two, so a layer fragment can never render as a blank.
	Placeholder string
}

// Fragment is one anchored change: what to add, and where.
type Fragment struct {
	Anchor Anchor
	Kind   Kind

	// Exactly one of the following is set, matching Kind.
	Env               []EnvVar
	Volume            *Volume
	Mount             *Mount
	Helper            *HelperContainer
	StartupDependency *StartupDependency
	Layer             *Layer
}

// Plan is the complete instrumentation for one workload type and language.
//
// It is per (type, language) rather than per workload: everything a customer pastes — the staging command,
// the activation env, the mount path — is determined by those two, and the handful of genuinely
// per-workload values (their container's name, the OTLP endpoint, the layer ARN for their region) are
// carried as placeholders. That is what makes one plan renderable into an artifact a whole account can use,
// and it is why publishing instructions needs no per-workload query.
type Plan struct {
	// Type is the spec instrumentation type name, e.g. "aws.lambda", "gcp.cloud-run".
	Type string
	// Language is the canonical connector language key ("java", "python", …). A type whose instrumentation
	// is language-independent leaves it empty.
	Language string
	// Mode is the injection shape, for prose that has to name it: "layer" (Lambda) or "staged-agent".
	Mode string
	// Activation distinguishes plans that share a type and language but activate the agent differently, and
	// are therefore genuinely different instruction sets. Lambda has two — an exec wrapper on a RequestHandler
	// function, the language's own loader variable on a web-server function behind the AWS Lambda Web Adapter
	// — and applying the wrong one breaks the function. Empty where a type has only one activation.
	Activation string
	// Fragments are the anchored changes, in application order once Sort has run.
	Fragments []Fragment
	// Notes are provider-authored caveats that belong with the instructions rather than in the code — a
	// platform limit, a value the customer must supply. Rendered after the steps.
	Notes []string
	// Placeholders documents every substitution the plan carries. Validate requires this to match the tokens
	// actually present, in BOTH directions: an undocumented token is one a customer is asked to replace with
	// no idea what belongs there, and a documented token that appears nowhere is a form field for a value
	// nothing consumes.
	Placeholders []Placeholder
}

// Injection modes for Plan.Mode.
const (
	// ModeLayer: the agent arrives as a platform-provided artifact attached to the workload (a Lambda
	// layer). Nothing is staged, so a plan in this mode has no helper container, volume or mount.
	ModeLayer = "layer"
	// ModeStagedAgent: a helper container copies the agent into a shared volume the application container
	// reads — the OSS Odigos k8s-init-container mount method, adapted per platform.
	ModeStagedAgent = "staged-agent"
)

// Placeholders are the values a plan cannot resolve, because they belong to the customer's own deployment
// rather than to the instrumentation: which container is theirs, what the workload is called in Odigos,
// where the collector lives. A plan is per (type, language) precisely so one rendering serves a whole
// account, and these are what a customer substitutes on the way in.
//
// The convention is deliberately NOT the spec's `{{token}}` syntax. That syntax is substituted client-side
// by ui-kit from create-connector FORM values, and a token it has no value for is left verbatim — so a
// per-workload token spelled that way would render as literal `{{containerName}}` and read as a rendering
// bug rather than as a field to fill in. Angle-bracketed upper snake case is unambiguously "replace this",
// and Validate enforces the shape so a lowercase or spaced variant cannot slip through.
const (
	// PlaceholderContainer is the customer's application container.
	PlaceholderContainer = "<YOUR_CONTAINER>"
	// PlaceholderServiceName is the workload's name in Odigos. It must match, not merely look right: the
	// observer reports a mismatch as serviceName drift, so a customer who invents a name gets told their
	// deployment is broken.
	PlaceholderServiceName = "<ODIGOS_SOURCE_NAME>"
	// PlaceholderOTLPEndpoint is the destination's OTLP endpoint as host:port, the form the Odigos-namespaced
	// collector variables take.
	PlaceholderOTLPEndpoint = "<OTLP_ENDPOINT>"
	// PlaceholderOTLPEndpointURL is the same endpoint as a scheme-qualified URL, the form the OpenTelemetry
	// SDK variables take. It is a separate placeholder because the two are not interchangeable: pasting a
	// bare host:port into OTEL_EXPORTER_OTLP_ENDPOINT is accepted and then silently exports nowhere.
	PlaceholderOTLPEndpointURL = "<OTLP_ENDPOINT_URL>"
	// PlaceholderOTLPInsecure is whether the destination is plaintext ("true"/"false"), which must match how
	// the connector's destination is configured.
	PlaceholderOTLPInsecure = "<OTLP_INSECURE>"
	// PlaceholderRegion is the workload's region, which rides in the OTel resource attributes.
	PlaceholderRegion = "<REGION>"
	// PlaceholderRuntimeVersion is the application's runtime major.minor, needed by the agents whose file
	// layout is per-version (PHP, Ruby). Those languages have no single correct activation path, so a plan
	// for them is necessarily parameterized rather than copy-paste complete.
	PlaceholderRuntimeVersion = "<RUNTIME_VERSION>"
	// PlaceholderPackageVersion is the version of the Odigos IaC package an artifact tells the customer to
	// install (@odigos/aws-cdk today). Resolved by the provider, not by the customer — see
	// PackageVersionPlaceholder.
	PlaceholderPackageVersion = "<PACKAGE_VERSION>"
)

// Placeholder is one substitution a customer (or the connector, when it can resolve the value) has to make,
// with what it means.
//
// The description travels with the token because the token alone is not self-explanatory in the two cases
// that matter most: <ODIGOS_SOURCE_NAME> must MATCH the workload's name in Odigos rather than being any
// sensible label, and <OTLP_ENDPOINT> and <OTLP_ENDPOINT_URL> are the same destination in two forms that are
// not interchangeable. A client rendering a fill-in form has nowhere else to get that from.
type Placeholder struct {
	// Token is the literal as it appears in the plan and in the artifacts, e.g. "<YOUR_CONTAINER>".
	Token string
	// Description says what to put there, and where to find it.
	Description string
}

// The base-defined placeholders with their canonical descriptions. Providers use these rather than writing
// their own copy: the same placeholder described three ways across three providers is the same documentation
// bug three times, and the two endpoint forms in particular are exactly where a reworded description turns
// into a customer pasting the wrong one.
var (
	// ContainerPlaceholder is the customer's application container.
	ContainerPlaceholder = Placeholder{
		Token:       PlaceholderContainer,
		Description: "The name of your application container, as it appears in your own deployment definition.",
	}
	// ServiceNamePlaceholder is the workload's Odigos name — and it must MATCH, not merely look sensible.
	ServiceNamePlaceholder = Placeholder{
		Token:       PlaceholderServiceName,
		Description: "The workload's name in Odigos. It must match exactly: a different value is reported as instrumentation drift, not as a naming preference.",
	}
	// OTLPEndpointPlaceholder is the raw authority form.
	OTLPEndpointPlaceholder = Placeholder{
		Token:       PlaceholderOTLPEndpoint,
		Description: "Your destination's OTLP endpoint as host:port, with no scheme.",
	}
	// OTLPEndpointURLPlaceholder is the scheme-qualified form, and the distinction is load-bearing: the
	// OpenTelemetry SDK accepts a bare authority here and then exports nowhere.
	OTLPEndpointURLPlaceholder = Placeholder{
		Token:       PlaceholderOTLPEndpointURL,
		Description: "The same destination endpoint as a full URL, including the scheme (http:// or https://). Not interchangeable with the host:port form — the OpenTelemetry SDK accepts a bare host:port here and then exports nowhere.",
	}
	// OTLPInsecurePlaceholder must match how the destination is configured on the connector.
	OTLPInsecurePlaceholder = Placeholder{
		Token:       PlaceholderOTLPInsecure,
		Description: `"true" if your destination is plaintext, "false" if it uses TLS. It has to match how the destination is configured on this connector.`,
	}
	// RegionPlaceholder is the workload's region, which rides in the OTel resource attributes.
	RegionPlaceholder = Placeholder{
		Token:       PlaceholderRegion,
		Description: "The cloud region the workload runs in.",
	}
	// RuntimeVersionPlaceholder is the application's runtime major.minor, for the agents whose file layout is
	// per-version.
	RuntimeVersionPlaceholder = Placeholder{
		Token:       PlaceholderRuntimeVersion,
		Description: "Your application's runtime major.minor version, for example 8.3. This agent's file paths are per version, so it has to be the version your application actually runs.",
	}
	// PackageVersionPlaceholder is the IaC package release an artifact tells the customer to install.
	//
	// It is the odd one out among these: every other placeholder names something only the customer's account
	// can answer, and this one names something only ODIGOS can. Left undeclared it fell to the generic
	// fallback — "a value from your own account or deployment definition" — which is not merely unhelpful but
	// wrong, and it is wrong about the version of a package that pins which agent bytes get installed.
	PackageVersionPlaceholder = Placeholder{
		Token:       PlaceholderPackageVersion,
		Description: "The Odigos IaC package version to install. It is the release this connector is built against, so pin it exactly rather than tracking latest — the package version is what decides which agent build your workload gets.",
	}
)

// placeholderRe matches a well-formed placeholder: <UPPER_SNAKE_CASE>.
var placeholderRe = regexp.MustCompile(`^<[A-Z][A-Z0-9_]*>$`)

// UsesPlaceholder reports whether the token appears anywhere in the plan's rendered literals.
//
// It exists so a provider can DERIVE which placeholders to declare from the plan it just built, rather than
// keeping a parallel list of "which languages are version-templated" that has to be updated whenever an
// upstream distro changes. Validate checks the declaration against exactly this, so deriving it cannot
// disagree.
func UsesPlaceholder(p Plan, token string) bool {
	for _, tok := range placeholderTokens(p) {
		if tok == token {
			return true
		}
	}
	return false
}

// placeholderTokens returns every placeholder appearing in the plan's rendered literals, sorted and
// deduplicated. It is what Validate checks the declared Placeholders against, so a token a customer must
// substitute cannot reach them undocumented.
func placeholderTokens(p Plan) []string {
	seen := map[string]bool{}
	for _, f := range p.Fragments {
		for _, lit := range fragmentLiterals(f) {
			for _, tok := range placeholdersIn(lit) {
				seen[tok] = true
			}
		}
		for _, tok := range placeholdersIn(f.Anchor.Container) {
			seen[tok] = true
		}
	}
	out := make([]string, 0, len(seen))
	for tok := range seen {
		out = append(out, tok)
	}
	sort.Strings(out)
	return out
}

// placeholdersIn returns the well-formed placeholders in one value.
func placeholdersIn(v string) []string {
	var out []string
	for {
		i := strings.IndexByte(v, '<')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(v[i:], '>')
		if j < 0 {
			return out
		}
		if tok := v[i : i+j+1]; placeholderRe.MatchString(tok) {
			out = append(out, tok)
		}
		v = v[i+j+1:]
	}
}

// malformedPlaceholders returns the angle-bracketed tokens in v that do not match the convention. A
// lowercase or spaced token still reads as a placeholder to a human and is invisible to any test that looks
// for the canonical form, so it is caught at authoring time instead.
func malformedPlaceholders(v string) []string {
	var out []string
	for {
		i := strings.IndexByte(v, '<')
		if i < 0 {
			return out
		}
		j := strings.IndexByte(v[i:], '>')
		if j < 0 {
			return out
		}
		tok := v[i : i+j+1]
		if !placeholderRe.MatchString(tok) {
			out = append(out, tok)
		}
		v = v[i+j+1:]
	}
}

// Startup-dependency conditions, platform-neutral.
const (
	// ConditionSuccess: the helper ran to completion and exited 0. Available where the platform has
	// run-to-completion containers (ECS non-essential containers, Container Apps initContainers).
	ConditionSuccess = "success"
	// ConditionStarted: the helper is up and its startup probe passes. The best a platform without
	// run-to-completion containers can offer (Cloud Run).
	ConditionStarted = "started"
)

// Key identifies the plan for logs, test names and artifact ids. It is the tuple that actually distinguishes
// one instruction set from another — a type and language alone do not, since Lambda has two activations per
// language and confusing them breaks the function.
func (p Plan) Key() string {
	k := p.Type
	if p.Language != "" {
		k += "/" + p.Language
	}
	if p.Activation != "" {
		k += "/" + p.Activation
	}
	return k
}

// Sort orders the fragments into an order that applies cleanly (see kindOrder), stably within a kind by
// anchor then by first key, so a rendered artifact is byte-stable across runs. An unstable rendering would
// show a customer a spurious diff on every refresh and make a golden test useless.
func (p *Plan) Sort() {
	sort.SliceStable(p.Fragments, func(i, j int) bool {
		a, b := p.Fragments[i], p.Fragments[j]
		if ka, kb := kindOrder[a.Kind], kindOrder[b.Kind]; ka != kb {
			return ka < kb
		}
		if a.Anchor.String() != b.Anchor.String() {
			return a.Anchor.String() < b.Anchor.String()
		}
		return firstKey(a) < firstKey(b)
	})
	// Env within a fragment is sorted too: providers build it from maps (recipe.staticEnv), whose iteration
	// order is randomized per process, so leaving it alone would make the rendering differ run to run.
	for i := range p.Fragments {
		if p.Fragments[i].Kind == KindEnv {
			env := p.Fragments[i].Env
			sort.SliceStable(env, func(a, b int) bool { return env[a].Name < env[b].Name })
		}
	}
}

func firstKey(f Fragment) string {
	switch f.Kind {
	case KindEnv:
		if len(f.Env) > 0 {
			return f.Env[0].Name
		}
	case KindVolume:
		if f.Volume != nil {
			return f.Volume.Name
		}
	case KindVolumeMount:
		if f.Mount != nil {
			return f.Mount.Volume
		}
	case KindHelperContainer:
		if f.Helper != nil {
			return f.Helper.Name
		}
	case KindStartupDependency:
		if f.StartupDependency != nil {
			return f.StartupDependency.On
		}
	case KindLayer:
		if f.Layer != nil {
			return f.Layer.ARN + f.Layer.Placeholder
		}
	}
	return ""
}

// Validate reports every problem with the plan at once, because these are authoring errors found by a
// provider's test rather than runtime conditions, and fixing them one per test run is needless work.
//
// What it enforces is the set of ways a plan can render into instructions that LOOK fine and are wrong:
// an append with no separator (renders as a literal and eats the customer's value), a container-scoped
// anchor with no container (unfollowable on a multi-container definition), a payload that does not match
// its Kind (a renderer switch hitting nil), a secret in a copy-paste document, an empty value where the
// customer would paste a blank.
func (p Plan) Validate() error {
	var errs []string
	add := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }

	if p.Type == "" {
		add("plan declares no instrumentation type")
	}
	if p.Mode != ModeLayer && p.Mode != ModeStagedAgent {
		add("plan mode %q is not one of %q, %q", p.Mode, ModeLayer, ModeStagedAgent)
	}
	if len(p.Fragments) == 0 {
		add("plan declares no fragments, so there is nothing for a customer to apply")
	}

	// A key must not be claimed twice at one anchor: two fragments setting OTEL_SERVICE_NAME differently
	// render as two contradictory instructions, and whichever the customer applies second silently wins.
	seen := map[string]bool{}
	claim := func(anchor, key string) {
		k := anchor + "\x00" + key
		if seen[k] {
			add("%s: %q is declared twice", anchor, key)
		}
		seen[k] = true
	}

	for i, f := range p.Fragments {
		label := fmt.Sprintf("fragment %d (%s at %s)", i, f.Kind, f.Anchor)
		switch f.Anchor.Scope {
		case ScopeContainer:
			if f.Anchor.Container == "" {
				add("%s: a container-scoped anchor needs the container it applies to", label)
			}
		case ScopeWorkload:
			if f.Anchor.Container != "" {
				add("%s: a workload-scoped anchor must not name a container (got %q)", label, f.Anchor.Container)
			}
		default:
			add("%s: unknown anchor scope %q", label, f.Anchor.Scope)
		}
		if f.Anchor.Field == "" {
			add("%s: anchor declares no field", label)
		}
		switch set := payloadKinds(f); {
		case len(set) != 1:
			add("%s: %d payloads set, want exactly 1 (%v)", label, len(set), set)
		case set[0] != f.Kind:
			// Caught here rather than left to the renderer: the payload switch is keyed on Kind, so a
			// mismatch reaches it as a nil dereference in a code path that only runs for one provider's
			// artifact.
			add("%s: kind is %q but the payload set is a %q", label, f.Kind, set[0])
		}
		p.validatePayload(f, label, add, claim)
		// Every literal that will be rendered, checked in one place: a placeholder is only useful if a
		// customer recognizes it, and the same list is what RequiredLiterals asserts the artifact templates
		// carry — so the two cannot disagree about what a fragment actually says.
		for _, lit := range fragmentLiterals(f) {
			for _, bad := range malformedPlaceholders(lit) {
				add("%s: %q is not a well-formed placeholder (want <UPPER_SNAKE_CASE>, e.g. %s)", label, bad, PlaceholderContainer)
			}
		}
		for _, bad := range malformedPlaceholders(f.Anchor.Container) {
			add("%s: container %q is not a well-formed placeholder (want %s)", label, bad, PlaceholderContainer)
		}
	}

	// Placeholders, both directions. Present-but-undocumented is the one that reaches a customer as an
	// unexplained token; documented-but-absent is a fill-in field for a value nothing substitutes.
	declared := make(map[string]bool, len(p.Placeholders))
	for _, ph := range p.Placeholders {
		if !placeholderRe.MatchString(ph.Token) {
			add("placeholder %q is not well-formed (want <UPPER_SNAKE_CASE>)", ph.Token)
		}
		if ph.Description == "" {
			add("placeholder %s has no description, so nothing tells a customer what belongs there", ph.Token)
		}
		if declared[ph.Token] {
			add("placeholder %s is declared twice", ph.Token)
		}
		declared[ph.Token] = true
	}
	present := placeholderTokens(p)
	for _, tok := range present {
		if !declared[tok] {
			add("placeholder %s appears in the plan but is not declared, so a customer is told to replace it with no idea what belongs there", tok)
		}
	}
	inPlan := make(map[string]bool, len(present))
	for _, tok := range present {
		inPlan[tok] = true
	}
	for _, ph := range p.Placeholders {
		if !inPlan[ph.Token] {
			add("placeholder %s is declared but appears nowhere in the plan", ph.Token)
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("invalid instrumentation plan for %s/%s:\n  - %s", p.Type, p.Language, strings.Join(errs, "\n  - "))
	}
	return nil
}

func (p Plan) validatePayload(f Fragment, label string, add func(string, ...any), claim func(string, string)) {
	switch f.Kind {
	case KindEnv:
		if len(f.Env) == 0 {
			add("%s: declares no environment variables", label)
		}
		for _, e := range f.Env {
			switch {
			case e.Name == "":
				add("%s: an environment variable has no name", label)
			case e.Secret:
				add("%s: %s is marked secret and must not be rendered into a copy-paste artifact", label, e.Name)
			case e.Value == "" && e.Merge != MergeRemove:
				// An empty value would render as `NAME=` — indistinguishable from a deliberate blank and
				// impossible for a customer to spot as missing data. A removal has nothing to render.
				add("%s: %s has an empty value", label, e.Name)
			}
			switch e.Merge {
			case MergeSet:
				if e.Separator != "" {
					add("%s: %s is a plain set but declares separator %q", label, e.Name, e.Separator)
				}
			case MergeRemove:
				if e.Value != "" {
					add("%s: %s must not be set, so it must declare no value (got %q)", label, e.Name, e.Value)
				}
				if e.Separator != "" {
					add("%s: %s is a removal but declares separator %q", label, e.Name, e.Separator)
				}
			case MergeAppend, MergePrepend:
				if e.Separator == "" {
					add("%s: %s asks to %s an existing value but declares no separator, so it can only render as a literal — which discards whatever the customer already has set",
						label, e.Name, mergeVerbs[e.Merge])
				}
			default:
				add("%s: %s has unknown merge %q", label, e.Name, e.Merge)
			}
			claim(f.Anchor.String(), e.Name)
		}
	case KindVolume:
		if f.Volume == nil {
			return
		}
		if f.Volume.Name == "" {
			add("%s: volume has no name", label)
		}
		claim(f.Anchor.String(), f.Volume.Name)
	case KindVolumeMount:
		if f.Mount == nil {
			return
		}
		if f.Mount.Volume == "" {
			add("%s: mount names no volume", label)
		}
		if f.Mount.Path == "" {
			add("%s: mount of %q has no path", label, f.Mount.Volume)
		}
		claim(f.Anchor.String(), f.Mount.Volume)
	case KindHelperContainer:
		if f.Helper == nil {
			return
		}
		if f.Helper.Name == "" {
			add("%s: helper container has no name", label)
		}
		if f.Helper.Image == "" {
			add("%s: helper container %q has no image", label, f.Helper.Name)
		}
		if len(f.Helper.Command) == 0 {
			add("%s: helper container %q has no command, so it would stage nothing", label, f.Helper.Name)
		}
		if f.Helper.Mount.Path == "" {
			add("%s: helper container %q does not mount the agent volume anywhere", label, f.Helper.Name)
		}
		// A long-lived stager the platform cannot gate on is a race: the application container may start
		// before the agent files exist, and the failure is an intermittent missing-agent at cold start.
		if !f.Helper.Init && f.Helper.StartupProbePort == 0 {
			add("%s: helper container %q is not run-to-completion and declares no startup-probe port, so nothing gates the application container on staging being finished", label, f.Helper.Name)
		}
		claim(f.Anchor.String(), f.Helper.Name)
	case KindStartupDependency:
		if f.StartupDependency == nil {
			return
		}
		if f.StartupDependency.On == "" {
			add("%s: startup dependency names no container", label)
		}
		if f.StartupDependency.Condition == "" {
			add("%s: startup dependency on %q declares no condition", label, f.StartupDependency.On)
		}
		claim(f.Anchor.String(), f.StartupDependency.On)
	case KindLayer:
		if f.Layer == nil {
			return
		}
		if f.Layer.ARN == "" && f.Layer.Placeholder == "" {
			add("%s: layer declares neither an ARN nor a placeholder, so it would render as a blank", label)
		}
		claim(f.Anchor.String(), f.Layer.ARN+f.Layer.Placeholder)
	default:
		add("%s: unknown fragment kind %q", label, f.Kind)
	}
}

// payloadKinds is the kind each set payload implies. Validate needs both the COUNT (two payloads means two
// contradictory instructions) and the IDENTITY (a Kind that names a different payload than the one set is
// how a nil reaches a renderer), and one pass over the fragment answers both.
func payloadKinds(f Fragment) []Kind {
	var out []Kind
	if len(f.Env) > 0 {
		out = append(out, KindEnv)
	}
	if f.Volume != nil {
		out = append(out, KindVolume)
	}
	if f.Mount != nil {
		out = append(out, KindVolumeMount)
	}
	if f.Helper != nil {
		out = append(out, KindHelperContainer)
	}
	if f.StartupDependency != nil {
		out = append(out, KindStartupDependency)
	}
	if f.Layer != nil {
		out = append(out, KindLayer)
	}
	return out
}
