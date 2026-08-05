// Package controller reconciles OdigosCloudConnector resources into a provider connector Deployment +
// Service and mirrors runtime-authoritative state into the CRD status cache.
package controller

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	cloudconnectorsv1 "github.com/odigos-io/odigos/api/cloud-connectors/v1alpha1"

	"github.com/odigos-io/odigos-enterprise/connector-runtime/internal/heartbeat"
	"github.com/odigos-io/odigos-enterprise/connector-runtime/internal/resources"
	cpg "github.com/odigos-io/odigos-enterprise/connectors/postgres"
)

const finalizer = "odigos.io/connector-runtime"

// annotationDeletionPolicy selects whether provider instrumentation is rolled back when the CR is
// deleted. Default is retain (no rollback); set to "cleanup" to drain on delete.
const annotationDeletionPolicy = "odigos.io/deletion-policy"

const (
	deletionPolicyCleanup = "cleanup"
	deletionPolicyRetain  = "retain"
)

// annotationProviderLock records the provider a connector was first reconciled with. spec.provider is
// immutable: changing it would change the workload name + immutable Deployment selector and strand the
// old Deployment (still instrumenting), so the controller refuses a changed provider rather than
// orphaning live instrumentation. Enforced here because the CRD type lives in an external module.
const annotationProviderLock = "odigos.io/connector-provider"

// fieldOwner identifies this controller's server-side-apply field ownership.
const fieldOwner = client.FieldOwner("connector-runtime")

// Condition types mirrored onto the CRD status.
const (
	condDeploymentReady = "RuntimeDeploymentReady"
	condSchemaReady     = "SchemaReady"
	condRegistered      = "RuntimeRegistered"
	condAuthenticated   = "Authenticated"
	condDiscoveryReady  = "DiscoveryReady"
	condReplicasClamped = "RuntimeReplicasClamped"
	condProviderLocked  = "ProviderImmutable"
)

// Config holds controller-level settings.
type Config struct {
	// DBSecretName/Key reference the Secret holding the Postgres DSN handed to connector pods.
	DBSecretName string
	DBSecretKey  string
	// ProviderImages maps a provider to its default connector image, used when the CR omits one.
	ProviderImages map[string]string
	// FargateAgentsImage optionally overrides the odigos-fargate-agents bundle the AWS connector's ECS
	// Fargate instrumentor stages agents from (stamped as ODIGOS_FARGATE_AGENTS_IMAGE on AWS connector
	// pods). Empty keeps the connector's compiled-in default.
	FargateAgentsImage string
	// CentralBackendURL is passed to connector pods so they dial Central for the agents-api WebSocket.
	CentralBackendURL string
	// RequeueInterval controls how often status is refreshed from the heartbeat table.
	RequeueInterval time.Duration
	// CleanupTimeout is how long to wait for instrumentation rollback before removing the finalizer.
	CleanupTimeout time.Duration
	// DrainRequeueInterval controls how often delete reconciliation polls shutdown completion.
	DrainRequeueInterval time.Duration
	// HeartbeatStaleAfter is how old the heartbeat's last_seen_at may be before the connector is treated as
	// not-Connected (Degraded). A connector that wedges or loses its DB connection stops updating last_seen_at
	// but its last-written status lingers, so without this it would report Connected forever. Default is a
	// few connector heartbeat intervals (connector default is 30s).
	HeartbeatStaleAfter time.Duration
}

type Reconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Config    Config
	Heartbeat *heartbeat.Reader
	// Recorder emits Kubernetes Events so operationally important outcomes (cleanup timeout leaving
	// instrumentation live, an invalid deletion-policy annotation) are durable and visible via kubectl,
	// not only in a controller log line on an object that may be about to be deleted.
	Recorder record.EventRecorder
}

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Config.RequeueInterval == 0 {
		r.Config.RequeueInterval = 30 * time.Second
	}
	if r.Config.CleanupTimeout == 0 {
		r.Config.CleanupTimeout = 15 * time.Minute
	}
	if r.Config.DrainRequeueInterval == 0 {
		r.Config.DrainRequeueInterval = 10 * time.Second
	}
	if r.Config.HeartbeatStaleAfter == 0 {
		r.Config.HeartbeatStaleAfter = 90 * time.Second
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&cloudconnectorsv1.OdigosCloudConnector{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cr cloudconnectorsv1.OdigosCloudConnector
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !cr.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, &cr)
	}

	// Add the finalizer and record the immutable provider lock in a single write on first observation.
	changed := controllerutil.AddFinalizer(&cr, finalizer)
	if cr.Annotations[annotationProviderLock] == "" {
		if cr.Annotations == nil {
			cr.Annotations = map[string]string{}
		}
		cr.Annotations[annotationProviderLock] = cr.Spec.Provider
		changed = true
	}
	if changed {
		if err := r.Update(ctx, &cr); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}

	// Surface a mistyped deletion-policy annotation: an unrecognized value silently means retain, so a typo
	// like "Cleanup"/"true" would leave provider resources instrumented on delete without any signal.
	if v := cr.Annotations[annotationDeletionPolicy]; !validDeletionPolicy(v) {
		msg := fmt.Sprintf("unrecognized %s=%q; treating as %q. Valid values: %q, %q.",
			annotationDeletionPolicy, v, deletionPolicyRetain, deletionPolicyRetain, deletionPolicyCleanup)
		logger.Info("WARNING: " + msg)
		r.event(&cr, corev1.EventTypeWarning, "InvalidDeletionPolicy", msg)
	}

	before := cr.Status.DeepCopy()

	// spec.provider is immutable. A changed provider would change the workload name + immutable Deployment
	// selector, leaving the old Deployment (and its live instrumentation) orphaned. Refuse to reconcile
	// resources — which keeps the original Deployment intact — and surface the conflict.
	if locked, violated := providerImmutabilityViolated(&cr); violated {
		msg := fmt.Sprintf("spec.provider is immutable: locked to %q, cannot change to %q", locked, cr.Spec.Provider)
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condProviderLocked, metav1.ConditionTrue, "ProviderChanged", msg, cr.Generation))
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condDeploymentReady, metav1.ConditionFalse, "ProviderImmutable", msg, cr.Generation))
		cr.Status.Phase = cloudconnectorsv1.CloudConnectorPhaseError
		logger.Error(nil, msg)
		return r.finishStatus(ctx, &cr, before)
	}

	image, err := r.resolveImage(&cr)
	if err != nil {
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condDeploymentReady, metav1.ConditionFalse, "ImageUnresolved", err.Error(), cr.Generation))
		cr.Status.Phase = cloudconnectorsv1.CloudConnectorPhaseError
		return r.finishStatus(ctx, &cr, before)
	}

	schemaName := "connector_" + cr.Spec.Provider
	params := resources.DeploymentParams{
		Namespace:          cr.Namespace,
		WorkloadName:       resources.WorkloadName(cr.Spec.Provider, cr.Name),
		Image:              image,
		DBSecretName:       r.Config.DBSecretName,
		DBSecretKey:        r.Config.DBSecretKey,
		SchemaName:         schemaName,
		SchemaVersion:      cpg.ExpectedSchemaVersions[schemaName],
		CentralBackendURL:  r.Config.CentralBackendURL,
		FargateAgentsImage: r.Config.FargateAgentsImage,
	}

	if err := r.ensureService(ctx, &cr, params); err != nil {
		return ctrl.Result{}, err
	}
	deploymentReady, err := r.ensureDeployment(ctx, &cr, params)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.computeStatus(ctx, &cr, params, deploymentReady)
	logger.V(1).Info("reconciled connector", "phase", cr.Status.Phase, "deploymentReady", deploymentReady)
	return r.finishStatus(ctx, &cr, before)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, cr *cloudconnectorsv1.OdigosCloudConnector) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !wantsCleanupOnDelete(cr) {
		return r.removeFinalizer(ctx, cr)
	}

	if !r.Heartbeat.Enabled() {
		logger.Info("heartbeat access disabled; skipping instrumentation cleanup on delete")
		return r.removeFinalizer(ctx, cr)
	}

	hb, err := r.Heartbeat.Get(ctx, cr.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !hb.Found {
		logger.Info("connector never registered; nothing to drain on delete")
		return r.removeFinalizer(ctx, cr)
	}
	if hb.ShutdownComplete {
		return r.removeFinalizer(ctx, cr)
	}

	// Cleanup is performed by the RUNNING connector pod: it observes this drain request in its loop and
	// rolls back instrumentation with per-tick retry, then records shutdownComplete (handled above). We do
	// NOT scale the pod to zero to trigger cleanup — draining happens while the pod runs, so a transient
	// provider (AWS) error simply retries on the next loop tick instead of being force-killed mid-rollback
	// by pod termination (which cannot be halted once started). The pod is torn down by owner-reference GC
	// once the finalizer is removed here.
	if !hb.DrainRequested {
		if err := r.Heartbeat.RequestDrain(ctx, cr.Name, "connector_deleted"); err != nil {
			return ctrl.Result{}, err
		}
		logger.Info("drain requested; waiting for the connector to roll back instrumentation")
	}

	// Bound the wait by the CR's deletion timestamp — a stable, monotonic clock that survives connector pod
	// restarts. A restart's Register clears the drain flag; the next reconcile re-requests it, which would
	// keep resetting a drain_requested_at-based deadline (and a crashlooping pod could wedge the CR in
	// Deleting forever). Timing out orphans instrumentation, so it is a last resort, surfaced as a
	// condition (the pod keeps retrying until GC removes it after the finalizer is dropped).
	if time.Since(cr.DeletionTimestamp.Time) > r.Config.CleanupTimeout {
		logger.Info("instrumentation cleanup timed out; removing finalizer")
		meta.SetStatusCondition(&cr.Status.Conditions, cond("CleanupFailed", metav1.ConditionTrue, "CleanupTimedOut", "Instrumentation rollback did not complete before the cleanup timeout", cr.Generation))
		if err := r.Status().Update(ctx, cr); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
		// Emit a durable Event: the CR (and its CleanupFailed condition) is about to be deleted, so a log
		// line would be the only trace that instrumentation was left live in the customer's account.
		r.event(cr, corev1.EventTypeWarning, "CleanupFailed",
			"Instrumentation rollback did not complete before the cleanup timeout; provider resources may still be instrumented")
		return r.removeFinalizer(ctx, cr)
	}

	return ctrl.Result{RequeueAfter: r.Config.DrainRequeueInterval}, nil
}

