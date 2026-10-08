// Package spec is the provider connector specification: requirements, configurable fields, and
// permission sets per feature, as typed Go structs from an embedded YAML file.
//
// Mirrors odigos destinations (data/*.yaml + model.go + load.go): YAML is the editable source of
// truth, loaded at startup via go:embed — no build-time codegen.
//
// Permission strings are opaque and provider-defined. The model does not interpret them; it
// documents and aggregates them for test-connection and UI.
package spec

import "fmt"

// Three parallel domains (separate detectors, capabilities, reports), sharing only codebase and
// connector pod:
//   - workloads: discover directly-instrumentable resources; instrument in place (Lambda layer,
//     ECS Fargate sidecar). Instrumentation gated on an OTLP destination.
//   - computePlatforms: discover managed environments; install a separate Odigos agent
//     (ECS-EC2 → ecs agent; EKS → k8s odiglet; EC2 → vm-agent). Agent owns OTel export — no
//     destination. Installation not supported yet (no types); platforms are reported only.
//   - collectors: managed OTel Collector in the customer's account. Management owns OpAMP config;
//     deployment creates/converges the workload. No discovery — management is the superset;
//     deployment types reference management types. Management without deployment = manual (IaC).
//
// `gateway` is connector-wide (collector wiring), not part of any domain.

// Instrumentation method values for Type.Method (provider-generic).
const (
	// MethodAgent: one agent per node/host instruments all workloads on it.
	MethodAgent = "agent"
	// MethodSidecar: per-workload sidecar injected alongside the workload.
	MethodSidecar = "sidecar"
	// MethodService: workload instrumented directly (no sidecar/agent on the host).
	MethodService = "service"
)

// Collector deployment method values for Type.Method (collectors domain only).
//
// Separate from instrumentation methods: no overlapping values or shared meaning. A shared set
// would accept `method: sidecar` on a Collector deployment type. validateFeatureTypes takes the
// valid set per domain for this reason.
const (
	// MethodFargateService: AWS — ECS Fargate service behind an internal NLB.
	MethodFargateService = "fargate-service"
	// MethodContainerApp: Azure — Container App with internal ingress.
	MethodContainerApp = "container-app"
	// MethodCloudRunService: GCP — Cloud Run service with internal ingress.
	MethodCloudRunService = "cloud-run-service"
)

// CloudConnectorSpec is the top-level document, one per provider (e.g. aws.yaml, gcp.yaml).
type CloudConnectorSpec struct {
	// Provider family, e.g. "aws", "gcp", "azure".
	Provider string `yaml:"provider"`
	// DisplayName shown in the UI.
	DisplayName string `yaml:"displayName"`
	// Connector holds provider boundary / non-secret configuration fields.
	Connector ConnectorConfig `yaml:"connector"`
	// Auth describes supported authentication methods and their credential fields.
	Auth AuthConfig `yaml:"auth"`
	// Workloads: discover and instrument directly-instrumentable resources.
	Workloads WorkloadDomain `yaml:"workloads"`
	// ComputePlatforms: discover managed environments and install an Odigos agent.
	ComputePlatforms ComputePlatformDomain `yaml:"computePlatforms"`
	// Collectors: managed OTel Collector in the customer's account (egress tier).
	Collectors CollectorDomain `yaml:"collectors"`
	// Gateway is connector-wide collector wiring; not part of any domain.
	Gateway Feature `yaml:"gateway"`
	// Setup: declarative onboarding artifacts (IAM preview, CLI snippets) and, sectioned by type
	// (see InstrumentationSection), manual-mode instrumentation instructions. Provider-authored
	// templates; UI fills from aggregated permissions — no per-provider Go rendering.
	Setup Setup `yaml:"setup"`
}

// Setup groups declarative onboarding artifacts. Struct (not bare slice) so metadata can grow
// without breaking the schema.
type Setup struct {
	// Subtitle under the step-2 ("Setup credentials") header; provider-owned to avoid UI branching.
	Subtitle string `yaml:"subtitle"`
	// Artifacts are the renderable onboarding pieces (policy preview, create-role variants, etc.).
	Artifacts []SetupArtifact `yaml:"artifacts"`
}

