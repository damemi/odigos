// Package spec exposes the provider connector specification (requirements, configurable fields and
// permission sets per feature) as typed Go structs loaded from an embedded YAML file. This mirrors
// the odigos destinations pattern (data/*.yaml + model.go + load.go): the YAML is the single
// human-editable source of truth, and it is read into structs at startup via go:embed — there is no
// build-time code generation step.
//
// Permission strings are opaque and provider-defined (e.g. AWS IAM actions, GCP permission names,
// Azure role actions). The model does not interpret them — it documents and aggregates them for
// test-connection and UI.
package spec

import "fmt"

// The spec has two parallel domains, completely separate (separate detectors, capabilities, reports),
// sharing only the codebase and running connector pod:
//   - workloads: directly-instrumentable resources. A `discovery` feature enumerates them and an
//     `instrumentation` feature instruments a discovered workload in place (Lambda layer, ECS Fargate
//     sidecar). Instrumentation is gated on an OTLP destination.
//   - computePlatforms: managed environments. A `discovery` feature detects them and an `installation`
//     feature installs a separate Odigos agent onto one (ECS-EC2 → ecs agent; EKS → k8s odiglet; EC2 →
//     vm-agent). The agent owns OTel export, so this needs NO destination. Installation is not supported
//     yet: its feature declares no types, so discovered compute platforms are reported but never
//     instrumented.
// `gateway` is a connector-wide feature (collector wiring), not part of either domain.

// Instrumentation method values (provider-generic) for Type.Method. They describe how a connector
// injects instrumentation for a supported type, resolved at connector + service level.
const (
	// MethodAgent: a single agent per node/host instruments all workloads on it.
	MethodAgent = "agent"
	// MethodSidecar: a per-workload sidecar is injected alongside the workload.
	MethodSidecar = "sidecar"
	// MethodService: the workload itself is instrumented directly (no sidecar/agent on the host).
	MethodService = "service"
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
	// Workloads is the workload domain: discover directly-instrumentable resources and instrument them.
	Workloads WorkloadDomain `yaml:"workloads"`
	// ComputePlatforms is the compute-platform domain: discover managed environments and install a separate
	// Odigos agent onto them.
	ComputePlatforms ComputePlatformDomain `yaml:"computePlatforms"`
	// Gateway is connector-wide collector wiring; not part of either domain.
	Gateway Feature `yaml:"gateway"`
	// Setup declares provider-defined renderable artifacts: the onboarding pieces shown in the
	// create-connector flow (IAM policy preview, CLI setup snippets) and — sectioned by instrumentation type,
	// see InstrumentationSection — the manual-mode instrumentation instructions a customer applies in their
	// own deployment definition. It is intentionally declarative: each provider authors artifact templates in
	// its YAML, and the UI fills them from the aggregated spec permissions (and, later, live form values), so
	// the UI stays provider-agnostic and no per-provider Go rendering code is needed.
	Setup Setup `yaml:"setup"`
}

// Setup groups a provider's declarative onboarding artifacts. Kept as a struct (rather than a bare
// slice) so it can grow additional setup metadata without breaking the schema.
type Setup struct {
	// Subtitle is optional copy shown under the step-2 ("Setup credentials") header. Provider-owned so
	// the UI stays free of per-provider branching (e.g. AWS: "Apply the selected IAM policy...").
	Subtitle string `yaml:"subtitle"`
	// Artifacts are the renderable onboarding pieces (policy preview, create-role variants, etc.).
	Artifacts []SetupArtifact `yaml:"artifacts"`
}

