package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
)

// ECSService is a discovered ECS service inventory record. Only Fargate services are stored (EC2-backed
// services belong to the cluster agent, compute-platform domain). It is the stable per-service home for
// Fargate instrumentation state: task ARNs recycle on every deploy/scale, so state written on task rows
// would be lost — the (cluster, service) identity survives.
type ECSService struct {
	Region     string
	ClusterARN string
	// ServiceName is the ECS service's short name — with ClusterARN it forms the workload's stable
	// identity and the source external id (cluster_arn || '/' || service_name).
	ServiceName string
	ServiceARN  string
	// TaskDefinition is the taskDefinition ARN the service currently points at (from DescribeServices).
	TaskDefinition string
	// LaunchType is FARGATE, or empty for capacity-provider services (which report no launch type; the
	// detector only stores services whose strategy resolves to Fargate).
	LaunchType string
	// DeploymentController is the service's deployment controller type (ECS | CODE_DEPLOY | EXTERNAL).
	// Only ECS-controlled services can be instrumented (UpdateService may not change the task definition
	// under the other controllers).
	DeploymentController string
	Status               string
	DesiredCount         int32
	RunningCount         int32
	// Instrumented is whether Odigos instrumentation is currently applied. Written through by the instrumentor
	// in automatic mode and by the observer in manual mode — see SetECSServiceInstrumented for why both, and
	// why they cannot disagree. Discovery never derives it: that would cost a DescribeTaskDefinition per
	// service across the whole account every cycle.
	Instrumented bool
	// RuntimeLanguage / RuntimeVersion are the resolved workload runtime, written by the instrumentor once
	// resolved (version is the major.minor used by version-templated agents, e.g. php/ruby).
	RuntimeLanguage string
	RuntimeVersion  string
	// InstrumentationError is the last instrumentation/rollback error; empty when healthy.
	InstrumentationError string
	// InstrumentationPhase is the last in-flight instrumentation progress phase; empty when converged.
	InstrumentationPhase string
	// Observation is the read-only conformance verdict, written by the observer rather than by discovery or
	// the instrumentor. Zero until a pass has run.
	Observation ECSObservation
	// OwnershipCheckedRevision is the taskDefinition ARN the observer last read to decide Instrumented, for a
	// service with no enabled source (see ECSFargateObserver.Observe and api.FingerprintChanged). It is what
	// lets the observer skip a service on most passes — nothing to check has changed — while still catching
	// the one pass where TaskDefinition moves to a revision it has not yet inspected. Empty until the observer
	// has looked at this service at least once.
	OwnershipCheckedRevision string
	Metadata                 map[string]any
}

// ECSObservation is the persisted result of one read-only conformance observation of a Fargate service: does
// the instrumentation actually on its task definition match what Odigos wants for it. Stored per service so a
// WorkloadReport (served on the GraphQL read path, which must make no AWS calls) can report the verdict
// without a live read.
//
// Unlike the Lambda record this carries no fingerprints. Lambda has PK-indexed env_hash/config_hash columns
// sitting unused, and its observer needed a cheap "did anything move" test over a large per-function env map.
// Here the verdict tuple below IS that test — any desired input that matters shows up as a changed conformance
// or a changed component list — so hashing would add a second thing to keep in step for nothing.
type ECSObservation struct {
	// Conformance is the verdict. ConformanceUnknown means never observed, or the last read failed.
	Conformance api.Conformance
	// Missing names the components that are absent or stale, in the provider-neutral vocabulary the base
	// renders. Empty unless Conformance is Partial.
	Missing []string
	// Since is when this verdict was first observed, carried forward by api.ConformanceSince while it holds.
	Since time.Time
	// Settling is true when the service had a deployment still rolling out at the last observation. A rollout
	// mid-flight is indistinguishable from a half-applied instrumentation, so the verdict above is NOT
	// refreshed while this holds — it is the last settled one.
	Settling bool
	// Error records why the last observation could not be completed. Not drift: the base keeps reporting the
	// last confirmed verdict and raises a transient issue.
	Error string
}

