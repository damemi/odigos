package config

import (
	"context"
	"fmt"
	"sync"
	"time"

	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
)

var cfgLog = connlog.Subsystem("config")

// Config keys. These are also the agents-api config-map keys exposed via the GraphQL config/updateConfig
// operations, so the map is the single source of truth for operational tunables. Bootstrap settings
// (credentials, account, provider, DSN, capabilities) are NOT here — they stay on the command line/CR.
const (
	KeyDiscoveryInterval = "discoveryInterval"
	KeyHeartbeatInterval = "heartbeatInterval"
	KeyReconcileInterval = "reconcileInterval"
	KeyShutdownTimeout   = "shutdownTimeout"
	KeyDiscoveryTags     = "discoveryTags"
)

// durationKeys are the config keys whose values are Go duration strings (e.g. "5m"); used for validation
// and typed reads.
var durationKeys = []string{KeyDiscoveryInterval, KeyHeartbeatInterval, KeyReconcileInterval, KeyShutdownTimeout}

// Defaults returns the base (provider-agnostic) operational config defaults. Providers merge their own
// defaults on top (MergeDefaults) before constructing the Manager, so every key has a value even when the
// connector has never been configured.
func Defaults() map[string]any {
	return map[string]any{
		KeyDiscoveryInterval: "5m",
		KeyHeartbeatInterval: "30s",
		KeyReconcileInterval: "1m",
		KeyShutdownTimeout:   "10m",
		KeyDiscoveryTags:     map[string]any{},
		// The managed-Collector tree. Declared here, provider-agnostically, so config.Manager
		// accepts an updateConfig patch for it — the Manager rejects unknown top-level keys, and the
		// per-region ids inside are discovered dynamically and cannot be declared in advance.
		//
		// Present on every connector, including one with no collectors capability: `enabled` is false,
		// so it is inert, and making the defaults depend on capabilities would make the config surface
		// differ between connectors for no gain.
		collectorsettings.Key: collectorsettings.Defaults()[collectorsettings.Key],
	}
}

// MergeDefaults returns base with extra overlaid (extra wins). Used to fold provider-specific defaults
// (e.g. a provider's debug toggle) into the base defaults.
func MergeDefaults(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// Settings is the typed view of the base tunables, extracted from the effective config.
type Settings struct {
	DiscoveryInterval time.Duration
	HeartbeatInterval time.Duration
	ReconcileInterval time.Duration
	ShutdownTimeout   time.Duration
	DiscoveryTags     map[string]string
}

// Store is the persistence the Manager reads/writes the config-override map through (satisfied by the base
// *store.Store). Kept as a local interface so config doesn't import store.
type Store interface {
	GetConfig(ctx context.Context) (map[string]any, error)
	SetConfig(ctx context.Context, overrides map[string]any) error
}

// Manager holds the effective connector config (code defaults overlaid with the DB-stored overrides) and
// serves it live: the loops read the current Settings each cycle and updateConfig applies without a
// restart. Concurrency-safe.
type Manager struct {
	store    Store
	defaults map[string]any
	mu       sync.RWMutex
	stored   map[string]any // explicit overrides from the DB
	eff      map[string]any // defaults + stored
}

// NewManager builds a Manager over the config store and the merged defaults. Call Refresh before use to
// load the stored overrides; until then it serves pure defaults.
func NewManager(store Store, defaults map[string]any) *Manager {
	m := &Manager{store: store, defaults: defaults, stored: map[string]any{}}
	m.recompute()
	return m
}

// Refresh reloads the stored overrides from the DB and recomputes the effective config.
func (m *Manager) Refresh(ctx context.Context) error {
	stored, err := m.store.GetConfig(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.stored = stored
	m.recompute()
	m.mu.Unlock()
	return nil
}

// recompute rebuilds the effective map (defaults overlaid with stored). Caller holds the write lock, or it
// is called from the constructor before the Manager is shared.
func (m *Manager) recompute() {
	eff := make(map[string]any, len(m.defaults)+len(m.stored))
	for k, v := range m.defaults {
		eff[k] = v
	}
	for k, v := range m.stored {
		eff[k] = v
	}
	m.eff = eff
}

// Effective returns a copy of the effective config map (defaults + overrides) for the GraphQL config query.
func (m *Manager) Effective() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return copyMap(m.eff)
}

// Defaults returns a copy of the code defaults for the GraphQL configDefaults query.
func (m *Manager) Defaults() map[string]any { return copyMap(m.defaults) }

// Update merges patch into the stored overrides (a nil value deletes an override, reverting to default),
// validates the result, persists it, and refreshes so the change applies live. Only known keys are
// accepted so a typo can't silently do nothing.
func (m *Manager) Update(ctx context.Context, patch map[string]any) error {
	m.mu.RLock()
	merged := copyMap(m.stored)
	m.mu.RUnlock()
	for k, v := range patch {
		if _, known := m.defaults[k]; !known {
			return fmt.Errorf("unknown config key %q", k)
		}
		if v == nil {
			delete(merged, k)
			continue
		}
		merged[k] = v
	}
	if err := validate(mergeOver(m.defaults, merged)); err != nil {
		return err
	}
	if err := m.store.SetConfig(ctx, merged); err != nil {
		return err
	}
	cfgLog.Info("connector config updated", "keys", len(patch))
	return m.Refresh(ctx)
}

// Settings extracts the typed base tunables from the effective config, falling back to the hardcoded
// defaults for any missing/malformed value (belt-and-suspenders — defaults always populate these keys).
func (m *Manager) Settings() Settings {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Settings{
		DiscoveryInterval: durationOf(m.eff, KeyDiscoveryInterval, 5*time.Minute),
		HeartbeatInterval: durationOf(m.eff, KeyHeartbeatInterval, 30*time.Second),
		ReconcileInterval: durationOf(m.eff, KeyReconcileInterval, time.Minute),
		ShutdownTimeout:   durationOf(m.eff, KeyShutdownTimeout, 10*time.Minute),
		DiscoveryTags:     stringMapOf(m.eff, KeyDiscoveryTags),
	}
}

// Bool reads a boolean config value (for provider keys such as a debug toggle), falling back to def.
func (m *Manager) Bool(key string, def bool) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if v, ok := m.eff[key].(bool); ok {
		return v
	}
	return def
}

