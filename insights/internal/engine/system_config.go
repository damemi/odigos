package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// systemSnapshot pairs the settings with the identity fingerprint derived from
// them, so a reader can never see a fingerprint from a different generation than
// the dimensions it is using to build transaction identities.
type systemSnapshot struct {
	settings            repo.SystemSettings
	identityFingerprint string
}

type SystemConfig struct {
	snap atomic.Pointer[systemSnapshot]
}

// defaultSystemSnapshot is what readers get before any settings have loaded:
// compiled defaults, derived once.
var defaultSystemSnapshot = sync.OnceValue(func() *systemSnapshot {
	return newSystemSnapshot(repo.DefaultSystemSettings())
})

func newSystemConfig(settings repo.SystemSettings) *SystemConfig {
	cfg := &SystemConfig{}
	cfg.snap.Store(newSystemSnapshot(settings))
	return cfg
}

func newSystemSnapshot(settings repo.SystemSettings) *systemSnapshot {
	resolved := settings.WithDefaults()
	return &systemSnapshot{
		settings:            resolved,
		identityFingerprint: repo.IdentityDimensionsFingerprint(resolved.TransactionIdentityDimensions),
	}
}

func LoadSystemConfig(ctx context.Context, r repo.Repository) (*SystemConfig, error) {
	settings, err := r.GetSystemSettings(ctx)
	if err != nil {
		return newSystemConfig(repo.DefaultSystemSettings()), err
	}
	return newSystemConfig(settings), nil
}

func (cfg *SystemConfig) Reload(ctx context.Context, r repo.Repository) error {
	settings, err := r.GetSystemSettings(ctx)
	if err != nil {
		return err
	}
	cfg.snap.Store(newSystemSnapshot(settings))
	return nil
}

// snapshot returns the generation readers should use. Callers that need more
// than one field (settings plus the derived fingerprint) take the snapshot once
// so their values cannot straddle a reload.
func (cfg *SystemConfig) snapshot() *systemSnapshot {
	if cfg != nil {
		if snap := cfg.snap.Load(); snap != nil {
			return snap
		}
	}
	return defaultSystemSnapshot()
}

func (cfg *SystemConfig) Get() repo.SystemSettings {
	return cfg.snapshot().settings
}

// IdentityFingerprint returns the stamp of the identity scheme this generation
// builds transaction identities with. One atomic load and no hashing: the stamp
// is derived per reload in newSystemSnapshot, so callers on the per-trace path
// only read a string.
func (cfg *SystemConfig) IdentityFingerprint() string {
	return cfg.snapshot().identityFingerprint
}

func (cfg *SystemConfig) ExampleSampleInterval() time.Duration {
	return time.Duration(cfg.Get().ExampleSampleIntervalSeconds) * time.Second
}

func (cfg *SystemConfig) BaselineFlushInterval() time.Duration {
	secs := cfg.Get().FlushIntervalSeconds
	if secs <= 0 {
		secs = repo.DefaultFlushIntervalSeconds
	}
	return time.Duration(secs) * time.Second
}
