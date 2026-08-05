package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/types"
)

// Workload kinds — directly-instrumentable resources. Kind strings match the spec type names so the
// coordinator resolves a workload's instrumentation availability from the spec.
const (
	KindLambda  = types.Lambda
	KindECSTask = types.FargateTask
)

// ECS launch types as reported by the AWS API.
const (
	LaunchTypeFargate = "FARGATE"
	LaunchTypeEC2     = "EC2"
)

// EnvExecWrapper is the Lambda environment variable the instrumentor sets to enable OTel
// auto-instrumentation; the detector reads it back to derive live instrumentation state. Kept here as
// neutral ground shared by the workload detector and instrumentor.
const EnvExecWrapper = "AWS_LAMBDA_EXEC_WRAPPER"

// EnvOdigosManaged marks a Lambda function as instrumented by THIS connector and (in its value) records
// exactly what we changed, so rollback removes only our additions and restores any customer values we
// overwrote. Its presence is the authoritative proof of Odigos ownership.
const EnvOdigosManaged = "ODIGOS_MANAGED"

// envCollectorEndpoint / envCollectorInsecure are the odigos-namespaced collector vars the instrumentor
// sets (mirrored as instrumentor.EnvCollectorEndpoint/EnvCollectorInsecure). Their presence proves we
// instrumented a function even before EnvOdigosManaged existed — no customer OTel/ADOT setup sets them.
const (
	envCollectorEndpoint = "ODIGOS_OTLP_ENDPOINT"
	envCollectorInsecure = "ODIGOS_OTLP_INSECURE"
)

// Instrumentation phases track a workload's progress through the instrument flow, live. The instrumentor
// writes them as it advances so a WorkloadReport (pulled by central) always reflects the current step:
//
//	(source matched) -> Verifying -> LayerReady -> Instrumenting -> Instrumented
//
// They are persisted in the inventory row's metadata (no schema change) and surfaced as the WorkloadReport
// progress stage. Verifying/LayerReady are only written while a function is not yet instrumented, so a
// steady-state instrumented function does not flip phases every reconcile.
const (
	PhaseVerifying     = api.PhaseVerifying     // validating the function and resolving/installing the OTel layer
	PhaseLayerReady    = api.PhaseLayerReady    // the OTel layer is resolved and available for this function
	PhaseInstrumenting = api.PhaseInstrumenting // applying the layer + env to the function (may settle async)
	PhaseInstrumented  = api.PhaseInstrumented  // instrumentation applied and confirmed
)

// OdigosInstrumented reports whether a Lambda function's environment shows instrumentation owned by THIS
// connector: the ownership marker, or — for functions instrumented before the marker existed — the
// odigos-namespaced collector vars. It deliberately does NOT treat a generic OpenTelemetry/ADOT layer or
// exec wrapper as ours: a customer's pre-existing OTel setup is not Odigos-owned and must never be flagged
// as instrumented by us nor rolled back.
func OdigosInstrumented(env map[string]string) bool {
	if env == nil {
		return false
	}
	if _, ok := env[EnvOdigosManaged]; ok {
		return true
	}
	return env[envCollectorEndpoint] != "" || env[envCollectorInsecure] != ""
}

// InstrumentationType returns the connector-spec instrumentation type for a discovered task.
func (t ECSTask) InstrumentationType() string {
	return KindECSTask
}

// SupportedKinds returns the workload kinds this connector publishes.
func SupportedKinds() []string {
	return []string{KindLambda, KindECSTask}
}

// LambdaFunction is a discovered Lambda function inventory record.
type LambdaFunction struct {
	Region       string
	FunctionARN  string
	FunctionName string
	Runtime      string
	Architecture string
	RoleARN      string
	TracingMode  string
	// Instrumented is the live instrumentation state derived from the function's env at discovery time;
	// persisted in metadata so WorkloadReports and the instrumentor's reconcile see the same truth.
	Instrumented bool
	Metadata     map[string]any
}

// ECSTask is a discovered ECS task inventory record. Only FARGATE-launch-type tasks are stored:
// EC2-launch-type tasks belong to the cluster agent (compute-platform domain) and are never per-task
// sidecar candidates. Service-managed tasks are coverage detail under their ecs_services row; standalone
// tasks (no service) are workloads of their own, keyed by task ARN.
type ECSTask struct {
	Region         string
	ClusterARN     string
	TaskARN        string
	ServiceName    string
	TaskDefinition string
	LaunchType     string // FARGATE (possibly via a FARGATE/FARGATE_SPOT capacity provider)
	LastStatus     string
	DesiredStatus  string
	Metadata       map[string]any
}