// Metadata keys the instrumentor persists into ecs_services.metadata (snake_case, matching the
// lambda_functions metadata convention in this schema). Kept in JSONB rather than columns so
// instrumentation state evolves without a migration.
const (
	metaInstrumented         = "instrumented"
	metaRuntimeLanguage      = "runtime_language"
	metaRuntimeVersion       = "runtime_version"
	metaInstrumentationError = "instrumentation_error"
	metaInstrumentationPhase = "instrumentation_phase"
	metaOdigosTaskDefinition = "odigos_task_definition"
	metaContainers           = "containers"
	// The read-only observation record. Written only by the observer; UpsertECSService already MERGES
	// metadata rather than replacing it, so these survive a discovery refresh without a preserve-list —
	// the trap the lambda_functions upsert fell into.
	metaConformance        = "conformance"
	metaConformanceMissing = "conformance_missing"
	metaConformanceSince   = "conformance_since"
	metaObservationError   = "observation_error"
	metaSettling           = "settling"
	// metaOwnershipCheckedRevision is written only by the observer's no-source path (refreshOwnership), never
	// by discovery or the instrumentor — see ECSService.OwnershipCheckedRevision.
	metaOwnershipCheckedRevision = "ownership_checked_revision"
)

// ECSContainer is one container of a service's task definition as the instrumentor resolved it.
//
// The workload report is otherwise flat — one service, one runtime — which cannot describe a task
// definition packing several applications. Recording the inventory here is what lets the report
// show the container layer, and it is the instrumentor that records it because only the
// instrumentation pass reads the task definition and runs per-container runtime detection.
type ECSContainer struct {
	Name            string `json:"name"`
	Image           string `json:"image,omitempty"`
	RuntimeLanguage string `json:"runtime_language,omitempty"`
	RuntimeVersion  string `json:"runtime_version,omitempty"`
	// OverrideLanguage/OverrideVersion record a per-container source override that applied.
	OverrideLanguage string `json:"override_language,omitempty"`
	OverrideVersion  string `json:"override_version,omitempty"`
	Instrumented     bool   `json:"instrumented,omitempty"`
	Eligible         bool   `json:"eligible,omitempty"`
	ExcludedReason   string `json:"excluded_reason,omitempty"`
	Distro           string `json:"distro,omitempty"`
	Essential        bool   `json:"essential,omitempty"`
	// OdigosManaged marks the agents sidecar Odigos injects, so a client can tell it apart from the
	// customer's own containers.
	OdigosManaged bool `json:"odigos_managed,omitempty"`
}

// SetECSServiceContainers records the container inventory for a service, replacing the previous one
// (a task definition revision defines the whole set, so merging stale containers would resurrect
// containers a deploy removed).
func (s *Store) SetECSServiceContainers(ctx context.Context, clusterARN, serviceName string, containers []ECSContainer) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, map[string]any{metaContainers: containers})
}

// UpsertECSService inserts or refreshes an ECS service inventory row, clearing any soft-delete marker.
// Metadata is MERGED (not replaced) so instrumentation state written by the instrumentor (instrumented/
// runtime_language/instrumentation_error/...) survives a discovery refresh, while discovery-owned keys
// overlay with fresh values.
func (s *Store) UpsertECSService(ctx context.Context, svc ECSService) error {
	inventoryLog.Debug("upsert ecs service", "region", svc.Region, "cluster", svc.ClusterARN, "service", svc.ServiceName)
	meta, err := metaJSON(svc.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.ecs_services
			(connector_name, region, cluster_arn, service_name, service_arn, task_definition, launch_type,
			 deployment_controller, status, desired_count, running_count, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb, now(), now())
		ON CONFLICT (connector_name, cluster_arn, service_name) DO UPDATE SET
			region = EXCLUDED.region, service_arn = EXCLUDED.service_arn,
			task_definition = EXCLUDED.task_definition, launch_type = EXCLUDED.launch_type,
			deployment_controller = EXCLUDED.deployment_controller, status = EXCLUDED.status,
			desired_count = EXCLUDED.desired_count, running_count = EXCLUDED.running_count,
			metadata = connector_aws.ecs_services.metadata || EXCLUDED.metadata,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), svc.Region, svc.ClusterARN, svc.ServiceName, svc.ServiceARN, svc.TaskDefinition,
		svc.LaunchType, svc.DeploymentController, svc.Status, svc.DesiredCount, svc.RunningCount, meta)
	return err
}

// ecsObservationCols selects the observation record in a fixed order, shared by every read of it so the scan
// stays in one place. Mirrors lambdaObservationCols.
const ecsObservationCols = `COALESCE(metadata->>'conformance', ''), metadata->'conformance_missing', metadata->>'conformance_since', COALESCE(metadata->>'observation_error', ''), COALESCE(metadata->>'settling','false')='true'`

