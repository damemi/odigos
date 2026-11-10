package store

import (
	"context"
	"encoding/json"
)

// GetConfig returns the connector's stored config overrides (only the explicitly-set keys; code defaults
// are merged on top by config.Manager). An absent row yields an empty map, not an error.
func (s *Store) GetConfig(ctx context.Context) (map[string]any, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT config FROM connector_platform.connector_config WHERE connector_name = $1`,
		s.connectorName).Scan(&raw)
	if isNoRows(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		storeLog.Error("get connector config failed", "connector", s.connectorName, "error", err)
		return nil, err
	}
	out := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			// Don't fail on a corrupt row — fall back to defaults — but never swallow it silently.
			storeLog.Warn("connector config unmarshal failed; using defaults", "connector", s.connectorName, "error", err)
			return map[string]any{}, nil
		}
	}
	return out, nil
}

// SetConfig persists the connector's config overrides (the explicitly-set keys). Callers pass the full
// override map; it is stored verbatim and merged over defaults on read.
func (s *Store) SetConfig(ctx context.Context, overrides map[string]any) error {
	if overrides == nil {
		overrides = map[string]any{}
	}
	b, err := json.Marshal(overrides)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO connector_platform.connector_config (connector_name, config, updated_at)
		VALUES ($1, $2::jsonb, now())
		ON CONFLICT (connector_name) DO UPDATE SET config = EXCLUDED.config, updated_at = now()
	`, s.connectorName, string(b))
	if err != nil {
		storeLog.Error("set connector config failed", "connector", s.connectorName, "error", err)
	}
	return err
}