func wantsCleanupOnDelete(cr *cloudconnectorsv1.OdigosCloudConnector) bool {
	return cr.Annotations != nil && cr.Annotations[annotationDeletionPolicy] == deletionPolicyCleanup
}

// validDeletionPolicy reports whether the deletion-policy annotation value is recognized. Unset is valid
// (defaults to retain). Because the CRD type lives in an external module we can't enforce an enum in the
// API server, so an unrecognized value is validated here and surfaced (see reconcile) rather than
// silently treated as retain — the failure mode is "your production resources stay instrumented".
func validDeletionPolicy(v string) bool {
	switch v {
	case "", deletionPolicyRetain, deletionPolicyCleanup:
		return true
	default:
		return false
	}
}

// event records a Kubernetes Event when a recorder is configured (no-op in unit tests without one).
func (r *Reconciler) event(cr *cloudconnectorsv1.OdigosCloudConnector, eventtype, reason, message string) {
	if r.Recorder != nil {
		r.Recorder.Event(cr, eventtype, reason, message)
	}
}

// providerImmutabilityViolated reports whether the CR's spec.provider differs from the provider locked
// (recorded in an annotation) on first reconcile. An empty lock — not yet recorded — is never a
// violation. Pure so it can be unit-tested without a client.
func providerImmutabilityViolated(cr *cloudconnectorsv1.OdigosCloudConnector) (locked string, violated bool) {
	locked = cr.Annotations[annotationProviderLock]
	return locked, locked != "" && locked != cr.Spec.Provider
}