// ListECSServices returns the full live ECS service inventory for the instrumentor and the observer, with the
// instrumentation state and the observation record parsed out of the metadata JSONB.
func (s *Store) ListECSServices(ctx context.Context) ([]ECSService, error) {
	inventoryLog.Debug("list ecs services", "connector", s.ConnectorName())
	rows, err := s.Pool().Query(ctx, `
		SELECT region, cluster_arn, service_name, COALESCE(service_arn, ''), COALESCE(task_definition, ''),
			COALESCE(launch_type, ''), COALESCE(deployment_controller, ''), COALESCE(status, ''),
			desired_count, running_count,
			COALESCE(metadata->>'instrumented','false')='true',
			COALESCE(metadata->>'runtime_language',''),
			COALESCE(metadata->>'runtime_version',''),
			COALESCE(metadata->>'instrumentation_error',''),
			COALESCE(metadata->>'instrumentation_phase',''),
			COALESCE(metadata->>'`+metaOwnershipCheckedRevision+`',''),
			`+ecsObservationCols+`
		FROM connector_aws.ecs_services WHERE connector_name = $1 AND deleted_at IS NULL
	`, s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ECSService
	for rows.Next() {
		var svc ECSService
		var missingJSON []byte
		var since *string
		if err := rows.Scan(&svc.Region, &svc.ClusterARN, &svc.ServiceName, &svc.ServiceARN, &svc.TaskDefinition,
			&svc.LaunchType, &svc.DeploymentController, &svc.Status, &svc.DesiredCount, &svc.RunningCount,
			&svc.Instrumented, &svc.RuntimeLanguage, &svc.RuntimeVersion,
			&svc.InstrumentationError, &svc.InstrumentationPhase, &svc.OwnershipCheckedRevision,
			&conformanceTarget{&svc.Observation.Conformance}, &missingJSON, &since,
			&svc.Observation.Error, &svc.Observation.Settling); err != nil {
			return nil, err
		}
		svc.Observation.Missing = decodeStringSlice(missingJSON, metaConformanceMissing)
		svc.Observation.Since = parseObservedAt(since)
		out = append(out, svc)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	inventoryLog.Debug("list ecs services complete", "connector", s.ConnectorName(), "count", len(out))
	return out, nil
}

// mergeECSServiceMetadata overlays patch onto a service row's metadata JSONB (right wins), leaving other
// keys intact. Used by the instrumentor to record instrumentation state without a read-modify-write.
func (s *Store) mergeECSServiceMetadata(ctx context.Context, clusterARN, serviceName string, patch map[string]any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_aws.ecs_services SET metadata = metadata || $4::jsonb
		WHERE connector_name = $1 AND cluster_arn = $2 AND service_name = $3
	`, s.ConnectorName(), clusterARN, serviceName, string(b))
	return err
}

// deleteECSServiceMetadataKey removes a single key from a service row's metadata JSONB (idempotent).
func (s *Store) deleteECSServiceMetadataKey(ctx context.Context, clusterARN, serviceName, key string) error {
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_aws.ecs_services SET metadata = metadata - $4
		WHERE connector_name = $1 AND cluster_arn = $2 AND service_name = $3
	`, s.ConnectorName(), clusterARN, serviceName, key)
	return err
}

// SetECSServiceObservation records one read-only conformance observation for a service.
//
// It writes the observation keys and NOTHING else — in particular not instrumentation_phase or
// instrumentation_error, which belong to the instrumentor, and not last_seen_at, which is discovery's liveness
// signal and the prune cutoff. `instrumented` is the one deliberate exception, and it goes through
// SetECSServiceInstrumented below so that exception stays explicit rather than buried in this patch.
func (s *Store) SetECSServiceObservation(ctx context.Context, clusterARN, serviceName string, obs ECSObservation) error {
	inventoryLog.Debug("set ecs service observation", "cluster", clusterARN, "service", serviceName, "conformance", obs.Conformance)
	patch := map[string]any{
		metaConformance:        string(obs.Conformance),
		metaConformanceMissing: nonNilStrings(obs.Missing),
		metaObservationError:   obs.Error,
		metaSettling:           obs.Settling,
	}
	// A nil (rather than empty-string) timestamp keeps the key readable as SQL NULL, which parseObservedAt
	// renders as "unknown since" instead of a zero time that would read as 1970.
	if obs.Since.IsZero() {
		patch[metaConformanceSince] = nil
	} else {
		patch[metaConformanceSince] = obs.Since.UTC().Format(time.RFC3339)
	}
	if err := s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, patch); err != nil {
		inventoryLog.Error("set ecs service observation failed", "cluster", clusterARN, "service", serviceName, "error", err)
		return err
	}
	return nil
}