// ecsServiceWorkloadName composes a Fargate SERVICE workload's display name over ecs_services rows:
// "{cluster short name}-{service name}". ecsStandaloneTaskWorkloadName is the analog over standalone
// ecs_tasks rows (no service): "{cluster short name}-{task id}". Each is used identically in
// ListWorkloads (to produce the name) and LookupDiscoveredWorkload (to match it) so a created source's
// name resolves back to its workload.
const (
	ecsServiceWorkloadName        = `regexp_replace(cluster_arn, '^.*/', '') || '-' || service_name`
	ecsStandaloneTaskWorkloadName = `regexp_replace(cluster_arn, '^.*/', '') || '-' || regexp_replace(task_arn, '^.*/', '')`
)

func metaJSON(m map[string]any) (string, error) {
	if m == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// UpsertLambda inserts or refreshes a Lambda inventory row, clearing any soft-delete marker.
func (s *Store) UpsertLambda(ctx context.Context, fn LambdaFunction) error {
	inventoryLog.Debug("upsert lambda", "region", fn.Region, "name", fn.FunctionName)
	if fn.Metadata == nil {
		fn.Metadata = map[string]any{}
	}
	fn.Metadata["instrumented"] = fn.Instrumented
	meta, err := metaJSON(fn.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.lambda_functions
			(connector_name, region, function_arn, function_name, runtime, architecture, role_arn, tracing_mode, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb, now(), now())
		ON CONFLICT (connector_name, function_arn) DO UPDATE SET
			region = EXCLUDED.region, function_name = EXCLUDED.function_name, runtime = EXCLUDED.runtime,
			architecture = EXCLUDED.architecture, role_arn = EXCLUDED.role_arn, tracing_mode = EXCLUDED.tracing_mode,
			metadata = EXCLUDED.metadata
				|| CASE
					WHEN connector_aws.lambda_functions.metadata ? 'instrumentation_error'
					THEN jsonb_build_object('instrumentation_error', connector_aws.lambda_functions.metadata->'instrumentation_error')
					ELSE '{}'::jsonb END
				|| CASE
					WHEN connector_aws.lambda_functions.metadata ? 'instrumentation_phase'
					THEN jsonb_build_object('instrumentation_phase', connector_aws.lambda_functions.metadata->'instrumentation_phase')
					ELSE '{}'::jsonb END,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), fn.Region, fn.FunctionARN, fn.FunctionName, fn.Runtime, fn.Architecture, fn.RoleARN, fn.TracingMode, meta)
	return err
}

const lambdaSelectCols = `region, function_arn, function_name, COALESCE(runtime, ''), COALESCE(architecture, ''), COALESCE(role_arn, ''), COALESCE(tracing_mode, ''), COALESCE(metadata->>'instrumented', 'false') = 'true'`

func scanLambda(row interface{ Scan(...any) error }) (LambdaFunction, error) {
	var fn LambdaFunction
	err := row.Scan(&fn.Region, &fn.FunctionARN, &fn.FunctionName, &fn.Runtime, &fn.Architecture, &fn.RoleARN, &fn.TracingMode, &fn.Instrumented)
	return fn, err
}

// ListLambdaFunctions returns all live (non-deleted) Lambda inventory rows, used by the instrumentor.
func (s *Store) ListLambdaFunctions(ctx context.Context) ([]LambdaFunction, error) {
	rows, err := s.Pool().Query(ctx,
		"SELECT "+lambdaSelectCols+" FROM connector_aws.lambda_functions WHERE connector_name = $1 AND deleted_at IS NULL",
		s.ConnectorName())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LambdaFunction
	for rows.Next() {
		fn, err := scanLambda(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fn)
	}
	return out, rows.Err()
}