func (r *Reconciler) removeFinalizer(ctx context.Context, cr *cloudconnectorsv1.OdigosCloudConnector) (ctrl.Result, error) {
	if controllerutil.RemoveFinalizer(cr, finalizer) {
		if err := r.Update(ctx, cr); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *Reconciler) resolveImage(cr *cloudconnectorsv1.OdigosCloudConnector) (string, error) {
	if cr.Spec.Runtime.Image != "" {
		return cr.Spec.Runtime.Image, nil
	}
	if img := r.Config.ProviderImages[cr.Spec.Provider]; img != "" {
		return img, nil
	}
	return "", fmt.Errorf("no runtime image configured for provider %q (set spec.runtime.image or a controller default)", cr.Spec.Provider)
}

// ensureDeployment server-side-applies the desired Deployment. SSA only manages the fields this
// controller sets, leaving server-defaulted fields untouched — so it does not churn or conflict on
// every reconcile the way a get-mutate-update would.
func (r *Reconciler) ensureDeployment(ctx context.Context, cr *cloudconnectorsv1.OdigosCloudConnector, params resources.DeploymentParams) (bool, error) {
	dep := resources.BuildDeployment(cr, params)
	if err := controllerutil.SetControllerReference(cr, dep, r.Scheme); err != nil {
		return false, err
	}
	if err := r.Patch(ctx, dep, client.Apply, fieldOwner, client.ForceOwnership); err != nil {
		return false, err
	}
	return dep.Status.ReadyReplicas >= 1, nil
}

func (r *Reconciler) ensureService(ctx context.Context, cr *cloudconnectorsv1.OdigosCloudConnector, params resources.DeploymentParams) error {
	svc := resources.BuildService(cr, params)
	if err := controllerutil.SetControllerReference(cr, svc, r.Scheme); err != nil {
		return err
	}
	// SSA does not set spec.clusterIP, so the server-assigned value is preserved.
	return r.Patch(ctx, svc, client.Apply, fieldOwner, client.ForceOwnership)
}

// computeStatus derives conditions, phase and the status cache from the Deployment readiness and the
// best-effort heartbeat snapshot.
func (r *Reconciler) computeStatus(ctx context.Context, cr *cloudconnectorsv1.OdigosCloudConnector, params resources.DeploymentParams, deploymentReady bool) {
	if _, clamped := resources.ReplicasFor(cr); clamped {
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condReplicasClamped, metav1.ConditionTrue, "ClampedToOne",
			fmt.Sprintf("spec.runtime.replicas was clamped to %d: the connector runtime is not leader-elected and more than one replica would race provider mutations", resources.MaxReplicas), cr.Generation))
	} else {
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condReplicasClamped, metav1.ConditionFalse, "WithinLimit", "Replica count is within the supported limit", cr.Generation))
	}

	if deploymentReady {
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condDeploymentReady, metav1.ConditionTrue, "DeploymentAvailable", "Deployment available", cr.Generation))
	} else {
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condDeploymentReady, metav1.ConditionFalse, "DeploymentUnavailable", "Waiting for the connector Deployment to become available", cr.Generation))
	}

	hb, hbErr := r.Heartbeat.Get(ctx, cr.Name)
	// A registered connector whose last_seen_at has aged past the threshold is not actively heartbeating
	// (wedged or lost its DB connection); treat it as stale rather than trusting its lingering status.
	stale := hb.Found && r.Config.HeartbeatStaleAfter > 0 && time.Since(hb.LastSeenAt) > r.Config.HeartbeatStaleAfter
	switch {
	case hbErr != nil:
		setUnknown(cr, "HeartbeatUnavailable", "Could not read the runtime heartbeat from Postgres")
	case !hb.Found:
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condRegistered, metav1.ConditionFalse, "RuntimeNotRegistered", "Runtime has not registered yet", cr.Generation))
		for _, t := range []string{condSchemaReady, condAuthenticated, condDiscoveryReady} {
			meta.SetStatusCondition(&cr.Status.Conditions, cond(t, metav1.ConditionUnknown, "AwaitingHeartbeat", "Awaiting first runtime heartbeat", cr.Generation))
		}
	default:
		// A registered runtime has already passed its startup schema check.
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condSchemaReady, metav1.ConditionTrue, "SchemaUpToDate", "Provider schema at expected version", cr.Generation))
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condAuthenticated, boolCond(hb.Authenticated), authReason(hb.Authenticated), authMsg(hb.Authenticated), cr.Generation))
		meta.SetStatusCondition(&cr.Status.Conditions, cond(condDiscoveryReady, boolCond(hb.DiscoveryReady), discoveryReason(hb.DiscoveryReady), discoveryMsg(hb.DiscoveryReady), cr.Generation))
		if stale {
			// Row exists but last_seen_at is stale: the connector isn't actively heartbeating (wedged or lost
			// its DB connection). Don't report it as healthily registered — surface the staleness.
			meta.SetStatusCondition(&cr.Status.Conditions, cond(condRegistered, metav1.ConditionFalse, "HeartbeatStale",
				fmt.Sprintf("No runtime heartbeat for over %s (last seen %s)", r.Config.HeartbeatStaleAfter, hb.LastSeenAt.UTC().Format(time.RFC3339)), cr.Generation))
		} else {
			meta.SetStatusCondition(&cr.Status.Conditions, cond(condRegistered, metav1.ConditionTrue, "HeartbeatReceived", "Registered and heartbeating", cr.Generation))
		}
	}

	cr.Status.Phase = computePhase(deploymentReady, hb, stale)

	rt := &cloudconnectorsv1.CloudConnectorRuntimeStatus{
		WorkloadName: params.WorkloadName,
		Namespace:    params.Namespace,
		Image:        params.Image,
		Version:      hb.Version,
	}
	if hb.Found {
		t := metav1.NewTime(hb.LastSeenAt)
		rt.LastSeenAt = &t
		rt.Message = "Healthy"
		if stale {
			rt.Message = "Heartbeat stale"
		}
	}
	cr.Status.Runtime = rt
	cr.Status.ProviderIdentity = &cloudconnectorsv1.CloudConnectorProviderIdentity{
		AccountID:   cr.Spec.Account.ID,
		DisplayName: cr.Spec.Account.DisplayName,
	}
}

