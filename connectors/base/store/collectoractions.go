package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectoraction"
)

// ListCollectorActions returns the actions owned by a single regional Collector.
func (s *Store) ListCollectorActions(ctx context.Context, region string) ([]collectoraction.Action, error) {
	return s.listCollectorActions(ctx, `WHERE connector_name = $1 AND region = $2 ORDER BY name`, s.connectorName, region)
}

// ListAllCollectorActions is intentionally available for a future copy-actions
// workflow. It returns independent regional definitions; it never implies that
// an action belongs to every Collector.
func (s *Store) ListAllCollectorActions(ctx context.Context) ([]collectoraction.Action, error) {
	return s.listCollectorActions(ctx, `WHERE connector_name = $1 ORDER BY region, name`, s.connectorName)
}

func (s *Store) listCollectorActions(ctx context.Context, clause string, args ...any) ([]collectoraction.Action, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT region, name, action_type, action_name, notes, disabled, signals, pipelines, order_hint, config
		FROM %s.collector_actions %s
	`, s.schema, clause), args...)
	if err != nil {
		return nil, fmt.Errorf("listing Collector actions: %w", err)
	}
	defer rows.Close()
	var out []collectoraction.Action
	for rows.Next() {
		a, err := scanCollectorAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing Collector actions: %w", err)
	}
	return out, nil
}

func (s *Store) GetCollectorAction(ctx context.Context, region, name string) (*collectoraction.Action, error) {
	row := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT region, name, action_type, action_name, notes, disabled, signals, pipelines, order_hint, config
		FROM %s.collector_actions WHERE connector_name = $1 AND region = $2 AND name = $3
	`, s.schema), s.connectorName, region, name)
	a, err := scanCollectorAction(row)
	if isNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading action %q of Collector %s: %w", name, region, err)
	}
	return &a, nil
}

func (s *Store) UpsertCollectorAction(ctx context.Context, a collectoraction.Action) error {
	signals, err := json.Marshal(nonNilSlice(a.Signals))
	if err != nil {
		return err
	}
	pipelines, err := json.Marshal(nonNilSlice(a.Pipelines))
	if err != nil {
		return err
	}
	config := a.Config
	if len(config) == 0 {
		config = []byte("{}")
	}
	_, err = s.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s.collector_actions (connector_name, region, name, action_type, action_name, notes, disabled, signals, pipelines, order_hint, config)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10,$11::jsonb)
		ON CONFLICT (connector_name, region, name) DO UPDATE SET
			action_type=EXCLUDED.action_type, action_name=EXCLUDED.action_name, notes=EXCLUDED.notes,
			disabled=EXCLUDED.disabled, signals=EXCLUDED.signals, pipelines=EXCLUDED.pipelines,
			order_hint=EXCLUDED.order_hint, config=EXCLUDED.config, updated_at=now()
	`, s.schema), s.connectorName, a.Region, a.Name, a.Type, a.ActionName, a.Notes, a.Disabled, string(signals), string(pipelines), a.OrderHint, string(config))
	if err != nil {
		return fmt.Errorf("writing action %q of Collector %s: %w", a.Name, a.Region, err)
	}
	return nil
}

func (s *Store) DeleteCollectorAction(ctx context.Context, region, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s.collector_actions WHERE connector_name=$1 AND region=$2 AND name=$3`, s.schema), s.connectorName, region, name)
	if err != nil {
		return false, fmt.Errorf("deleting action %q of Collector %s: %w", name, region, err)
	}
	return tag.RowsAffected() > 0, nil
}

func (s *Store) DeleteCollectorActionsForRegion(ctx context.Context, region string) error {
	_, err := s.pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s.collector_actions WHERE connector_name=$1 AND region=$2`, s.schema), s.connectorName, region)
	if err != nil {
		return fmt.Errorf("deleting actions of Collector %s: %w", region, err)
	}
	return nil
}

func scanCollectorAction(row pgx.Row) (collectoraction.Action, error) {
	var a collectoraction.Action
	var signals, pipelines []byte
	if err := row.Scan(&a.Region, &a.Name, &a.Type, &a.ActionName, &a.Notes, &a.Disabled, &signals, &pipelines, &a.OrderHint, &a.Config); err != nil {
		return collectoraction.Action{}, err
	}
	if err := json.Unmarshal(signals, &a.Signals); err != nil {
		return collectoraction.Action{}, fmt.Errorf("decode action %q signals: %w", a.Name, err)
	}
	if err := json.Unmarshal(pipelines, &a.Pipelines); err != nil {
		return collectoraction.Action{}, fmt.Errorf("decode action %q pipelines: %w", a.Name, err)
	}
	return a, nil
}
