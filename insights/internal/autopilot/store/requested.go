package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// States of a requested probe. Only the run and the API set them; the
// reconciler reads them.
const (
	// ProbeApproved is a temporary probe, in the rule until ExpiresAt.
	ProbeApproved = "approved"
	// ProbeKept is a probe a person kept: it has no expiry.
	ProbeKept = "kept"
	// ProbeReverted is a probe that ended; EndReason says why.
	ProbeReverted = "reverted"
)

var (
	// ErrRunNotOpen means the interrogation ended, or was stopped with revert.
	ErrRunNotOpen = errors.New("interrogation is not open")
	// ErrRunBudgetUsed means the interrogation has requested all the probes it may.
	ErrRunBudgetUsed = errors.New("interrogation probe budget used")
)

// placingTimeout is how long a rule write may still land after its rows were
// flagged, well past the API server's one minute request timeout.
const placingTimeout = 5 * time.Minute

// maxProbeLifetime bounds a temporary probe's expiry whatever its run's
// deadline says.
const maxProbeLifetime = 24 * time.Hour

// RequestedProbe is a probe an interrogation asked for: a row of
// interrogation_requested_probes. The embedded Recommendation holds the probe as the loop
// stores its own, so both build the same rule entry.
type RequestedProbe struct {
	Recommendation
	ChangeID        string
	InterrogationID string
	Round           int
	State           string
	ExpiresAt       time.Time
	KeptBy          string
	KeptAt          time.Time
	EndReason       string

	// Written only by the reconciler.
	InRule      bool
	BeingPlaced bool
	PlaceGen    int64
	AppliedAt   time.Time
	RemovedAt   time.Time
	BlockReason string

	UpdatedAt time.Time
}

// Live reports whether the probe should be in the workload's rule at now.
func (p RequestedProbe) Live(now time.Time) bool {
	return p.State == ProbeKept || (p.State == ProbeApproved && p.ExpiresAt.After(now))
}

const requestedProbeColumns = `
	change_id, interrogation_id, round, first_seen,
	namespace, workload_kind, workload_name, container_name, transaction_id,
	language, target, category, function, why,
	class_name, method_name, package_name, function_name, receiver_name, receiver_method_name, signature,
	analysis_revision, state, expires_at, kept_by, kept_at, end_reason,
	in_rule, being_placed, place_gen, applied_at, removed_at, block_reason, updated_at`

func scanRequestedProbe(row pgx.Row) (RequestedProbe, error) {
	var p RequestedProbe
	var txID int64
	err := row.Scan(
		&p.ChangeID, &p.InterrogationID, &p.Round, &p.FirstSeen,
		&p.Namespace, &p.WorkloadKind, &p.WorkloadName, &p.ContainerName, &txID,
		&p.Language, &p.Target, &p.Category, &p.Function, &p.Why,
		&p.ClassName, &p.MethodName, &p.PackageName, &p.FunctionName, &p.ReceiverName, &p.ReceiverMethodName, &p.Signature,
		&p.AnalysisRevision, &p.State, &p.ExpiresAt, &p.KeptBy, &p.KeptAt, &p.EndReason,
		&p.InRule, &p.BeingPlaced, &p.PlaceGen, &p.AppliedAt, &p.RemovedAt, &p.BlockReason, &p.UpdatedAt,
	)
	p.TransactionID = strconv.FormatInt(txID, 10)
	return p, err
}

