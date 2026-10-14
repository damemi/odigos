package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var credentialLog = connlog.Subsystem("store-collector-credentials")

// credentialsTable is in connector_platform, not the provider schema, because connector-opamp-gateway
// resolves a credential by token hash before it knows which provider issued it — the provider is a
// column of the row it is looking for.
const credentialsTable = "connector_platform.collector_credentials"

// credentialColumns is the row shape, in table order. Shared by every read here so a column added to
// the table reaches all of them at once; collectorcredentials_test.go checks it against the migration.
//
// connector_name is selected even though Record has no field for it: it is how a connector finds its
// own credentials without knowing every Collector id it ever issued.
const credentialColumns = `
	collector_id, credential_id, connector_name, connector_uid, provider, account_id, region, network_id,
	secret_ref, secret_version, token_sha256,
	state, issued_at, activated_at, superseded_by, expires_at, revoked_at, revoked_reason`

// ErrCredentialNotFound is returned when no row matches.
var ErrCredentialNotFound = errors.New("no such Collector credential")

// IssueCollectorCredential mints a credential, persists its verifier and binding, and records the
// issuance — all or nothing.
//
// The returned value is the only time it exists outside the caller's own memory. Write it to the
// provider secret store (or reveal it once, in manual mode) and then let it go: only the hash is
// persisted, so nothing can produce it again.
//
// The row is written pending. It becomes active when the caller confirms the value reached the secret
// store, via MarkCollectorCredentialActive — a credential presented before that is one whose value was
// not supposed to have left the issuer.
func (s *Store) IssueCollectorCredential(ctx context.Context, req collectorcredential.IssueRequest) (collectorcredential.Issued, error) {
	var zero collectorcredential.Issued

	minted, record, err := s.prepareIssuance(req)
	if err != nil {
		return zero, err
	}

	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.insertCredential(ctx, tx, record); err != nil {
			return err
		}
		return s.audit(ctx, tx, collectorcredential.EventCredentialIssued, req.Actor, record)
	})
	if err != nil {
		credentialLog.Error("issue collector credential failed",
			"connector", s.connectorName, "collector_id", record.Binding.CollectorID, "error", err)
		return zero, err
	}

	credentialLog.Info("issued collector credential",
		"connector", s.connectorName,
		"collector_id", record.Binding.CollectorID,
		"credential_id", record.CredentialID,
		"fingerprint", collectorcredential.FingerprintOfHash(minted.Hash),
		"network_id", record.Binding.NetworkID,
		"actor", string(req.Actor),
	)
	return collectorcredential.Issued{Minted: minted, Record: record}, nil
}

// prepareIssuance does everything an issuance needs before it touches the database: validate, allocate
// the ids, mint the value, and assemble the row.
//
// Minting outside the transaction is deliberate. A value minted for a transaction that then rolls back
// has been written nowhere and is simply discarded; a transaction that committed around a mint which
// failed would be a row with no credential behind it.
func (s *Store) prepareIssuance(req collectorcredential.IssueRequest) (collectorcredential.Minted, collectorcredential.Record, error) {
	var (
		noValue  collectorcredential.Minted
		noRecord collectorcredential.Record
	)

	// A guard, not the reporting path. Callers that surface status should call req.Validate themselves —
	// it is pure and returns Problems with severities. Reaching here with an error-severity problem
	// means something skipped that, and minting anyway would persist a binding nobody chose.
	if problems := req.Validate(time.Now()); collectorstatus.HasError(problems) {
		return noValue, noRecord, fmt.Errorf("refusing to issue a Collector credential: %s", errorProblems(problems))
	}

	collectorID := req.Binding.CollectorID
	if req.NeedsCollectorID() {
		id, err := collectorcredential.NewCollectorID()
		if err != nil {
			return noValue, noRecord, err
		}
		collectorID = id
	}
	credentialID, err := collectorcredential.NewCredentialID()
	if err != nil {
		return noValue, noRecord, err
	}
	minted, err := collectorcredential.Mint()
	if err != nil {
		return noValue, noRecord, err
	}
	return minted, collectorcredential.NewRecord(req, collectorID, credentialID, minted.Hash, time.Now().UTC()), nil
}

