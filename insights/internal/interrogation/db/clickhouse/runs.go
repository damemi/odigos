package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const runsTable = "tx_runs"

// ApplyFailed marks older runs whose rule write failed after the model call.
// Those runs are not acknowledged. The loop no longer writes ApplyStatus:
// rules are reconciled from tx_recommendations instead of per run.
const ApplyFailed = "failed"

// Analysis statuses written by the control loop. Only AnalysisSucceeded runs
// (without a legacy ApplyFailed) count as evidence the model has seen. Failed runs
// are kept for auditing and never used as the base for a later delta.
const (
	AnalysisSucceeded       = "succeeded"
	AnalysisFailedTransient = "failed_transient"
	AnalysisFailedPermanent = "failed_permanent"
)

// Run is one analysis attempt for a source + transaction.
type Run struct {
	Timestamp     time.Time
	Namespace     string
	WorkloadKind  string
	WorkloadName  string
	ContainerName string
	TransactionID string
	// TrieHash is the structural hash of EvidenceSnapshot (call-graph edges).
	TrieHash string
	// EvidenceSnapshot is the cumulative call graph the model has seen once
	// this run succeeds: the previous acknowledged graph plus the edges sent.
	EvidenceSnapshot string
	Model            string
	// PromptHash is a misnomer kept for the ClickHouse column: hash of the user
	// JSON payload + schema.PromptRevision (not the system prompt text).
	PromptHash  string
	RawResponse string
	Parsed      string
	ApplyStatus string
	Error       string
	// AnalysisStatus is one of the Analysis* constants.
	AnalysisStatus string
	// AnalysisRevision identifies the prompt, request schema, and model the run
	// used. Acknowledged evidence from another revision is ignored.
	AnalysisRevision string
}

// InsertRun appends one analysis row. tx_runs is created by the insights
// migrate Job (database "interrogation"), not by the loop.
func (c *Client) InsertRun(ctx context.Context, run Run) error {
	if c == nil || c.conn == nil {
		return fmt.Errorf("clickhouse: nil connection")
	}
	if run.Timestamp.IsZero() {
		run.Timestamp = time.Now().UTC()
	}
	sql := fmt.Sprintf(`
INSERT INTO %q.%q (
	Timestamp, Namespace, WorkloadKind, WorkloadName, ContainerName,
	TransactionId, TrieHash, EvidenceSnapshot, Model, PromptHash, RawResponse, Parsed, ApplyStatus, Error,
	AnalysisStatus, AnalysisRevision
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, DatabaseName, runsTable)

	if err := c.conn.Exec(ctx, sql,
		run.Timestamp,
		run.Namespace,
		run.WorkloadKind,
		run.WorkloadName,
		run.ContainerName,
		run.TransactionID,
		run.TrieHash,
		run.EvidenceSnapshot,
		run.Model,
		run.PromptHash,
		run.RawResponse,
		run.Parsed,
		run.ApplyStatus,
		run.Error,
		run.AnalysisStatus,
		run.AnalysisRevision,
	); err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

// LastSuccessfulRun returns the newest run for a transaction whose analysis
// succeeded at revision and whose apply did not fail, or nil if none.
func (c *Client) LastSuccessfulRun(ctx context.Context, source SourceKey, transactionID, revision string) (*Run, error) {
	if c == nil || c.conn == nil {
		return nil, fmt.Errorf("clickhouse: nil connection")
	}
	if err := validateK8SSource(source); err != nil {
		return nil, err
	}
	transactionID = strings.TrimSpace(transactionID)
	if transactionID == "" {
		return nil, fmt.Errorf("transactionId is required")
	}
	sql := fmt.Sprintf(`
SELECT
	Timestamp, Namespace, WorkloadKind, WorkloadName, ContainerName,
	TransactionId, TrieHash, EvidenceSnapshot, Model, PromptHash, RawResponse, Parsed, ApplyStatus, Error,
	AnalysisStatus, AnalysisRevision
FROM %q.%q
WHERE Namespace = ?
	AND WorkloadKind = ?
	AND WorkloadName = ?
	AND ContainerName = ?
	AND TransactionId = ?
	AND AnalysisStatus = ?
	AND AnalysisRevision = ?
	AND ApplyStatus != ?
ORDER BY Timestamp DESC
LIMIT 1
`, DatabaseName, runsTable)

	rows, err := c.conn.Query(ctx, sql,
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName, transactionID,
		AnalysisSucceeded, revision, ApplyFailed)
	if err != nil {
		return nil, fmt.Errorf("query last successful run: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var run Run
	if err := rows.Scan(
		&run.Timestamp,
		&run.Namespace,
		&run.WorkloadKind,
		&run.WorkloadName,
		&run.ContainerName,
		&run.TransactionID,
		&run.TrieHash,
		&run.EvidenceSnapshot,
		&run.Model,
		&run.PromptHash,
		&run.RawResponse,
		&run.Parsed,
		&run.ApplyStatus,
		&run.Error,
		&run.AnalysisStatus,
		&run.AnalysisRevision,
	); err != nil {
		return nil, fmt.Errorf("scan last successful run: %w", err)
	}
	return &run, rows.Err()
}
