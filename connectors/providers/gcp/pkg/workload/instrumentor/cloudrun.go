// Package instrumentor holds the GCP workload instrumentation reconcilers. Cloud Run instrumentation
// injects the OpenTelemetry agent into a service revision using the same non-eBPF mechanism as OSS Odigos'
// "k8s-init-container" mount method: an init container (from the busybox-based odigos-agents image, which
// bundles every language's native agent files under /instrumentations) copies the language agent into a
// shared volume the app container mounts, and the app container is given the agent-activation env vars
// (JAVA_TOOL_OPTIONS / PYTHONPATH / NODE_OPTIONS / the .NET CORECLR_* set) resolved against /var/odigos —
// verbatim from the OSS community distro YAMLs.
//
// Cloud Run adapts that method in two ways forced by the platform:
//   - eBPF is unavailable (and there is no odiglet/node to run a device plugin, host-path, or CSI mount),
//     so the OSS DEFAULT delivery (device plugin mounting a node-staged /var/odigos) cannot be used;
//     init-container-into-a-shared-volume is the only OSS method that is self-contained within the
//     instance and therefore portable to serverless Cloud Run.
//   - Cloud Run has no run-to-completion init containers, so instead of a true init container the agent
//     copier runs as a startup-ordered sidecar: it stages the files then holds a TCP port, and the ingress
//     container's DependsOn + the sidecar's startup probe gate the app start until staging completes.
//
// The agent then exports OTLP directly to the connector's destination (Cloud Run has no node-local
// collector). Supported languages mirror the OSS community distros: Java, Python, Node.js and .NET.
package instrumentor

import (
	"context"
	"errors"
	"fmt"

	run "cloud.google.com/go/run/apiv2"
	runpb "cloud.google.com/go/run/apiv2/runpb"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/gcpclient"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/workload/store"
)

var log = connlog.Subsystem("gcp-workload-instrumentor")

// CloudRunInstrumentor reconciles Cloud Run instrumentation against source intent. Each cycle it
// instruments enabled Cloud Run sources (inject the agent sidecar + shared volume + activation/OTLP env)
// and rolls back services that are instrumented but no longer have an enabled source. It is idempotent:
// the live Cloud Run service is the source of truth, so a converged service is left untouched.
type CloudRunInstrumentor struct {
	store   *store.Store
	clients *gcpclient.Clients
	// image is the odigos-agents image the sidecar stages the language agents from.
	image string
	// debug returns the live debug-telemetry toggle: when true, instrumented services get OTEL_LOG_LEVEL=debug.
	debug func() bool
	// images reads the container image config for language detection (CNB buildpack metadata labels plus
	// the image's own entrypoint/cmd/env). Optional: nil disables the image-config detection layer (the
	// service-spec image/command/env heuristics still apply).
	images ImageInspector
}

// NewCloudRun builds the Cloud Run instrumentor over the workload store, shared client factory, the
// odigos-agents image, a getter for the live debug-telemetry toggle, and an optional image inspector used
// to read buildpack language metadata from the service's container image.
func NewCloudRun(st *store.Store, clients *gcpclient.Clients, image string, debug func() bool, images ImageInspector) *CloudRunInstrumentor {
	if debug == nil {
		debug = func() bool { return false }
	}
	return &CloudRunInstrumentor{store: st, clients: clients, image: image, debug: debug, images: images}
}

// EnsurePrerequisite implements base.Prerequisite: Cloud Run instrumentation exports OTLP directly to the
// connector's destination, so it waits for one to exist before instrumenting. This is not an error (the
// coordinator skips the whole reconcile pass on a prerequisite error, which would also skip rollbacks) —
// Reconcile enforces the same gate per-service while still allowing rollbacks to run.
func (i *CloudRunInstrumentor) EnsurePrerequisite(ctx context.Context) error {
	if _, haveDest, _ := i.store.OTLPExportTarget(ctx); !haveDest {
		log.Debug("cloud-run prerequisite waiting for an OTLP destination")
	}
	return nil
}

// enabledSource is the per-service instrumentation intent resolved from an enabled Cloud Run source.
type enabledSource struct {
	serviceName string // reported OTel service.name
	language    string // runtime language override, if the source set one
}

func (i *CloudRunInstrumentor) enabledSources(ctx context.Context) (map[string]enabledSource, error) {
	sources, err := i.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]enabledSource)
	for _, s := range sources {
		if s.Kind == store.KindCloudRun && s.Enabled && s.ExternalResourceID != "" {
			enabled[s.ExternalResourceID] = enabledSource{
				serviceName: s.ReportedServiceName(),
				language:    s.Config.RuntimeOverride.Language,
			}
		}
	}
	return enabled, nil
}