// GetLambdaFunction returns a single Lambda inventory row by ARN, or (nil, false) if absent.
func (s *Store) GetLambdaFunction(ctx context.Context, functionARN string) (*LambdaFunction, bool, error) {
	row := s.Pool().QueryRow(ctx,
		"SELECT "+lambdaSelectCols+" FROM connector_aws.lambda_functions WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL",
		s.ConnectorName(), functionARN)
	fn, err := scanLambda(row)
	if err != nil {
		if isNoRows(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fn, true, nil
}

// SetLambdaInstrumented writes the live instrumentation flag through immediately after instrument/rollback
// (instead of waiting for the next discovery cycle), closing the stale-flag race. Clears any stored
// instrumentation error when instrumented becomes true.
func (s *Store) SetLambdaInstrumented(ctx context.Context, functionARN string, instrumented bool) error {
	inventoryLog.Debug("set lambda instrumented", "arn", functionARN, "instrumented", instrumented)
	var query string
	if instrumented {
		// Instrumented: set the flag and clear any stale error. The in-flight phase is deliberately LEFT
		// intact so "Instrumenting" survives the async settle window (Lambda applies config asynchronously,
		// LastUpdateStatus=InProgress) and is observable in the WorkloadReport; a later reconcile that finds
		// the function converged clears the phase (ClearLambdaInstrumentationPhase), and the terminal
		// "Instrumented" stage is then DERIVED from this flag — so a drift (flag flips false) shows "not
		// instrumented" rather than a stale "Instrumented".
		query = `
			UPDATE connector_aws.lambda_functions
			SET metadata = ((COALESCE(metadata, '{}'::jsonb) - 'instrumentation_error')
			    || jsonb_build_object('instrumented', $3::boolean)),
			    last_seen_at = now()
			WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL`
	} else {
		// Uninstrumented: clear the live phase (the function is back to discovered/source-matched).
		query = `
			UPDATE connector_aws.lambda_functions
			SET metadata = (jsonb_set(COALESCE(metadata, '{}'::jsonb), '{instrumented}', to_jsonb($3::boolean), true) - 'instrumentation_phase'),
			    last_seen_at = now()
			WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL`
	}
	_, err := s.Pool().Exec(ctx, query, s.ConnectorName(), functionARN, instrumented)
	if err != nil {
		inventoryLog.Error("set lambda instrumented failed", "arn", functionARN, "error", err)
	}
	return err
}

// SetLambdaInstrumentationPhase records an in-flight instrumentation phase (Verifying/LayerReady/
// Instrumenting) so a WorkloadReport reflects live progress. Best-effort: the caller logs and continues on
// error, since a missed phase write only delays a status update, it does not affect instrumentation. The
// terminal Instrumented phase is set by SetLambdaInstrumented, not here.
func (s *Store) SetLambdaInstrumentationPhase(ctx context.Context, functionARN, phase string) error {
	inventoryLog.Debug("set lambda instrumentation phase", "arn", functionARN, "phase", phase)
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{instrumentation_phase}', to_jsonb($3::text), true),
		    last_seen_at = now()
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN, phase)
	if err != nil {
		inventoryLog.Error("set lambda instrumentation phase failed", "arn", functionARN, "error", err)
	}
	return err
}

// ClearLambdaInstrumentationPhase removes the in-flight phase once a function has fully converged (the
// reconcile confirmed it instrumented-as-desired and settled). After this the WorkloadReport derives its
// terminal "Instrumented" stage from the instrumented flag, so a later drift shows as not-instrumented
// rather than a stale phase.
func (s *Store) ClearLambdaInstrumentationPhase(ctx context.Context, functionARN string) error {
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET metadata = COALESCE(metadata, '{}'::jsonb) - 'instrumentation_phase', last_seen_at = now()
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN)
	if err != nil {
		inventoryLog.Error("clear lambda instrumentation phase failed", "arn", functionARN, "error", err)
	}
	return err
}

// SetLambdaInstrumentationError records the last instrumentation/rollback failure for workload reporting.
// It deliberately does NOT touch the `instrumented` flag: that flag must reflect the function's actual live
// state, set only by SetLambdaInstrumented (true after a successful instrument, false after a successful
// rollback). Forcing it false on error was wrong for a FAILED ROLLBACK — the layer is still attached, so
// the function is still instrumented — and made the layer look unused, so layer GC would delete a
// still-attached version and break the customer's next deploy referencing that ARN. A failed instrument
// leaves the flag as it was (nothing was applied), which is already correct.
func (s *Store) SetLambdaInstrumentationError(ctx context.Context, functionARN, errMsg string) error {
	inventoryLog.Debug("set lambda instrumentation error", "arn", functionARN)
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET metadata = jsonb_set(COALESCE(metadata, '{}'::jsonb), '{instrumentation_error}', to_jsonb($3::text), true),
		    last_seen_at = now()
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN, errMsg)
	if err != nil {
		inventoryLog.Error("set lambda instrumentation error failed", "arn", functionARN, "error", err)
	}
	return err
}

