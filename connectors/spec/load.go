package spec

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"gopkg.in/yaml.v3"
)

//go:embed data/*
var dataFS embed.FS

func init() {
	if err := loadEmbedded(dataFS); err != nil {
		panic(fmt.Sprintf("connectors/spec: load embedded specs: %v", err))
	}
}

// loadEmbedded reads every *.yaml / *.yml under data/ and registers each parsed spec.
// Layout mirrors odigos destinations/data: one file per provider family.
func loadEmbedded(root embed.FS) error {
	return fs.WalkDir(root, "data", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			return nil
		}
		raw, err := root.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		s, err := Parse(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		Register(s)
		return nil
	})
}

// Parse unmarshals a connector spec YAML document. Provider YAML lives under
// connectors/spec/data/ and is loaded at package init via loadEmbedded.
func Parse(data []byte) (CloudConnectorSpec, error) {
	var s CloudConnectorSpec
	// KnownFields(true): an undeclared key is almost always a typo. Silent discard
	// would leave a default the author thought they overrode, and the bug would
	// only show up as wrong runtime behavior in a customer account.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return CloudConnectorSpec{}, fmt.Errorf("parse connector spec: %w", err)
	}
	if s.Provider == "" {
		return CloudConnectorSpec{}, fmt.Errorf("connector spec: provider is required")
	}
	if err := validateAuthMethods(s); err != nil {
		return CloudConnectorSpec{}, err
	}
	if err := validateFeatureTypes(s); err != nil {
		return CloudConnectorSpec{}, err
	}
	if err := validateSetupArtifacts(s); err != nil {
		return CloudConnectorSpec{}, err
	}
	if err := validateCollectorInstructions(s.Collectors.Instructions); err != nil {
		return CloudConnectorSpec{}, err
	}
	return s, nil
}

