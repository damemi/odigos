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

// Capabilities is the customer's allow-list, split into the two domains to mirror the CRD
// (OdigosCloudConnectorSpec.Workloads / .ComputePlatforms) and the spec's own two hierarchies. The
// connector resolves active detectors/reconcilers by intersecting these with the spec's supported types.
type Capabilities struct {
	Workloads        []WorkloadCapability        `json:"workloads,omitempty" yaml:"workloads,omitempty"`
	ComputePlatforms []ComputePlatformCapability `json:"computePlatforms,omitempty" yaml:"computePlatforms,omitempty"`
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

// ActiveInstrumentationTypes returns the workload instrumentation types to reconcile, given the capabilities.
func (s CloudConnectorSpec) ActiveInstrumentationTypes(caps Capabilities) []Type {
	return s.Workloads.Instrumentation.activeTypes(caps.workloadInstrumentation())
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
	for _, t := range s.ActiveInstrumentationTypes(caps) {
		add(t.Permissions)
	}
	for _, t := range s.ActiveComputePlatformTypes(caps) {
		add(t.Permissions)
	}
	return out
}