// InsertRequestedProbe stores p as approved unless the run has already
// requested runMaxProbes probes. The probe expires at the run's deadline, read
// from the run's two ADK sessions, and at most a day from now. Inserting a
// probe the run already requested returns the stored row.
//
// It fails with ErrRunNotOpen when the run has ended or its stop asks for a
// revert, and with ErrRunBudgetUsed when the budget is used.
func (c *Client) InsertRequestedProbe(ctx context.Context, p RequestedProbe, runMaxProbes int, now time.Time) (RequestedProbe, error) {
	if c == nil || c.pool == nil {
		return RequestedProbe{}, fmt.Errorf("autopilot store: nil pool")
	}
	if p.ChangeID == "" || p.InterrogationID == "" {
		return RequestedProbe{}, fmt.Errorf("change id and interrogation id are required")
	}
	if err := validateWorkload(SourceKey{Namespace: p.Namespace, WorkloadKind: p.WorkloadKind, WorkloadName: p.WorkloadName}); err != nil {
		return RequestedProbe{}, err
	}
	txID, err := parseTransactionID(p.TransactionID)
	if err != nil {
		return RequestedProbe{}, err
	}
	if p.FirstSeen.IsZero() {
		p.FirstSeen = now
	}
	var out RequestedProbe
	err = pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		// The run's row is locked for update: the event that closes the run
		// updates it, and so does every other insert for the run, so open and
		// the count below are exact. The control row is locked for share
		// against an extend, which locks both rows in the same order and moves
		// the run's probes with its deadline.
		var open bool
		var deadline *time.Time
		err := tx.QueryRow(ctx, `
SELECT coalesce(r.state->>'open', '') = 'true'
	AND coalesce(c.state->'stop'->>'changes', '') <> 'revert',
	(r.state->'run'->>'started_at')::timestamptz
		+ make_interval(secs => (r.state->'request'->'budget'->>'time_limit_seconds')::float8
			+ coalesce((c.state->>'extend_seconds')::float8, 0))
FROM adk.sessions r
JOIN adk.sessions c ON c.app_name = 'interrogation.ctl' AND c.user_id = r.user_id AND c.id = r.id
WHERE r.app_name = 'interrogation' AND r.user_id = $1 AND r.id = $1
FOR UPDATE OF r FOR SHARE OF c
`, p.InterrogationID).Scan(&open, &deadline)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunNotOpen
		}
		if err != nil {
			return fmt.Errorf("lock interrogation: %w", err)
		}
		if !open {
			return ErrRunNotOpen
		}
		// A run with no start time or no time limit has no deadline yet; the
		// day's clamp then bounds the probe.
		expires := now.Add(maxProbeLifetime)
		if deadline != nil && deadline.Before(expires) {
			expires = *deadline
		}

		var requested int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM interrogation_requested_probes WHERE interrogation_id = $1`,
			p.InterrogationID).Scan(&requested); err != nil {
			return fmt.Errorf("count requested probes: %w", err)
		}
		if requested < runMaxProbes {
			if _, err := tx.Exec(ctx, `
INSERT INTO interrogation_requested_probes (`+requestedProbeColumns+`)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21,
	$22, 'approved', $23, '', 'epoch', '', false, false, 0, 'epoch', 'epoch', '', $24)
ON CONFLICT (change_id) DO NOTHING`,
				p.ChangeID, p.InterrogationID, p.Round, p.FirstSeen,
				p.Namespace, p.WorkloadKind, p.WorkloadName, p.ContainerName, txID,
				p.Language, p.Target, p.Category, p.Function, p.Why,
				p.ClassName, p.MethodName, p.PackageName, p.FunctionName, p.ReceiverName, p.ReceiverMethodName, p.Signature,
				p.AnalysisRevision, expires, now,
			); err != nil {
				return fmt.Errorf("insert requested probe: %w", err)
			}
		}

		// A row that was there already is returned as stored, whatever the count.
		out, err = scanRequestedProbe(tx.QueryRow(ctx,
			`SELECT `+requestedProbeColumns+` FROM interrogation_requested_probes WHERE change_id = $1`, p.ChangeID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRunBudgetUsed
		}
		return err
	})
	if err != nil {
		return RequestedProbe{}, err
	}
	return out, nil
}

// GetRequestedProbe returns the row of one change.
func (c *Client) GetRequestedProbe(ctx context.Context, changeID string) (RequestedProbe, bool, error) {
	if c == nil || c.pool == nil {
		return RequestedProbe{}, false, fmt.Errorf("autopilot store: nil pool")
	}
	p, err := scanRequestedProbe(c.pool.QueryRow(ctx,
		`SELECT `+requestedProbeColumns+` FROM interrogation_requested_probes WHERE change_id = $1`, changeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return RequestedProbe{}, false, nil
	}
	if err != nil {
		return RequestedProbe{}, false, fmt.Errorf("get requested probe: %w", err)
	}
	return p, true, nil
}

// ListRequestedProbes returns every probe that is live at now or may still be
// in a rule, grouped by workload and oldest first within one.
func (c *Client) ListRequestedProbes(ctx context.Context, now time.Time) ([]RequestedProbe, error) {
	if c == nil || c.pool == nil {
		return nil, fmt.Errorf("autopilot store: nil pool")
	}
	rows, err := c.pool.Query(ctx, `
SELECT `+requestedProbeColumns+`
FROM interrogation_requested_probes
WHERE in_rule OR being_placed OR state = 'kept' OR (state = 'approved' AND expires_at > $1)
ORDER BY namespace COLLATE "C", workload_kind COLLATE "C", workload_name COLLATE "C",
	first_seen, language COLLATE "C", target COLLATE "C"
