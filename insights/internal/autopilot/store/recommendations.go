package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Recommendation is one probe the model recommended for a workload. Language
// and Target identify the probe; the remaining probe fields hold the
// language-specific target exactly as the model returned it.
type Recommendation struct {
	FirstSeen     time.Time
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
	TransactionID string
	Language      string
	Target        string
	Category      string
	Function      string
	Why           string

	ClassName          string
	MethodName         string
	PackageName        string
	FunctionName       string
	ReceiverName       string
	ReceiverMethodName string
	Signature          string

	AnalysisRevision string
}

// InsertRecommendations stores recommendations in one round trip. A probe
// already stored for the workload keeps its first recommendation.
func (c *Client) InsertRecommendations(ctx context.Context, recs []Recommendation) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	if len(recs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	b := &pgx.Batch{}
	for _, r := range recs {
		txID, err := parseTransactionID(r.TransactionID)
		if err != nil {
			return err
		}
		if r.FirstSeen.IsZero() {
			r.FirstSeen = now
		}
		b.Queue(`
INSERT INTO `+recommendationsTable+` (
	first_seen, namespace, workload_kind, workload_name, container_name, transaction_id,
	language, target, category, function, why,
	class_name, method_name, package_name, function_name, receiver_name, receiver_method_name, signature,
	analysis_revision
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
ON CONFLICT DO NOTHING`,
			r.FirstSeen, r.Namespace, r.WorkloadKind, r.WorkloadName, r.ContainerName, txID,
			r.Language, r.Target, r.Category, r.Function, r.Why,
			r.ClassName, r.MethodName, r.PackageName, r.FunctionName, r.ReceiverName, r.ReceiverMethodName, r.Signature,
			r.AnalysisRevision,
		)
	}
	if err := c.pool.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("insert recommendations: %w", err)
	}
	return nil
}

// ListRecommendations returns the desired probe set for the source's workload
// (all containers; ContainerName is ignored): one row per (Language, Target),
// oldest first.
func (c *Client) ListRecommendations(ctx context.Context, source SourceKey) ([]Recommendation, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	if err := validateWorkload(source); err != nil {
		return nil, err
	}
	rows, err := c.pool.Query(ctx, `
SELECT
	first_seen, namespace, workload_kind, workload_name, container_name, transaction_id,
	language, target, category, function, why,
	class_name, method_name, package_name, function_name, receiver_name, receiver_method_name, signature,
	analysis_revision
FROM `+recommendationsTable+`
WHERE namespace = $1
	AND workload_kind = $2
	AND workload_name = $3
ORDER BY first_seen, language, target`,
		source.Namespace, source.WorkloadKind, source.WorkloadName)
	if err != nil {
		return nil, fmt.Errorf("list recommendations: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Recommendation, error) {
		var r Recommendation
		var txID int64
		if err := row.Scan(
			&r.FirstSeen, &r.Namespace, &r.WorkloadKind, &r.WorkloadName, &r.ContainerName, &txID,
			&r.Language, &r.Target, &r.Category, &r.Function, &r.Why,
			&r.ClassName, &r.MethodName, &r.PackageName, &r.FunctionName, &r.ReceiverName, &r.ReceiverMethodName, &r.Signature,
			&r.AnalysisRevision,
		); err != nil {
			return r, err
		}
		r.TransactionID = strconv.FormatInt(txID, 10)
		return r, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list recommendations: %w", err)
	}
	return out, nil
}

// ListRecommendedWorkloads returns every workload with stored
// recommendations. ContainerName is empty in the returned keys.
func (c *Client) ListRecommendedWorkloads(ctx context.Context) ([]SourceKey, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	rows, err := c.pool.Query(ctx, `
SELECT DISTINCT namespace, workload_kind, workload_name
FROM `+recommendationsTable+`
ORDER BY namespace, workload_kind, workload_name`)
	if err != nil {
		return nil, fmt.Errorf("list recommended workloads: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (SourceKey, error) {
		var s SourceKey
		err := row.Scan(&s.Namespace, &s.WorkloadKind, &s.WorkloadName)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("list recommended workloads: %w", err)
	}
	return out, nil
}
