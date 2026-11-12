package store

import (
	"context"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/connectors/postgres"
)

// CollectorResourceState records whether Odigos may have created cloud resources
// for a regional Collector. It is independent of the provider's infrastructure
// row, which can be absent before provisioning and is removed after teardown.
type CollectorResourceState string

const (
	CollectorResourcesNotCreated   CollectorResourceState = "not-created"
	CollectorResourcesProvisioning CollectorResourceState = "provisioning"
	CollectorResourcesCreated      CollectorResourceState = "created"
	CollectorResourcesTearingDown  CollectorResourceState = "tearing-down"
	CollectorResourcesMoving       CollectorResourceState = "moving"
	CollectorResourcesOrphaned     CollectorResourceState = "orphaned"
	CollectorResourcesDestroyed    CollectorResourceState = "destroyed"
)

func (s *Store) GetCollectorResourceState(ctx context.Context, region string) (CollectorResourceState, bool, error) {
	var state CollectorResourceState
	err := s.pool.QueryRow(ctx, `
		SELECT state FROM `+postgres.TableCollectorResourceState+`
		WHERE connector_name = $1 AND provider_schema = $2 AND region = $3
	`, s.connectorName, s.schema, region).Scan(&state)
	if err != nil {
		if isNoRows(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading Collector resource state for region %s: %w", region, err)
	}
	return state, true, nil
}

// CollectorResourceStateDetail carries the target VPC while a network move is
// removing the old stack. The intent survives connector restarts.
func (s *Store) CollectorResourceStateDetail(ctx context.Context, region string) (string, error) {
	var detail *string
	err := s.pool.QueryRow(ctx, `
		SELECT detail FROM `+postgres.TableCollectorResourceState+`
		WHERE connector_name = $1 AND provider_schema = $2 AND region = $3
	`, s.connectorName, s.schema, region).Scan(&detail)
	if err != nil {
		return "", fmt.Errorf("reading Collector resource-state detail for %s: %w", region, err)
	}
	if detail == nil {
		return "", nil
	}
	return *detail, nil
}

// CollectorResourceStateUpdatedAt is the start of the current lifecycle state.
// A retry leaves this timestamp untouched, so elapsed teardown time survives restarts.
func (s *Store) CollectorResourceStateUpdatedAt(ctx context.Context, region string) (time.Time, error) {
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT updated_at FROM `+postgres.TableCollectorResourceState+`
		WHERE connector_name = $1 AND provider_schema = $2 AND region = $3
	`, s.connectorName, s.schema, region).Scan(&updatedAt)
	if err != nil {
		return time.Time{}, fmt.Errorf("reading Collector resource-state timestamp for %s: %w", region, err)
	}
	return updatedAt, nil
}

func (s *Store) ListCollectorResourceStates(ctx context.Context) (map[string]CollectorResourceState, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT region, state FROM `+postgres.TableCollectorResourceState+`
		WHERE connector_name = $1 AND provider_schema = $2
	`, s.connectorName, s.schema)
	if err != nil {
		return nil, fmt.Errorf("listing Collector resource states: %w", err)
	}
	defer rows.Close()
	states := make(map[string]CollectorResourceState)
	for rows.Next() {
		var region string
		var state CollectorResourceState
		if err := rows.Scan(&region, &state); err != nil {
			return nil, fmt.Errorf("scanning Collector resource state: %w", err)
		}
		states[region] = state
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing Collector resource states: %w", err)
	}
	return states, nil
}

func (s *Store) SetCollectorResourceState(ctx context.Context, region string, state CollectorResourceState, detail string) error {
	// Repeated retry writes must not move the teardown deadline forward.
	_, err := s.pool.Exec(ctx, `
		INSERT INTO `+postgres.TableCollectorResourceState+` AS current_state
			(connector_name, provider_schema, region, state, detail)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (connector_name, provider_schema, region) DO UPDATE SET
			state = EXCLUDED.state,
			detail = EXCLUDED.detail,
			updated_at = CASE
				WHEN current_state.state = 'tearing-down' AND EXCLUDED.state = 'tearing-down'
				THEN current_state.updated_at
				ELSE now()
			END
	`, s.connectorName, s.schema, region, state, detail)
	if err != nil {
		return fmt.Errorf("recording Collector resource state %s for region %s: %w", state, region, err)
	}
	return nil
}