// SetupArtifact is one renderable onboarding artifact. The UI renders Template, replacing the literal
// "{{permissions}}" placeholder line with the aggregated permission strings — each wrapped by
// ItemTemplate (whose "{{permission}}" token is substituted per permission), joined by ItemSeparator,
// and indented to match the placeholder's column. Scalar placeholders ({{accountId}}, {{projectId}},
// {{externalId}}, ...) are filled from a vars map assembled client-side from form values. Format is a
// syntax hint only. This is a plain placeholder convention, NOT Go text/template — nothing here is
// executed server-side.
type SetupArtifact struct {
	// ID is a stable key for the artifact (e.g. "policy-preview", "create-role-cloudformation").
	// Unique within a provider.
	ID string `yaml:"id"`
	// Section is the UI section the artifact belongs to (e.g. "permissions", "create-role",
	// "credentials"). Lets one provider declare artifacts for multiple onboarding steps; the UI
	// selects by section.
	Section string `yaml:"section"`
	// Variant / VariantLabel let a section declare a chooser. Distinct variants in the "create-role"
	// section are the "Setup method" segment options; the selected variant's artifact drives the
	// create-role instructions + snippet. Empty for single-artifact sections (e.g. policy-preview).
	Variant      string `yaml:"variant"`
	VariantLabel string `yaml:"variantLabel"`
	// Language narrows an INSTRUMENTATION artifact to one connector language key ("java", "python", …) —
	// the same key plan.Plan.Language carries, which is the workload's detected runtime or the runtime
	// override when the customer set one.
	//
	// It exists because the instructions are not the same document per language. A Fargate task stages a
	// different agent directory and activates it through a different variable for each; a Lambda function's
	// exec wrapper is /opt/otel-instrument for Python and /opt/otel-handler for everything else, and the
	// wrong one fails the function at cold start. An artifact that covered every language had to carry all of
	// them, which put six languages' worth of environment variables in front of a customer who has one — and
	// left the one that matters as an appendix to be found rather than as the snippet to copy.
	//
	// Empty means the artifact is language-neutral. That is correct for the onboarding sections (a policy
	// document has no language) and for an instrumentation type whose plan carries no language; validation
	// rejects an instrumentation section that mixes the two, because the selection below would then hide the
	// neutral variants from a language that has its own.
	Language string `yaml:"language"`
	// Title / Description are for the UI header.
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	// Steps are an ordered instruction list shown above the code snippet (create-role section).
	Steps []string `yaml:"steps"`
	// Format is a syntax hint for the UI code block: "json" | "yaml" | "shell" | "text".
	Format string `yaml:"format"`
	// Template is the artifact body. May contain "{{permissions}}" and/or scalar "{{name}}" tokens.
	Template string `yaml:"template"`
	// ItemTemplate renders a single permission; its literal "{{permission}}" token is replaced per item.
	ItemTemplate string `yaml:"itemTemplate"`
	// ItemSeparator joins the rendered permission items (e.g. ",\n" for a JSON array, "\n" for YAML).
	ItemSeparator string `yaml:"itemSeparator"`
}

// SetupArtifacts returns every declared onboarding artifact.
func (s CloudConnectorSpec) SetupArtifacts() []SetupArtifact { return s.Setup.Artifacts }

// SetupArtifactsForSection returns the declared artifacts for a UI section, in spec order.
func (s CloudConnectorSpec) SetupArtifactsForSection(section string) []SetupArtifact {
	var out []SetupArtifact
	for _, a := range s.Setup.Artifacts {
		if a.Section == section {
			out = append(out, a)
		}
	}
	return out
}

// instrumentationSectionPrefix namespaces the manual-mode instrumentation instructions inside
// Setup.Artifacts, one section per instrumentation type.
//
// They ride in Setup rather than getting a hierarchy of their own for one concrete reason: central-backend's
// cloudConnectorSetup maps the artifact list through UNINTERPRETED — id, section, variant, steps, format,
// template — so a new section reaches the create-connector catalog with no change there and no agents-api
// version bump. A new top-level field would need both, and until both shipped the instructions would simply
// not exist for a customer who has already been told their workload is awaiting them.
//
// Sectioning by TYPE rather than lumping them under one "instrumentation" section is what makes them
// selectable: a client showing instructions for a Fargate service must not have to filter Lambda's out of the
// same list, and the two share no anchors, no variants and no vocabulary.
const instrumentationSectionPrefix = "instrumentation:"

// InstrumentationSection is the Setup.Artifacts section carrying the manual-mode instrumentation instructions
// for one workload instrumentation type (e.g. "instrumentation:aws.lambda").
func InstrumentationSection(typeName string) string { return instrumentationSectionPrefix + typeName }

