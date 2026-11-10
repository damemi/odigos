package store

import (
	"context"
	"fmt"
	"time"
)

// CollectorLoadBalancerRetry is the durable circuit breaker for a regional AWS
// CreateLoadBalancer account restriction. It contains no credential material.
type CollectorLoadBalancerRetry struct {
	Attempts   int
	RetryAfter time.Time
	RequestID  string
}

// GetCollectorLoadBalancerRetry returns a zero value when creation has never been
// restricted (or when a successful read/create cleared the restriction).
func (s *Store) GetCollectorLoadBalancerRetry(ctx context.Context, key CollectorKey) (CollectorLoadBalancerRetry, error) {
	var retry CollectorLoadBalancerRetry
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT attempts, retry_after, COALESCE(request_id, '')
		FROM %s.collector_load_balancer_retries
		WHERE connector_name = $1 AND region = $2
	`, s.schema), s.connectorName, key.Region).Scan(&retry.Attempts, &retry.RetryAfter, &retry.RequestID)
	if isNoRows(err) {
		return CollectorLoadBalancerRetry{}, nil
	}
	if err != nil {
		return CollectorLoadBalancerRetry{}, fmt.Errorf("read load balancer retry for region %s: %w", key.Region, err)
	}
	return retry, nil
}

// RecordCollectorLoadBalancerRetry records the next attempt after AWS rejects
// creation. The Collector row must already exist; the FK prevents orphan state.
func (s *Store) RecordCollectorLoadBalancerRetry(ctx context.Context, key CollectorKey, retry CollectorLoadBalancerRetry) error {
	if retry.Attempts < 1 || retry.RetryAfter.IsZero() {
		return fmt.Errorf("load balancer retry requires attempts and retry time")
	}
	_, err := s.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s.collector_load_balancer_retries
			(connector_name, region, attempts, retry_after, request_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''))
		ON CONFLICT (connector_name, region) DO UPDATE SET
			attempts = EXCLUDED.attempts,
			retry_after = EXCLUDED.retry_after,
			request_id = EXCLUDED.request_id
	`, s.schema), s.connectorName, key.Region, retry.Attempts, retry.RetryAfter, retry.RequestID)
	if err != nil {
		return fmt.Errorf("record load balancer retry for region %s: %w", key.Region, err)
	}
	return nil
}

func (s *Store) ClearCollectorLoadBalancerRetry(ctx context.Context, key CollectorKey) error {
	_, err := s.pool.Exec(ctx, fmt.Sprintf(`
		DELETE FROM %s.collector_load_balancer_retries
		WHERE connector_name = $1 AND region = $2
	`, s.schema), s.connectorName, key.Region)
	if err != nil {
		return fmt.Errorf("clear load balancer retry for region %s: %w", key.Region, err)
	}
	return nil
}
