package repo

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	SystemSettingsID = 1

	MaxObservationRetentionDays = 120
	MaxFlushIntervalSeconds     = 300

	DefaultExamplesPerTransaction       = 10
	DefaultExampleSampleIntervalSeconds = 30
	DefaultObservationRetentionDays     = 120
	DefaultMaxResidentTransactions      = 50000
	// DefaultMaxBaselineSetMembers bounds one baseline set (edges, peers,
	// sites, libraries, tables). A class that crosses it is marked saturated:
	// it stops learning, is never promoted or scored, and its overflow never
	// reaches the store. Healthy transactions have tens to a few hundred.
	DefaultMaxBaselineSetMembers = 1000
	// DefaultMaxTransactionsPerService bounds how many distinct transactions
	// one service may add to the inventory. Span names that carry request ids
	// would otherwise mint a new transaction per request, and every one of
	// them is persisted (transactions, activity, baselines) with no TTL.
	DefaultMaxTransactionsPerService = 200
	DefaultFlushIntervalSeconds      = 15
	DefaultAutoTransactionGuardrail  = true

	// Relation samples feed the correlation recommendation miner. They need
	// more traces than UI examples (a relation that holds on 10 traces is weak
	// evidence, on 50 it is a pattern) but are only useful until the transaction
	// is mined, so they age out much sooner.
	DefaultRelationSamplesPerTransaction = 50
	DefaultRelationSampleIntervalSeconds = 5
	DefaultRelationSampleRetentionDays   = 14
	DefaultRecommendationMinDistinct     = 5
	DefaultRecommendationMinHoldPercent  = 98
	MaxRelationSamplesPerTransaction     = 500

	// MaxTransactionIdentityDimensions caps how many identity dimensions may be
	// enabled at once. Each enabled dimension multiplies transaction cardinality.
	MaxTransactionIdentityDimensions = 5

	// Fixed evidence caps: one stored trace is enough; occurrences carry the count.
	AnomalyEvidenceSamplesPerIssue = 1
	GuardrailEvidenceSamples       = 1
)

// TransactionIdentityDimension is one low-cardinality attribute (or built-in
// resolver key) folded into transaction identity from the sub-transaction
// entry span. High-cardinality keys will explode transaction count and memory.
type TransactionIdentityDimension struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

type SystemSettings struct {
	ExamplesPerTransaction        int                            `json:"examples_per_transaction"`
	ExampleSampleIntervalSeconds  int                            `json:"example_sample_interval_seconds"`
	ObservationRetentionDays      int                            `json:"observation_retention_days"`
	MaxResidentTransactions       int                            `json:"max_resident_transactions"`
	MaxBaselineSetMembers         int                            `json:"max_baseline_set_members"`
	MaxTransactionsPerService     int                            `json:"max_transactions_per_service"`
	FlushIntervalSeconds          int                            `json:"flush_interval_seconds"`
	AutoTransactionGuardrail      bool                           `json:"auto_transaction_guardrail"`
	TransactionIdentityDimensions []TransactionIdentityDimension `json:"transaction_identity_dimensions"`
	// Relation samples and the thresholds a mined correlation must clear to be
	// recommended (see recommendation.go).
	RelationSamplesPerTransaction int `json:"relation_samples_per_transaction"`
	RelationSampleIntervalSeconds int `json:"relation_sample_interval_seconds"`
	RecommendationMinDistinct     int `json:"recommendation_min_distinct"`
	RecommendationMinHoldPercent  int `json:"recommendation_min_hold_percent"`
	// TransactionInventoryEpoch is bumped when all transactions are wiped
	// (identity-dimension reset). Engines clear in-memory baselines when it
	// changes so multi-replica pods drop resurrectable state. Not an API field.
	TransactionInventoryEpoch int `json:"-"`
}

func DefaultTransactionIdentityDimensions() []TransactionIdentityDimension {
	return []TransactionIdentityDimension{
		{Key: IdentityDimHTTPStatusCode, Enabled: true},
		{Key: IdentityDimRPCGRPCStatus, Enabled: true},
	}
}

func DefaultSystemSettings() SystemSettings {
	return SystemSettings{
		ExamplesPerTransaction:        DefaultExamplesPerTransaction,
		ExampleSampleIntervalSeconds:  DefaultExampleSampleIntervalSeconds,
		ObservationRetentionDays:      DefaultObservationRetentionDays,
		MaxResidentTransactions:       DefaultMaxResidentTransactions,
		MaxBaselineSetMembers:         DefaultMaxBaselineSetMembers,
		MaxTransactionsPerService:     DefaultMaxTransactionsPerService,
		FlushIntervalSeconds:          DefaultFlushIntervalSeconds,
		AutoTransactionGuardrail:      DefaultAutoTransactionGuardrail,
		TransactionIdentityDimensions: DefaultTransactionIdentityDimensions(),
		RelationSamplesPerTransaction: DefaultRelationSamplesPerTransaction,
		RelationSampleIntervalSeconds: DefaultRelationSampleIntervalSeconds,
		RecommendationMinDistinct:     DefaultRecommendationMinDistinct,
		RecommendationMinHoldPercent:  DefaultRecommendationMinHoldPercent,
	}
}

