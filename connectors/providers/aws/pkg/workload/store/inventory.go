package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/types"
)

// Workload kinds — directly-instrumentable resources. Kind strings match the spec type names so the
// coordinator resolves a workload's instrumentation availability from the spec.
const (
	KindLambda    = types.Lambda
	KindAgentCore = types.AgentCore
	KindECSTask   = types.FargateTask
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

// EnvOdigosIaC is the base-owned attribution marker an Odigos IaC artifact writes (see api.EnvOdigosIaC for
// why it is a second variable rather than a value of the first). Aliased here so the detector, observer and
// instrumentor reach both markers through one package, as they already do for EnvOdigosManaged.
//
// It proves the instrumentation came from Odigos tooling; it does NOT prove ownership, because it carries no
// rollback snapshot. Nothing in the mutating path may key off it — see ownership().
const EnvOdigosIaC = api.EnvOdigosIaC

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

// OdigosInstrumented reports whether a Lambda function's environment shows instrumentation applied by
// Odigos: the ownership marker this connector writes when it instruments the function itself, the IaC marker
// an Odigos artifact writes when the customer applies it from their own deployment definition, or — for
// functions instrumented before either marker existed — the odigos-namespaced collector vars. It
// deliberately does NOT treat a generic OpenTelemetry/ADOT layer or exec wrapper as ours: a customer's
// pre-existing OTel setup is not Odigos instrumentation and must never be flagged as ours.
//
// This is ATTRIBUTION, not ownership, and the two markers are not equivalent past this line. Discovery runs
// with no management mode in hand, so a predicate that accepted only one of them would report every
// manual-mode function as untouched — which is the bug this widening fixes. What must not widen with it is
// the mutating path: rollback resolves ownership separately (see the instrumentor's ownership()), where the
// IaC marker does not qualify because it carries no snapshot of what to restore.
func OdigosInstrumented(env map[string]string) bool {
	return OdigosInstrumentationOrigin(env) != ""
}

// OdigosInstrumentationOrigin distinguishes an IaC marker from connector-owned instrumentation. The
// report uses this to surface an untracked IaC-applied function without flagging connector rollback in
// progress as a permanent source mismatch.
func OdigosInstrumentationOrigin(env map[string]string) string {
	if env == nil {
		return ""
	}
	if _, ok := env[EnvOdigosManaged]; ok {
		return "connector"
	}
	if _, ok := env[EnvOdigosIaC]; ok {
		return "external"
	}
	if env[envCollectorEndpoint] != "" || env[envCollectorInsecure] != "" {
		return "connector"
	}
	return ""
}

// OdigosLayerNamePrefix is the name prefix of every Lambda layer this connector publishes. It is the
// signature that recognizes OUR layers in an account (as opposed to a customer's, or a public
// OpenTelemetry/ADOT layer), so the connector only ever reuses, attaches, deletes or attributes a layer it
// owns. It lives here, next to EnvOdigosManaged, as the neutral ground the detector, the observer and the
// instrumentor all need — the instrumentor builds names from it, and the read-only paths recognize them.
const OdigosLayerNamePrefix = "odigos-otel-"

// IsOdigosLayerName reports whether a Lambda layer name is one this connector published.
func IsOdigosLayerName(name string) bool {
	return strings.HasPrefix(name, OdigosLayerNamePrefix)
}

// ParseLayerVersionARN extracts the layer name and version from a layer-version ARN of the form
// arn:aws:lambda:<region>:<account>:layer:<name>:<version>.
func ParseLayerVersionARN(arn string) (name string, version int64, ok bool) {
	parts := strings.Split(arn, ":")
	if len(parts) < 2 {
		return "", 0, false
	}
	v, err := strconv.ParseInt(parts[len(parts)-1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return parts[len(parts)-2], v, true
}

// OdigosLayerARN returns the first Odigos-published layer ARN in an observed layer set, or "" when the
// function carries none. Name-signature based, so a customer's or a public OTel/ADOT layer is never
// reported as Odigos instrumentation.
func OdigosLayerARN(layers []string) string {
	for _, arn := range layers {
		if name, _, ok := ParseLayerVersionARN(arn); ok && IsOdigosLayerName(name) {
			return arn
		}
	}
	return ""
}

// InstrumentationType returns the connector-spec instrumentation type for a discovered task.
func (t ECSTask) InstrumentationType() string {
	return KindECSTask
}

// SupportedKinds returns the workload kinds this connector publishes.
func SupportedKinds() []string {
	return []string{KindLambda, KindECSTask, KindAgentCore}
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
	// persisted in metadata so WorkloadReports and the instrumentor's reconcile see the same truth. It
	// answers only "is an Odigos ownership marker present" — see Observation for whether the instrumentation
	// behind that marker is actually complete.
	Instrumented bool
	// InstrumentationOrigin is "external" for ODIGOS_IAC and "connector" for connector-owned markers.
	InstrumentationOrigin string
	// LiveServiceName is the OTel service name the function's Odigos instrumentation reports, read from its
	// environment at discovery time; empty without a marker. See api.DiscoveredWorkload.LiveServiceName.
	LiveServiceName string
	// Layers is the function's attached layer-version ARN set, as ListFunctions returns it. Persisted in the
	// (until now unused) layers column: it is what makes "the marker is there but the layer was detached"
	// observable, and it fills the LayerArn a Lambda workload report has always declared and never carried.
	Layers []string
	// State / LastUpdateStatus are the function's lifecycle fields. Lambda applies configuration changes
	// asynchronously, so a change a customer just applied reports State=Pending or
	// LastUpdateStatus=InProgress while it settles — which looks identical to a half-applied
	// instrumentation. Captured so a settling change is reported as such instead of as drift.
	State            string
	LastUpdateStatus string
	// VPCID, SubnetIDs and SecurityGroupIDs are the function's VpcConfig, as ListFunctions returns it.
	// All three are EMPTY for a function with no VpcConfig: it runs in an
	// AWS-managed VPC, cannot reach an internal endpoint, and keeps exporting straight to the
	// destination. Persisted because this is what groups a Lambda onto a network for managed-Collector
	// placement — and unlike Fargate, Lambda hands over the VPC id itself, so no subnet lookup is
	// needed. The security groups are what authorizes Collector ingress from.
	VPCID            string
	SubnetIDs        []string
	SecurityGroupIDs []string
	// RoutingVia records the currently selected export path (direct or collector). It is persisted so a
	// reconcile can distinguish the route it last applied from a newly resolved route and roll back safely.
	RoutingVia string
	// RoutingNote is the last non-fatal reason the resolver selected (or retained) a route.
	RoutingNote *api.WorkloadRoutingNote
	// CutoverFailure is why the last attempt to move this function onto its regional Collector did not
	// take. Zero when there is none.
	CutoverFailure CutoverFailure
	// Observation is the read-only conformance verdict for this function, written by the observer rather than
	// by discovery. Zero until a pass has run.
	Observation LambdaObservation
	Metadata    map[string]any
}

// LambdaObservation is the persisted result of one read-only conformance observation of a function: does
// what is actually on the function match the instrumentation Odigos wants for it. It is stored per function
// so a WorkloadReport (served on the GraphQL read path, which must make no AWS calls) can report the verdict
// without a live read.
type LambdaObservation struct {
	// Conformance is the verdict. ConformanceUnknown means never observed, or the last read failed.
	Conformance api.Conformance
	// Missing names the components that are absent or stale, in the provider-neutral vocabulary the base
	// renders (see workloadreport.componentLabels). Empty unless Conformance is Partial.
	Missing []string
	// Since is when this verdict was first observed, so a report can say how long a drift has stood. The
	// observer carries it forward unchanged while the verdict holds, and stamps a new one when it flips.
	Since time.Time
	// ObservedPrint / DesiredPrint are the fingerprints of the live and wanted instrumentation, stored in the
	// env_hash / config_hash columns. They are the cheap equality test between passes: unchanged prints mean
	// nothing about this function moved, so the observer neither rewrites the row nor disturbs Since — which
	// is what keeps the since-clock meaningful across passes rather than resetting on every cycle.
	ObservedPrint string
	DesiredPrint  string
	// Error records why the last observation could not be completed. It is not drift: the base keeps
	// reporting the last confirmed verdict and raises a transient issue.
	Error string
}

// The two AWS lifecycle values that mean "a configuration change to a function is still being applied".
// ListWorkloads derives the report's Settling flag from them in SQL; they are declared as plain strings rather
// than pulling the Lambda SDK into the store, which persists plain data.
//
// There is deliberately no Go predicate over them here. The observer needs the same question answered about its
// own LIVE read, where the SDK enums are available and the answer is seconds fresh — see
// instrumentor.lifecycleSettling. A second string-based copy in the store would be a duplicate definition of
// "settling" with no production caller.
const (
	lambdaStatePending     = "Pending"
	lambdaUpdateInProgress = "InProgress"
)

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

// lambdaDiscoveryMetadataKeys are the metadata keys DISCOVERY owns and refreshes on every scan. Every other
// key on the row belongs to a different writer — the instrumentor's phase/error, the observer's conformance
// verdict — and must survive a discovery cycle.
//
// UpsertLambda expresses that as "drop the keys I own, then merge mine in", which is the inverse of the
// preserve-list it used to carry: that list named the two keys that existed when it was written, so every key
// added since (the whole observation record) would have been silently wiped on the next scan. Excluding what
// discovery owns keeps a new key by any other writer preserved by default, which is the safe direction to be
// wrong in.
var lambdaDiscoveryMetadataKeys = []string{
	"instrumented", "instrumentation_origin", metaLiveServiceName, "odigos_tags", "handler", "memorySize", "timeout", "version", "state", "last_update_status",
	// The VpcConfig keys have to be in this list, not merely written: detaching a function from its VPC
	// removes VpcConfig entirely, and a stale vpc_id left behind by an overlay would keep grouping the
	// function onto a network it no longer runs on — pointing it at a Collector it can no longer reach.
	metaLambdaVPCID, metaLambdaSubnetIDs, metaLambdaSecurityGroupIDs,
}

// metaLiveServiceName holds api.DiscoveredWorkload.LiveServiceName: written by discovery for Lambda and by the
// Fargate observer for ECS services, the two places that read the workload's live environment.
const metaLiveServiceName = "live_service_name"

// The function's VpcConfig, written by discovery on every scan — see LambdaFunction.VPCID.
const (
	metaLambdaVPCID            = "vpc_id"
	metaLambdaSubnetIDs        = "subnet_ids"
	metaLambdaSecurityGroupIDs = "security_group_ids"
)

// UpsertLambda inserts or refreshes a Lambda inventory row, clearing any soft-delete marker.
func (s *Store) UpsertLambda(ctx context.Context, fn LambdaFunction) error {
	inventoryLog.Debug("upsert lambda", "region", fn.Region, "name", fn.FunctionName)
	if fn.Metadata == nil {
		fn.Metadata = map[string]any{}
	}
	fn.Metadata["instrumented"] = fn.Instrumented
	fn.Metadata["instrumentation_origin"] = fn.InstrumentationOrigin
	fn.Metadata[metaLiveServiceName] = fn.LiveServiceName
	fn.Metadata["state"] = fn.State
	fn.Metadata["last_update_status"] = fn.LastUpdateStatus
	fn.Metadata[metaLambdaVPCID] = fn.VPCID
	fn.Metadata[metaLambdaSubnetIDs] = nonNilStrings(fn.SubnetIDs)
	fn.Metadata[metaLambdaSecurityGroupIDs] = nonNilStrings(fn.SecurityGroupIDs)
	meta, err := metaJSON(fn.Metadata)
	if err != nil {
		return err
	}
	layers, err := json.Marshal(nonNilStrings(fn.Layers))
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO connector_aws.lambda_functions
			(connector_name, region, function_arn, function_name, runtime, architecture, role_arn, tracing_mode, layers, metadata, first_seen_at, last_seen_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10::jsonb, now(), now())
		ON CONFLICT (connector_name, function_arn) DO UPDATE SET
			region = EXCLUDED.region, function_name = EXCLUDED.function_name, runtime = EXCLUDED.runtime,
			architecture = EXCLUDED.architecture, role_arn = EXCLUDED.role_arn, tracing_mode = EXCLUDED.tracing_mode,
			layers = EXCLUDED.layers,
			metadata = (COALESCE(connector_aws.lambda_functions.metadata, '{}'::jsonb) - $11::text[]) || EXCLUDED.metadata,
			last_seen_at = now(), deleted_at = NULL
	`, s.ConnectorName(), fn.Region, fn.FunctionARN, fn.FunctionName, fn.Runtime, fn.Architecture, fn.RoleARN, fn.TracingMode,
		string(layers), meta, lambdaDiscoveryMetadataKeys)
	return err
}

// SetLambdaObservation records one read-only conformance observation. It writes the two fingerprints to the
// env_hash / config_hash columns and the verdict to metadata, and touches nothing else — in particular not
// the `instrumented` flag, which discovery and the instrumentor own: the observer's job is to say whether the
// instrumentation is CORRECT, never to decide whether it exists.
//
// last_seen_at is deliberately left alone too. It is discovery's liveness signal and the prune cutoff, so
// refreshing it here would let an observation keep a function that no longer exists alive in the inventory.
func (s *Store) SetLambdaObservation(ctx context.Context, functionARN string, obs LambdaObservation) error {
	inventoryLog.Debug("set lambda observation", "arn", functionARN, "conformance", obs.Conformance)
	missing, err := json.Marshal(nonNilStrings(obs.Missing))
	if err != nil {
		return err
	}
	var since any
	if !obs.Since.IsZero() {
		since = obs.Since.UTC().Format(time.RFC3339)
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET env_hash = $3, config_hash = $4,
		    metadata = COALESCE(metadata, '{}'::jsonb) || jsonb_build_object(
		        'conformance', $5::text,
		        'conformance_missing', $6::jsonb,
		        'conformance_since', $7::text,
		        'observation_error', $8::text)
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN, obs.ObservedPrint, obs.DesiredPrint, string(obs.Conformance), string(missing), since, obs.Error)
	if err != nil {
		inventoryLog.Error("set lambda observation failed", "arn", functionARN, "error", err)
	}
	return err
}

// lambdaObservationCols selects the observation record in a fixed order, shared by every read so the scan
// stays in one place.
const lambdaObservationCols = `COALESCE(env_hash, ''), COALESCE(config_hash, ''), COALESCE(metadata->>'conformance', ''), metadata->'conformance_missing', metadata->>'conformance_since', COALESCE(metadata->>'observation_error', '')`

// noObservationCols pads ListWorkloads' fixed column shape for a kind with no observer: an Unknown verdict,
// no components, no timestamp, no error, not settling, no layers. It is not "conformant by default" — Unknown
// is the honest verdict for state nothing has looked at, and the base reports it as such.
//
// Standalone ECS tasks are the remaining user: they are discovered for coverage but cannot be instrumented at
// all (no UpdateService rollout path), so there is no conformance question to answer about them. The layers
// column stays NULL for every non-Lambda kind.
const noObservationCols = `'', NULL, NULL, '', false, NULL`

const lambdaSelectCols = `region, function_arn, function_name, COALESCE(runtime, ''), COALESCE(architecture, ''), COALESCE(role_arn, ''), COALESCE(tracing_mode, ''), COALESCE(metadata->>'instrumented', 'false') = 'true', layers, COALESCE(metadata->>'state', ''), COALESCE(metadata->>'last_update_status', ''), COALESCE(metadata->>'` + metaLambdaVPCID + `', ''), metadata->'` + metaLambdaSubnetIDs + `', metadata->'` + metaLambdaSecurityGroupIDs + `', COALESCE(metadata->>'` + metaRoutingVia + `',''), metadata->'` + metaRoutingNote + `', metadata->'` + metaCutoverFailure + `', ` + lambdaObservationCols

func scanLambda(row interface{ Scan(...any) error }) (LambdaFunction, error) {
	var fn LambdaFunction
	var layersJSON, missingJSON, subnetsJSON, groupsJSON, routingNoteJSON, cutoverJSON []byte
	var since *string
	err := row.Scan(&fn.Region, &fn.FunctionARN, &fn.FunctionName, &fn.Runtime, &fn.Architecture, &fn.RoleARN, &fn.TracingMode,
		&fn.Instrumented, &layersJSON, &fn.State, &fn.LastUpdateStatus,
		&fn.VPCID, &subnetsJSON, &groupsJSON,
		&fn.RoutingVia, &routingNoteJSON, &cutoverJSON,
		&fn.Observation.ObservedPrint, &fn.Observation.DesiredPrint, &conformanceTarget{&fn.Observation.Conformance}, &missingJSON, &since, &fn.Observation.Error)
	if err != nil {
		return fn, err
	}
	fn.Layers = decodeStringSlice(layersJSON, "layers")
	fn.SubnetIDs = decodeStringSlice(subnetsJSON, metaLambdaSubnetIDs)
	fn.SecurityGroupIDs = decodeStringSlice(groupsJSON, metaLambdaSecurityGroupIDs)
	fn.RoutingNote = decodeRoutingNote(routingNoteJSON)
	fn.CutoverFailure = decodeCutoverFailure(cutoverJSON)
	fn.Observation.Missing = decodeStringSlice(missingJSON, "conformance_missing")
	fn.Observation.Since = parseObservedAt(since)
	return fn, nil
}

// conformanceTarget lets a TEXT column scan straight into the typed api.Conformance without a separate
// string variable at every call site.
type conformanceTarget struct{ dst *api.Conformance }

func (t *conformanceTarget) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		*t.dst = api.ConformanceUnknown
	case string:
		*t.dst = api.Conformance(v)
	case []byte:
		*t.dst = api.Conformance(v)
	default:
		return fmt.Errorf("unsupported conformance value %T", src)
	}
	return nil
}

// decodeStringSlice reads a JSONB string array. An unreadable value degrades to none rather than failing a
// whole listing: a garbled layer set or component list is worth a log line, not a connector that cannot
// report any workload.
func decodeStringSlice(raw []byte, field string) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		inventoryLog.Warn("ignoring unreadable inventory field", "field", field, "error", err)
		return nil
	}
	return out
}

// parseObservedAt reads a stored RFC3339 timestamp. An absent or unparsable value yields the zero time,
// which the report renders as "unknown since" rather than as 1970.
func parseObservedAt(s *string) time.Time {
	if s == nil || *s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, *s)
	if err != nil {
		inventoryLog.Warn("ignoring unparsable observation timestamp", "value", *s, "error", err)
		return time.Time{}
	}
	return t
}

// nonNilStrings normalizes a nil slice to an empty one so it marshals as [] rather than null, keeping the
// JSONB columns' declared "not null, default []" shape.
func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
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

// SetLambdaRouting records the selected export path and its optional explanatory note without replacing
// metadata owned by discovery, observation, or instrumentation. Empty via and nil note clear their
// respective keys, making route convergence and rollback idempotent.
func (s *Store) SetLambdaRouting(ctx context.Context, functionARN, via string, note *api.WorkloadRoutingNote) error {
	inventoryLog.Debug("set lambda routing", "arn", functionARN, "via", via)
	patch := map[string]any{}
	deleteKeys := make([]string, 0, 2)
	if via == "" {
		deleteKeys = append(deleteKeys, metaRoutingVia)
	} else {
		patch[metaRoutingVia] = via
	}
	if note == nil {
		deleteKeys = append(deleteKeys, metaRoutingNote)
	} else {
		patch[metaRoutingNote] = storedRoutingNote{Code: note.Code, Message: note.Message, Transient: note.Transient}
	}
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET metadata = (COALESCE(metadata, '{}'::jsonb) || $3::jsonb) - $4::text[]
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN, string(b), deleteKeys)
	if err != nil {
		inventoryLog.Error("set lambda routing failed", "arn", functionARN, "error", err)
	}
	return err
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

// SetLambdaCutoverFailure records why the last attempt to move a function onto its regional Collector did not
// take — see CutoverFailure. Like SetLambdaRouting it leaves last_seen_at alone: that is discovery's liveness
// signal.
func (s *Store) SetLambdaCutoverFailure(ctx context.Context, functionARN string, f CutoverFailure) error {
	b, err := json.Marshal(map[string]any{metaCutoverFailure: storeCutoverFailure(f)})
	if err != nil {
		return err
	}
	_, err = s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET metadata = COALESCE(metadata, '{}'::jsonb) || $3::jsonb
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN, string(b))
	return err
}

// ClearLambdaCutoverFailure forgets a function's recorded cutover failure.
func (s *Store) ClearLambdaCutoverFailure(ctx context.Context, functionARN string) error {
	_, err := s.Pool().Exec(ctx, `
		UPDATE connector_aws.lambda_functions
		SET metadata = COALESCE(metadata, '{}'::jsonb) - $3::text
		WHERE connector_name = $1 AND function_arn = $2 AND deleted_at IS NULL
	`, s.ConnectorName(), functionARN, metaCutoverFailure)
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
	for _, table := range []string{"lambda_functions", "ecs_tasks", "ecs_services", "discovery_state", "agentcore_runtimes", "agentcore_instrumentation_snapshots"} {
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
//	name, region, external_id, language, version, last_seen_at, instrumented, error, phase, ...,
//	routing note, origin, waiting_for
//
// Lambda encodes language+version in its runtime identifier, split in Go (splitRuntime); the ECS rows
// carry them as separate metadata keys.
func (s *Store) ListWorkloads(ctx context.Context) ([]api.DiscoveredWorkload, error) {
	inventoryLog.Debug("list workloads", "connector", s.ConnectorName())
	var out []api.DiscoveredWorkload
	for _, q := range workloadQueries() {
		rows, err := s.Pool().Query(ctx, q.sql, s.ConnectorName())
		if err != nil {
			return nil, err
		}
		if err := scanWorkloadRows(rows, q, &out); err != nil {
			return nil, err
		}
	}
	inventoryLog.Debug("list workloads complete", "connector", s.ConnectorName(), "count", len(out))
	return out, nil
}

// workloadQuery is one kind's inventory listing. Every query must select the SAME column shape, because one
// Scan reads all of them (see scanWorkloadRows) — TestListWorkloadsQueriesShareColumnShape asserts it against
// the assembled SQL, since a mismatch is a runtime failure of the whole report path with no compile-time signal.
type workloadQuery struct {
	kind        string
	lambdaSplit bool // language column is a Lambda runtime id — split into (language, version)
	sql         string
}

// workloadQueries returns the per-kind listings. It is a function rather than an inline slice so a test can read
// the fully-assembled SQL — the column list is built from shared constants, so reading the source text instead
// would not see through the concatenation.
func workloadQueries() []workloadQuery {
	return []workloadQuery{
		{KindLambda, true, `SELECT function_name, region, function_arn, COALESCE(runtime, ''), '', last_seen_at,
			COALESCE(metadata->>'instrumented','false')='true',
			COALESCE(metadata->>'instrumentation_error', ''),
			COALESCE(metadata->>'instrumentation_phase', ''),
			COALESCE(architecture, ''), '', '', NULL,
			COALESCE(metadata->>'conformance', ''), metadata->'conformance_missing', metadata->>'conformance_since',
			COALESCE(metadata->>'observation_error', ''),
			COALESCE(metadata->>'state','') = '` + lambdaStatePending + `' OR COALESCE(metadata->>'last_update_status','') = '` + lambdaUpdateInProgress + `',
			layers, metadata->'` + metaRoutingNote + `', COALESCE(metadata->>'instrumentation_origin', ''),
			COALESCE(metadata->>'` + metaLiveServiceName + `', ''), ''
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
			metadata->'containers',
			` + ecsObservationCols + `, NULL, metadata->'` + metaRoutingNote + `', '',
			COALESCE(metadata->>'` + metaLiveServiceName + `', ''), ''
			FROM connector_aws.ecs_services WHERE connector_name = $1 AND deleted_at IS NULL`},
		// STANDALONE Fargate tasks (no ECS service — RunTask/EventBridge): inherently ephemeral, so the task
		// ARN is the identity. They stay discoverable/selectable, but instrumentation reports a terminal
		// unsupported error (there is no UpdateService rollout path), carried on the task row's metadata.
		{KindECSTask, false, `SELECT ` + ecsStandaloneTaskWorkloadName + `, region, task_arn,
			'', '', last_seen_at, false,
			COALESCE(metadata->>'instrumentation_error', ''), '',
			'', '', '', NULL,
			` + noObservationCols + `, NULL, '', '', ''
			FROM connector_aws.ecs_tasks
			WHERE connector_name = $1 AND deleted_at IS NULL AND COALESCE(service_name, '') = ''`},
		{KindAgentCore, false, `SELECT runtime_name, region, runtime_arn, COALESCE(inventory->>'language',''),
			COALESCE(inventory->>'runtimeVersion',''), last_seen_at,
			COALESCE(inventory->>'origin','') != '',
			COALESCE(metadata->>'instrumentation_error',''), COALESCE(metadata->>'instrumentation_phase',''),
			'', CASE WHEN inventory->>'status'='READY' THEN COALESCE(inventory->>'version','') ELSE '' END, '', NULL,
			COALESCE(metadata->>'conformance',''), metadata->'conformance_missing', metadata->>'conformance_since',
			COALESCE(metadata->>'observation_error',''), (inventory->>'status' IN ('CREATING','UPDATING') OR COALESCE(metadata->>'settling','false')='true'),
			NULL, metadata->'routing_note', COALESCE(inventory->>'origin',''), '', COALESCE(metadata->>'waiting_for','')
			FROM connector_aws.agentcore_runtimes WHERE connector_name=$1 AND deleted_at IS NULL`},
	}
}

// scanWorkloadRows reads one query's rows into out. Split out of ListWorkloads so all three queries provably
// share a single scan — the reason their column shapes must match.
func scanWorkloadRows(rows pgx.Rows, q workloadQuery, out *[]api.DiscoveredWorkload) error {
	defer rows.Close()
	{
		for rows.Next() {
			var name, region, externalID, language, version, instrErr, instrPhase, instrOrigin, waitingFor, liveServiceName string
			// The next four columns carry the kind-specific facts each query supplies and the others
			// pad out: architecture/launch type, task definition, deployment controller, and the ECS
			// container inventory. They feed DiscoveredWorkload.Details, which the report narrows to
			// the wire type matching the kind.
			var archOrLaunchType, taskDefinition, deploymentController string
			var containersJSON []byte
			// The trailing columns are the read-only observation record. Only Lambda has an observer today,
			// so the other queries select noObservationCols and report an Unknown verdict — which is the
			// truth for a type nothing has observed, and is distinct from "observed and found conformant".
			var conformance api.Conformance
			var missingJSON, layersJSON, routingNoteJSON []byte
			var conformanceSince *string
			var observationErr string
			var settling bool
			var lastSeen time.Time
			var instrumented bool
			if err := rows.Scan(&name, &region, &externalID, &language, &version, &lastSeen, &instrumented, &instrErr, &instrPhase,
				&archOrLaunchType, &taskDefinition, &deploymentController, &containersJSON,
				&conformanceTarget{&conformance}, &missingJSON, &conformanceSince, &observationErr, &settling, &layersJSON,
				&routingNoteJSON, &instrOrigin, &liveServiceName, &waitingFor); err != nil {
				return err
			}
			runtime := language
			if q.lambdaSplit {
				language, version = splitRuntime(language)
			}
			*out = append(*out, api.DiscoveredWorkload{
				ServiceName:           name,
				Kind:                  q.kind,
				Region:                region,
				ExternalID:            externalID,
				RuntimeLanguage:       language,
				RuntimeVersion:        version,
				Instrumented:          instrumented,
				LiveServiceName:       liveServiceName,
				InstrumentationOrigin: instrOrigin,
				InstrumentationType:   q.kind,
				InstrumentationError:  instrErr,
				InstrumentationPhase:  instrPhase,
				Conformance:           conformance,
				MissingComponents:     decodeStringSlice(missingJSON, "conformance_missing"),
				ConformanceSince:      parseObservedAt(conformanceSince),
				Settling:              settling,
				ObservationError:      observationErr,
				RoutingNote:           decodeRoutingNote(routingNoteJSON),
				WaitingFor:            waitingFor,
				Details: workloadDetails(q.kind, externalID, runtime,
					archOrLaunchType, taskDefinition, deploymentController, containersJSON,
					decodeStringSlice(layersJSON, "layers")),
				LastSeenAt: lastSeen,
			})
		}
	}
	return rows.Err()
}

// workloadDetails assembles the kind-specific detail block for one inventory row. It reports only
// what the store actually holds: a Fargate service's containers come from the inventory the
// instrumentor wrote, and a standalone task — which has no service, no revision and no
// instrumentation path — gets nothing rather than a block of empty fields.
func workloadDetails(kind, externalID, lambdaRuntime, archOrLaunchType, taskDefinition, deploymentController string, containersJSON []byte, layers []string) *api.WorkloadDetails {
	switch kind {
	case KindAgentCore:
		return &api.WorkloadDetails{Kind: kind, Service: externalID, LatestReadyRevision: taskDefinition}
	case KindLambda:
		return &api.WorkloadDetails{
			Kind:         kind,
			FunctionArn:  externalID,
			Runtime:      lambdaRuntime,
			Architecture: archOrLaunchType,
			// The Odigos layer actually attached to the function, from the observed layer set. This field has
			// been on the Lambda workload report since it was written and never carried a value, because
			// nothing captured the function's layers; a customer asking "which layer version am I on" — the
			// first question in manual mode, where they pinned it themselves — had no answer.
			LayerArn: OdigosLayerARN(layers),
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
	case KindAgentCore:
		err = s.Pool().QueryRow(ctx, `SELECT runtime_arn,region FROM connector_aws.agentcore_runtimes
		WHERE connector_name=$1 AND runtime_name=$2 AND deleted_at IS NULL AND ($3='' OR region=$3)
		ORDER BY region LIMIT 1`, s.ConnectorName(), serviceName, region).Scan(&externalID, &foundRegion)
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
