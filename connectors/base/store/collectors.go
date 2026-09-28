package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
)

var collectorsLog = connlog.Subsystem("store-managed-collectors")

// ErrCollectorNotFound is returned when no row exists for a region.
var ErrCollectorNotFound = errors.New("no managed Collector recorded for this region")

// CollectorKey identifies one regional Collector. The host network is metadata on CollectorRecord,
// not part of the Collector identity.
type CollectorKey struct {
	Region string
}

// CollectorRecord is one managed_collectors row.
//
// It carries refs and never secret material: CredentialRef says WHERE the value lives and
// ActiveCredentialID says WHICH issuance is in force. The value exists only between minting and the
// secret-store write, and its hash lives in connector_platform.collector_credentials — not here, and not
// anywhere a report or a backup of this table would carry it.
type CollectorRecord struct {
	Key  CollectorKey
	Kind string
	// NetworkID is the VPC/VNet hosting this regional Collector. It is metadata, not identity.
	NetworkID string

	// Mode and Origin are two independent bits, never one enum. Mode says who converges the
	// Collector from now on; Origin says who created its infrastructure. Only (automatic, odigos) may be
	// deleted, and changing Mode never rewrites Origin — which is what makes the automatic → manual →
	// automatic round trip safe.
	Mode      string
	Origin    string
	AdoptedAt *time.Time

	CollectorID        string
	CredentialRef      string
	ActiveCredentialID string

	// Infrastructure refs. Also the second half of the deletion gate: an object may be deleted
	// only when its tag says Odigos created it AND its ref is recorded here.
	WorkloadRef   string
	RevisionRef   string
	RuntimeRef    string
	FrontendRef   string
	BackendRefs   []string
	IngressRef    string
	PlacementRefs []string

	EndpointGRPC   string
	EndpointHTTP   string
	EndpointSource string

	TLSMode        string
	TLSTermination string
	IngressTier    string

	ImageDigest      string
	ImageTag         string
	ImageArch        string
	LicenseSecretRef string
	LicenseExpiresAt *time.Time

	ConfigHash     string
	ConfigPushedAt *time.Time

	ScalingMode  string
	DesiredCount *int
	RunningCount *int
	MinCount     *int
	MaxCount     *int
	CPU          string
	Memory       string

	OpAMPInstanceUID string
	OpAMPConnectedAt *time.Time
	OpAMPLastSeenAt  *time.Time
	AgentHealth      string

	Status    collectorstatus.Status
	Message   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Recorded reports whether this row holds an infrastructure ref for the object, which is what the
// deletion gate reads. A row with no refs grants no delete authority.
func (r CollectorRecord) Recorded(ref string) bool {
	if ref == "" {
		return false
	}
	for _, candidate := range append([]string{
		r.WorkloadRef, r.RevisionRef, r.RuntimeRef, r.FrontendRef, r.IngressRef,
	}, append(r.BackendRefs, r.PlacementRefs...)...) {
		if candidate == ref {
			return true
		}
	}
	return false
}

// collectorColumns is the row shape, in table order. One list drives every read so a column added to the
// table reaches all of them at once; collectors_test.go checks it against the migration.
const collectorColumns = `
	connector_name, region, network_id, kind,
	mode, origin, adopted_at,
	collector_id, credential_ref, active_credential_id,
	workload_ref, revision_ref, runtime_ref, frontend_ref, backend_refs, ingress_ref, placement_refs,
	endpoint_grpc, endpoint_http, endpoint_source,
	tls_mode, tls_termination, ingress_tier,
	image_digest, image_tag, image_arch, license_secret_ref, license_expires_at,
	config_hash, config_pushed_at,
	scaling_mode, desired_count, running_count, min_count, max_count, cpu, memory,
	opamp_instance_uid, opamp_connected_at, opamp_last_seen_at, agent_health,
	status, message, created_at, updated_at`

// Writers are split by CONCERN, and the split is the concurrency design.
//
// Several loops write this row at once: the deployer converges infrastructure, the OpAMP server records
// liveness, the config pusher records a push, the observer records replica counts. Each accessor below
// names ONLY its own columns, so two loops writing the same row at the same instant cannot clobber each
// other's work — which a whole-row UPSERT from each of them would, silently, by writing back whatever it
// last read.
//
// No row-level fence beyond the primary key, deliberately: connector-runtime clamps a connector to one
// replica and deploys it with the Recreate strategy, so two pods for one connector do not run at once.
// collectors_test.go asserts the column sets stay disjoint.

// UpsertManagedCollector records that a regional Collector exists, with its authority bits and host
// network metadata.
//
// Owns kind, mode, origin and adopted_at, and nothing else — a converge pass must not reset a Collector's
// endpoint or liveness just because it re-declared the row.
//
// Origin is written only on INSERT. It records who CREATED the infrastructure, which no later event
// changes: re-running a converge over an adopted stack must not quietly relabel it as Odigos-created,
// because that is the difference between a stack teardown removes and one it leaves alone.
func (s *Store) UpsertManagedCollector(ctx context.Context, key CollectorKey, networkID, kind, mode, origin string, adoptedAt *time.Time) error {
	collectorsLog.Debug("upsert managed collector",
		"connector", s.connectorName, "region", key.Region, "network", networkID, "mode", mode, "origin", origin)

	_, err := s.pool.Exec(ctx, fmt.Sprintf(`
		INSERT INTO %s.managed_collectors
			(connector_name, region, network_id, kind, mode, origin, adopted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (connector_name, region) DO UPDATE SET
			network_id = EXCLUDED.network_id,
			kind = EXCLUDED.kind,
			mode = EXCLUDED.mode,
			adopted_at = COALESCE(managed_collectors.adopted_at, EXCLUDED.adopted_at),
			updated_at = now()
	`, s.schema), s.connectorName, key.Region, networkID, kind, mode, origin, adoptedAt)
	if err != nil {
		collectorsLog.Error("upsert managed collector failed",
			"connector", s.connectorName, "network", networkID, "error", err)
		return fmt.Errorf("recording the managed Collector for region %s: %w", key.Region, err)
	}
	return nil
}

// RecordCollectorIdentity records which Collector identity and credential this row is on.
//
// Refs only. The credential VALUE never reaches this table, and neither does its hash — that lives in
// collector_credentials, so a report or a backup of this table carries nothing that authenticates.
func (s *Store) RecordCollectorIdentity(ctx context.Context, key CollectorKey, collectorID, credentialRef, activeCredentialID string) error {
	return s.update(ctx, key, "identity", `
		collector_id = $3,
		credential_ref = NULLIF($4, ''),
		active_credential_id = NULLIF($5, '')`,
		collectorID, credentialRef, activeCredentialID)
}

// ClearCollectorCredential drops the pointer to a credential that is no longer in force, for every
// Collector under a Collector identity.
//
// Called by the connector-recreation sweep after revoking the predecessor's credentials: leaving the row
// naming a revoked credential would report a Collector as configured when nothing can authenticate it,
// and would let a rotation read the revoked id as the one to supersede.
//
// The Collector identity is kept. The infrastructure is adopted across a connector recreation
// and its collector_id names that same Collector; only the credential is not inherited.
func (s *Store) ClearCollectorCredential(ctx context.Context, collectorID string) (int64, error) {
	if collectorID == "" {
		return 0, fmt.Errorf("refusing to clear credentials for an empty Collector identity")
	}
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s.managed_collectors
		SET active_credential_id = NULL, updated_at = now()
		WHERE connector_name = $1 AND collector_id = $2 AND active_credential_id IS NOT NULL
	`, s.schema), s.connectorName, collectorID)
	if err != nil {
		collectorsLog.Error("clear collector credential failed",
			"connector", s.connectorName, "collector_id", collectorID, "error", err)
		return 0, fmt.Errorf("clearing the active credential for Collector %s: %w", collectorID, err)
	}
	return tag.RowsAffected(), nil
}

// RecordCollectorInfrastructure records the objects the deployer created or adopted.
//
// These refs are half the deletion gate, so an accessor that loses one costs delete authority rather
// than granting it — the safe direction.
func (s *Store) RecordCollectorInfrastructure(ctx context.Context, key CollectorKey, rec CollectorRecord) error {
	backends, err := json.Marshal(nonNilSlice(rec.BackendRefs))
	if err != nil {
		return fmt.Errorf("encoding backend refs: %w", err)
	}
	placements, err := json.Marshal(nonNilSlice(rec.PlacementRefs))
	if err != nil {
		return fmt.Errorf("encoding placement refs: %w", err)
	}
	return s.update(ctx, key, "infrastructure", `
		workload_ref = NULLIF($3, ''),
		revision_ref = NULLIF($4, ''),
		runtime_ref = NULLIF($5, ''),
		frontend_ref = NULLIF($6, ''),
		backend_refs = $7::jsonb,
		ingress_ref = NULLIF($8, ''),
		placement_refs = $9::jsonb`,
		rec.WorkloadRef, rec.RevisionRef, rec.RuntimeRef, rec.FrontendRef,
		string(backends), rec.IngressRef, string(placements))
}

// RecordCollectorEndpoint records what workloads are pointed at and what that hop costs.
//
// Transport posture is stored rather than derived at read time because who can write to this Collector,
// and whether the hop is encrypted, are not visible from the endpoint alone.
func (s *Store) RecordCollectorEndpoint(ctx context.Context, key CollectorKey, grpc, http, source, tlsMode, tlsTermination, ingressTier string) error {
	return s.update(ctx, key, "endpoint", `
		endpoint_grpc = NULLIF($3, ''),
		endpoint_http = NULLIF($4, ''),
		endpoint_source = NULLIF($5, ''),
		tls_mode = $6,
		tls_termination = NULLIF($7, ''),
		ingress_tier = $8`,
		grpc, http, source, tlsMode, tlsTermination, ingressTier)
}

// RecordCollectorImage records the image and licence the Collector is running.
//
// The DIGEST is the identity; the tag is recorded alongside only so a human can recognise the release. A
// drift check against a tag can only ever report that the tag still matches.
func (s *Store) RecordCollectorImage(ctx context.Context, key CollectorKey, digest, tag, arch, licenseSecretRef string, licenseExpiresAt *time.Time) error {
	return s.update(ctx, key, "image", `
		image_digest = NULLIF($3, ''),
		image_tag = NULLIF($4, ''),
		image_arch = NULLIF($5, ''),
		license_secret_ref = NULLIF($6, ''),
		license_expires_at = $7`,
		digest, tag, arch, licenseSecretRef, licenseExpiresAt)
}

// RecordCollectorConfigPush records the configuration now in force. The hash is what decides whether the
// next reconcile pushes at all, so steady state is zero pushes.
func (s *Store) RecordCollectorConfigPush(ctx context.Context, key CollectorKey, configHash string, pushedAt time.Time) error {
	return s.update(ctx, key, "config", `
		config_hash = NULLIF($3, ''),
		config_pushed_at = $4`,
		configHash, pushedAt)
}

// RecordCollectorScaling records the scaling configuration and what is actually running.
func (s *Store) RecordCollectorScaling(ctx context.Context, key CollectorKey, mode string, desired, running, min, max *int, cpu, memory string) error {
	return s.update(ctx, key, "scaling", `
		scaling_mode = NULLIF($3, ''),
		desired_count = $4,
		running_count = $5,
		min_count = $6,
		max_count = $7,
		cpu = NULLIF($8, ''),
		memory = NULLIF($9, '')`,
		mode, desired, running, min, max, cpu, memory)
}

// RecordCollectorLiveness records the OpAMP session.
//
// Reported, never an input to endpoint selection: re-pointing workloads on a Collector's health would
// rewrite every task definition on the network during a blip and again on recovery.
func (s *Store) RecordCollectorLiveness(ctx context.Context, key CollectorKey, instanceUID string, connectedAt, lastSeenAt *time.Time, agentHealth string) error {
	return s.update(ctx, key, "liveness", `
		opamp_instance_uid = NULLIF($3, ''),
		opamp_connected_at = COALESCE($4, managed_collectors.opamp_connected_at),
		opamp_last_seen_at = $5,
		agent_health = NULLIF($6, '')`,
		instanceUID, connectedAt, lastSeenAt, agentHealth)
}

// SetCollectorStatus records the coarse state and its message. One writer for both, so a status can
// never disagree with the sentence explaining it.
func (s *Store) SetCollectorStatus(ctx context.Context, key CollectorKey, status collectorstatus.Status, message string) error {
	return s.update(ctx, key, "status", `
		status = $3,
		message = NULLIF($4, '')`,
		string(status), message)
}

// update applies one concern's columns to one row. Every writer goes through it, so every write is
// scoped to a single row, stamps updated_at, and reports a missing row the same way.
func (s *Store) update(ctx context.Context, key CollectorKey, concern, set string, args ...any) error {
	params := append([]any{s.connectorName, key.Region}, args...)
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		UPDATE %s.managed_collectors
		SET %s, updated_at = now()
		WHERE connector_name = $1 AND region = $2
	`, s.schema, set), params...)
	if err != nil {
		collectorsLog.Error("update managed collector failed",
			"connector", s.connectorName, "region", key.Region, "concern", concern, "error", err)
		return fmt.Errorf("recording %s for the Collector in region %s: %w", concern, key.Region, err)
	}
	if tag.RowsAffected() == 0 {
		// The row has to exist first. Creating it here would let any writer invent a Collector with no
		// mode and no origin, which is a row the ownership rules cannot classify.
		return fmt.Errorf("%w: region %s", ErrCollectorNotFound, key.Region)
	}
	return nil
}

