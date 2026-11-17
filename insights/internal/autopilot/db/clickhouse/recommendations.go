package clickhouse

import (
	"context"
	"fmt"
	"time"
)

const recommendationsTable = "tx_recommendations"

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

// InsertRecommendations appends recommendations in one batch. Inserting a
// probe that is already stored is harmless: readers keep the earliest row.
func (c *Client) InsertRecommendations(ctx context.Context, recs []Recommendation) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("clickhouse: nil connection")
	}
	if len(recs) == 0 {
		return nil
	}
	batch, err := c.conn.PrepareBatch(ctx, fmt.Sprintf(`
INSERT INTO %q.%q (
	FirstSeen, Namespace, WorkloadKind, WorkloadName, ContainerName, TransactionId,
	Language, Target, Category, Function, Why,
	ClassName, MethodName, PackageName, FunctionName, ReceiverName, ReceiverMethodName, Signature,
	AnalysisRevision
)`, DatabaseName, recommendationsTable))
	if err != nil {
		return fmt.Errorf("prepare recommendations batch: %w", err)
	}
	now := time.Now().UTC()
	for _, r := range recs {
		if r.FirstSeen.IsZero() {
			r.FirstSeen = now
		}
		if err := batch.Append(
			r.FirstSeen, r.Namespace, r.WorkloadKind, r.WorkloadName, r.ContainerName, r.TransactionID,
			r.Language, r.Target, r.Category, r.Function, r.Why,
			r.ClassName, r.MethodName, r.PackageName, r.FunctionName, r.ReceiverName, r.ReceiverMethodName, r.Signature,
			r.AnalysisRevision,
		); err != nil {
			_ = batch.Abort()
			return fmt.Errorf("append recommendation: %w", err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("insert recommendations: %w", err)
	}
	return nil
}

// ListRecommendations returns the desired probe set for the source's workload
// (all containers; ContainerName is ignored): one row per (Language, Target), taken from its earliest
// recommendation, oldest first.
func (c *Client) ListRecommendations(ctx context.Context, source SourceKey) ([]Recommendation, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse: nil connection")
	}
	if err := validateWorkload(source); err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(`
SELECT
	min(FirstSeen) AS FirstSeenMin,
	any(Namespace), any(WorkloadKind), any(WorkloadName),
	argMin(ContainerName, FirstSeen), argMin(TransactionId, FirstSeen),
	Language, Target,
	argMin(Category, FirstSeen), argMin(Function, FirstSeen), argMin(Why, FirstSeen),
	argMin(ClassName, FirstSeen), argMin(MethodName, FirstSeen), argMin(PackageName, FirstSeen),
	argMin(FunctionName, FirstSeen), argMin(ReceiverName, FirstSeen), argMin(ReceiverMethodName, FirstSeen),
	argMin(Signature, FirstSeen),
	argMin(AnalysisRevision, FirstSeen)
FROM %q.%q
WHERE Namespace = ?
	AND WorkloadKind = ?
	AND WorkloadName = ?
GROUP BY Language, Target
ORDER BY FirstSeenMin, Language, Target
`, DatabaseName, recommendationsTable)

	rows, err := c.conn.Query(ctx, sql, source.Namespace, source.WorkloadKind, source.WorkloadName)
	if err != nil {
		return nil, fmt.Errorf("list recommendations: %w", err)
	}
	defer rows.Close()

	var out []Recommendation
	for rows.Next() {
		var r Recommendation
		if err := rows.Scan(
			&r.FirstSeen, &r.Namespace, &r.WorkloadKind, &r.WorkloadName,
			&r.ContainerName, &r.TransactionID,
			&r.Language, &r.Target,
			&r.Category, &r.Function, &r.Why,
			&r.ClassName, &r.MethodName, &r.PackageName,
			&r.FunctionName, &r.ReceiverName, &r.ReceiverMethodName,
			&r.Signature,
			&r.AnalysisRevision,
		); err != nil {
			return nil, fmt.Errorf("scan recommendation: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRecommendedWorkloads returns every workload with stored
// recommendations. ContainerName is empty in the returned keys.
func (c *Client) ListRecommendedWorkloads(ctx context.Context) ([]SourceKey, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse: nil connection")
	}
	sql := fmt.Sprintf(`
SELECT DISTINCT Namespace, WorkloadKind, WorkloadName
FROM %q.%q
ORDER BY Namespace, WorkloadKind, WorkloadName
`, DatabaseName, recommendationsTable)

	rows, err := c.conn.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("list recommended workloads: %w", err)
	}
	defer rows.Close()

	var out []SourceKey
	for rows.Next() {
		var s SourceKey
		if err := rows.Scan(&s.Namespace, &s.WorkloadKind, &s.WorkloadName); err != nil {
			return nil, fmt.Errorf("scan recommended workload: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