// validate checks the effective config is well-formed (durations parse, tags is a string map) so a bad
// updateConfig is rejected rather than silently ignored at read time.
func validate(eff map[string]any) error {
	for _, k := range durationKeys {
		if v, ok := eff[k]; ok {
			s, isStr := v.(string)
			if !isStr {
				return fmt.Errorf("config %q must be a duration string (e.g. \"5m\")", k)
			}
			if _, err := time.ParseDuration(s); err != nil {
				return fmt.Errorf("config %q: invalid duration %q: %w", k, s, err)
			}
		}
	}
	if v, ok := eff[KeyDiscoveryTags]; ok {
		if _, err := toStringMap(v); err != nil {
			return fmt.Errorf("config %q: %w", KeyDiscoveryTags, err)
		}
	}
	// Shape only. collectorsettings.Read is deliberately forgiving about individual fields — one
	// mistyped value on one network must not stop a connector reconciling anything — so the gross
	// error is caught here, at write time, where there is somebody to tell.
	if v, ok := eff[collectorsettings.Key]; ok {
		if _, isMap := v.(map[string]any); !isMap {
			return fmt.Errorf("config %q must be an object with enabled/defaults/networks", collectorsettings.Key)
		}
	}
	return nil
}

func durationOf(m map[string]any, key string, def time.Duration) time.Duration {
	s, ok := m[key].(string)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

func stringMapOf(m map[string]any, key string) map[string]string {
	out, err := toStringMap(m[key])
	if err != nil {
		return nil
	}
	return out
}

// toStringMap coerces a config value (nil, map[string]string, or JSON-decoded map[string]any of strings)
// into a map[string]string, erroring on a non-string value.
func toStringMap(v any) (map[string]string, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case map[string]string:
		return t, nil
	case map[string]any:
		out := make(map[string]string, len(t))
		for k, val := range t {
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("tag %q must be a string", k)
			}
			out[k] = s
		}
		return out, nil
	default:
		return nil, fmt.Errorf("must be an object of string tags")
	}
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mergeOver(base, over map[string]any) map[string]any {
	out := copyMap(base)
	for k, v := range over {
		out[k] = v
	}
	return out
}
