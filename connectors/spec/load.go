package spec

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed data/*
var dataFS embed.FS

func init() {
	if err := loadEmbedded(dataFS); err != nil {
		panic(fmt.Sprintf("connectors/spec: load embedded specs: %v", err))
	}
}

// loadEmbedded reads every *.yaml / *.yml under data/ and registers each parsed spec. Mirrors the
// odigos destinations/data layout: one file per provider family (aws.yaml, gcp.yaml, …).
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

// Parse unmarshals a single connector spec YAML document into a typed CloudConnectorSpec, validating
// that the provider family is set. Provider YAML files live under connectors/spec/data/ and are loaded
// at package init (see loadEmbedded).
func Parse(data []byte) (CloudConnectorSpec, error) {
	var s CloudConnectorSpec
	// KnownFields(true) rather than yaml.Unmarshal: an undeclared key is a typo, and silently discarding it
	// means the connector runs with a default the spec author believed they had overridden. A misspelled
	// permission list or feature flag would otherwise surface only as wrong runtime behavior in a customer
	// account. Every key a provider YAML uses must therefore have a field on these structs.
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
	return s, nil
}

// validateSetupArtifacts enforces the invariants the artifact list's consumers assume.
//
// The id must be unique: it is the only key an artifact has, and it is what a UI keys a rendered list by and
// what a test names when an artifact is incomplete. A duplicate does not fail anywhere — one entry simply
// shadows the other, in whichever place happens to index by id — so it has to be rejected here.
//
// Within a section, the variant must be unique PER LANGUAGE, because distinct variants in a section ARE the
// "choose a method" chooser and the language is chosen for the customer (from the workload's detected runtime,
// or its override): two artifacts claiming the same variant for the same language give the chooser two
// identical options and only one of them can ever be selected. The same variant for two different languages is
// the normal shape — one Terraform snippet per language — because only one language's set is ever served at a
// time. A section with a single artifact legitimately declares no variant.
//
// An instrumentation section must also not MIX language-scoped and language-neutral artifacts.
// InstrumentationArtifactsFor prefers the language-scoped ones and falls back to the neutral ones only when a
// language has none, so a section holding both would serve a Java workload its Java variants and silently
// withhold every neutral variant beside them — a chooser missing options that exist, which reads as the
// connector not supporting a tool it does support.
func validateSetupArtifacts(s CloudConnectorSpec) error {
	ids := map[string]bool{}
	variants := map[string]bool{}
	// Per instrumentation section: whether it has declared a language-scoped / a language-neutral artifact.
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
			// Outside an instrumentation section nothing selects by language, so a language here is a
			// declaration that quietly does nothing — a policy document has no language.
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

// validateFeatureTypes enforces the two-domain taxonomy. In each domain, discovery is the superset: every
// discovery type has a unique name and no injection method. The domain's second feature references those
// discovery types by name: workloads.instrumentation instruments a discovered workload directly (method
// required), computePlatforms.installation installs an agent onto a discovered compute platform (method
// optional but valid if set). Type names are unique across BOTH domains. It runs regardless of a feature's
// Enabled flag so a disabled-but-declared feature cannot smuggle an invalid taxonomy past parse.
func validateFeatureTypes(s CloudConnectorSpec) error {
	validMethods := map[string]bool{MethodAgent: true, MethodSidecar: true, MethodService: true}

	wlDisc, err := validateDiscovery(s.Workloads.Discovery, "workloads.discovery", nil)
	if err != nil {
		return err
	}
	cpDisc, err := validateDiscovery(s.ComputePlatforms.Discovery, "computePlatforms.discovery", wlDisc)
	if err != nil {
		return err
	}
	// workloads.instrumentation types MUST declare a method and reference a workload-discovery type.
	if err := validateInjectableTypes(s.Workloads.Instrumentation, "workloads.instrumentation", wlDisc, validMethods, true); err != nil {
		return err
	}
	// computePlatforms.installation types reference a compute-platform-discovery type; a method is optional
	// but must be valid if set. Currently this feature declares no types (installation is not supported).
	if err := validateInjectableTypes(s.ComputePlatforms.Installation, "computePlatforms.installation", cpDisc, validMethods, false); err != nil {
		return err
	}
	return nil
}

// validateDiscovery validates a domain's discovery feature: each type has a unique name (across this
// feature and the already-seen `taken` set from the other domain) and declares no injection method. It
// returns the set of discovery type names declared here.
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
			return nil, fmt.Errorf("connector spec: %s type %q is already declared in the other domain (type names must be unique across domains)", label, t.Name)
		}
		names[t.Name] = true
		if t.Method != "" {
			return nil, fmt.Errorf("connector spec: %s type %q must not set a method (got %q)", label, t.Name, t.Method)
		}
	}
	return names, nil
}

// validateInjectableTypes validates a feature whose types reference their domain's discovery types
// (instrumentation / installation): each type needs a unique name and a matching discovery type in the
// same domain. A method is required when requireMethod is true; otherwise it is optional but, if set,
// must be valid.
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
				return fmt.Errorf("connector spec: %s type %q requires a method (agent|sidecar|service)", label, t.Name)
			}
			continue
		}
		if !validMethods[t.Method] {
			return fmt.Errorf("connector spec: %s type %q has invalid method %q", label, t.Name, t.Method)
		}
	}
	return nil
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

// MustParse is Parse that panics on error. Provider spec packages use it on their embedded YAML, which
// is validated in tests, so a failure is a build-time programming error surfaced immediately.
func MustParse(data []byte) CloudConnectorSpec {
	s, err := Parse(data)
	if err != nil {
		panic(fmt.Sprintf("connectors/spec: %v", err))
	}
	return s
}

// --- provider registry ---
//
// Specs from connectors/spec/data/*.yaml are registered in init(). Importing this package makes every
// provider available through Get/All.

var (
	mu              sync.RWMutex
	specsByProvider = map[string]CloudConnectorSpec{}
)

// Register adds (or replaces) a provider's spec in the registry. Used by loadEmbedded and tests.
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

// MustGet returns the spec for a provider, panicking if it is not registered (import connectors/spec
// so embedded data/*.yaml are loaded).
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