// ClearLambdaInstrumentationError removes a stored instrumentation failure after a successful converge.
func (s *Store) ClearLambdaInstrumentationError(ctx context.Context, functionARN string) error {
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET metadata = COALESCE(metadata, '{}'::jsonb) - 'instrumentation_error',
		    last_seen_at = now()
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN)
	return err
}

// UpsertECSTask inserts or refreshes a Fargate task inventory row. The instrumentation_error metadata key
// is preserved across the refresh (same jsonb merge as UpsertLambda): the reconciler writes a terminal
// unsupported error onto standalone-task rows, and a wholesale metadata replace would erase it every scan.
func (s *Store) UpsertECSTask(ctx context.Context, t ECSTask) error {
	inventoryLog.Debug("upsert ecs task", "region", t.Region, "arn", t.TaskARN)
	meta, err := metaJSON(t.Metadata)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.ecs_tasks
			(connector_name, region, cluster_arn, task_arn, service_name, task_definition, launch_type, last_status, desired_status, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb, now(), now())
		ON CONFLICT (connector_name, task_arn) DO UPDATE SET
			region = EXCLUDED.region, cluster_arn = EXCLUDED.cluster_arn, service_name = EXCLUDED.service_name,
			task_definition = EXCLUDED.task_definition, launch_type = EXCLUDED.launch_type,
			last_status = EXCLUDED.last_status, desired_status = EXCLUDED.desired_status,
			metadata = EXCLUDED.metadata
				|| CASE
					WHEN connector_aws.ecs_tasks.metadata ? 'instrumentation_error'
					THEN jsonb_build_object('instrumentation_error', connector_aws.ecs_tasks.metadata->'instrumentation_error')
					ELSE '{}'::jsonb END,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), t.Region, t.ClusterARN, t.TaskARN, t.ServiceName, t.TaskDefinition, t.LaunchType, t.LastStatus, t.DesiredStatus, meta)
	return err
}

func (s *Store) prune(ctx context.Context, table, region string, before time.Time) error {
	inventoryLog.Debug("prune workload inventory", "table", table, "region", region)
	_, err := s.Pool().Exec(ctx,
		"UPDATE connector_aws."+table+" SET deleted_at = now() WHERE connector_name = $1 AND region = $2 AND last_seen_at < $3 AND deleted_at IS NULL",
		s.ConnectorName(), region, before)
	return err
}

// PruneLambda soft-deletes stale Lambda rows for a region (scoped per type so the Lambda detector never
// prunes task rows the task detector owns, and vice-versa).
func (s *Store) PruneLambda(ctx context.Context, region string, before time.Time) error {
	return s.prune(ctx, "lambda_functions", region, before)
}

// PruneECSTasks soft-deletes stale Fargate-cluster task rows for a region.
func (s *Store) PruneECSTasks(ctx context.Context, region string, before time.Time) error {
	return s.prune(ctx, "ecs_tasks", region, before)
}

// SoftDeleteRetention is how long a soft-deleted inventory row is retained before the purge hard-deletes
// it. It gives operators/UI a window to see recently-removed resources while bounding table growth
// (ECS tasks especially churn on every deploy/scale).
const SoftDeleteRetention = 7 * 24 * time.Hour

// purgeDeleted hard-deletes rows soft-deleted before the cutoff, reclaiming space so churned inventory
// does not accumulate forever. Bounded by the deleted_at partial index.
func (s *Store) purgeDeleted(ctx context.Context, table string, before time.Time) error {
	tag, err := s.Pool().Exec(ctx,
		"DELETE FROM connector_aws."+table+" WHERE connector_name = $1 AND deleted_at IS NOT NULL AND deleted_at < $2",
		s.ConnectorName(), before)
	if err != nil {
		inventoryLog.Error("purge soft-deleted inventory failed", "table", table, "error", err)
		return err
	}
	if n := tag.RowsAffected(); n > 0 {
		inventoryLog.Info("purged soft-deleted inventory", "table", table, "rows", n)
	}
	return err
}

