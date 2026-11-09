package clickhouse

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"

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
	s, ok, err := queryRow(ctx, r.conn, "get system settings",
		`SELECT `+systemSettingsCols+`
		 FROM system_settings FINAL WHERE id = ? LIMIT 1`,
		scanSystemSettings, uint8(repo.SystemSettingsID))
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
	autoGuardrail := uint8(0)
	if s.AutoTransactionGuardrail {
		autoGuardrail = 1
	}
	dimsJSON, err := marshalTransactionIdentityDimensions(s.TransactionIdentityDimensions)
	if err != nil {
		return fmt.Errorf("upsert system settings: %w", err)
	}
	err = r.insert(ctx,
		`INSERT INTO system_settings (
			id, updated_at,
			examples_per_transaction, example_sample_interval_seconds,
			observation_retention_days,
			max_resident_transactions, max_baseline_set_members,
			flush_interval_seconds,
			auto_transaction_guardrail,
			transaction_identity_dimensions,
			transaction_inventory_epoch,
			relation_samples_per_transaction, relation_sample_interval_seconds,
			recommendation_min_distinct, recommendation_min_hold_percent,
			max_transactions_per_service)`,
		func(b driver.Batch) error {
			return b.Append(
				uint8(repo.SystemSettingsID), time.Now(),
				uint32(s.ExamplesPerTransaction), uint32(s.ExampleSampleIntervalSeconds),
				uint32(s.ObservationRetentionDays),
				uint32(s.MaxResidentTransactions), uint32(s.MaxBaselineSetMembers),
				uint32(s.FlushIntervalSeconds),
				autoGuardrail,
				dimsJSON,
				uint32(s.TransactionInventoryEpoch),
				uint32(s.RelationSamplesPerTransaction), uint32(s.RelationSampleIntervalSeconds),
				uint32(s.RecommendationMinDistinct), uint32(s.RecommendationMinHoldPercent),
				uint32(s.MaxTransactionsPerService),
			)
		})
	if err != nil {
		return fmt.Errorf("upsert system settings: %w", err)
	}
	return nil
}

func (r *Repo) SetObservationRetentionDays(ctx context.Context, days int) error {
	if days <= 0 || days > repo.MaxObservationRetentionDays {
		return fmt.Errorf("observation retention days must be 1..%d", repo.MaxObservationRetentionDays)
	}
	// Relation samples keep their own short retention; only examples and
	// evidence follow the operator-set value.
	if err := r.conn.Exec(syncMutationCtx(ctx),
		fmt.Sprintf(`ALTER TABLE trace_observations MODIFY TTL toDateTime(observed_at) + toIntervalDay(if(sample_reason = '%s', %d, %d))`,
			repo.RelationSampleReason, repo.DefaultRelationSampleRetentionDays, days)); err != nil {
		return fmt.Errorf("modify observation ttl: %w", err)
	}
	return nil
}

func scanSystemSettings(s rowScanner) (repo.SystemSettings, error) {
	var (
		out                                  repo.SystemSettings
		examples, exampleInterval, retention uint32
		maxTx, maxSet, flushSecs             uint32
		autoGuardrail                        uint8
		dimsJSON                             string
		inventoryEpoch                       uint32
		relationSamples, relationInterval    uint32
		recMinDistinct, recMinHoldPercent    uint32
		maxPerService                        uint32
	)
	if err := s.Scan(
		&examples, &exampleInterval,
		&retention,
		&maxTx, &maxSet,
		&flushSecs,
		&autoGuardrail,
		&dimsJSON,
		&inventoryEpoch,
		&relationSamples, &relationInterval,
		&recMinDistinct, &recMinHoldPercent,
		&maxPerService,
	); err != nil {
		return repo.SystemSettings{}, err
	}
	out.MaxTransactionsPerService = int(maxPerService)
	out.RelationSamplesPerTransaction = int(relationSamples)
	out.RelationSampleIntervalSeconds = int(relationInterval)
	out.RecommendationMinDistinct = int(recMinDistinct)
	out.RecommendationMinHoldPercent = int(recMinHoldPercent)
	out.ExamplesPerTransaction = int(examples)
	out.ExampleSampleIntervalSeconds = int(exampleInterval)
	out.ObservationRetentionDays = int(retention)
	out.MaxResidentTransactions = int(maxTx)
	out.MaxBaselineSetMembers = int(maxSet)
	out.FlushIntervalSeconds = int(flushSecs)
	out.AutoTransactionGuardrail = autoGuardrail != 0
	out.TransactionInventoryEpoch = int(inventoryEpoch)
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