// ReplaceCollectorCredential issues a replacement for a Collector that already has one, keeping its
// Collector identity and moving the outgoing credential aside.
//
// The outgoing credential is left ACCEPTED, in superseded state, and is not revoked here. That is the
// rotation ordering from PlanRotation: revoking now would disconnect a running Collector for the whole
// length of a deployment, in service of an operation that had no urgency. The caller revokes it after
// the replacement connects.
//
// A compromise does not use this path — there the old credential is revoked first, before a replacement
// exists, because every second it stays valid is a second someone may be reading the desired
// configuration out of it.
func (s *Store) ReplaceCollectorCredential(ctx context.Context, currentCredentialID string, req collectorcredential.IssueRequest) (collectorcredential.Issued, error) {
	var zero collectorcredential.Issued

	if req.NeedsCollectorID() {
		return zero, fmt.Errorf("replacing a credential requires the Collector's existing identity: a " +
			"replacement under a new collector id is a new Collector, not a rotation")
	}
	current, err := s.CollectorCredential(ctx, req.Binding.CollectorID, currentCredentialID)
	if err != nil {
		return zero, err
	}
	if err := collectorcredential.Transition(current.State, collectorcredential.StateSuperseded); err != nil {
		return zero, err
	}

	minted, record, err := s.prepareIssuance(req)
	if err != nil {
		return zero, err
	}

	superseded := current
	superseded.State = collectorcredential.StateSuperseded
	superseded.SupersededBy = record.CredentialID

	// One transaction for both halves. Split across two, a failure between them would leave a pending
	// credential behind on every retry — harmless, since the gateway refuses a pending credential, but
	// it would accumulate rows that look like abandoned issuances and make the audit trail harder to
	// read than the thing it is there to explain.
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		if err := s.insertCredential(ctx, tx, record); err != nil {
			return err
		}
		if err := s.audit(ctx, tx, collectorcredential.EventCredentialIssued, req.Actor, record); err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `
			UPDATE `+credentialsTable+`
			SET state = $3, superseded_by = $4
			WHERE collector_id = $1 AND credential_id = $2 AND state = $5
		`, current.Binding.CollectorID, current.CredentialID,
			string(collectorcredential.StateSuperseded), record.CredentialID, string(current.State))
		if err != nil {
			return fmt.Errorf("superseding credential %s: %w", current.CredentialID, err)
		}
		if tag.RowsAffected() == 0 {
			// Fenced on the state we read. Something else moved this credential in between — most likely
			// a revocation — and writing superseded over that would resurrect a revoked credential into
			// a state the gateway accepts. Rolling back takes the replacement with it, which is right:
			// a rotation away from a credential that was just revoked is not the operation the caller
			// asked for.
			return fmt.Errorf("credential %s changed state while being superseded; no replacement was issued",
				current.CredentialID)
		}
		return s.audit(ctx, tx, collectorcredential.EventCredentialSuperseded, req.Actor, superseded)
	})
	if err != nil {
		credentialLog.Error("replace collector credential failed",
			"connector", s.connectorName, "credential_id", current.CredentialID, "error", err)
		return zero, err
	}

	credentialLog.Info("replaced collector credential",
		"connector", s.connectorName,
		"collector_id", req.Binding.CollectorID,
		"outgoing", current.CredentialID,
		"replacement", record.CredentialID,
		"fingerprint", collectorcredential.FingerprintOfHash(minted.Hash),
		"actor", string(req.Actor),
	)
	return collectorcredential.Issued{Minted: minted, Record: record, Superseded: &superseded}, nil
}

// MarkCollectorCredentialActive records that a credential's value reached the provider secret store.
//
// Separate from issuance because the two can fail independently: the value is written by a cloud API
// call that the database knows nothing about. Until this lands the credential is pending, and the
// gateway refuses a pending credential.
func (s *Store) MarkCollectorCredentialActive(ctx context.Context, collectorID, credentialID, secretVersion string) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE `+credentialsTable+`
		SET state = $3, secret_version = NULLIF($4, '')
		WHERE collector_id = $1 AND credential_id = $2 AND state = $5
	`, collectorID, credentialID,
		string(collectorcredential.StateActive), secretVersion, string(collectorcredential.StatePending))
	if err != nil {
		credentialLog.Error("activate collector credential failed",
			"connector", s.connectorName, "credential_id", credentialID, "error", err)
		return fmt.Errorf("activating credential %s: %w", credentialID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("credential %s is not pending; only a pending credential becomes active", credentialID)
	}
	credentialLog.Info("collector credential active",
		"connector", s.connectorName, "collector_id", collectorID, "credential_id", credentialID)
	return nil
}

// RecordCollectorCredentialConnected latches the first successful OpAMP
// connection for this issuance. Store activation only means the secret was
// written; it does not prove the replacement task has started using it.
func (s *Store) RecordCollectorCredentialConnected(ctx context.Context, collectorID, credentialID string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE `+credentialsTable+`
		SET activated_at = COALESCE(activated_at, $3)
		WHERE collector_id = $1 AND credential_id = $2 AND state IN ($4, $5)
	`, collectorID, credentialID, at, string(collectorcredential.StateActive), string(collectorcredential.StateSuperseded))
	return err
}

