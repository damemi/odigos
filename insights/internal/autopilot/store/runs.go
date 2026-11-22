package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Analysis statuses written by the control loop. Only AnalysisSucceeded runs
// count as evidence the model has seen. Failed runs are kept for auditing and
// never used as the base for a later delta.
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
	// PromptHash is the hash of the user JSON payload + schema.PromptRevision
	// (not the system prompt text).
	PromptHash  string
	RawResponse string
	Parsed      string
	Error       string
	// AnalysisStatus is one of the Analysis* constants.
	AnalysisStatus string
	// AnalysisRevision identifies the prompt, request schema, and model the run
	// used. Acknowledged evidence from another revision is ignored.
	AnalysisRevision string
}

// InsertRun appends one analysis row.
func (c *Client) InsertRun(ctx context.Context, run Run) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	txID, err := parseTransactionID(run.TransactionID)
	if err != nil {
		return err
	}
	if run.Timestamp.IsZero() {
		run.Timestamp = time.Now().UTC()
	}
	if _, err := c.pool.Exec(ctx, `
INSERT INTO `+runsTable+` (
	created_at, namespace, workload_kind, workload_name, container_name,
	transaction_id, trie_hash, evidence_snapshot, model, prompt_hash, raw_response, parsed, error,
	analysis_status, analysis_revision
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		run.Timestamp,
		run.Namespace,
		run.WorkloadKind,
		run.WorkloadName,
		run.ContainerName,
		txID,
		run.TrieHash,
		run.EvidenceSnapshot,
		run.Model,
		run.PromptHash,
		run.RawResponse,
		run.Parsed,
		run.Error,
		run.AnalysisStatus,
		run.AnalysisRevision,
	); err != nil {
		return fmt.Errorf("insert run: %w", err)
	}
	return nil
}

// LastSuccessfulRun returns the newest run for a transaction whose analysis
// succeeded at revision, or nil if none.
func (c *Client) LastSuccessfulRun(ctx context.Context, source SourceKey, transactionID, revision string) (*Run, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	if err := validateK8SSource(source); err != nil {
		return nil, err
	}
	txID, err := parseTransactionID(transactionID)
	if err != nil {
		return nil, err
	}
	var run Run
	var id int64
	err = c.pool.QueryRow(ctx, `
SELECT
	created_at, namespace, workload_kind, workload_name, container_name,
	transaction_id, trie_hash, evidence_snapshot, model, prompt_hash, raw_response, parsed, error,
	analysis_status, analysis_revision
FROM `+runsTable+`
WHERE namespace = $1
	AND workload_kind = $2
	AND workload_name = $3
	AND container_name = $4
	AND transaction_id = $5
	AND analysis_status = $6
	AND analysis_revision = $7
ORDER BY created_at DESC
LIMIT 1`,
		source.Namespace, source.WorkloadKind, source.WorkloadName, source.ContainerName, txID,
		AnalysisSucceeded, revision,
	).Scan(
		&run.Timestamp,
		&run.Namespace,
		&run.WorkloadKind,
		&run.WorkloadName,
		&run.ContainerName,
		&id,
		&run.TrieHash,
		&run.EvidenceSnapshot,
		&run.Model,
		&run.PromptHash,
		&run.RawResponse,
		&run.Parsed,
		&run.Error,
		&run.AnalysisStatus,
		&run.AnalysisRevision,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query last successful run: %w", err)
	}
	run.TransactionID = strconv.FormatInt(id, 10)
	return &run, nil
}