// SetupArtifact is one renderable onboarding artifact. UI replaces "{{permissions}}" with items
// from ItemTemplate ("{{permission}}" per item), joined by ItemSeparator, indented to the
// placeholder column. Scalar tokens ({{accountId}}, …) come from a client-side vars map. Format
// is a syntax hint only. Plain placeholders — NOT Go text/template; nothing executes server-side.
type SetupArtifact struct {
	// ID is a stable key (e.g. "policy-preview"). Unique within a provider.
	ID string `yaml:"id"`
	// Section groups artifacts for one onboarding step (e.g. "permissions", "create-role").
	Section string `yaml:"section"`
	// Variant / VariantLabel: section chooser (e.g. "Setup method" segments). Empty for single-artifact sections.
	Variant      string `yaml:"variant"`
	VariantLabel string `yaml:"variantLabel"`
	// Language narrows an INSTRUMENTATION artifact to one runtime key ("java", "python", …) —
	// same key as plan.Plan.Language. Empty = language-neutral. Mixing language-specific and
	// neutral in one instrumentation section is rejected at parse time.
	Language string `yaml:"language"`
	// Title / Description are for the UI header.
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Steps: ordered instructions above the code snippet (create-role section).
	Steps []string `yaml:"steps"`
	// Prerequisites: "Before you deploy" caveats for instrumentation artifacts. Empty on onboarding.
	Prerequisites []string `yaml:"prerequisites"`
	// Format: syntax hint for the UI code block — "json" | "yaml" | "shell" | "text".
	Format string `yaml:"format"`
	// Template is the artifact body. May contain "{{permissions}}" and/or scalar "{{name}}" tokens.
	Template string `yaml:"template"`
	// ItemTemplate renders one permission; "{{permission}}" replaced per item.
	ItemTemplate string `yaml:"itemTemplate"`
	// ItemSeparator joins rendered permission items (e.g. ",\n" for JSON, "\n" for YAML).
	ItemSeparator string `yaml:"itemSeparator"`
}

// SetupArtifacts returns every declared onboarding artifact.
func (s CloudConnectorSpec) SetupArtifacts() []SetupArtifact { return s.Setup.Artifacts }

// SetupArtifactsForSection returns artifacts for a UI section, in spec order.
func (s CloudConnectorSpec) SetupArtifactsForSection(section string) []SetupArtifact {
	var out []SetupArtifact
	for _, a := range s.Setup.Artifacts {
		if a.Section == section {
			out = append(out, a)
		}
	}
	return out
}

// instrumentationSectionPrefix namespaces manual-mode instrumentation instructions in
// Setup.Artifacts, one section per type. They ride in Setup so central-backend's
// cloudConnectorSetup can pass them through uninterpreted — no agents-api bump for a new section.
const instrumentationSectionPrefix = "instrumentation:"

// InstrumentationSection is the Setup.Artifacts section for one instrumentation type
// (e.g. "instrumentation:aws.lambda").
func InstrumentationSection(typeName string) string { return instrumentationSectionPrefix + typeName }

// InstrumentationArtifacts returns every manual-mode instruction for one instrumentation type,
// across all languages. For a specific workload language, use InstrumentationArtifactsFor.
func (s CloudConnectorSpec) InstrumentationArtifacts(typeName string) []SetupArtifact {
	return s.SetupArtifactsForSection(InstrumentationSection(typeName))
}

// InstrumentationArtifactsFor returns artifacts for one instrumentation type narrowed to one
// language. Prefers language-specific; falls back to language-neutral. Empty is legitimate —
// caller renders the plan as prose rather than handing another language's snippet.
func (s CloudConnectorSpec) InstrumentationArtifactsFor(typeName, language string) []SetupArtifact {
	all := s.InstrumentationArtifacts(typeName)
	if language != "" {
		if matched := artifactsForLanguage(all, language); len(matched) > 0 {
			return matched
		}
	}
	return artifactsForLanguage(all, "")
}

// artifactsForLanguage filters to artifacts declaring exactly the given language ("" = neutral).
func artifactsForLanguage(arts []SetupArtifact, language string) []SetupArtifact {
	var out []SetupArtifact
	for _, a := range arts {
		if a.Language == language {
			out = append(out, a)
		}
	}
	return out
}

// collectorSectionPrefix namespaces managed-Collector deployment instructions in Setup.Artifacts,
// one section per type — same uninterpreted-passthrough reason as instrumentationSectionPrefix.
const collectorSectionPrefix = "collector:"

// CollectorSection is the Setup.Artifacts section for one Collector type
// (e.g. "collector:aws.fargate-collector").
func CollectorSection(typeName string) string { return collectorSectionPrefix + typeName }

// CollectorArtifacts returns every manual-mode deployment instruction for one Collector type.
// Language-neutral by construction — a Collector has no runtime language.
func (s CloudConnectorSpec) CollectorArtifacts(typeName string) []SetupArtifact {
	return s.SetupArtifactsForSection(CollectorSection(typeName))
}