// InstrumentationArtifacts returns EVERY manual-mode instrumentation instruction declared for one
// instrumentation type, in spec order, across all languages. Each is a delivery variant (terraform,
// cloudformation, manual, …) of the same underlying change, for one language.
//
// Callers serving a specific workload want InstrumentationArtifactsFor: the whole list is a Java function's
// instructions and a Ruby function's side by side. This one is for the checks that have to see all of them —
// the coverage guard that asserts the published instructions still match what the injector applies.
func (s CloudConnectorSpec) InstrumentationArtifacts(typeName string) []SetupArtifact {
	return s.SetupArtifactsForSection(InstrumentationSection(typeName))
}

// InstrumentationArtifactsFor returns the artifacts for one instrumentation type narrowed to ONE language —
// the instructions a customer with a Java service should be handed, and only those.
//
// Selection, in order: the artifacts declaring this language; failing that, the language-neutral ones. The
// fallback is what keeps the two useful things possible — a type whose instrumentation does not depend on the
// language declares neutral artifacts and is served them for any language, and a provider that has not yet
// authored a language's snippets serves its neutral ones rather than nothing. Mixing the two within one
// section is rejected at parse time, so the fallback is never a silent half-answer for a language that does
// have its own artifacts.
//
// An empty result is a legitimate answer and not a gap to paper over: the caller renders the plan as prose
// instead, which describes the same desired state in the one form every platform shares. Handing over another
// language's snippet would be worse than describing the change.
func (s CloudConnectorSpec) InstrumentationArtifactsFor(typeName, language string) []SetupArtifact {
	all := s.InstrumentationArtifacts(typeName)
	if language != "" {
		if matched := artifactsForLanguage(all, language); len(matched) > 0 {
			return matched
		}
	}
	return artifactsForLanguage(all, "")
}

// artifactsForLanguage filters to the artifacts declaring exactly the given language ("" = language-neutral).
func artifactsForLanguage(arts []SetupArtifact, language string) []SetupArtifact {
	var out []SetupArtifact
	for _, a := range arts {
		if a.Language == language {
			out = append(out, a)
		}
	}
	return out
}

// WorkloadDomain groups the two features that operate on directly-instrumentable resources: Discovery
// enumerates them; Instrumentation instruments a discovered one in place. Each Instrumentation type
// references a Discovery type by name.
type WorkloadDomain struct {
	Discovery       Feature `yaml:"discovery"`
	Instrumentation Feature `yaml:"instrumentation"`
}

// ComputePlatformDomain groups the two features that operate on managed environments: Discovery detects
// them; Installation installs a separate Odigos agent onto a discovered one. Each Installation type
// references a Discovery type by name. Installation is not supported yet (declares no types).
type ComputePlatformDomain struct {
	Discovery    Feature `yaml:"discovery"`
	Installation Feature `yaml:"installation"`
}

// Instrumentation modes: WHO applies instrumentation for a workload type on a given connector. The mode is
// DERIVED from the capability booleans rather than stored as its own field (see WorkloadInstrumentationMode),
// so it needs no new field on the CRD, the GraphQL capability input, or this struct.
//
// The derivation is also fail-safe across connector image versions: manual mode is expressed as
// `instrumentation: false`, so a connector image that predates these modes wires no reconciler for the type
// and therefore cannot mutate a workload its customer chose to own. Encoding manual mode as a new field
// alongside `instrumentation: true` would have had the opposite property — ODIGOS_CAPABILITIES is parsed with
// encoding/json, which ignores unknown fields, so an older image would have read it as plain automatic
// instrumentation and instrumented workloads behind the customer's back.
const (
	// ModeAutomatic: Odigos applies, repairs and removes instrumentation itself. Requires provider mutation
	// permissions. This is the pre-existing behavior.
	ModeAutomatic = "automatic"
	// ModeManual: the customer applies instrumentation themselves (IaC or by hand). Odigos publishes what to
	// apply, observes the result and reports conformance; it never mutates the workload.
	ModeManual = "manual"
)