func (i *CloudRunInstrumentor) Reconcile(ctx context.Context) error {
	enabled, err := i.enabledSources(ctx)
	if err != nil {
		return err
	}
	services, err := i.store.ListCloudRunServices(ctx)
	if err != nil {
		return err
	}

	// The OTLP destination is required to instrument (the agent's export target). Without one, skip
	// instrumenting but still roll back anything previously instrumented.
	dest, haveDest, derr := i.store.OTLPExportTarget(ctx)
	if derr != nil {
		log.Error("resolve otlp export target failed", "error", derr)
		haveDest = false
	}

	client, err := i.clients.CloudRun(ctx)
	if err != nil {
		return err
	}
	defer client.Close()

	var errs []error
	for _, svc := range services {
		src, want := enabled[svc.ResourceName]
		if want {
			if !haveDest {
				log.Warn("skip cloud-run instrumentation: no OTLP destination configured", "service", svc.ServiceID)
				continue
			}
			if err := i.instrument(ctx, client, svc, src, dest); err != nil {
				log.Error("instrument cloud-run failed", "service", svc.ServiceID, "location", svc.Location, "error", err)
				if serr := i.store.SetCloudRunInstrumentationError(ctx, svc.ResourceName, err.Error()); serr != nil {
					log.Warn("set cloud-run instrumentation error failed", "service", svc.ServiceID, "error", serr)
				}
				errs = append(errs, fmt.Errorf("instrument %s: %w", svc.ServiceID, err))
			} else if serr := i.store.ClearCloudRunInstrumentationError(ctx, svc.ResourceName); serr != nil {
				log.Warn("clear cloud-run instrumentation error failed", "service", svc.ServiceID, "error", serr)
			}
			continue
		}
		// Not enabled: only services the inventory flags as instrumented can need rollback.
		if !svc.Instrumented {
			continue
		}
		if err := i.rollback(ctx, client, svc); err != nil {
			log.Error("rollback cloud-run failed", "service", svc.ServiceID, "location", svc.Location, "error", err)
			if serr := i.store.SetCloudRunInstrumentationError(ctx, svc.ResourceName, err.Error()); serr != nil {
				log.Warn("set cloud-run instrumentation error failed", "service", svc.ServiceID, "error", serr)
			}
			errs = append(errs, fmt.Errorf("rollback %s: %w", svc.ServiceID, err))
		} else if serr := i.store.ClearCloudRunInstrumentationError(ctx, svc.ResourceName); serr != nil {
			log.Warn("clear cloud-run instrumentation error failed", "service", svc.ServiceID, "error", serr)
		}
	}
	return errors.Join(errs...)
}

func (i *CloudRunInstrumentor) instrument(ctx context.Context, client *run.ServicesClient, rec store.CloudRunService, src enabledSource, dest domaindest.OTLPTarget) error {
	serviceName := src.serviceName
	if serviceName == "" {
		serviceName = rec.ServiceID
	}

	live, err := client.GetService(ctx, &runpb.GetServiceRequest{Name: rec.ResourceName})
	if err != nil {
		return err
	}

	lang := i.resolveLanguage(ctx, src, rec, live)
	if !SupportedLanguage(lang) {
		return fmt.Errorf("%w: could not determine a supported language for Cloud Run service %q (set a runtime language override on the source)", errUnsupportedLanguage, rec.ServiceID)
	}
	// Persist the resolved language so the workload report shows it (best-effort).
	if lang != rec.RuntimeLanguage {
		if serr := i.store.SetCloudRunRuntime(ctx, rec.ResourceName, lang, rec.RuntimeVersion); serr != nil {
			log.Warn("set cloud-run runtime failed", "service", rec.ServiceID, "error", serr)
		}
	}

	// Already converged — mark instrumented and clear the in-flight phase, skipping the update so we don't
	// churn a new revision every reconcile.
	if alreadyInstrumented(live, lang, i.image, serviceName, rec.Location, dest, i.debug()) {
		if err := i.store.SetCloudRunInstrumented(ctx, rec.ResourceName, true); err != nil {
			log.Warn("set cloud-run instrumented flag failed", "service", rec.ServiceID, "error", err)
		}
		i.clearPhase(ctx, rec)
		return nil
	}

	if !rec.Instrumented {
		i.setPhase(ctx, rec, baseapi.PhaseVerifying)
	}
	if err := applyInstrumentation(live, lang, i.image, serviceName, rec.Location, dest, i.debug()); err != nil {
		return err
	}

	i.setPhase(ctx, rec, baseapi.PhaseInstrumenting)
	log.Info("instrumenting cloud-run service", "service", rec.ServiceID, "location", rec.Location, "language", lang)
	if _, err := client.UpdateService(ctx, &runpb.UpdateServiceRequest{Service: live}); err != nil {
		return err
	}
	// Write the live flag through immediately so a reconcile before the next discovery cycle sees the
	// service as instrumented. The rollout settles asynchronously; the next converged reconcile clears the
	// phase.
	if err := i.store.SetCloudRunInstrumented(ctx, rec.ResourceName, true); err != nil {
		log.Warn("set cloud-run instrumented flag failed", "service", rec.ServiceID, "error", err)
	}
	return nil
}