// WorkloadDomain: Discovery enumerates; Instrumentation instruments in place.
// Each Instrumentation type references a Discovery type by name.
type WorkloadDomain struct {
	Discovery       Feature `yaml:"discovery"`
	Instrumentation Feature `yaml:"instrumentation"`
}

// ComputePlatformDomain: Discovery detects; Installation installs an Odigos agent.
// Each Installation type references a Discovery type by name. Installation not supported yet.
type ComputePlatformDomain struct {
	Discovery    Feature `yaml:"discovery"`
	Installation Feature `yaml:"installation"`
}

// CollectorDomain: Management is the OpAMP config plane (both modes); Deployment is the
// infrastructure plane. Each Deployment type references a Management type by name.
// No discovery — a managed Collector is put there, not found; Management is the superset.
type CollectorDomain struct {
	Management Feature `yaml:"management"`
	Deployment Feature `yaml:"deployment"`
}

// Instrumentation modes: WHO applies instrumentation. DERIVED from capability booleans
// (see WorkloadInstrumentationMode) — no separate CRD/GraphQL/struct field.
//
// Fail-safe across connector image versions: manual = `instrumentation: false`, so an older
// image wires no reconciler and cannot mutate a customer-owned workload. A new field beside
// `instrumentation: true` would do the opposite — encoding/json ignores unknown fields, so an
// older image would treat it as automatic and instrument behind the customer's back.
const (
	// ModeAutomatic: Odigos applies, repairs, and removes instrumentation. Needs mutation permissions.
	ModeAutomatic = "automatic"
	// ModeManual: customer applies instrumentation; Odigos publishes, observes, reports — never mutates.
	ModeManual = "manual"
)