// DeleteManagedCollector removes the row.
//
// Called only AFTER teardown has removed the infrastructure, and the ordering matters: these refs are
// half the deletion gate, so deleting the row first would strip delete authority from anything still
// standing and leave it orphaned with no record that Odigos ever created it. It is the mirror of
// the credential rule, where revocation comes first — there the record is the danger, here it is the
// authority.
func (s *Store) DeleteManagedCollector(ctx context.Context, key CollectorKey) error {
	tag, err := s.pool.Exec(ctx, fmt.Sprintf(`
		DELETE FROM %s.managed_collectors
		WHERE connector_name = $1 AND region = $2
	`, s.schema), s.connectorName, key.Region)
	if err != nil {
		collectorsLog.Error("delete managed collector failed",
			"connector", s.connectorName, "region", key.Region, "error", err)
		return fmt.Errorf("deleting the Collector record for region %s: %w", key.Region, err)
	}
	collectorsLog.Info("deleted managed collector record",
		"connector", s.connectorName, "region", key.Region, "rows", tag.RowsAffected())
	return nil
}

// ListManagedCollectors returns every Collector this connector records, ordered so two reads of the same
// state produce the same order.
func (s *Store) ListManagedCollectors(ctx context.Context) ([]CollectorRecord, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT%s FROM %s.managed_collectors
		WHERE connector_name = $1
		ORDER BY region
	`, collectorColumns, s.schema), s.connectorName)
	if err != nil {
		collectorsLog.Error("list managed collectors failed", "connector", s.connectorName, "error", err)
		return nil, fmt.Errorf("listing managed Collectors: %w", err)
	}
	defer rows.Close()

	var out []CollectorRecord
	for rows.Next() {
		rec, err := scanCollectorRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// GetManagedCollector returns one Collector, or ErrCollectorNotFound.
func (s *Store) GetManagedCollector(ctx context.Context, key CollectorKey) (CollectorRecord, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT%s FROM %s.managed_collectors
		WHERE connector_name = $1 AND region = $2
	`, collectorColumns, s.schema), s.connectorName, key.Region)
	if err != nil {
		return CollectorRecord{}, fmt.Errorf("reading the Collector for region %s: %w", key.Region, err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return CollectorRecord{}, err
		}
		return CollectorRecord{}, fmt.Errorf("%w: region %s", ErrCollectorNotFound, key.Region)
	}
	return scanCollectorRow(rows)
}