`, now)
	if err != nil {
		return nil, fmt.Errorf("list requested probes: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RequestedProbe, error) {
		return scanRequestedProbe(row)
	})
	if err != nil {
		return nil, fmt.Errorf("scan requested probe: %w", err)
	}
	return out, nil
}

// MarkRequestedProbesBeingPlaced records, before a rule write, the rows whose
// probes it adds, and bumps their PlaceGen. Until SettleRequestedProbes
// records the outcome, ListRequestedProbes returns them even if they end, so
// the reconciler takes out a probe the write placed whatever happens to the
// settle. Only rows whose PlaceGen is still the one read are flagged; it
// reports false when one was not, and the caller must not write the rule.
func (c *Client) MarkRequestedProbesBeingPlaced(ctx context.Context, rows []RequestedProbe) (bool, error) {
	if c == nil || c.pool == nil {
		return false, fmt.Errorf("autopilot store: nil pool")
	}
	if len(rows) == 0 {
		return true, nil
	}
	ids, gens := placeGens(rows)
	tag, err := c.pool.Exec(ctx, `
UPDATE interrogation_requested_probes p SET being_placed = true, being_placed_at = now(), place_gen = p.place_gen + 1
FROM unnest($1::text[], $2::bigint[]) AS r(id, gen)
WHERE p.change_id = r.id AND p.place_gen = r.gen
`, ids, gens)
	if err != nil {
		return false, fmt.Errorf("mark requested probes being placed: %w", err)
	}
	return tag.RowsAffected() == int64(len(rows)), nil
}

// SettleRequestedProbes records the outcome of a rule write for the rows read
// before it: placed are in the rule now, the rest of read are not, and blocked
// gives the reason for live rows that could not be placed. Liveness is not
// checked again: the rule holds what was placed, and a row that ended
// meanwhile is taken out by the next pass. A row whose PlaceGen is no longer
// the one in read is left alone: a later write may have placed its probe, and
// that write's own settle or the next pass records it. A flag younger than
// placingTimeout stays on a row recorded as not placed: the rule read may
// predate another leader's write of the probe that has not landed yet.
func (c *Client) SettleRequestedProbes(ctx context.Context, read []RequestedProbe, placed []string, blocked map[string]string, now time.Time) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	if len(read) == 0 {
		return nil
	}
	// pgx sends a nil slice as NULL, and in_rule is NOT NULL.
	if placed == nil {
		placed = []string{}
	}
	if blocked == nil {
		blocked = map[string]string{}
	}
	ids, gens := placeGens(read)
	if _, err := c.pool.Exec(ctx, `
UPDATE interrogation_requested_probes p SET
	applied_at = CASE WHEN change_id = ANY($2) AND NOT in_rule THEN $4 ELSE applied_at END,
	removed_at = CASE WHEN in_rule AND NOT (change_id = ANY($2)) THEN $4 ELSE removed_at END,
	in_rule = (change_id = ANY($2)),
	being_placed = being_placed AND NOT (change_id = ANY($2)) AND being_placed_at > now() - make_interval(secs => $6),
	block_reason = CASE WHEN change_id = ANY($2) THEN '' ELSE coalesce($3::jsonb ->> change_id, '') END
FROM unnest($1::text[], $5::bigint[]) AS r(id, gen)
WHERE p.change_id = r.id AND p.place_gen = r.gen
`, ids, placed, blocked, now, gens, placingTimeout.Seconds()); err != nil {
		return fmt.Errorf("settle requested probes: %w", err)
	}
	return nil
}

func placeGens(rows []RequestedProbe) ([]string, []int64) {
	ids := make([]string, len(rows))
	gens := make([]int64, len(rows))
	for i, p := range rows {
		ids[i], gens[i] = p.ChangeID, p.PlaceGen
	}
	return ids, gens
}

// BlockRequestedProbes records why the reconciler left a workload's rule
// alone. Nothing else about the rows changes.
func (c *Client) BlockRequestedProbes(ctx context.Context, ids []string, reason string) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err := c.pool.Exec(ctx, `
UPDATE interrogation_requested_probes SET block_reason = $2
WHERE change_id = ANY($1) AND block_reason <> $2
`, ids, reason); err != nil {
		return fmt.Errorf("block requested probes: %w", err)
	}
	return nil
}

// EndWorkloadProbes ends every approved and kept probe of a workload that is
// no longer instrumented. ContainerName is ignored.
func (c *Client) EndWorkloadProbes(ctx context.Context, w SourceKey, now time.Time) error {
	if c == nil || c.pool == nil {
		return fmt.Errorf("autopilot store: nil pool")
	}
	if err := validateWorkload(w); err != nil {
		return err
	}
	if _, err := c.pool.Exec(ctx, `
UPDATE interrogation_requested_probes
SET state = 'reverted', end_reason = 'source_gone', updated_at = $4
WHERE namespace = $1 AND workload_kind = $2 AND workload_name = $3 AND state IN ('approved', 'kept')
`, w.Namespace, w.WorkloadKind, w.WorkloadName, now); err != nil {
		return fmt.Errorf("end workload probes: %w", err)
	}
	return nil
}