// Capabilities is the customer's allow-list, split into the two domains to mirror the CRD
// (OdigosCloudConnectorSpec.Workloads / .ComputePlatforms) and the spec's own two hierarchies. The
// connector resolves active detectors/reconcilers by intersecting these with the spec's supported types.
type Capabilities struct {
	Workloads        []WorkloadCapability        `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	ComputePlatforms []ComputePlatformCapability `json:"computePlatforms,omitempty" yaml:"computePlatforms,omitempty"`
}

// WorkloadInstrumentationMode returns who instruments the named workload type on this connector:
// ModeAutomatic, ModeManual, or "" when the type is not in the allow-list at all.
//
// A discovered workload type is instrumented BY THE CUSTOMER by default; only granting the connector
// instrumentation capability for that type makes Odigos do the installation itself. So:
//
//	discovery: true,  instrumentation: true   -> automatic
//	discovery: true,  instrumentation: false  -> manual   (the default for a discovered type)
//	discovery: false, instrumentation: false  -> ""       (type not enabled at all)
//
// `instrumentation: true` maps to automatic regardless of the discovery flag, so this never contradicts
// ActiveInstrumentationTypes — which gates the mutating reconcilers on that boolean alone.
func (c Capabilities) WorkloadInstrumentationMode(typeName string) string {
	// Both flags are OR-ed over EVERY entry for the type, matching workloadInstrumentation and
	// workloadInstrumentationAnyMode below. Nothing along the carrier — the CRD list (no +listMapKey), the
	// GraphQL capability input, connector-runtime's marshal, or the ODIGOS_CAPABILITIES unmarshal — enforces
	// one entry per type, so duplicates do reach here. Returning on the FIRST match instead would let this
	// disagree with the reconciler gate: a list of [{lambda, manual}, {lambda, automatic}] would report the
	// type as manual ("waiting for your deployment") while the reconciler was wired and rewriting the
	// function. Resolving both from the same OR keeps the report honest about what the connector actually does.
	//
	// Validate() rejects that contradictory list outright, so this agreement is the second line of defense
	// rather than the only one.
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

// Validate rejects a capability allow-list that cannot be honored as written. It is called where the
// capabilities enter the runtime, so a contradictory set stops the connector at startup with a clear message
// instead of being silently resolved into one of the two things the customer asked for.
//
// The only such contradiction today is two entries for the same workload type that disagree about
// instrumentation: one says the customer instruments it, the other says Odigos does. That is a question only a
// human can answer — and the unsafe reading (Odigos mutates a workload the customer may have meant to own) is
// the one the reconciler gate would take — so it is refused rather than guessed. Byte-identical duplicates are
// merely redundant and pass.
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

// UnsupportedCapabilities returns human-readable warnings for capability entries this provider spec cannot
// honor as written. Callers should log them at startup.
//
// It deliberately returns warnings rather than an error, and must never fail the runtime. Every case below
// degrades to a correct-but-surprising per-workload report, whereas refusing to start would turn a recoverable
// misconfiguration into a connector that never registers, heartbeats or reports at all — strictly less
// observable than the state being warned about.
//
// This is deliberately NOT a permission check. Whether the connector's credentials actually carry the mutation
// permissions automatic mode needs cannot be established without a provider policy-simulation call (nothing in
// connectors/ makes one), and inferring it would risk exactly that false negative. The authorization error from
// the first real mutation attempt is the honest signal there, and it is already reported per workload.
func (s CloudConnectorSpec) UnsupportedCapabilities(caps Capabilities) []string {
	discovers := make(map[string]bool, len(s.Workloads.Discovery.Types))
	for _, t := range s.Workloads.Discovery.Types {
		discovers[t.Name] = true
	}

	// Fold duplicate entries with the same OR the mode resolver and the reconciler gate use, so a warning is
	// reported once per type and describes the flags that actually take effect.
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

	var out []string
	for _, name := range order {
		f := folded[name]
		if !f.discovery && !f.instrumentation {
			continue // the type is simply switched off; nothing to warn about
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

// WorkloadCapability is the allow-list entry for one directly-instrumentable resource type: whether the
// connector may discover and/or instrument that workload type.
type WorkloadCapability struct {
	Type            string `json:"type" yaml:"type"`
	Discovery       bool   `json:"discovery,omitempty" yaml:"discovery,omitempty"`
	Instrumentation bool   `json:"instrumentation,omitempty" yaml:"instrumentation,omitempty"`
}

// ComputePlatformCapability is the allow-list entry for one managed-environment type: whether the connector
// may discover the environment and/or install a separate Odigos agent onto it.
type ComputePlatformCapability struct {
	Type         string `json:"type" yaml:"type"`
	Discovery    bool   `json:"discovery,omitempty" yaml:"discovery,omitempty"`
	Installation bool   `json:"installation,omitempty" yaml:"installation,omitempty"`
}

// workloadDiscovery / workloadInstrumentation / computePlatformDiscovery / computePlatformInstallation
// return the set of type names enabled for each (domain, feature) pair.
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

// workloadInstrumentationAnyMode is workloadInstrumentation widened to the manual-mode types as well — every
// type WorkloadInstrumentationMode resolves to a mode. Backs InstrumentationTypes.
func (c Capabilities) workloadInstrumentationAnyMode() map[string]bool {
	out := map[string]bool{}
	for _, w := range c.Workloads {
		if w.Instrumentation || w.Discovery {
			out[w.Type] = true
		}
	}
	return out
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
	// Permissions are baseline provider permissions needed to validate authentication (e.g. an identity
	// probe). Format is provider-specific.
	Permissions []string `yaml:"permissions"`
	// DefaultMethod is the auth method id pre-selected in the create-connector UI when set (e.g.
	// "assume-role" for AWS). When empty, the UI picks the first method that declares required fields.
	DefaultMethod string `yaml:"defaultMethod"`
	// Methods are the supported authentication paths declared by the provider.
	Methods []AuthMethod `yaml:"methods"`
}

// AuthMethod is one supported authentication path. Methods may compose via BaseMethods (e.g. a
// federated/assumed-role step on top of a base credential source). Each method declares its own
// credential fields inline.
type AuthMethod struct {
	ID          string `yaml:"id"`
	DisplayName string `yaml:"displayName"`
	Description string `yaml:"description"`
	// BaseMethods lists method ids that must be satisfied before this method applies (composable auth).
	BaseMethods []string `yaml:"baseMethods"`
	Fields      []Field  `yaml:"fields"`
}

type Feature struct {
	// Enabled reports whether this feature is implemented in the current release. P0: only discovery.
	Enabled bool `yaml:"enabled"`
	// DisplayName / Description are for UI + generated setup docs.
	DisplayName string `yaml:"displayName"`
	Description string `yaml:"description"`
	// Permissions are baseline provider permissions the feature needs that are not tied to a specific
	// supported type. Prefer auth.permissions for identity probes.
	Permissions []string `yaml:"permissions"`
	// Types are the individual resource types this feature supports, each declaring its own permissions
	// and (for instrumentation) how the agent is injected.
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

// activeTypes returns the feature's types whose name is in the enabled set, but only when the feature is
// enabled in this release. It intersects the customer's allow-list with the declared types (in spec
// order), so a capability for an unsupported type or a disabled feature is ignored. A declared-but-
// unimplemented type (implemented:false) is also excluded, so the connector never wires a reconciler for
// behavior that is not built — reports surface it as NotImplemented instead.
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

// unimplementedKinds returns the feature's type names whose runtime behavior is not built (implemented:false).
func (f Feature) unimplementedKinds() map[string]bool {
	out := map[string]bool{}
	for _, t := range f.Types {
		if !t.IsImplemented() {
			out[t.Name] = true
		}
	}
	return out
}

// Type is a single resource type a feature supports (e.g. discovery of "aws.lambda", "aws.ec2").
// It carries the permissions that type requires and, for instrumentation types, a provider-generic
// Method describing how instrumentation is injected.
type Type struct {
	// Name is the provider-specific type key. Unique across both domains; an instrumentation/installation
	// type references its discovery type by this name.
	Name string `yaml:"name"`
	// DisplayName / Description are for UI + generated setup docs.
	DisplayName string `yaml:"displayName"`
	Description string `yaml:"description"`
	// Permissions are the provider-specific permission strings this type needs.
	Permissions []string `yaml:"permissions"`
	// Method is the provider-generic instrumentation method for this type. One of:
	//   "agent"   – a single agent per node/host instruments all workloads on it;
	//   "sidecar" – a per-workload sidecar is injected;
	//   "service" – the workload itself is instrumented directly.
	// MUST be empty for discovery types and MUST be one of the three values for instrumentation types —
	// enforced at parse time by spec.Parse (see load.go validateFeatureTypes).
	Method string `yaml:"method"`
	// Implemented reports whether this type's runtime behavior is actually built in the current release.
	// A nil value means implemented (the default). Set `implemented: false` for a type that is declared
	// (so it is discoverable and its permissions are documented) but whose instrumentation/service
	// reconcile is not built yet: the connector neither runs it nor reports it as active, and reports
	// surface it as NotImplemented rather than as instrumented/assigned. Discovery of such a type is
	// unaffected — the flag is per (feature, type), so it is set only on the instrumentation/installation
	// entry, never on the discovery entry.
	Implemented *bool `yaml:"implemented"`
}

// IsImplemented reports whether this type's runtime behavior is built in the current release (nil ==
// implemented).
func (t Type) IsImplemented() bool { return t.Implemented == nil || *t.Implemented }

// Field mirrors the odigos destinations Field shape so the UI can render connector config forms with
// the same component vocabulary.
type Field struct {
	Name           string                 `yaml:"name"`
	DisplayName    string                 `yaml:"displayName"`
	ComponentType  string                 `yaml:"componentType"`
	ComponentProps map[string]interface{} `yaml:"componentProps"`
	Required       bool                   `yaml:"required"`
	Secret         bool                   `yaml:"secret"`
	Tooltip        string                 `yaml:"tooltip"`
	InitialValue   string                 `yaml:"initialValue"`
	// ShowInUi is a UI hint: whether the create-connector form renders this field up-front. It exists so a
	// rarely-needed optional credential can stay hidden behind a disclosure (e.g. the AWS sessionToken,
	// which only temporary access keys use) instead of cluttering the common path.
	//
	// NOTE: it is declared here so the provider YAML is honest — spec.Parse now rejects undeclared keys —
	// but nothing consumes it yet: central-backend's CloudConnectorField does not carry it, so every field
	// currently renders regardless. Either plumb it through to the UI or drop it from the YAML; do not
	// assume a `showInUi: false` field is hidden today.
	ShowInUi bool `yaml:"showInUi"`
	// EnvVar is the environment variable the connector runtime stamps this field's value into (for
	// auth fields, via a Secret keyRef whose key is Name). It is the bridge from the provider-agnostic
	// field name to the env var the connector binary reads (e.g. accessKeyId -> AWS_ACCESS_KEY_ID).
	EnvVar string `yaml:"envVar"`
}

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

// CredentialFields returns the de-duplicated (by Name) union of every auth method's fields. Every auth
// field is a credential sourced from the connector's credentials Secret (the `secret` flag is only a UI
// masking hint), so this is the set the connector-runtime wires into the pod as Secret keyRefs and the
// set the create-connector flow collects from the user. Order follows first appearance across methods.
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

// UnimplementedComputePlatformKinds returns the set of compute-platform type names that cannot be
// instrumented in this release. Installing an agent onto a compute platform is the installation feature,
// which is not supported yet (declares no types), so EVERY discovered compute-platform kind is
// unimplemented. Compute-platform reports use this to surface NotImplemented instead of Assigned for a
// discovered managed environment. A kind stops being reported here once it is referenced under the
// installation feature as implemented.
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

// UnimplementedInstrumentationKinds returns the set of declared instrumentation type names whose runtime
// behavior is not built in this release (implemented:false). Workload reports use this to surface
// NotImplemented for a workload whose instrumentation type is declared but not yet built.
func (s CloudConnectorSpec) UnimplementedInstrumentationKinds() map[string]bool {
	return s.Workloads.Instrumentation.unimplementedKinds()
}

// ActiveInstrumentationTypes returns the workload instrumentation types to RECONCILE — that is, the types
// this connector may mutate — given the capabilities. It resolves AUTOMATIC-mode types only.
//
// This is the mutation gate, and it is structurally airtight: the coordinator wires a reconciler only for a
// type returned here, so a manual-mode type has no reconciler in the runtime at all and cannot be mutated by
// any code path, including the shutdown drain. Callers that mean "which types does this connector handle in
// SOME mode" (reporting, availability, observation) must use InstrumentationTypes instead — keeping this
// function's existing narrow meaning is what makes every pre-existing caller correct by default.
func (s CloudConnectorSpec) ActiveInstrumentationTypes(caps Capabilities) []Type {
	return s.Workloads.Instrumentation.activeTypes(caps.workloadInstrumentation())
}

// InstrumentationTypes returns every instrumentation type this connector handles in EITHER mode: the
// automatic-mode types plus the manual-mode ones the customer instruments themselves.
//
// Used for reporting and availability, never to authorize a mutation — see ActiveInstrumentationTypes. A
// manual-mode type must appear here so a workload marked for instrumentation reports as awaiting the
// customer's deployment rather than as "no instrumentation defined for this workload".
func (s CloudConnectorSpec) InstrumentationTypes(caps Capabilities) []Type {
	return s.Workloads.Instrumentation.activeTypes(caps.workloadInstrumentationAnyMode())
}

// ActiveComputePlatformTypes returns the compute-platform types to reconcile by installing an Odigos
// agent, given the capabilities. These come from the installation feature, which is not supported yet
// (declares no types), so this currently returns nil. Not gated on a destination — the agent owns OTel
// export.
func (s CloudConnectorSpec) ActiveComputePlatformTypes(caps Capabilities) []Type {
	return s.ComputePlatforms.Installation.activeTypes(caps.computePlatformInstallation())
}

// ActiveWorkloadDiscoveryTypes returns the active workload-discovery types — directly-instrumentable
// resources (e.g. aws.lambda, aws.fargate-task). Gated on the workload discovery capability. The
// connector's workload discovery loop runs these.
func (s CloudConnectorSpec) ActiveWorkloadDiscoveryTypes(caps Capabilities) []Type {
	return s.Workloads.Discovery.activeTypes(caps.workloadDiscovery())
}

// ActiveComputePlatformDiscoveryTypes returns the active compute-platform-discovery types — managed infra
// (e.g. aws.ecs-cluster, aws.fargate-cluster, aws.eks, aws.ec2). Gated on the compute-platform discovery
// capability. The connector's compute-platform discovery loop runs these.
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

// RequiredPermissions returns the de-duplicated permission strings to validate for the given
// capabilities: the auth baseline, plus each active domain's discovery baseline (e.g. region enumeration),
// plus every active discovery/instrumentation/installation type's permissions. Used by test-connection
// for remediation messages and by providers during probes.
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
	// Deliberately ActiveInstrumentationTypes (automatic only), NOT InstrumentationTypes: Type.Permissions is
	// the set needed to MUTATE the workload, so aggregating it for a manual-mode type would demand exactly the
	// mutation permissions manual mode exists to avoid. A manual-mode type therefore contributes nothing here
	// beyond its DISCOVERY permissions — and that is the whole mechanism, not a gap: the extra reads manual mode
	// needs to judge conformance (ecs:DescribeTaskDefinition, run.revisions.*, containerApps/revisions/read) are
	// declared on the discovery type, because discovery is the only thing manual mode runs.
	//
	// A separate observe-permissions list was considered and rejected. Nothing consumes this function to build
	// the policy the customer actually applies — that is aggregated client-side in ui-kit from each type's
	// declared permissions, gated on the same two capability booleans — so a new field would have to be threaded
	// through CloudConnectorType in central-backend and then through ui-kit before it rendered anywhere. Until
	// both shipped, the rendered policy would OMIT the reads and manual-mode observation would fail with 403s in
	// the field. Declaring them on discovery makes them render today, in every access level, with no change
	// outside connectors/.
	//
	// The cost is that automatic mode is also granted those reads. They are read-only, and automatic mode wants
	// them anyway: judging whether instrumentation is effective (serving traffic) rather than merely declared is
	// the same question in both modes.
	for _, t := range s.ActiveInstrumentationTypes(caps) {
		add(t.Permissions)
	}
	for _, t := range s.ActiveComputePlatformTypes(caps) {
		add(t.Permissions)
	}
	return out
}