func scanCollectorRow(row pgx.Row) (CollectorRecord, error) {
	var (
		rec           CollectorRecord
		connectorName string
		status        string
		backendsRaw   []byte
		placementsRaw []byte
	)
	// Nullable text columns scan through pointers so an absent value stays distinguishable from an empty
	// one at the database boundary, then collapses to "" in the record — the shape every caller wants.
	var (
		collectorID, credentialRef, activeCredentialID     *string
		workloadRef, revisionRef, runtimeRef, frontendRef  *string
		ingressRef, endpointGRPC, endpointHTTP             *string
		endpointSource, tlsTermination                     *string
		imageDigest, imageTag, imageArch, licenseSecretRef *string
		configHash, scalingMode, cpu, memory               *string
		opampInstanceUID, agentHealth, message             *string
	)

	if err := row.Scan(
		&connectorName, &rec.Key.Region, &rec.NetworkID, &rec.Kind,
		&rec.Mode, &rec.Origin, &rec.AdoptedAt,
		&collectorID, &credentialRef, &activeCredentialID,
		&workloadRef, &revisionRef, &runtimeRef, &frontendRef, &backendsRaw, &ingressRef, &placementsRaw,
		&endpointGRPC, &endpointHTTP, &endpointSource,
		&rec.TLSMode, &tlsTermination, &rec.IngressTier,
		&imageDigest, &imageTag, &imageArch, &licenseSecretRef, &rec.LicenseExpiresAt,
		&configHash, &rec.ConfigPushedAt,
		&scalingMode, &rec.DesiredCount, &rec.RunningCount, &rec.MinCount, &rec.MaxCount, &cpu, &memory,
		&opampInstanceUID, &rec.OpAMPConnectedAt, &rec.OpAMPLastSeenAt, &agentHealth,
		&status, &message, &rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		collectorsLog.Error("scan managed collector row failed", "error", err)
		return CollectorRecord{}, fmt.Errorf("scanning a managed Collector row: %w", err)
	}

	rec.CollectorID = derefString(collectorID)
	rec.CredentialRef = derefString(credentialRef)
	rec.ActiveCredentialID = derefString(activeCredentialID)
	rec.WorkloadRef = derefString(workloadRef)
	rec.RevisionRef = derefString(revisionRef)
	rec.RuntimeRef = derefString(runtimeRef)
	rec.FrontendRef = derefString(frontendRef)
	rec.IngressRef = derefString(ingressRef)
	rec.EndpointGRPC = derefString(endpointGRPC)
	rec.EndpointHTTP = derefString(endpointHTTP)
	rec.EndpointSource = derefString(endpointSource)
	rec.TLSTermination = derefString(tlsTermination)
	rec.ImageDigest = derefString(imageDigest)
	rec.ImageTag = derefString(imageTag)
	rec.ImageArch = derefString(imageArch)
	rec.LicenseSecretRef = derefString(licenseSecretRef)
	rec.ConfigHash = derefString(configHash)
	rec.ScalingMode = derefString(scalingMode)
	rec.CPU = derefString(cpu)
	rec.Memory = derefString(memory)
	rec.OpAMPInstanceUID = derefString(opampInstanceUID)
	rec.AgentHealth = derefString(agentHealth)
	rec.Message = derefString(message)
	rec.Status = collectorstatus.Status(status)

	if err := json.Unmarshal(backendsRaw, &rec.BackendRefs); err != nil {
		return CollectorRecord{}, fmt.Errorf("decoding backend refs: %w", err)
	}
	if err := json.Unmarshal(placementsRaw, &rec.PlacementRefs); err != nil {
		return CollectorRecord{}, fmt.Errorf("decoding placement refs: %w", err)
	}
	return rec, nil
}

// nonNilSlice keeps a nil slice from encoding as JSON null, which the NOT NULL jsonb columns reject.
func nonNilSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