// Capabilities is the customer's allow-list across the three domains (Workloads, ComputePlatforms,
// Collectors), mirroring the CRD. Active detectors/reconcilers = intersection with supported types.
type Capabilities struct {
	Workloads        []WorkloadCapability        `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	ComputePlatforms []ComputePlatformCapability `json:"computePlatforms,omitempty" yaml:"computePlatforms,omitempty"`
	Collectors       []CollectorCapability       `json:"collectors,omitempty" yaml:"collectors,omitempty"`
}

// WorkloadInstrumentationMode returns who instruments the named type: ModeAutomatic, ModeManual,
// or "" when not in the allow-list.
//
//	discovery: true,  instrumentation: true   -> automatic
//	discovery: true,  instrumentation: false  -> manual   (default for a discovered type)
//	discovery: false, instrumentation: false  -> ""
//
// `instrumentation: true` → automatic regardless of discovery, matching ActiveInstrumentationTypes
// which gates mutating reconcilers on that boolean alone.
func (c Capabilities) WorkloadInstrumentationMode(typeName string) string {
	// OR every entry for the type (same as workloadInstrumentation / workloadInstrumentationAnyMode).
	// Nothing along the carrier enforces one entry per type. Returning on FIRST match would disagree
	// with the reconciler gate (report manual while reconciler rewrites). Validate rejects
	// contradictory lists; this OR is the second line of defense.
	var discovery, instrumentation bool
	for _, w := range c.Workloads {
		if w.Type != typeName {
			continue
		}
		discovery = discovery || w.Discovery
		instrumentation = instrumentation || w.Instrumentation
	}
	switch {
	case instrumentation:
		return ModeAutomatic
	case discovery:
		return ModeManual
	default:
		return ""
	}
}

// CollectorMode returns who deploys the named Collector type: ModeAutomatic, ModeManual, or "".
//
//	management: true,  deployment: true   -> automatic
//	management: true,  deployment: false  -> manual   (default for an enabled Collector)
//	management: false, deployment: false  -> ""
//
// `deployment: false` carries manual for the same fail-safe reason as `instrumentation: false`:
// an older image ignores unknown `collectors` and wires no deployer.
//
// `management: false, deployment: true` is refused by Validate. This resolver still reports
// automatic, matching ActiveCollectorDeploymentTypes which gates on deployment alone.
func (c Capabilities) CollectorMode(typeName string) string {
	// OR every entry for the type (same as collectorDeployment / collectorAnyMode) — see
	// WorkloadInstrumentationMode. Validate rejects contradictory lists; this is defense-in-depth.
	var management, deployment bool
	for _, cc := range c.Collectors {
		if cc.Type != typeName {
			continue
		}
		management = management || cc.Management
		deployment = deployment || cc.Deployment
	}
	switch {
	case deployment:
		return ModeAutomatic
	case management:
		return ModeManual
	default:
		return ""
	}
}

// Validate rejects a capability allow-list that cannot be honored. Called at runtime entry so
// contradictions fail startup with a clear message instead of silent resolution.
//
// Refuses: (1) conflicting duplicate entries for the same type (byte-identical dups pass);
// (2) collectors: `deployment: true` without `management: true` (creates an unconfigured Collector).
func (c Capabilities) Validate() error {
	seen := make(map[string]WorkloadCapability, len(c.Workloads))
	for _, w := range c.Workloads {
		prev, dup := seen[w.Type]
		if dup && prev != w {
			return fmt.Errorf(
				"workload capability %q is declared more than once with conflicting flags (discovery %v/%v, instrumentation %v/%v); declare it once",
				w.Type, prev.Discovery, w.Discovery, prev.Instrumentation, w.Instrumentation)
		}
		seen[w.Type] = w
	}
	colls := make(map[string]CollectorCapability, len(c.Collectors))
	for _, cc := range c.Collectors {
		prev, dup := colls[cc.Type]
		if dup && prev != cc {
			return fmt.Errorf(
				"collector capability %q is declared more than once with conflicting flags (management %v/%v, deployment %v/%v); declare it once",
				cc.Type, prev.Management, cc.Management, prev.Deployment, cc.Deployment)
		}
		colls[cc.Type] = cc
	}
	// Folded flags, not per entry: catches single-entry form and any carrier that splits one type.
	for _, name := range collectorOrder(c.Collectors) {
		management, deployment := collectorFlags(c.Collectors, name)
		if deployment && !management {
			return fmt.Errorf(
				"collector capability %q enables deployment without management: Odigos would create a Collector it never configures, which serves an inert fallback for ever; enable management too, or disable deployment to own the Collector yourself",
				name)
		}
	}
	cps := make(map[string]ComputePlatformCapability, len(c.ComputePlatforms))
	for _, p := range c.ComputePlatforms {
		prev, dup := cps[p.Type]
		if dup && prev != p {
			return fmt.Errorf(
				"compute-platform capability %q is declared more than once with conflicting flags (discovery %v/%v, installation %v/%v); declare it once",
				p.Type, prev.Discovery, p.Discovery, prev.Installation, p.Installation)
		}
		cps[p.Type] = p
	}
	return nil
}

// UnsupportedCapabilities returns startup warnings for capability entries this provider cannot
// honor. Warnings only — never fail the runtime (degrades to a surprising report; refusing start
// loses heartbeat entirely). Not a permission check: that needs a provider policy simulation.
func (s CloudConnectorSpec) UnsupportedCapabilities(caps Capabilities) []string {
	discovers := make(map[string]bool, len(s.Workloads.Discovery.Types))
	for _, t := range s.Workloads.Discovery.Types {
		discovers[t.Name] = true
	}

	// Fold duplicates with the same OR as the mode resolver and reconciler gate.
	type flags struct{ discovery, instrumentation bool }
	folded := make(map[string]flags, len(caps.Workloads))
	order := make([]string, 0, len(caps.Workloads))
	for _, w := range caps.Workloads {
		f, seen := folded[w.Type]
		if !seen {
			order = append(order, w.Type)
		}
		folded[w.Type] = flags{f.discovery || w.Discovery, f.instrumentation || w.Instrumentation}
	}

	out := s.unsupportedCollectorCapabilities(caps)
	for _, name := range order {
		f := folded[name]
		if !f.discovery && !f.instrumentation {
			continue // switched off
		}
		if !discovers[name] {
			out = append(out, fmt.Sprintf(
				"workload capability %q is not a workload type this provider discovers; it will be ignored", name))
			continue
		}
		if !f.instrumentation {
			continue // manual mode: needs nothing from the instrumentation feature
		}
		it, declared := s.Workloads.Instrumentation.findType(name)
		switch {
		case !s.Workloads.Instrumentation.Enabled || !declared:
			out = append(out, fmt.Sprintf(
				"workload capability %q requests automatic instrumentation, but this provider declares none for it; "+
					"discovered workloads of this type will report no instrumentation defined", name))
		case !it.IsImplemented():
			out = append(out, fmt.Sprintf(
				"workload capability %q requests automatic instrumentation, but it is declared unimplemented in this "+
					"release; discovered workloads of this type will report NotImplemented", name))
		case !f.discovery:
			out = append(out, fmt.Sprintf(
				"workload capability %q enables instrumentation without discovery; no workload of this type will be "+
					"discovered, so none will be instrumented", name))
		}
	}
	return out
}

// unsupportedCollectorCapabilities warns about collectors entries this provider cannot honor.
// Same contract as caller: warnings only, never an error.
func (s CloudConnectorSpec) unsupportedCollectorCapabilities(caps Capabilities) []string {
	manages := make(map[string]bool, len(s.Collectors.Management.Types))
	for _, t := range s.Collectors.Management.Types {
		manages[t.Name] = true
	}
	var out []string
	for _, name := range collectorOrder(caps.Collectors) {
		management, deployment := collectorFlags(caps.Collectors, name)
		if !management && !deployment {
			continue // switched off
		}
		if !s.Collectors.Management.Enabled || !manages[name] {
			out = append(out, fmt.Sprintf(
				"collector capability %q is not a managed-Collector type this provider declares; it will be ignored", name))
			continue
		}
		if !deployment {
			continue // manual mode needs nothing from the deployment feature
		}
		dt, declared := s.Collectors.Deployment.findType(name)
		switch {
		case !s.Collectors.Deployment.Enabled || !declared:
			out = append(out, fmt.Sprintf(
				"collector capability %q requests automatic deployment, but this provider declares none for it; "+
					"its networks will report as awaiting your own deployment", name))
		case !dt.IsImplemented():
			out = append(out, fmt.Sprintf(
				"collector capability %q requests automatic deployment, but it is declared unimplemented in this "+
					"release; its networks will report as awaiting your own deployment", name))
		}
	}
	return out
}

// WorkloadCapability: whether the connector may discover and/or instrument one workload type.
type WorkloadCapability struct {
	Type            string `json:"type" yaml:"type"`
	Discovery       bool   `json:"discovery,omitempty" yaml:"discovery,omitempty"`
	Instrumentation bool   `json:"instrumentation,omitempty" yaml:"instrumentation,omitempty"`
}

// CollectorCapability: whether the connector may manage config and/or deploy one Collector type.
type CollectorCapability struct {
	Type       string `json:"type" yaml:"type"`
	Management bool   `json:"management,omitempty" yaml:"management,omitempty"`
	Deployment bool   `json:"deployment,omitempty" yaml:"deployment,omitempty"`
}

// ComputePlatformCapability: whether the connector may discover and/or install onto one type.
type ComputePlatformCapability struct {
	Type         string `json:"type" yaml:"type"`
	Discovery    bool   `json:"discovery,omitempty" yaml:"discovery,omitempty"`
	Installation bool   `json:"installation,omitempty" yaml:"installation,omitempty"`
}

// workloadDiscovery / workloadInstrumentation / computePlatformDiscovery / computePlatformInstallation
// return type names enabled for each (domain, feature) pair.
func (c Capabilities) workloadDiscovery() map[string]bool {
	out := map[string]bool{}
	for _, w := range c.Workloads {
		if w.Discovery {
			out[w.Type] = true
		}
	}
	return out
}

func (c Capabilities) workloadInstrumentation() map[string]bool {
	out := map[string]bool{}
	for _, w := range c.Workloads {
		if w.Instrumentation {
			out[w.Type] = true
		}
	}
	return out
}

// workloadInstrumentationAnyMode: automatic + manual types. Backs InstrumentationTypes.
func (c Capabilities) workloadInstrumentationAnyMode() map[string]bool {
	out := map[string]bool{}
	for _, w := range c.Workloads {
		if w.Instrumentation || w.Discovery {
			out[w.Type] = true
		}
	}
	return out
}

// collectorDeployment: types Odigos may create infrastructure for. Backs ActiveCollectorDeploymentTypes.
func (c Capabilities) collectorDeployment() map[string]bool {
	out := map[string]bool{}
	for _, cc := range c.Collectors {
		if cc.Deployment {
			out[cc.Type] = true
		}
	}
	return out
}

// collectorAnyMode: automatic + manual types. Backs CollectorTypes.
func (c Capabilities) collectorAnyMode() map[string]bool {
	out := map[string]bool{}
	for _, cc := range c.Collectors {
		if cc.Management || cc.Deployment {
			out[cc.Type] = true
		}
	}
	return out
}

// collectorOrder returns distinct collector type names in first-appearance order.
func collectorOrder(caps []CollectorCapability) []string {
	seen := map[string]bool{}
	var out []string
	for _, cc := range caps {
		if !seen[cc.Type] {
			seen[cc.Type] = true
			out = append(out, cc.Type)
		}
	}
	return out
}

// collectorFlags OR-folds every entry for one collector type (same as mode resolver / deployer gate).
func collectorFlags(caps []CollectorCapability, typeName string) (management, deployment bool) {
	for _, cc := range caps {
		if cc.Type == typeName {
			management = management || cc.Management
			deployment = deployment || cc.Deployment
		}
	}
	return management, deployment
}

func (c Capabilities) computePlatformDiscovery() map[string]bool {
	out := map[string]bool{}
	for _, p := range c.ComputePlatforms {
		if p.Discovery {
			out[p.Type] = true
		}
	}
	return out
}

func (c Capabilities) computePlatformInstallation() map[string]bool {
	out := map[string]bool{}
	for _, p := range c.ComputePlatforms {
		if p.Installation {
			out[p.Type] = true
		}
	}
	return out
}

// ConnectorConfig is non-auth connector configuration surfaced in the onboarding UI.
type ConnectorConfig struct {
	Fields []Field `yaml:"fields"`
}

// AuthConfig holds authentication permission probes and supported methods.
type AuthConfig struct {
	// Permissions: baseline to validate auth (e.g. identity probe). Provider-specific format.
	Permissions []string `yaml:"permissions"`
	// DefaultMethod: auth method id pre-selected in UI when set. Empty → first with required fields.
	DefaultMethod string `yaml:"defaultMethod"`
	// Methods are the supported authentication paths declared by the provider.
	Methods []AuthMethod `yaml:"methods"`
}

// AuthMethod is one authentication path. May compose via BaseMethods; declares credential fields inline.
type AuthMethod struct {
	ID          string `yaml:"id"`
	DisplayName string `yaml:"displayName"`
	Description string `yaml:"description"`
	// BaseMethods: method ids that must be satisfied first (composable auth).
	BaseMethods []string `yaml:"baseMethods"`
	Fields      []Field  `yaml:"fields"`
}

type Feature struct {
	// Enabled: whether this feature is implemented in the current release.
	Enabled bool `yaml:"enabled"`
	// DisplayName / Description for UI + generated setup docs.
	DisplayName string `yaml:"displayName"`
	Description string `yaml:"description"`
	// Permissions: feature baseline not tied to a specific type. Prefer auth.permissions for probes.
	Permissions []string `yaml:"permissions"`
	// Types: supported resource types, each with its own permissions (and Method for instrumentation).
	Types []Type `yaml:"types"`
}

// findType returns the named type within the feature, if present.
func (f Feature) findType(typeName string) (Type, bool) {
	for _, t := range f.Types {
		if t.Name == typeName {
			return t, true
		}
	}
	return Type{}, false
}

// PermissionsForType returns the permission strings the named type requires (nil if not found).
func (f Feature) PermissionsForType(typeName string) []string {
	if t, ok := f.findType(typeName); ok {
		return t.Permissions
	}
	return nil
}

// MethodForType returns the injection method declared for the named type (empty if not found).
func (f Feature) MethodForType(typeName string) string {
	if t, ok := f.findType(typeName); ok {
		return t.Method
	}
	return ""
}

// activeTypes: feature types in the enabled set, only when the feature is Enabled. Intersects
// allow-list with declared types (spec order). Excludes implemented:false so no reconciler is
// wired for unbuilt behavior — reports surface NotImplemented instead.
func (f Feature) activeTypes(enabled map[string]bool) []Type {
	if !f.Enabled {
		return nil
	}
	var out []Type
	for _, t := range f.Types {
		if enabled[t.Name] && t.IsImplemented() {
			out = append(out, t)
		}
	}
	return out
}

// unimplementedKinds returns type names with implemented:false.
func (f Feature) unimplementedKinds() map[string]bool {
	out := map[string]bool{}
	for _, t := range f.Types {
		if !t.IsImplemented() {
			out[t.Name] = true
		}
	}
	return out
}

// Type is one resource type a feature supports (e.g. "aws.lambda"). Carries permissions and,
// for instrumentation types, a provider-generic Method.
type Type struct {
	// Name: provider-specific type key. Unique across domains; install/instrument refs discovery by name.
	Name string `yaml:"name"`
	// DisplayName / Description for UI + generated setup docs.
	DisplayName string `yaml:"displayName"`
	Description string `yaml:"description"`
	// Permissions: provider-specific permission strings this type needs.
	Permissions []string `yaml:"permissions"`
	// Method: instrumentation/deployment method. Empty for discovery; one of the method consts
	// for instrumentation/deployment — enforced by spec.Parse (validateFeatureTypes).
	Method string `yaml:"method"`
	// Implemented: nil means implemented (default). `implemented: false` = declared but unbuilt;
	// connector neither runs nor reports it as active (NotImplemented). Per (feature, type) —
	// set on instrumentation/installation, never discovery.
	Implemented *bool `yaml:"implemented"`
}

// IsImplemented reports whether runtime behavior is built (nil == implemented).
func (t Type) IsImplemented() bool { return t.Implemented == nil || *t.Implemented }

// Field mirrors the odigos destinations Field shape for connector config forms.
type Field struct {
	Name           string                 `yaml:"name"`
	DisplayName    string                 `yaml:"displayName"`
	ComponentType  string                 `yaml:"componentType"`
	ComponentProps map[string]interface{} `yaml:"componentProps"`
	Required       bool                   `yaml:"required"`
	Secret         bool                   `yaml:"secret"`
	Tooltip        string                 `yaml:"tooltip"`
	InitialValue   string                 `yaml:"initialValue"`
	// ShowInUi: nil = shown (default). `showInUi: false` hides behind "advanced". Default-shown
	// is load-bearing — gcp.yaml declares the key on no field; hidden-by-default would empty it.
	ShowInUi *bool `yaml:"showInUi"`
	// EnvVar: env the runtime stamps this field into (auth via Secret keyRef). Bridges field
	// name to the binary's env (e.g. accessKeyId → AWS_ACCESS_KEY_ID).
	EnvVar string `yaml:"envVar"`
}

// IsShownInUi reports whether the create-connector form renders this field up-front (nil == shown).
func (f Field) IsShownInUi() bool { return f.ShowInUi == nil || *f.ShowInUi }

// ConnectorFields returns the provider boundary configuration fields.
func (s CloudConnectorSpec) ConnectorFields() []Field {
	return s.Connector.Fields
}

// AuthMethods returns the declared authentication methods.
func (s CloudConnectorSpec) AuthMethods() []AuthMethod {
	return s.Auth.Methods
}

// AuthMethodByID returns a declared authentication method by id.
func (s CloudConnectorSpec) AuthMethodByID(id string) (AuthMethod, bool) {
	for _, m := range s.Auth.Methods {
		if m.ID == id {
			return m, true
		}
	}
	return AuthMethod{}, false
}

// CredentialFields returns the de-duplicated (by Name) union of every auth method's fields.
// Wired into the pod as Secret keyRefs; order is first appearance across methods.
func (s CloudConnectorSpec) CredentialFields() []Field {
	seen := map[string]bool{}
	var out []Field
	for _, m := range s.Auth.Methods {
		for _, f := range m.Fields {
			if f.Name == "" || seen[f.Name] {
				continue
			}
			seen[f.Name] = true
			out = append(out, f)
		}
	}
	return out
}

// UnimplementedComputePlatformKinds: discovered compute-platform kinds with no implemented
// installation type. Installation declares no types yet, so every discovered kind is here.
func (s CloudConnectorSpec) UnimplementedComputePlatformKinds() map[string]bool {
	installable := map[string]bool{}
	for _, t := range s.ComputePlatforms.Installation.Types {
		if t.IsImplemented() {
			installable[t.Name] = true
		}
	}
	out := map[string]bool{}
	for _, t := range s.ComputePlatforms.Discovery.Types {
		if !installable[t.Name] {
			out[t.Name] = true
		}
	}
	return out
}

// UnimplementedInstrumentationKinds: instrumentation types with implemented:false.
func (s CloudConnectorSpec) UnimplementedInstrumentationKinds() map[string]bool {
	return s.Workloads.Instrumentation.unimplementedKinds()
}

// ActiveInstrumentationTypes: types to RECONCILE (mutate) — automatic mode only.
// Mutation gate: coordinator wires reconcilers only for types returned here. For "handles in
// some mode" (reporting, observation) use InstrumentationTypes.
func (s CloudConnectorSpec) ActiveInstrumentationTypes(caps Capabilities) []Type {
	return s.Workloads.Instrumentation.activeTypes(caps.workloadInstrumentation())
}

// InstrumentationTypes: every type handled in either mode (automatic + manual).
// Reporting/availability only — never authorize mutation (see ActiveInstrumentationTypes).
func (s CloudConnectorSpec) InstrumentationTypes(caps Capabilities) []Type {
	return s.Workloads.Instrumentation.activeTypes(caps.workloadInstrumentationAnyMode())
}

// ActiveCollectorDeploymentTypes: Collector types whose infrastructure this connector may create —
// automatic mode only. Mutation gate (same pattern as ActiveInstrumentationTypes). For "handles
// in some mode" use CollectorTypes — wrong pick either drops manual from reports or mutates customer infra.
func (s CloudConnectorSpec) ActiveCollectorDeploymentTypes(caps Capabilities) []Type {
	return s.Collectors.Deployment.activeTypes(caps.collectorDeployment())
}

// CollectorTypes: every Collector type in either mode. Resolved against MANAGEMENT (superset;
// carries read-only permissions both modes need). Reporting/enablement only — never mutation.
func (s CloudConnectorSpec) CollectorTypes(caps Capabilities) []Type {
	return s.Collectors.Management.activeTypes(caps.collectorAnyMode())
}

// ActiveComputePlatformTypes: types to reconcile by installing an agent. Installation has no
// types yet → currently nil. Not gated on a destination — the agent owns OTel export.
func (s CloudConnectorSpec) ActiveComputePlatformTypes(caps Capabilities) []Type {
	return s.ComputePlatforms.Installation.activeTypes(caps.computePlatformInstallation())
}

// ActiveWorkloadDiscoveryTypes: active workload-discovery types (e.g. aws.lambda).
func (s CloudConnectorSpec) ActiveWorkloadDiscoveryTypes(caps Capabilities) []Type {
	return s.Workloads.Discovery.activeTypes(caps.workloadDiscovery())
}

// ActiveComputePlatformDiscoveryTypes: active compute-platform-discovery types (e.g. aws.ecs-cluster).
func (s CloudConnectorSpec) ActiveComputePlatformDiscoveryTypes(caps Capabilities) []Type {
	return s.ComputePlatforms.Discovery.activeTypes(caps.computePlatformDiscovery())
}

// ActiveDiscoveryTypes returns every active discovery type across both domains.
func (s CloudConnectorSpec) ActiveDiscoveryTypes(caps Capabilities) []Type {
	wl := s.ActiveWorkloadDiscoveryTypes(caps)
	cp := s.ActiveComputePlatformDiscoveryTypes(caps)
	if len(cp) == 0 {
		return wl
	}
	seen := map[string]bool{}
	var out []Type
	for _, t := range append(wl, cp...) {
		if !seen[t.Name] {
			seen[t.Name] = true
			out = append(out, t)
		}
	}
	return out
}

// RequiredPermissions: de-duplicated permission strings for the given capabilities — auth
// baseline, active discovery baselines/types, plus mutation-gated instrumentation/installation/
// deployment. Used by test-connection and provider probes.
func (s CloudConnectorSpec) RequiredPermissions(caps Capabilities) []string {
	seen := map[string]bool{}
	var out []string
	add := func(perms []string) {
		for _, p := range perms {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	add(s.Auth.Permissions)
	wl := s.ActiveWorkloadDiscoveryTypes(caps)
	if len(wl) > 0 {
		add(s.Workloads.Discovery.Permissions)
	}
	cp := s.ActiveComputePlatformDiscoveryTypes(caps)
	if len(cp) > 0 {
		add(s.ComputePlatforms.Discovery.Permissions)
	}
	for _, t := range wl {
		add(t.Permissions)
	}
	for _, t := range cp {
		add(t.Permissions)
	}
	// ActiveInstrumentationTypes only (not InstrumentationTypes): Type.Permissions are MUTATION
	// perms — aggregating for manual mode would demand what manual mode exists to avoid. Manual
	// observation reads live on the discovery type (the only feature manual mode runs). A separate
	// observe-permissions list was rejected: nothing here builds the customer policy (ui-kit does),
	// so a new field would omit reads until threaded through central-backend + ui-kit. Cost: automatic
	// also gets those reads — fine; both modes need them to judge effectiveness.
	for _, t := range s.ActiveInstrumentationTypes(caps) {
		add(t.Permissions)
	}
	for _, t := range s.ActiveComputePlatformTypes(caps) {
		add(t.Permissions)
	}

	// Collector permissions, including the management baseline, belong to its
	// customer-provisioned role. They are never added to the connector's base identity.
	return out
}

// CollectorRolePermissions lists the provider API actions carried by the customer-provisioned
// Collector role. Management reads apply in both modes; deployment writes apply only in automatic mode.
func (s CloudConnectorSpec) CollectorRolePermissions(caps Capabilities) []string {
	seen := map[string]bool{}
	var out []string
	add := func(perms []string) {
		for _, p := range perms {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	if len(s.CollectorTypes(caps)) > 0 {
		add(s.Collectors.Management.Permissions)
	}
	for _, t := range s.CollectorTypes(caps) {
		add(t.Permissions)
	}
	for _, t := range s.ActiveCollectorDeploymentTypes(caps) {
		add(t.Permissions)
	}
	return out
}