// finishStatus writes the CRD status only when it meaningfully changed (ignoring the LastSyncedAt
// freshness stamp), then requeues for the next periodic refresh. Skipping no-op writes avoids the
// self-triggered reconcile storm; conflicts are treated as a quiet requeue rather than an error.
func (r *Reconciler) finishStatus(ctx context.Context, cr *cloudconnectorsv1.OdigosCloudConnector, before *cloudconnectorsv1.OdigosCloudConnectorStatus) (ctrl.Result, error) {
	cr.Status.ObservedGeneration = cr.Generation
	if statusChanged(before, &cr.Status) {
		now := metav1.Now()
		cr.Status.LastSyncedAt = &now
		if err := r.Status().Update(ctx, cr); err != nil {
			if apierrors.IsConflict(err) {
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: r.Config.RequeueInterval}, nil
}

// statusChanged reports whether two statuses differ, ignoring the LastSyncedAt freshness timestamp.
func statusChanged(a, b *cloudconnectorsv1.OdigosCloudConnectorStatus) bool {
	x := a.DeepCopy()
	y := b.DeepCopy()
	x.LastSyncedAt = nil
	y.LastSyncedAt = nil
	return !apiequality.Semantic.DeepEqual(x, y)
}

func computePhase(deploymentReady bool, hb heartbeat.Snapshot, stale bool) cloudconnectorsv1.CloudConnectorPhase {
	if !deploymentReady {
		return cloudconnectorsv1.CloudConnectorPhaseStarting
	}
	if !hb.Found {
		return cloudconnectorsv1.CloudConnectorPhaseStarting
	}
	if stale {
		// Heartbeat row exists but last_seen_at is stale — the connector is not actively heartbeating.
		// Report Degraded instead of trusting the last-written (possibly Connected) status.
		return cloudconnectorsv1.CloudConnectorPhaseDegraded
	}
	switch hb.Status {
	case cpg.HeartbeatStatusConnected:
		return cloudconnectorsv1.CloudConnectorPhaseConnected
	case cpg.HeartbeatStatusDegraded:
		return cloudconnectorsv1.CloudConnectorPhaseDegraded
	case cpg.HeartbeatStatusError:
		return cloudconnectorsv1.CloudConnectorPhaseError
	case cpg.HeartbeatStatusDisabled:
		return cloudconnectorsv1.CloudConnectorPhaseDisabled
	default:
		return cloudconnectorsv1.CloudConnectorPhaseStarting
	}
}

func setUnknown(cr *cloudconnectorsv1.OdigosCloudConnector, reason, msg string) {
	for _, t := range []string{condSchemaReady, condRegistered, condAuthenticated, condDiscoveryReady} {
		meta.SetStatusCondition(&cr.Status.Conditions, cond(t, metav1.ConditionUnknown, reason, msg, cr.Generation))
	}
}

func cond(t string, status metav1.ConditionStatus, reason, msg string, gen int64) metav1.Condition {
	return metav1.Condition{Type: t, Status: status, Reason: reason, Message: msg, ObservedGeneration: gen}
}

func boolCond(b bool) metav1.ConditionStatus {
	if b {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func authReason(ok bool) string {
	if ok {
		return "AuthSucceeded"
	}
	return "AuthPending"
}
func authMsg(ok bool) string {
	if ok {
		return "Provider identity verified"
	}
	return "Provider authentication not yet confirmed"
}
func discoveryReason(ok bool) string {
	if ok {
		return "InitialDiscoveryDone"
	}
	return "DiscoveryPending"
}
func discoveryMsg(ok bool) string {
	if ok {
		return "Initial discovery completed"
	}
	return "Initial discovery not yet completed"
}
