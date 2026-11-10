package instrumentor

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

var log = connlog.Subsystem("mulesoft-workload-instrumentor")

// inventory is the slice of the workload store the reconciler uses; an interface so the reconciler is testable
// against the fake Anypoint API without a database.
type inventory interface {
	ListApplications(ctx context.Context, kinds ...string) ([]store.Application, error)
	ListSources(ctx context.Context) ([]basestore.SourceRecord, error)
	DeletedSourceKeys(ctx context.Context) (map[string]bool, error)
	OTLPExportTarget(ctx context.Context) (domaindest.OTLPTarget, bool, error)
	SetInstrumented(ctx context.Context, externalID, origin string) error
	SetInstrumentationError(ctx context.Context, externalID, msg string) error
	ClearInstrumentationError(ctx context.Context, externalID string) error
	SetInstrumentationPhase(ctx context.Context, externalID, phase string) error
	ClearInstrumentationPhase(ctx context.Context, externalID string) error
	SetGatewaySnapshot(ctx context.Context, externalID string, snap *store.GatewaySnapshot) error
}

// Settings are the live operational knobs, re-read every reconcile pass.
type Settings struct {
	SampleRatio  float64
	MaxRedeploys int
}

// AMC reconciles Direct Telemetry Stream properties on the Application Manager deployments of ONE kind
// (CloudHub 2.0 or Runtime Fabric) against source intent.
//
// Every properties change makes the platform roll the application, which restarts it. So the reconciler reads
// the live deployment, computes the properties it should have, and writes only when they differ; it never
// writes to a deployment whose last update is still rolling out; and it writes to at most MaxRedeploys
// applications per pass, which bounds how many restarts a mistake can cause before someone notices.
type AMC struct {
	kind     string
	store    inventory
	client   *anypoint.Client
	orgID    string
	settings func() Settings

	// verifiedClean holds applications with a disabled or deleted source that a live read has already shown to
	// carry no Odigos ownership marker, so they are not re-read every pass. In memory on purpose: after a restart
	// each is verified once more.
	mu            sync.Mutex
	verifiedClean map[string]bool
}

// NewAMC builds the reconciler for one Application Manager kind.
func NewAMC(kind string, st inventory, client *anypoint.Client, orgID string, settings func() Settings) *AMC {
	return &AMC{kind: kind, store: st, client: client, orgID: orgID, settings: settings, verifiedClean: map[string]bool{}}
}

// EnsurePrerequisite implements base.Prerequisite. Instrumenting needs an OTLP destination, but returning an
// error here would skip the whole pass — rollbacks included — so the gate is enforced per application in
// Reconcile instead.
func (r *AMC) EnsurePrerequisite(ctx context.Context) error {
	if _, ok, _ := r.store.OTLPExportTarget(ctx); !ok {
		log.Debug("mule instrumentation waiting for an OTLP destination", "kind", r.kind)
	}
	return nil
}

// errDeferred marks an application that cannot be changed right now and will be retried on a later pass; it is
// logged, not reported as an instrumentation error.
var errDeferred = errors.New("deferred")

