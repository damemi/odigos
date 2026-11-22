package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

const systemSettingsCols = `examples_per_transaction, example_sample_interval_seconds,
	observation_retention_days,
	max_resident_transactions, max_baseline_set_members,
	flush_interval_seconds,
	auto_transaction_guardrail,
	transaction_identity_dimensions,
	transaction_inventory_epoch,
	relation_samples_per_transaction, relation_sample_interval_seconds,
	recommendation_min_distinct, recommendation_min_hold_percent,
	max_transactions_per_service`

func (r *Repo) GetSystemSettings(ctx context.Context) (repo.SystemSettings, error) {
	s, ok, err := queryRow(ctx, r.pool, "get system settings",
		`SELECT `+systemSettingsCols+` FROM system_settings WHERE id = $1`,
		scanSystemSettings, int16(repo.SystemSettingsID))
	if err != nil {
		return repo.SystemSettings{}, err
	}
	if !ok {
		return repo.DefaultSystemSettings(), nil
	}
	return s.WithDefaults(), nil
}

func (r *Repo) UpsertSystemSettings(ctx context.Context, s repo.SystemSettings) error {
	s = s.WithDefaults()
	if err := s.Validate(); err != nil {
		return err
	}
	dimsJSON, err := marshalTransactionIdentityDimensions(s.TransactionIdentityDimensions)
	if err != nil {
		return fmt.Errorf("upsert system settings: %w", err)
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO system_settings (
			id, updated_at,
			`+systemSettingsCols+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		 ON CONFLICT (id) DO UPDATE SET
			updated_at = EXCLUDED.updated_at,
			examples_per_transaction = EXCLUDED.examples_per_transaction,
			example_sample_interval_seconds = EXCLUDED.example_sample_interval_seconds,
			observation_retention_days = EXCLUDED.observation_retention_days,
			max_resident_transactions = EXCLUDED.max_resident_transactions,
			max_baseline_set_members = EXCLUDED.max_baseline_set_members,
			flush_interval_seconds = EXCLUDED.flush_interval_seconds,
			auto_transaction_guardrail = EXCLUDED.auto_transaction_guardrail,
			transaction_identity_dimensions = EXCLUDED.transaction_identity_dimensions,
			transaction_inventory_epoch = EXCLUDED.transaction_inventory_epoch,
			relation_samples_per_transaction = EXCLUDED.relation_samples_per_transaction,
			relation_sample_interval_seconds = EXCLUDED.relation_sample_interval_seconds,
			recommendation_min_distinct = EXCLUDED.recommendation_min_distinct,
			recommendation_min_hold_percent = EXCLUDED.recommendation_min_hold_percent,
			max_transactions_per_service = EXCLUDED.max_transactions_per_service`,
		int16(repo.SystemSettingsID), time.Now(),
		s.ExamplesPerTransaction, s.ExampleSampleIntervalSeconds,
		s.ObservationRetentionDays,
		s.MaxResidentTransactions, s.MaxBaselineSetMembers,
		s.FlushIntervalSeconds,
		s.AutoTransactionGuardrail,
		dimsJSON,
		s.TransactionInventoryEpoch,
		s.RelationSamplesPerTransaction, s.RelationSampleIntervalSeconds,
		s.RecommendationMinDistinct, s.RecommendationMinHoldPercent,
		s.MaxTransactionsPerService,
	)
	if err != nil {
		return fmt.Errorf("upsert system settings: %w", err)
	}
	return nil
}

// SetObservationRetentionDays applies a new retention right away: it deletes
// examples and evidence older than days. The retention job keeps enforcing it
// from system_settings afterwards.
func (r *Repo) SetObservationRetentionDays(ctx context.Context, days int) error {
	if days <= 0 || days > repo.MaxObservationRetentionDays {
		return fmt.Errorf("observation retention days must be 1..%d", repo.MaxObservationRetentionDays)
	}
	if _, err := r.pruneObservations(ctx, days, time.Now()); err != nil {
		return fmt.Errorf("apply observation retention: %w", err)
	}
	return nil
}

func scanSystemSettings(s rowScanner) (repo.SystemSettings, error) {
	var (
		out      repo.SystemSettings
		dimsJSON string
	)
	if err := s.Scan(
		&out.ExamplesPerTransaction, &out.ExampleSampleIntervalSeconds,
		&out.ObservationRetentionDays,
		&out.MaxResidentTransactions, &out.MaxBaselineSetMembers,
		&out.FlushIntervalSeconds,
		&out.AutoTransactionGuardrail,
		&dimsJSON,
		&out.TransactionInventoryEpoch,
		&out.RelationSamplesPerTransaction, &out.RelationSampleIntervalSeconds,
		&out.RecommendationMinDistinct, &out.RecommendationMinHoldPercent,
		&out.MaxTransactionsPerService,
	); err != nil {
		return repo.SystemSettings{}, err
	}
	dims, err := unmarshalTransactionIdentityDimensions(dimsJSON)
	if err != nil {
		return repo.SystemSettings{}, err
	}
	out.TransactionIdentityDimensions = dims
	return out, nil
}

func marshalTransactionIdentityDimensions(dims []repo.TransactionIdentityDimension) (string, error) {
	if dims == nil {
		dims = repo.DefaultTransactionIdentityDimensions()
	}
	b, err := json.Marshal(dims)
	if err != nil {
		return "", fmt.Errorf("marshal transaction_identity_dimensions: %w", err)
	}
	return string(b), nil
}

func unmarshalTransactionIdentityDimensions(raw string) ([]repo.TransactionIdentityDimension, error) {
	if raw == "" {
		return nil, nil // WithDefaults fills defaults
	}
	var dims []repo.TransactionIdentityDimension
	if err := json.Unmarshal([]byte(raw), &dims); err != nil {
		return nil, fmt.Errorf("unmarshal transaction_identity_dimensions: %w", err)
	}
	if dims == nil {
		dims = []repo.TransactionIdentityDimension{}
	}
	return dims, nil
}