// PurgeDeletedLambdas hard-deletes Lambda rows soft-deleted before the cutoff.
func (s *Store) PurgeDeletedLambdas(ctx context.Context, before time.Time) error {
	return s.purgeDeleted(ctx, "lambda_functions", before)
}

// PurgeDeletedECSTasks hard-deletes ECS task rows soft-deleted before the cutoff.
func (s *Store) PurgeDeletedECSTasks(ctx context.Context, before time.Time) error {
	return s.purgeDeleted(ctx, "ecs_tasks", before)
}

// OwnerAccount returns the AWS account this connector name currently owns, and whether one is recorded.
func (s *Store) OwnerAccount(ctx context.Context) (string, bool, error) {
	var account string
	err := s.Pool().QueryRow(ctx,
		`SELECT account_id FROM connector_aws.connector_owner WHERE connector_name = $1`,
		s.ConnectorName()).Scan(&account)
	if isNoRows(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return account, true, nil
}

// SetOwnerAccount records the AWS account this connector name currently owns.
func (s *Store) SetOwnerAccount(ctx context.Context, accountID string) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.connector_owner (connector_name, account_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (connector_name) DO UPDATE SET account_id = EXCLUDED.account_id, updated_at = now()
	`, s.ConnectorName(), accountID)
	return err
}

// PurgeConnectorInventory hard-deletes all workload inventory + discovery cursors for this connector,
// used when the connector name is rebound to a different AWS account (the old account's inventory is
// meaningless for the new one). Sources/destinations are purged separately via the base PurgeIntent.
func (s *Store) PurgeConnectorInventory(ctx context.Context) error {
	for _, table := range []string{"lambda_functions", "ecs_tasks", "ecs_services", "discovery_state"} {
		if _, err := s.Pool().Exec(ctx, "DELETE FROM connector_aws."+table+" WHERE connector_name = $1", s.ConnectorName()); err != nil {
			return fmt.Errorf("purge %s: %w", table, err)
		}
	}
	return nil
}

// UpdateDiscoveryState records the last full-scan time for a region/kind cursor.
func (s *Store) UpdateDiscoveryState(ctx context.Context, region, kind string, scanAt time.Time) error {
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.discovery_state (connector_name, region, kind, cursor, last_full_scan_at)
		VALUES ($1,$2,$3,'{}'::jsonb,$4)
		ON CONFLICT (connector_name, region, kind) DO UPDATE SET last_full_scan_at = EXCLUDED.last_full_scan_at
	`, s.ConnectorName(), region, kind, scanAt)
	return err
}