func (s SystemSettings) WithDefaults() SystemSettings {
	d := DefaultSystemSettings()
	if s.ExamplesPerTransaction <= 0 {
		s.ExamplesPerTransaction = d.ExamplesPerTransaction
	}
	if s.ExampleSampleIntervalSeconds <= 0 {
		s.ExampleSampleIntervalSeconds = d.ExampleSampleIntervalSeconds
	}
	if s.ObservationRetentionDays <= 0 {
		s.ObservationRetentionDays = d.ObservationRetentionDays
	}
	if s.MaxResidentTransactions <= 0 {
		s.MaxResidentTransactions = d.MaxResidentTransactions
	}
	if s.MaxBaselineSetMembers <= 0 {
		s.MaxBaselineSetMembers = d.MaxBaselineSetMembers
	}
	if s.MaxTransactionsPerService <= 0 {
		s.MaxTransactionsPerService = d.MaxTransactionsPerService
	}
	if s.FlushIntervalSeconds <= 0 {
		s.FlushIntervalSeconds = d.FlushIntervalSeconds
	}
	if s.RelationSamplesPerTransaction <= 0 {
		s.RelationSamplesPerTransaction = d.RelationSamplesPerTransaction
	}
	if s.RelationSampleIntervalSeconds <= 0 {
		s.RelationSampleIntervalSeconds = d.RelationSampleIntervalSeconds
	}
	if s.RecommendationMinDistinct <= 0 {
		s.RecommendationMinDistinct = d.RecommendationMinDistinct
	}
	if s.RecommendationMinHoldPercent <= 0 {
		s.RecommendationMinHoldPercent = d.RecommendationMinHoldPercent
	}
	// AutoTransactionGuardrail is a real bool (false is valid); default
	// applies only via DefaultSystemSettings when no row exists.
	//
	// TransactionIdentityDimensions: nil means unset (pre-migration / zero
	// value) → apply defaults. Non-nil empty slice means explicitly disabled.
	if s.TransactionIdentityDimensions == nil {
		s.TransactionIdentityDimensions = DefaultTransactionIdentityDimensions()
	}
	return s
}

func (s SystemSettings) Validate() error {
	s = s.WithDefaults()
	positive := []struct {
		name string
		v    int
	}{
		{"examples_per_transaction", s.ExamplesPerTransaction},
		{"example_sample_interval_seconds", s.ExampleSampleIntervalSeconds},
		{"observation_retention_days", s.ObservationRetentionDays},
		{"max_resident_transactions", s.MaxResidentTransactions},
		{"max_baseline_set_members", s.MaxBaselineSetMembers},
		{"max_transactions_per_service", s.MaxTransactionsPerService},
		{"flush_interval_seconds", s.FlushIntervalSeconds},
		{"relation_samples_per_transaction", s.RelationSamplesPerTransaction},
		{"relation_sample_interval_seconds", s.RelationSampleIntervalSeconds},
		{"recommendation_min_distinct", s.RecommendationMinDistinct},
		{"recommendation_min_hold_percent", s.RecommendationMinHoldPercent},
	}
	for _, f := range positive {
		if f.v <= 0 {
			return fmt.Errorf("%s must be > 0", f.name)
		}
	}
	if s.RelationSamplesPerTransaction > MaxRelationSamplesPerTransaction {
		return fmt.Errorf("relation_samples_per_transaction must be <= %d", MaxRelationSamplesPerTransaction)
	}
	if s.RecommendationMinHoldPercent > 100 {
		return fmt.Errorf("recommendation_min_hold_percent must be <= 100")
	}
	if s.ObservationRetentionDays > MaxObservationRetentionDays {
		return fmt.Errorf("observation_retention_days must be <= %d", MaxObservationRetentionDays)
	}
	if s.FlushIntervalSeconds > MaxFlushIntervalSeconds {
		return fmt.Errorf("flush_interval_seconds must be <= %d", MaxFlushIntervalSeconds)
	}
	if err := validateTransactionIdentityDimensions(s.TransactionIdentityDimensions); err != nil {
		return err
	}
	return nil
}

func validateTransactionIdentityDimensions(dims []TransactionIdentityDimension) error {
	seen := make(map[string]struct{}, len(dims))
	enabled := 0
	for i, d := range dims {
		key := strings.TrimSpace(d.Key)
		if key == "" {
			return fmt.Errorf("transaction_identity_dimensions[%d].key must not be empty", i)
		}
		if key != d.Key {
			return fmt.Errorf("transaction_identity_dimensions[%d].key must not have leading/trailing whitespace", i)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("transaction_identity_dimensions: duplicate key %q", key)
		}
		seen[key] = struct{}{}
		if d.Enabled {
			enabled++
		}
	}
	if enabled > MaxTransactionIdentityDimensions {
		return fmt.Errorf("transaction_identity_dimensions: at most %d enabled dimensions", MaxTransactionIdentityDimensions)
	}
	return nil
}

// IdentityDimensionsFingerprint returns a short deterministic stamp of the
// identity scheme these dimensions produce: the ordered list of enabled keys,
// which is the only input that shapes an operation string (see the engine's
// applyIdentityDimensions). Disabled keys, and their ordering, are excluded
// because they do not change identity.
//
// It exists so stored data can say which scheme it was built under. Editing the
// identity dimensions is an operator action that reshapes every transaction
// identity, and each replica applies the edit on its own reload tick - anything
// holding identity strings (today: allowed_transactions allowlists) must be able
// to tell "built under the scheme I am using" from "built under the previous
// one". Because every replica derives the same stamp from the same config with
// no coordination, that check is local and needs no reload to be correct.
func IdentityDimensionsFingerprint(dims []TransactionIdentityDimension) string {
	hash := sha256.New()
	for _, dim := range dims {
		if !dim.Enabled || dim.Key == "" {
			continue
		}
		hash.Write([]byte(dim.Key))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil)[:8])
}

// IdentityDimensionsEqual reports whether a and b are the same ordered config.
func IdentityDimensionsEqual(a, b []TransactionIdentityDimension) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Key != b[i].Key || a[i].Enabled != b[i].Enabled {
			return false
		}
	}
	return true
}
