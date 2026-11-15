package notify

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Deps is the shared infrastructure handed to every Factory.
type Deps struct {
	// HTTPClient is used for every outbound request; tests swap the transport.
	HTTPClient *http.Client
}

// DefaultDeps returns Deps with a bounded-timeout HTTP client so a hung
// backend cannot stall a tick.
func DefaultDeps() Deps {
	return Deps{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

type entry struct {
	desc    Descriptor
	factory Factory
}

// Registry is the backend catalog: every supported integration type with its
// Descriptor and Factory. Adding a backend is one Register call from its
// package (see sinks/all.go) — the dispatcher and API are generic over the
// registry.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]entry
	deps    Deps
}

// NewRegistry returns an empty registry whose sinks share deps.
func NewRegistry(deps Deps) *Registry {
	if deps.HTTPClient == nil {
		deps.HTTPClient = DefaultDeps().HTTPClient
	}
	return &Registry{entries: make(map[string]entry), deps: deps}
}

// Register adds a backend. It panics on a duplicate or malformed descriptor
// because registration happens at startup from static code.
func (r *Registry) Register(desc Descriptor, factory Factory) {
	if desc.Type == "" || factory == nil {
		panic("notify: Register requires a type and a factory")
	}
	if desc.Capabilities.MaxBatch < 1 {
		desc.Capabilities.MaxBatch = 1
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.entries[desc.Type]; dup {
		panic(fmt.Sprintf("notify: backend %q registered twice", desc.Type))
	}
	r.entries[desc.Type] = entry{desc: desc, factory: factory}
}

// Descriptors lists the catalog sorted by type for a stable API/UI order.
func (r *Registry) Descriptors() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Descriptor, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.desc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Descriptor returns the catalog entry for a backend type.
func (r *Registry) Descriptor(typ string) (Descriptor, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[typ]
	return e.desc, ok
}

// Build validates the integration's config against its descriptor and
// constructs the sink. It is the single path from a stored Integration to a
// live Sink, used by the dispatcher and by the test endpoint.
func (r *Registry) Build(in repo.Integration) (Sink, Descriptor, error) {
	r.mu.RLock()
	e, ok := r.entries[in.Type]
	deps := r.deps
	r.mu.RUnlock()
	if !ok {
		return nil, Descriptor{}, fmt.Errorf("unknown integration type %q", in.Type)
	}
	cfg, err := ValidateConfig(e.desc.Fields, in.Config)
	if err != nil {
		return nil, e.desc, err
	}
	s, err := e.factory(cfg, deps)
	if err != nil {
		return nil, e.desc, err
	}
	return s, e.desc, nil
}