// SetECSServiceInstrumented records whether Odigos instrumentation is currently applied to a service.
//
// Two writers, and they never run together. The instrumentor writes it through after instrument/rollback so a
// reconcile before the next discovery cycle sees the current state. The OBSERVER writes it from the ownership
// markers it reads on the live task definition, which is what makes the flag correct in manual (IaC) mode at
// all: nothing else derives it there, so before the observer existed a service the customer instrumented
// themselves stayed flagged false forever, and one they later un-instrumented stayed flagged true forever.
//
// The two cannot disagree because the modes partition — a type is either reconciled or observed, never both
// (coordinator.activeObservers) — and they compute the same fact from the same source: the ownership marker on
// the task definition the service points at.
func (s *Store) SetECSServiceInstrumented(ctx context.Context, clusterARN, serviceName string, instrumented bool) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, map[string]any{metaInstrumented: instrumented})
}

// SetECSServiceOwnershipChecked records the taskDefinition revision the observer just inspected for a service
// with no enabled source, regardless of what it found — see ECSService.OwnershipCheckedRevision. Written on
// every no-source observation pass (not merged conditionally like SetECSServiceInstrumented, which only fires
// on a change) because the whole point is to remember that THIS revision has been looked at, so
// api.FingerprintChanged stops asking for it again until the service moves to a different one.
func (s *Store) SetECSServiceOwnershipChecked(ctx context.Context, clusterARN, serviceName, revision string) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, map[string]any{metaOwnershipCheckedRevision: revision})
}

// SetECSServiceRuntime records the detected/overridden runtime language (and major.minor version, when known).
func (s *Store) SetECSServiceRuntime(ctx context.Context, clusterARN, serviceName, language, version string) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName,
		map[string]any{metaRuntimeLanguage: language, metaRuntimeVersion: version})
}

// SetECSServiceOdigosTaskDefinition records the task-definition revision ARN the instrumentor registered
// for this service (informational — helps operators tell Odigos revisions apart from customer deploys).
func (s *Store) SetECSServiceOdigosTaskDefinition(ctx context.Context, clusterARN, serviceName, taskDefARN string) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, map[string]any{metaOdigosTaskDefinition: taskDefARN})
}

// SetECSServiceInstrumentationError records the last instrumentation/rollback error for a service.
func (s *Store) SetECSServiceInstrumentationError(ctx context.Context, clusterARN, serviceName, msg string) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, map[string]any{metaInstrumentationError: msg})
}

// ClearECSServiceInstrumentationError clears the recorded instrumentation error for a service.
func (s *Store) ClearECSServiceInstrumentationError(ctx context.Context, clusterARN, serviceName string) error {
	return s.deleteECSServiceMetadataKey(ctx, clusterARN, serviceName, metaInstrumentationError)
}

// SetECSServiceInstrumentationPhase records the in-flight instrumentation progress phase for a service.
func (s *Store) SetECSServiceInstrumentationPhase(ctx context.Context, clusterARN, serviceName, phase string) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, map[string]any{metaInstrumentationPhase: phase})
}

// ClearECSServiceInstrumentationPhase clears the in-flight phase once a service has converged.
func (s *Store) ClearECSServiceInstrumentationPhase(ctx context.Context, clusterARN, serviceName string) error {
	return s.deleteECSServiceMetadataKey(ctx, clusterARN, serviceName, metaInstrumentationPhase)
}

// PruneECSServices soft-deletes stale ECS service rows for a region.
func (s *Store) PruneECSServices(ctx context.Context, region string, before time.Time) error {
	return s.prune(ctx, "ecs_services", region, before)
}

// PurgeDeletedECSServices hard-deletes ECS service rows soft-deleted before the cutoff.
func (s *Store) PurgeDeletedECSServices(ctx context.Context, before time.Time) error {
	return s.purgeDeleted(ctx, "ecs_services", before)
}

// SetECSTaskInstrumentationError records an instrumentation error on a TASK row (keyed by task ARN). Used
// for standalone tasks (no ECS service): they cannot be instrumented — there is no UpdateService rollout
// path — so an enabled source on one gets a terminal unsupported error surfaced via the workload report.
func (s *Store) SetECSTaskInstrumentationError(ctx context.Context, taskARN, msg string) error {
	b, err := json.Marshal(map[string]any{metaInstrumentationError: msg})
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_aws.ecs_tasks SET metadata = metadata || $3::jsonb
		WHERE connector_name = $1 AND task_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), taskARN, string(b))
	return err
}