// ListWorkloads returns the discovered WORKLOAD inventory (Lambda functions + Fargate services and
// standalone Fargate tasks) as raw DiscoveredWorkload objects; the base coordinator enriches them with
// source intent. Every query returns the same column shape:
//
//	name, region, external_id, language, version, last_seen_at, instrumented, error, phase
//
// Lambda encodes language+version in its runtime identifier, split in Go (splitRuntime); the ECS rows
// carry them as separate metadata keys.
func (s *Store) ListWorkloads(ctx context.Context) ([]api.DiscoveredWorkload, error) {
	inventoryLog.Debug("list workloads", "connector", s.ConnectorName())
	queries := []struct {
		kind        string
		lambdaSplit bool // language column is a Lambda runtime id — split into (language, version)
		sql         string
	}{
		{KindLambda, true, `SELECT function_name, region, function_arn, COALESCE(runtime, ''), '', last_seen_at,
			COALESCE(metadata->>'instrumented','false')='true',
			COALESCE(metadata->>'instrumentation_error', ''),
			COALESCE(metadata->>'instrumentation_phase', ''),
			COALESCE(architecture, ''), '', '', NULL
			FROM connector_aws.lambda_functions WHERE connector_name = $1 AND deleted_at IS NULL`},
		// Fargate SERVICES: one workload per (cluster, service) from the ecs_services inventory. The
		// external id — a source's stable identity — is cluster_arn || '/' || service_name (NOT a task ARN,
		// which recycles on every deploy/scale and would churn the intent row). Instrumentation state comes
		// from the metadata the instrumentor writes through.
		{KindECSTask, false, `SELECT ` + ecsServiceWorkloadName + `, region, cluster_arn || '/' || service_name,
			COALESCE(metadata->>'runtime_language',''), COALESCE(metadata->>'runtime_version',''), last_seen_at,
			COALESCE(metadata->>'instrumented','false')='true',
			COALESCE(metadata->>'instrumentation_error', ''),
			COALESCE(metadata->>'instrumentation_phase', ''),
			COALESCE(launch_type, ''), COALESCE(task_definition, ''), COALESCE(deployment_controller, ''),
			metadata->'containers'
			FROM connector_aws.ecs_services WHERE connector_name = $1 AND deleted_at IS NULL`},
		// STANDALONE Fargate tasks (no ECS service — RunTask/EventBridge): inherently ephemeral, so the task
		// ARN is the identity. They stay discoverable/selectable, but instrumentation reports a terminal
		// unsupported error (there is no UpdateService rollout path), carried on the task row's metadata.
		{KindECSTask, false, `SELECT ` + ecsStandaloneTaskWorkloadName + `, region, task_arn,
			'', '', last_seen_at, false,
			COALESCE(metadata->>'instrumentation_error', ''), '',
			'', '', '', NULL
			FROM connector_aws.ecs_tasks
			WHERE connector_name = $1 AND deleted_at IS NULL AND COALESCE(service_name, '') = ''`},
	}

	var out []api.DiscoveredWorkload
	for _, q := range queries {
		rows, err := s.Pool().Query(ctx, q.sql, s.ConnectorName())
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name, region, externalID, language, version, instrErr, instrPhase string
			// The last four columns carry the kind-specific facts each query supplies and the others
			// pad out: architecture/launch type, task definition, deployment controller, and the ECS
			// container inventory. They feed DiscoveredWorkload.Details, which the report narrows to
			// the wire type matching the kind.
			var archOrLaunchType, taskDefinition, deploymentController string
			var containersJSON []byte
			var lastSeen time.Time
			var instrumented bool
			if err := rows.Scan(&name, &region, &externalID, &language, &version, &lastSeen, &instrumented, &instrErr, &instrPhase,
				&archOrLaunchType, &taskDefinition, &deploymentController, &containersJSON); err != nil {
				rows.Close()
				return nil, err
			}
			runtime := language
			if q.lambdaSplit {
				language, version = splitRuntime(language)
			}
			out = append(out, api.DiscoveredWorkload{
				ServiceName:          name,
				Kind:                 q.kind,
				Region:               region,
				ExternalID:           externalID,
				RuntimeLanguage:      language,
				RuntimeVersion:       version,
				Instrumented:         instrumented,
				InstrumentationType:  q.kind,
				InstrumentationError: instrErr,
				InstrumentationPhase: instrPhase,
				Details: workloadDetails(q.kind, externalID, runtime,
					archOrLaunchType, taskDefinition, deploymentController, containersJSON),
				LastSeenAt: lastSeen,
			})
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	inventoryLog.Debug("list workloads complete", "connector", s.ConnectorName(), "count", len(out))
	return out, nil
}

// workloadDetails assembles the kind-specific detail block for one inventory row. It reports only
// what the store actually holds: a Fargate service's containers come from the inventory the
// instrumentor wrote, and a standalone task — which has no service, no revision and no
// instrumentation path — gets nothing rather than a block of empty fields.
func workloadDetails(kind, externalID, lambdaRuntime, archOrLaunchType, taskDefinition, deploymentController string, containersJSON []byte) *api.WorkloadDetails {
	switch kind {
	case KindLambda:
		return &api.WorkloadDetails{
			Kind:         kind,
			FunctionArn:  externalID,
			Runtime:      lambdaRuntime,
			Architecture: archOrLaunchType,
		}
	case KindECSTask:
		// Standalone tasks share this kind but are keyed by task ARN, so they carry none of the
		// service columns; without them there is nothing worth reporting.
		if taskDefinition == "" && archOrLaunchType == "" && len(containersJSON) == 0 {
			return nil
		}
		return &api.WorkloadDetails{
			Kind:                 kind,
			ClusterArn:           clusterARNFromExternalID(externalID),
			TaskDefinition:       taskDefinition,
			LaunchType:           archOrLaunchType,
			DeploymentController: deploymentController,
			Containers:           decodeECSContainers(containersJSON),
		}
	default:
		return nil
	}
}

// clusterARNFromExternalID recovers the cluster ARN from an ECS service external id
// (cluster_arn || '/' || service_name). It splits on the LAST separator, not the first: a cluster
// ARN is itself "arn:...:cluster/<name>", so cutting at the first slash would return a truncated ARN.
func clusterARNFromExternalID(externalID string) string {
	if i := strings.LastIndex(externalID, "/"); i >= 0 {
		return externalID[:i]
	}
	return externalID
}

// decodeECSContainers reads the container inventory the instrumentor persisted. A row written by an
// older connector has no inventory, and a malformed one is not worth failing a whole listing over —
// both degrade to no container layer, which the report renders as a workload without containers.
func decodeECSContainers(raw []byte) []api.WorkloadContainer {
	if len(raw) == 0 {
		return nil
	}
	var stored []ECSContainer
	if err := json.Unmarshal(raw, &stored); err != nil {
		inventoryLog.Warn("ignoring unreadable ECS container inventory", "error", err)
		return nil
	}
	out := make([]api.WorkloadContainer, 0, len(stored))
	for _, c := range stored {
		out = append(out, api.WorkloadContainer{
			Name:             c.Name,
			Image:            c.Image,
			RuntimeLanguage:  c.RuntimeLanguage,
			RuntimeVersion:   c.RuntimeVersion,
			OverrideLanguage: c.OverrideLanguage,
			OverrideVersion:  c.OverrideVersion,
			Instrumented:     c.Instrumented,
			Eligible:         c.Eligible,
			ExcludedReason:   c.ExcludedReason,
			Distro:           c.Distro,
			Essential:        c.Essential,
			OdigosManaged:    c.OdigosManaged,
		})
	}
	return out
}

// splitRuntime splits an AWS Lambda runtime identifier into (language, version) at the first digit:
// "java17" -> ("java","17"), "python3.11" -> ("python","3.11"), "nodejs20.x" -> ("nodejs","20.x"),
// "provided.al2" -> ("provided.al","2"). An empty or version-less runtime yields an empty version.
func splitRuntime(runtime string) (language, version string) {
	for i := 0; i < len(runtime); i++ {
		if runtime[i] >= '0' && runtime[i] <= '9' {
			return runtime[:i], runtime[i:]
		}
	}
	return runtime, ""
}

// LookupDiscoveredWorkload finds a workload inventory row matching a prospective source name/kind/region.
// region pins the exact regional row when non-empty; empty matches by name+kind alone (deterministic).
func (s *Store) LookupDiscoveredWorkload(ctx context.Context, serviceName, kind, region string) (externalID, foundRegion string, found bool, err error) {
	inventoryLog.Debug("lookup workload", "connector", s.ConnectorName(), "service", serviceName, "kind", kind, "region", region)
	switch kind {
	case KindLambda:
		err = s.Pool().QueryRow(ctx, `
			SELECT function_arn, region FROM connector_aws.lambda_functions
			WHERE connector_name = $1 AND function_name = $2 AND deleted_at IS NULL
			  AND ($3 = '' OR region = $3)
			ORDER BY region LIMIT 1
		`, s.ConnectorName(), serviceName, region).Scan(&externalID, &foundRegion)
	case KindECSTask:
		// Match by the SAME composed display name ListWorkloads reports (serviceName is that handle) and
		// return the SAME stable external id so a created source's external_resource_id matches the
		// discovered workload. Services (from ecs_services) are tried first; a standalone task (from
		// ecs_tasks, task ARN identity) is the fallback.
		err = s.Pool().QueryRow(ctx, `
			SELECT cluster_arn || '/' || service_name, region
			FROM connector_aws.ecs_services
			WHERE connector_name = $1 AND (`+ecsServiceWorkloadName+`) = $2 AND deleted_at IS NULL
			  AND ($3 = '' OR region = $3)
			ORDER BY region LIMIT 1
		`, s.ConnectorName(), serviceName, region).Scan(&externalID, &foundRegion)
		if isNoRows(err) {
			err = s.Pool().QueryRow(ctx, `
				SELECT task_arn, region
				FROM connector_aws.ecs_tasks
				WHERE connector_name = $1 AND (`+ecsStandaloneTaskWorkloadName+`) = $2 AND deleted_at IS NULL
				  AND COALESCE(service_name, '') = ''
				  AND ($3 = '' OR region = $3)
				ORDER BY region LIMIT 1
			`, s.ConnectorName(), serviceName, region).Scan(&externalID, &foundRegion)
		}
	default:
		return "", "", false, fmt.Errorf("unsupported workload kind %q", kind)
	}
	if err != nil {
		if isNoRows(err) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return externalID, foundRegion, true, nil
}
