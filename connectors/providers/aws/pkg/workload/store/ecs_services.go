package store

import (
	"context"
	"encoding/json"
	"time"
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
	// Instrumented is the live instrumentation state, written through by the instrumentor (authoritative;
	// the detector never derives it).
	Instrumented bool
	// RuntimeLanguage / RuntimeVersion are the resolved workload runtime, written by the instrumentor once
	// resolved (version is the major.minor used by version-templated agents, e.g. php/ruby).
	RuntimeLanguage string
	RuntimeVersion  string
	// InstrumentationError is the last instrumentation/rollback error; empty when healthy.
	InstrumentationError string
	// InstrumentationPhase is the last in-flight instrumentation progress phase; empty when converged.
	InstrumentationPhase string
	Metadata             map[string]any
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

// ListECSServices returns the full live ECS service inventory for the instrumentor, with the
// instrumentation state parsed out of the metadata JSONB.
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
			COALESCE(metadata->>'instrumentation_phase','')
		FROM connector_aws.ecs_services WHERE connector_name = $1 AND deleted_at IS NULL
	`, s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ECSService
	for rows.Next() {
		var svc ECSService
		if err := rows.Scan(&svc.Region, &svc.ClusterARN, &svc.ServiceName, &svc.ServiceARN, &svc.TaskDefinition,
			&svc.LaunchType, &svc.DeploymentController, &svc.Status, &svc.DesiredCount, &svc.RunningCount,
			&svc.Instrumented, &svc.RuntimeLanguage, &svc.RuntimeVersion,
			&svc.InstrumentationError, &svc.InstrumentationPhase); err != nil {
			return nil, err
		}
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

// SetECSServiceInstrumented records whether Odigos instrumentation is currently applied to a service
// (write-through after instrument/rollback, so a reconcile before the next discovery cycle sees the
// current state).
func (s *Store) SetECSServiceInstrumented(ctx context.Context, clusterARN, serviceName string, instrumented bool) error {
	return s.mergeECSServiceMetadata(ctx, clusterARN, serviceName, map[string]any{metaInstrumented: instrumented})
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