func validateCollectorInstructions(instructions CollectorInstructions) error {
	variants := map[string]bool{}
	ids := map[string]bool{}
	checkTemplate := func(label, source string) error {
		if _, err := template.New(label).Funcs(template.FuncMap{"quote": strconv.Quote}).Parse(source); err != nil {
			return fmt.Errorf("connector spec: Collector %s template: %w", label, err)
		}
		return nil
	}
	for i, note := range instructions.Notes {
		if err := checkTemplate(fmt.Sprintf("note %d", i), note); err != nil {
			return err
		}
	}
	for _, document := range instructions.Documents {
		if document.ID == "" || document.Variant == "" || document.Template == "" {
			return fmt.Errorf("connector spec: Collector instruction document requires id, variant and template")
		}
		if ids[document.ID] || variants[document.Variant] {
			return fmt.Errorf("connector spec: duplicate Collector instruction document %q (%q)", document.ID, document.Variant)
		}
		ids[document.ID], variants[document.Variant] = true, true
		for label, source := range map[string]string{"title": document.Title, "description": document.Description, "content": document.Template} {
			if err := checkTemplate(document.ID+" "+label, source); err != nil {
				return err
			}
		}
		for i, step := range document.Steps {
			if err := checkTemplate(fmt.Sprintf("%s step %d", document.ID, i), step); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateSetupArtifacts enforces invariants consumers of the artifact list assume:
//
//   - IDs are unique (the only key; duplicates would silently shadow).
//   - Within a section, variants are unique per language. Same variant across
//     languages is normal (one snippet per language; only one language is served).
//   - Instrumentation sections must not mix language-scoped and language-neutral
//     artifacts: InstrumentationArtifactsFor prefers scoped and falls back to
//     neutral only when a language has none — mixing would hide the neutrals.
func validateSetupArtifacts(s CloudConnectorSpec) error {
	ids := map[string]bool{}
	variants := map[string]bool{}
	// Per instrumentation section: has language-scoped / language-neutral artifacts.
	scoped, neutral := map[string]bool{}, map[string]bool{}
	for _, a := range s.Setup.Artifacts {
		if a.ID == "" {
			return fmt.Errorf("connector spec: setup artifact id is required")
		}
		if ids[a.ID] {
			return fmt.Errorf("connector spec: duplicate setup artifact id %q", a.ID)
		}
		ids[a.ID] = true

		if strings.HasPrefix(a.Section, instrumentationSectionPrefix) {
			if a.Language == "" {
				neutral[a.Section] = true
			} else {
				scoped[a.Section] = true
			}
			if scoped[a.Section] && neutral[a.Section] {
				return fmt.Errorf("connector spec: setup section %q mixes language-scoped and language-neutral artifacts; "+
					"a language that declares its own would be served those alone and the neutral ones withheld", a.Section)
			}
		} else if a.Language != "" {
			// Outside instrumentation, nothing selects by language.
			return fmt.Errorf("connector spec: setup artifact %q declares language %q outside an instrumentation section (%q), where nothing selects by it",
				a.ID, a.Language, a.Section)
		}

		if a.Variant == "" {
			continue
		}
		k := a.Section + "/" + a.Language + "/" + a.Variant
		if variants[k] {
			if a.Language == "" {
				return fmt.Errorf("connector spec: setup section %q declares variant %q twice", a.Section, a.Variant)
			}
			return fmt.Errorf("connector spec: setup section %q declares variant %q twice for language %q", a.Section, a.Variant, a.Language)
		}
		variants[k] = true
	}
	return nil
}

// validateFeatureTypes enforces the three-domain taxonomy. In each domain one
// feature is the superset (unique names, no method); the domain's second feature
// references those types by name.
//
// collectors has no discovery feature: management is its discovery-like
// superset, so validateDiscovery serves it unchanged.
//
// Type names are unique across all three domains (union threaded through).
// Runs regardless of Enabled so a disabled feature cannot smuggle bad taxonomy.
//
// Valid methods are per-domain: sharing one set would accept sidecar on a
// Collector deployment or fargate-service on instrumentation.
func validateFeatureTypes(s CloudConnectorSpec) error {
	instrumentationMethods := map[string]bool{MethodAgent: true, MethodSidecar: true, MethodService: true}
	collectorMethods := map[string]bool{MethodFargateService: true, MethodContainerApp: true, MethodCloudRunService: true}

	wlDisc, err := validateDiscovery(s.Workloads.Discovery, "workloads.discovery", nil)
	if err != nil {
		return err
	}
	cpDisc, err := validateDiscovery(s.ComputePlatforms.Discovery, "computePlatforms.discovery", wlDisc)
	if err != nil {
		return err
	}
	collMgmt, err := validateDiscovery(s.Collectors.Management, "collectors.management", union(wlDisc, cpDisc))
	if err != nil {
		return err
	}
	// workloads.instrumentation: method required; references a workload-discovery type.
	if err := validateInjectableTypes(s.Workloads.Instrumentation, "workloads.instrumentation", wlDisc, instrumentationMethods, true); err != nil {
		return err
	}
	// computePlatforms.installation: method optional but must be valid if set.
	if err := validateInjectableTypes(s.ComputePlatforms.Installation, "computePlatforms.installation", cpDisc, instrumentationMethods, false); err != nil {
		return err
	}
	// collectors.deployment: method required (tells the deployer which workload kind to create).
	if err := validateInjectableTypes(s.Collectors.Deployment, "collectors.deployment", collMgmt, collectorMethods, true); err != nil {
		return err
	}
	return nil
}

// union merges name sets so uniqueness spans all three domains.
func union(sets ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, s := range sets {
		for name := range s {
			out[name] = true
		}
	}
	return out
}

// validateDiscovery validates a domain's SUPERSET feature (discovery, or
// collectors.management): unique names across this feature and taken, no method.
func validateDiscovery(f Feature, label string, taken map[string]bool) (map[string]bool, error) {
	names := map[string]bool{}
	for _, t := range f.Types {
		if t.Name == "" {
			return nil, fmt.Errorf("connector spec: %s type name is required", label)
		}
		if names[t.Name] {
			return nil, fmt.Errorf("connector spec: %s has duplicate type %q", label, t.Name)
		}
		if taken[t.Name] {
			return nil, fmt.Errorf("connector spec: %s type %q is already declared in another domain (type names must be unique across all three domains)", label, t.Name)
		}
		names[t.Name] = true
		if t.Method != "" {
			return nil, fmt.Errorf("connector spec: %s type %q must not set a method (got %q)", label, t.Name, t.Method)
		}
	}
	return names, nil
}

// validateInjectableTypes validates types that reference their domain's
// discovery/superset names. Method required when requireMethod; otherwise
// optional but must be in validMethods if set.
func validateInjectableTypes(f Feature, label string, discoveryNames map[string]bool, validMethods map[string]bool, requireMethod bool) error {
	seen := map[string]bool{}
	for _, t := range f.Types {
		if t.Name == "" {
			return fmt.Errorf("connector spec: %s type name is required", label)
		}
		if seen[t.Name] {
			return fmt.Errorf("connector spec: %s has duplicate type %q", label, t.Name)
		}
		seen[t.Name] = true
		if !discoveryNames[t.Name] {
			return fmt.Errorf("connector spec: %s type %q has no matching discovery type in the same domain", label, t.Name)
		}
		if t.Method == "" {
			if requireMethod {
				// List methods from the caller's set so the error names the
				// domain-appropriate options (not the other domain's).
				return fmt.Errorf("connector spec: %s type %q requires a method (%s)", label, t.Name, methodList(validMethods))
			}
			continue
		}
		if !validMethods[t.Method] {
			return fmt.Errorf("connector spec: %s type %q has invalid method %q (want one of %s)", label, t.Name, t.Method, methodList(validMethods))
		}
	}
	return nil
}

// methodList renders a valid-method set deterministically for stable error messages.
func methodList(validMethods map[string]bool) string {
	names := make([]string, 0, len(validMethods))
	for m := range validMethods {
		names = append(names, m)
	}
	sort.Strings(names)
	return strings.Join(names, "|")
}

func validateAuthMethods(s CloudConnectorSpec) error {
	seen := map[string]bool{}
	for _, m := range s.Auth.Methods {
		if m.ID == "" {
			return fmt.Errorf("connector spec: auth method id is required")
		}
		if seen[m.ID] {
			return fmt.Errorf("connector spec: duplicate auth method id %q", m.ID)
		}
		seen[m.ID] = true
		if err := validateAuthMethodFields(m); err != nil {
			return err
		}
		for _, base := range m.BaseMethods {
			if base == m.ID {
				return fmt.Errorf("connector spec: auth method %q must not list itself in baseMethods", m.ID)
			}
		}
	}
	for _, m := range s.Auth.Methods {
		for _, base := range m.BaseMethods {
			if !seen[base] {
				return fmt.Errorf("connector spec: auth method %q references unknown baseMethod %q", m.ID, base)
			}
		}
	}
	return nil
}

func validateAuthMethodFields(method AuthMethod) error {
	seen := map[string]bool{}
	for _, f := range method.Fields {
		if f.Name == "" {
			return fmt.Errorf("connector spec: auth method %q field name is required", method.ID)
		}
		if seen[f.Name] {
			return fmt.Errorf("connector spec: auth method %q has duplicate field %q", method.ID, f.Name)
		}
		seen[f.Name] = true
	}
	return nil
}

// MustParse is Parse that panics on error — for embedded YAML validated in tests.
func MustParse(data []byte) CloudConnectorSpec {
	s, err := Parse(data)
	if err != nil {
		panic(fmt.Sprintf("connectors/spec: %v", err))
	}
	return s
}

// --- provider registry ---
// Specs from connectors/spec/data/*.yaml are registered in init().

var (
	mu              sync.RWMutex
	specsByProvider = map[string]CloudConnectorSpec{}
)

// Register adds (or replaces) a provider's spec. Used by loadEmbedded and tests.
func Register(s CloudConnectorSpec) {
	mu.Lock()
	defer mu.Unlock()
	specsByProvider[s.Provider] = s
}

// Get returns the registered spec for a provider family (e.g. "aws").
func Get(provider string) (CloudConnectorSpec, bool) {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := specsByProvider[provider]
	return s, ok
}

// MustGet returns the spec for a provider, panicking if unregistered.
func MustGet(provider string) CloudConnectorSpec {
	s, ok := Get(provider)
	if !ok {
		panic(fmt.Sprintf("connectors/spec: provider %q not registered (missing connectors/spec/data/%s.yaml?)", provider, provider))
	}
	return s
}

// All returns every registered provider spec, ordered by provider name.
func All() []CloudConnectorSpec {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]CloudConnectorSpec, 0, len(specsByProvider))
	for _, s := range specsByProvider {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider < out[j].Provider })
	return out
}

// Providers returns the registered provider names, sorted.
func Providers() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(specsByProvider))
	for p := range specsByProvider {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