// resolveLanguage determines the workload language for instrumentation, strongest signal first:
//  1. the source runtime override (explicit, authoritative),
//  2. a language already stored on the inventory record (from a prior detection),
//  3. the container image config: CNB buildpack metadata (the definitive signal for buildpack-built
//     services, the default `gcloud run deploy --source` path) then the image's own entrypoint/cmd/env
//     QuickScan (catches Dockerfile / official-base-image builds),
//  4. the static image/command/env heuristics over the Cloud Run service spec.
//
// Returns "" when undetermined; instrument() then reports the workload as unsupported until an override is
// set.
func (i *CloudRunInstrumentor) resolveLanguage(ctx context.Context, src enabledSource, rec store.CloudRunService, live *runpb.Service) string {
	if l := normalizeLanguage(src.language); l != "" {
		return l
	}
	if l := normalizeLanguage(rec.RuntimeLanguage); l != "" {
		return l
	}
	if l := i.languageFromImage(ctx, live); l != "" {
		return l
	}
	return guessLanguageFromService(live)
}

// languageFromImage reads the ingress container's image config once and derives the language from it,
// strongest signal first: CNB buildpack metadata labels, then the image's own entrypoint/cmd/env
// (procdiscovery-style QuickScan). Best-effort: a missing inspector, an unreadable image, or no
// recognizable signal all yield "" (the caller falls back to the service-spec heuristics). The result is
// normalized, so a detected-but-unsupported runtime (e.g. Go/Ruby/PHP, which have no Cloud Run agent yet)
// resolves to "" rather than a language we cannot inject.
func (i *CloudRunInstrumentor) languageFromImage(ctx context.Context, live *runpb.Service) string {
	if i.images == nil {
		return ""
	}
	c := ingressContainer(live)
	if c == nil || c.GetImage() == "" {
		return ""
	}
	cfg, err := i.images.ImageConfig(ctx, c.GetImage())
	if err != nil {
		log.Debug("read image config for language detection failed", "image", c.GetImage(), "error", err)
		return ""
	}
	if cfg == nil {
		return ""
	}
	// CNB buildpack labels are authoritative for buildpack-built (--source) images.
	if l := normalizeLanguage(languageFromBuildpackLabels(cfg.Labels)); l != "" {
		return l
	}
	// Otherwise fall back to the image's own process signals (Dockerfile / official-base-image builds),
	// using only the high-confidence env/exe QuickScan — the loose image-string scan runs later over the
	// service spec (which carries the image reference).
	return normalizeLanguage(guessStrongFromSignals(signalsFromImageConfig(cfg)))
}

func (i *CloudRunInstrumentor) rollback(ctx context.Context, client *run.ServicesClient, rec store.CloudRunService) error {
	live, err := client.GetService(ctx, &runpb.GetServiceRequest{Name: rec.ResourceName})
	if err != nil {
		return err
	}
	// Ownership gate: only roll back instrumentation we can attribute to ourselves (the ownership marker).
	// A service a customer instrumented with their own OpenTelemetry setup is left completely untouched.
	if !serviceOwned(live) {
		if err := i.store.SetCloudRunInstrumented(ctx, rec.ResourceName, false); err != nil {
			log.Warn("set cloud-run instrumented flag failed", "service", rec.ServiceID, "error", err)
		}
		return nil
	}
	if !rollbackInstrumentation(live) {
		if err := i.store.SetCloudRunInstrumented(ctx, rec.ResourceName, false); err != nil {
			log.Warn("set cloud-run instrumented flag failed", "service", rec.ServiceID, "error", err)
		}
		return nil
	}

	log.Info("rolling back cloud-run instrumentation", "service", rec.ServiceID, "location", rec.Location)
	if _, err := client.UpdateService(ctx, &runpb.UpdateServiceRequest{Service: live}); err != nil {
		return err
	}
	if err := i.store.SetCloudRunInstrumented(ctx, rec.ResourceName, false); err != nil {
		log.Warn("set cloud-run instrumented flag failed", "service", rec.ServiceID, "error", err)
	}
	i.clearPhase(ctx, rec)
	return nil
}

// setPhase records an in-flight instrumentation phase for live WorkloadReport progress. Best-effort.
func (i *CloudRunInstrumentor) setPhase(ctx context.Context, rec store.CloudRunService, phase string) {
	if err := i.store.SetCloudRunInstrumentationPhase(ctx, rec.ResourceName, phase); err != nil {
		log.Warn("set cloud-run instrumentation phase failed", "service", rec.ServiceID, "phase", phase, "error", err)
	}
}

// clearPhase drops the in-flight phase once a service has converged. Best-effort like setPhase.
func (i *CloudRunInstrumentor) clearPhase(ctx context.Context, rec store.CloudRunService) {
	if err := i.store.ClearCloudRunInstrumentationPhase(ctx, rec.ResourceName); err != nil {
		log.Warn("clear cloud-run instrumentation phase failed", "service", rec.ServiceID, "error", err)
	}
}
