package apimap

import (
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func FromSystemSettings(s repo.SystemSettings) apitypes.SystemSettings {
	s = s.WithDefaults()
	return apitypes.SystemSettings{
		Sampling: apitypes.SystemSamplingSettings{
			ExamplesPerTransaction:        s.ExamplesPerTransaction,
			ExampleSampleIntervalSeconds:  s.ExampleSampleIntervalSeconds,
			RelationSamplesPerTransaction: s.RelationSamplesPerTransaction,
			RelationSampleIntervalSeconds: s.RelationSampleIntervalSeconds,
		},
		Retention: apitypes.SystemRetentionSettings{
			ObservationRetentionDays: s.ObservationRetentionDays,
		},
		Capacity: apitypes.SystemCapacitySettings{
			MaxResidentTransactions:   s.MaxResidentTransactions,
			MaxBaselineSetMembers:     s.MaxBaselineSetMembers,
			MaxTransactionsPerService: s.MaxTransactionsPerService,
		},
		Writeback: apitypes.SystemWritebackSettings{
			FlushIntervalSeconds: s.FlushIntervalSeconds,
		},
		Detection: apitypes.SystemDetectionSettings{
			AutoTransactionGuardrail:     s.AutoTransactionGuardrail,
			RecommendationMinDistinct:    s.RecommendationMinDistinct,
			RecommendationMinHoldPercent: s.RecommendationMinHoldPercent,
		},
		Identity: apitypes.SystemIdentitySettings{
			TransactionIdentityDimensions: fromIdentityDimensions(s.TransactionIdentityDimensions),
		},
	}
}

func ToSystemSettings(in apitypes.SystemSettings) repo.SystemSettings {
	return repo.SystemSettings{
		ExamplesPerTransaction:        in.Sampling.ExamplesPerTransaction,
		ExampleSampleIntervalSeconds:  in.Sampling.ExampleSampleIntervalSeconds,
		ObservationRetentionDays:      in.Retention.ObservationRetentionDays,
		MaxResidentTransactions:       in.Capacity.MaxResidentTransactions,
		MaxBaselineSetMembers:         in.Capacity.MaxBaselineSetMembers,
		MaxTransactionsPerService:     in.Capacity.MaxTransactionsPerService,
		FlushIntervalSeconds:          in.Writeback.FlushIntervalSeconds,
		AutoTransactionGuardrail:      in.Detection.AutoTransactionGuardrail,
		TransactionIdentityDimensions: toIdentityDimensions(in.Identity.TransactionIdentityDimensions),
		RelationSamplesPerTransaction: in.Sampling.RelationSamplesPerTransaction,
		RelationSampleIntervalSeconds: in.Sampling.RelationSampleIntervalSeconds,
		RecommendationMinDistinct:     in.Detection.RecommendationMinDistinct,
		RecommendationMinHoldPercent:  in.Detection.RecommendationMinHoldPercent,
	}.WithDefaults()
}

func fromIdentityDimensions(in []repo.TransactionIdentityDimension) []apitypes.SystemTransactionIdentityDimension {
	if in == nil {
		return nil
	}
	out := make([]apitypes.SystemTransactionIdentityDimension, len(in))
	for i, d := range in {
		out[i] = apitypes.SystemTransactionIdentityDimension{
			Key:     d.Key,
			Enabled: d.Enabled,
		}
	}
	return out
}

func toIdentityDimensions(in []apitypes.SystemTransactionIdentityDimension) []repo.TransactionIdentityDimension {
	if in == nil {
		return nil
	}
	out := make([]repo.TransactionIdentityDimension, len(in))
	for i, d := range in {
		out[i] = repo.TransactionIdentityDimension{
			Key:     d.Key,
			Enabled: d.Enabled,
		}
	}
	return out
}