// RevokeSupersededCollectorCredentials closes rotation windows after the new
// issuance has connected and a grace period has allowed old tasks to drain.
func (s *Store) RevokeSupersededCollectorCredentials(ctx context.Context, collectorID string, now time.Time, grace time.Duration) error {
	records, err := s.CollectorCredentials(ctx, collectorID)
	if err != nil {
		return err
	}
	byID := make(map[string]collectorcredential.Record, len(records))
	for _, record := range records {
		byID[record.CredentialID] = record
	}
	for _, record := range records {
		if record.State != collectorcredential.StateSuperseded || record.SupersededBy == "" {
			continue
		}
		replacement, found := byID[record.SupersededBy]
		if !found || !collectorcredential.SupersededCanRevoke(record, replacement, now, grace) {
			continue
		}
		if err := s.RevokeCollectorCredential(ctx, collectorID, record.CredentialID, collectorcredential.ReasonRotation, collectorcredential.ActorConnector); err != nil {
			return err
		}
	}
	return nil
}

// RevokeCollectorCredential revokes one credential and records why.
//
// Terminal. There is no un-revoke, here or anywhere: the tempting case is real — a compromise took a
// Collector down and reinstating is far cheaper than issuing and redeploying — and it hands the
// credential back to whoever took it.
func (s *Store) RevokeCollectorCredential(ctx context.Context, collectorID, credentialID string, reason collectorcredential.Reason, actor collectorcredential.Actor) error {
	current, err := s.CollectorCredential(ctx, collectorID, credentialID)
	if err != nil {
		return err
	}
	if current.State == collectorcredential.StateRevoked {
		// Idempotent rather than an error: teardown and compromise handling can both reach the same
		// credential, and a second revoke should not fail a teardown that is otherwise correct.
		credentialLog.Info("collector credential already revoked",
			"connector", s.connectorName, "credential_id", credentialID)
		return nil
	}
	if err := collectorcredential.Transition(current.State, collectorcredential.StateRevoked); err != nil {
		return err
	}
	return s.revoke(ctx, current, reason, actor)
}

// RevokeCollectorCredentialsForRecreatedConnector revokes every live credential this connector issued
// under a previous CR UID, and returns them.
//
// Called at startup, where the connector first learns its own UID. A recreated CR has a new one, and a
// credential is bound to the UID at issue — so the Collector's INFRASTRUCTURE is adopted by tag while
// its credential is not. Revoking here rather than leaving the gateway to refuse them is about
// the authority, not the access: with the interim local mint the hash lives only in this database, so a
// credential not revoked while the connector can still see it is one nothing will ever be able to
// revoke.
func (s *Store) RevokeCollectorCredentialsForRecreatedConnector(ctx context.Context, currentConnectorUID string, actor collectorcredential.Actor) ([]collectorcredential.Record, error) {
	if currentConnectorUID == "" {
		// Without its own identity a connector cannot tell its predecessor's credentials from its own,
		// and guessing would revoke a live fleet's configuration plane.
		return nil, fmt.Errorf("refusing to look for superseded credentials without this connector's own UID")
	}

	records, err := s.collectorCredentialsWhere(ctx, `connector_name = $1`, s.connectorName)
	if err != nil {
		return nil, err
	}
	victims := collectorcredential.RecreationVictims(records, currentConnectorUID)
	if len(victims) == 0 {
		return nil, nil
	}

	credentialLog.Info("revoking credentials issued under a previous connector UID",
		"connector", s.connectorName, "current_uid", currentConnectorUID, "count", len(victims))

	revoked := make([]collectorcredential.Record, 0, len(victims))
	for _, victim := range victims {
		if err := s.revoke(ctx, victim, collectorcredential.ReasonConnectorRecreated, actor); err != nil {
			// Report what was done rather than unwinding. Each revoke is independently correct and
			// independently valuable, and a partial sweep is retried on the next startup.
			return revoked, fmt.Errorf("revoking credential %s from a previous connector UID: %w", victim.CredentialID, err)
		}
		revoked = append(revoked, victim)

		// Drop the Collector row's pointer to the credential just revoked. Leaving it would report the
		// Collector as configured when nothing can authenticate it, and would let a later rotation read a
		// revoked id as the credential to supersede. Best-effort: the revocation is what closes the
		// access, and failing the sweep over a stale pointer would leave the REST of the predecessor's
		// credentials live.
		if cleared, err := s.ClearCollectorCredential(ctx, victim.Binding.CollectorID); err != nil {
			credentialLog.Warn("could not clear a revoked credential from its Collector record",
				"connector", s.connectorName, "collector_id", victim.Binding.CollectorID, "error", err)
		} else if cleared > 0 {
			credentialLog.Info("cleared a revoked credential from its Collector record",
				"connector", s.connectorName, "collector_id", victim.Binding.CollectorID, "rows", cleared)
		}
	}
	return revoked, nil
}