// Reconcile instruments applications with an enabled source and rolls back the ones Odigos instrumented whose
// source is gone or disabled.
func (r *AMC) Reconcile(ctx context.Context) error {
	sources, err := r.store.ListSources(ctx)
	if err != nil {
		return err
	}
	enabled := map[string]string{}
	// sourced holds every application a source has ever named, enabled or not. Discovery can overwrite the
	// instrumented flag with a read taken before a PATCH landed, and the delete drain disables every source and
	// then rolls back only what it considers instrumented — so an application with a source is checked live
	// rather than trusted to that flag.
	sourced := map[string]bool{}
	for _, s := range sources {
		if s.Kind != r.kind || s.ExternalResourceID == "" {
			continue
		}
		sourced[s.ExternalResourceID] = true
		if s.Enabled {
			enabled[s.ExternalResourceID] = s.ReportedServiceName()
		}
	}
	deleted, err := r.store.DeletedSourceKeys(ctx)
	if err != nil {
		return err
	}
	apps, err := r.store.ListApplications(ctx, r.kind)
	if err != nil {
		return err
	}
	dest, haveDest, err := r.store.OTLPExportTarget(ctx)
	if err != nil {
		log.Error("resolve otlp export target failed", "error", err)
		haveDest = false
	}

	settings := r.settings()
	budget := settings.MaxRedeploys
	instrument, rollback := r.instrument, r.rollback
	if r.kind == store.KindOmniGateway {
		instrument, rollback = r.instrumentGateway, r.rollbackGateway
	}
	var errs []error
	for _, app := range apps {
		serviceName, want := enabled[app.ExternalID]
		var opErr error
		rollingBack := false
		switch {
		case want && !haveDest:
			log.Warn("skip mule instrumentation: no OTLP destination configured", "app", app.AppName)
			continue
		case want:
			if serviceName == "" {
				serviceName = app.AppName
			}
			opErr = instrument(ctx, app, serviceName, dest, settings.SampleRatio, &budget)
		case app.Instrumented && app.Origin != store.OriginExternal,
			(sourced[app.ExternalID] || deleted[basestore.SourceIdentityKey(r.kind, app.ExternalID)]) && !r.isClean(app.ExternalID):
			// A customer-instrumented application is never ours to undo; reading it every pass would only spend
			// API calls.
			rollingBack = true
			opErr = rollback(ctx, app)
		default:
			continue
		}
		switch {
		case errors.Is(opErr, errDeferred) && rollingBack:
			// A deferred rollback leaves Odigos instrumentation live, so the pass must not look clean: the drain
			// that runs when a connector is deleted treats a nil Reconcile as "everything rolled back" and would
			// release the connector with this application still exporting. Returned, not recorded on the
			// workload, because nothing is wrong with the application — the next pass finishes the job.
			log.Info("mule rollback deferred", "app", app.AppName, "environment", app.EnvironmentName, "reason", opErr)
			errs = append(errs, fmt.Errorf("%s/%s: rollback %w", app.EnvironmentName, app.AppName, opErr))
		case errors.Is(opErr, errDeferred):
			log.Info("mule application change deferred", "app", app.AppName, "environment", app.EnvironmentName, "reason", opErr)
		case opErr != nil:
			log.Error("mule instrumentation failed", "app", app.AppName, "environment", app.EnvironmentName, "error", opErr)
			r.setError(ctx, app, opErr.Error())
			errs = append(errs, fmt.Errorf("%s/%s: %w", app.EnvironmentName, app.AppName, opErr))
		default:
			if app.InstrumentationError != "" {
				if err := r.store.ClearInstrumentationError(ctx, app.ExternalID); err != nil {
					log.Warn("clear instrumentation error failed", "app", app.AppName, "error", err)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func (r *AMC) instrument(ctx context.Context, app store.Application, serviceName string, dest domaindest.OTLPTarget, ratio float64, budget *int) error {
	dep, err := r.client.GetDeployment(ctx, r.orgID, app.EnvironmentID, app.ExternalID)
	if err != nil {
		return fmt.Errorf("read deployment: %s", anypoint.Summarize(err))
	}
	version, ok := anypoint.ParseMuleVersion(dep.RuntimeVersion())
	if !ok || !Eligible(version) {
		return fmt.Errorf("Mule runtime %q does not support Direct Telemetry Stream; upgrade the application to Mule 4.11 or later", dep.RuntimeVersion())
	}

	current := dep.Properties()
	marker := current[store.PropOdigosManaged]
	if marker == "" {
		if keys := conflicts(current, dep.SecurePropertyKeys()); len(keys) > 0 {
			return &ConflictError{Keys: keys}
		}
	} else if err := ownedBy(marker, app.ExternalID); err != nil {
		return err
	}
	r.markDirty(app.ExternalID)
	desired := DesiredProperties(Workload{
		ServiceName:     serviceName,
		EnvironmentName: app.EnvironmentName,
		OrgID:           r.orgID,
		TargetType:      app.TargetType,
		Version:         version,
		API:             app.API,
	}, dest, ratio)
	next, err := Apply(current, desired, marker, app.ExternalID)
	if err != nil {
		return err
	}

	if maps.Equal(next, current) {
		// Converged. The phase follows the rollout: Verifying until the platform reports the deployment applied
		// and the application running, then cleared.
		r.setInstrumented(ctx, app, store.OriginConnector)
		if dep.Status == anypoint.DeploymentApplied && dep.Application.Status == "RUNNING" {
			r.clearPhase(ctx, app)
		} else {
			r.setPhase(ctx, app, baseapi.PhaseVerifying)
		}
		return nil
	}
	if err := r.changeable(dep, budget); err != nil {
		return err
	}

	log.Info("instrumenting mule application", "app", app.AppName, "environment", app.EnvironmentName, "runtime", version.Raw)
	r.setPhase(ctx, app, baseapi.PhaseInstrumenting)
	if err := r.client.UpdateDeploymentProperties(ctx, r.orgID, app.EnvironmentID, app.ExternalID, dep.Name, next); err != nil {
		return fmt.Errorf("update deployment properties: %s", anypoint.Summarize(err))
	}
	*budget--
	r.setInstrumented(ctx, app, store.OriginConnector)
	return nil
}

// rollback undoes Odigos's change to one application. Unlike instrument it neither waits for a running
// application nor spends the redeploy budget: a rollback is what deleting a source or the connector asks for,
// the drain that runs on connector delete must finish within the controller's cleanup timeout, and removing
// Odigos's properties from a stopped or failed deployment is exactly the end state wanted. It only waits for an
// update that is still rolling out, which it would otherwise race.
func (r *AMC) rollback(ctx context.Context, app store.Application) error {
	dep, err := r.client.GetDeployment(ctx, r.orgID, app.EnvironmentID, app.ExternalID)
	if err != nil {
		if anypoint.IsNotFound(err) {
			// Undeployed: nothing left to roll back. Discovery prunes the row.
			r.setInstrumented(ctx, app, "")
			return nil
		}
		return fmt.Errorf("read deployment: %s", anypoint.Summarize(err))
	}
	next, owned := Rollback(dep.Properties(), app.ExternalID)
	if !owned {
		// Not ours to undo: the customer instrumented it (their IaC marker), it carries a marker that is not this
		// deployment's record, or there is nothing to undo. Recorded the way discovery records it, so the two
		// agree and the next discovery does not flip the flag back.
		r.setInstrumented(ctx, app, store.MarkerOrigin(dep.Properties(), dep.SecurePropertyKeys(), app.ExternalID))
		r.markClean(app.ExternalID)
		return nil
	}
	if dep.Status != anypoint.DeploymentApplied && dep.Status != anypoint.DeploymentFailed {
		return fmt.Errorf("%w: deployment update in progress (status %s)", errDeferred, dep.Status)
	}

	log.Info("rolling back mule instrumentation", "app", app.AppName, "environment", app.EnvironmentName)
	if err := r.client.UpdateDeploymentProperties(ctx, r.orgID, app.EnvironmentID, app.ExternalID, dep.Name, next); err != nil {
		return fmt.Errorf("update deployment properties: %s", anypoint.Summarize(err))
	}
	r.setInstrumented(ctx, app, "")
	r.clearPhase(ctx, app)
	r.markClean(app.ExternalID)
	return nil
}

func (r *AMC) isClean(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verifiedClean[id]
}

func (r *AMC) markClean(id string) {
	r.mu.Lock()
	r.verifiedClean[id] = true
	r.mu.Unlock()
}

func (r *AMC) markDirty(id string) {
	r.mu.Lock()
	delete(r.verifiedClean, id)
	r.mu.Unlock()
}

// changeable gates an instrumenting write: the deployment must be settled (an update in flight would be
// superseded mid-roll), not failed (a properties change would redeploy a broken application), not stopped (the
// change would apply on its next start, which is the customer's call to make), and the pass must have redeploy
// budget left. Rollbacks have their own, narrower gate (see rollback).
func (r *AMC) changeable(dep anypoint.Deployment, budget *int) error {
	switch {
	case dep.Status == anypoint.DeploymentFailed:
		return fmt.Errorf("the deployment is in a failed state; Odigos changes it once it deploys successfully")
	case dep.Status != anypoint.DeploymentApplied:
		return fmt.Errorf("%w: deployment update in progress (status %s)", errDeferred, dep.Status)
	case dep.Application.DesiredState == "STOPPED":
		return fmt.Errorf("%w: application is stopped", errDeferred)
	case *budget <= 0:
		return fmt.Errorf("%w: redeploy budget for this pass is spent", errDeferred)
	}
	return nil
}

func (r *AMC) setInstrumented(ctx context.Context, app store.Application, origin string) {
	if err := r.store.SetInstrumented(ctx, app.ExternalID, origin); err != nil {
		log.Warn("set instrumented failed", "app", app.AppName, "error", err)
	}
}

func (r *AMC) setError(ctx context.Context, app store.Application, msg string) {
	if err := r.store.SetInstrumentationError(ctx, app.ExternalID, msg); err != nil {
		log.Warn("set instrumentation error failed", "app", app.AppName, "error", err)
	}
}

func (r *AMC) setPhase(ctx context.Context, app store.Application, phase string) {
	if err := r.store.SetInstrumentationPhase(ctx, app.ExternalID, phase); err != nil {
		log.Warn("set instrumentation phase failed", "app", app.AppName, "phase", phase, "error", err)
	}
}

func (r *AMC) clearPhase(ctx context.Context, app store.Application) {
	if app.InstrumentationPhase == "" {
		return
	}
	if err := r.store.ClearInstrumentationPhase(ctx, app.ExternalID); err != nil {
		log.Warn("clear instrumentation phase failed", "app", app.AppName, "error", err)
	}
}