// RevokeCollectorCredentials revokes every live credential for one Collector, for teardown or for
// capability removal. A teardown during a rotation has two live credentials; both go.
func (s *Store) RevokeCollectorCredentials(ctx context.Context, collectorID string, reason collectorcredential.Reason, actor collectorcredential.Actor) ([]collectorcredential.Record, error) {
	records, err := s.CollectorCredentials(ctx, collectorID)
	if err != nil {
		return nil, err
	}
	revoked := make([]collectorcredential.Record, 0, len(records))
	for _, r := range records {
		if !r.Live() {
			continue
		}
		if err := s.revoke(ctx, r, reason, actor); err != nil {
			return revoked, fmt.Errorf("revoking credential %s: %w", r.CredentialID, err)
		}
		revoked = append(revoked, r)
	}
	return revoked, nil
}

// revoke writes the revocation and its audit row together. One transaction: a revocation with no audit
// row is untraceable, and an audit row for a revocation that did not happen is worse than none.
func (s *Store) revoke(ctx context.Context, current collectorcredential.Record, reason collectorcredential.Reason, actor collectorcredential.Actor) error {
	revokedAt := time.Now().UTC()
	after := current
	after.State = collectorcredential.StateRevoked
	after.RevokedAt = &revokedAt
	after.RevokedReason = reason

	err := s.inTx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE `+credentialsTable+`
			SET state = $3, revoked_at = $4, revoked_reason = $5
			WHERE collector_id = $1 AND credential_id = $2 AND state <> $3
		`, current.Binding.CollectorID, current.CredentialID,
			string(collectorcredential.StateRevoked), revokedAt, string(reason))
		if err != nil {
			return fmt.Errorf("revoking credential %s: %w", current.CredentialID, err)
		}
		if tag.RowsAffected() == 0 {
			// Already revoked by someone else between the read and here. The end state is the one we
			// wanted, so this is not a failure — but it must not produce a second audit row claiming a
			// different reason for the same revocation.
			return nil
		}
		return s.audit(ctx, tx, collectorcredential.EventCredentialRevoked, actor, after)
	})
	if err != nil {
		credentialLog.Error("revoke collector credential failed",
			"connector", s.connectorName, "credential_id", current.CredentialID, "error", err)
		return err
	}
	credentialLog.Info("revoked collector credential",
		"connector", s.connectorName,
		"collector_id", current.Binding.CollectorID,
		"credential_id", current.CredentialID,
		"reason", string(reason),
		"actor", string(actor),
	)
	return nil
}

// CollectorCredential reads one credential. Returns metadata only — no column holds a value.
func (s *Store) CollectorCredential(ctx context.Context, collectorID, credentialID string) (collectorcredential.Record, error) {
	records, err := s.collectorCredentialsWhere(ctx,
		`collector_id = $1 AND credential_id = $2`, collectorID, credentialID)
	if err != nil {
		return collectorcredential.Record{}, err
	}
	if len(records) == 0 {
		return collectorcredential.Record{}, fmt.Errorf("%w: %s/%s", ErrCredentialNotFound, collectorID, credentialID)
	}
	return records[0], nil
}

// CollectorCredentials reads every credential ever issued for one Collector, newest first. A Collector
// has several over its life and a rotation has two live at once.
func (s *Store) CollectorCredentials(ctx context.Context, collectorID string) ([]collectorcredential.Record, error) {
	return s.collectorCredentialsWhere(ctx, `collector_id = $1`, collectorID)
}

// LiveCollectorCredentials reads the credentials the gateway would still accept for one Collector.
func (s *Store) LiveCollectorCredentials(ctx context.Context, collectorID string) ([]collectorcredential.Record, error) {
	return s.collectorCredentialsWhere(ctx,
		`collector_id = $1 AND state IN ($2, $3)`,
		collectorID, string(collectorcredential.StateActive), string(collectorcredential.StateSuperseded))
}

func (s *Store) collectorCredentialsWhere(ctx context.Context, where string, args ...any) ([]collectorcredential.Record, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT`+credentialColumns+` FROM `+credentialsTable+` WHERE `+where+` ORDER BY issued_at DESC, credential_id`,
		args...)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", credentialsTable, err)
	}
	defer rows.Close()

	var out []collectorcredential.Record
	for rows.Next() {
		var (
			rec           collectorcredential.Record
			connectorName string
			state         string
			secretVersion *string
			supersededBy  *string
			revokedReason *string
		)
		if err := rows.Scan(
			&rec.Binding.CollectorID,
			&rec.CredentialID,
			&connectorName,
			&rec.Binding.ConnectorUID,
			&rec.Binding.Provider,
			&rec.Binding.AccountID,
			&rec.Binding.Region,
			&rec.Binding.NetworkID,
			&rec.SecretRef,
			&secretVersion,
			&rec.TokenSHA256,
			&state,
			&rec.IssuedAt,
			&rec.ActivatedAt,
			&supersededBy,
			&rec.ExpiresAt,
			&rec.RevokedAt,
			&revokedReason,
		); err != nil {
			return nil, fmt.Errorf("scanning %s: %w", credentialsTable, err)
		}
		rec.State = collectorcredential.State(state)
		rec.SecretVersion = derefString(secretVersion)
		rec.SupersededBy = derefString(supersededBy)
		rec.RevokedReason = collectorcredential.Reason(derefString(revokedReason))
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) insertCredential(ctx context.Context, tx pgx.Tx, r collectorcredential.Record) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO `+credentialsTable+`
			(collector_id, credential_id, connector_name, connector_uid, provider, account_id, region,
			 network_id, secret_ref, token_sha256, state, issued_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`,
		r.Binding.CollectorID, r.CredentialID, s.connectorName, r.Binding.ConnectorUID,
		r.Binding.Provider, r.Binding.AccountID, r.Binding.Region, r.Binding.NetworkID,
		r.SecretRef, r.TokenSHA256, string(r.State), r.IssuedAt, r.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("persisting credential %s: %w", r.CredentialID, err)
	}
	return nil
}

// audit appends one row to connector_platform.installation_audit inside the caller's transaction.
//
// Always in the same transaction as the change it describes. An issuance that committed without its
// audit row would be a credential with no account of who asked for it — and with the value unstored
// and the hash irreversible, the audit trail is the only such account there is.
func (s *Store) audit(ctx context.Context, tx pgx.Tx, event string, actor collectorcredential.Actor, r collectorcredential.Record) error {
	// AuditDetail is the single place that decides what an audit row says about a credential, and it
	// carries the fingerprint rather than the hash — an audit table is read by more people and exported
	// to more places than the credentials table.
	detail, err := json.Marshal(collectorcredential.AuditDetail(r, actor))
	if err != nil {
		return fmt.Errorf("rendering audit detail for %s: %w", event, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO connector_platform.installation_audit (connector_name, event, actor, detail)
		VALUES ($1, $2, $3, $4::jsonb)
	`, s.connectorName, event, string(actor), string(detail)); err != nil {
		return fmt.Errorf("recording audit event %s: %w", event, err)
	}
	return nil
}

// inTx runs fn in a transaction, rolling back on error or panic.
func (s *Store) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			// Best-effort: the transaction is already failing, and a rollback error would replace the
			// reason with a less useful one.
			_ = tx.Rollback(ctx)
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	committed = true
	return nil
}

// errorProblems joins every error-severity message. All of them, not the first: an incomplete binding
// usually has several missing fields, and reporting one at a time turns one fix into several rounds.
func errorProblems(problems []collectorstatus.Problem) string {
	var msgs []string
	for _, p := range problems {
		if p.Severity == collectorstatus.SeverityError {
			msgs = append(msgs, p.Message)
		}
	}
	return strings.Join(msgs, "; ")
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
