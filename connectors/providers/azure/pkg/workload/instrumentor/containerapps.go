// Package instrumentor implements per-type Azure instrumentation. Container Apps instrumentation
// injects the OpenTelemetry agent into a revision using the OSS Odigos k8s-init-container pattern:
// an init container from the odigos-agents image copies language agents into an EmptyDir mounted at
// /var/odigos, and the app container receives activation + OTLP export env vars.
package instrumentor

import (
	"context"
	"errors"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/azureclient"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/workload/store"
)

var log = connlog.Subsystem("azure-workload-instrumentor")

// ContainerAppsInstrumentor reconciles Container Apps instrumentation against source intent.
type ContainerAppsInstrumentor struct {
	store   *store.Store
	clients *azureclient.Clients
	image   string
	debug   func() bool
}

// NewContainerApps builds the Container Apps instrumentor.
func NewContainerApps(st *store.Store, clients *azureclient.Clients, image string, debug func() bool) *ContainerAppsInstrumentor {
	if debug == nil {
		debug = func() bool { return false }
	}
	return &ContainerAppsInstrumentor{store: st, clients: clients, image: image, debug: debug}
}

// EnsurePrerequisite waits for an OTLP destination before instrumenting (rollbacks still run).
func (i *ContainerAppsInstrumentor) EnsurePrerequisite(ctx context.Context) error {
	if _, haveDest, _ := i.store.OTLPExportTarget(ctx); !haveDest {
		log.Debug("container-apps prerequisite waiting for an OTLP destination")
	}
	return nil
}

type enabledSource struct {
	serviceName string
	language    string
}

func (i *ContainerAppsInstrumentor) enabledSources(ctx context.Context) (map[string]enabledSource, error) {
	sources, err := i.store.ListSources(ctx)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]enabledSource)
	for _, s := range sources {
		if s.Kind == store.KindContainerApps && s.Enabled && s.ExternalResourceID != "" {
			enabled[s.ExternalResourceID] = enabledSource{
				serviceName: s.ReportedServiceName(),
				language:    s.Config.RuntimeOverride.Language,
			}
		}
	}
	return enabled, nil
}

func (i *ContainerAppsInstrumentor) Reconcile(ctx context.Context) error {
	enabled, err := i.enabledSources(ctx)
	if err != nil {
		return err
	}
	apps, err := i.store.ListContainerApps(ctx)
	if err != nil {
		return err
	}

	dest, haveDest, derr := i.store.OTLPExportTarget(ctx)
	if derr != nil {
		log.Error("resolve otlp export target failed", "error", derr)
		haveDest = false
	}

	client, err := i.clients.ContainerApps()
	if err != nil {
		return err
	}

	var errs []error
	for _, rec := range apps {
		src, want := enabled[rec.ResourceID]
		if want {
			if !haveDest {
				log.Warn("skip container-apps instrumentation: no OTLP destination configured", "app", rec.AppName)
				continue
			}
			if err := i.instrument(ctx, client, rec, src, dest); err != nil {
				log.Error("instrument container-apps failed", "app", rec.AppName, "location", rec.Location, "error", err)
				if serr := i.store.SetContainerAppInstrumentationError(ctx, rec.ResourceID, err.Error()); serr != nil {
					log.Warn("set container-apps instrumentation error failed", "app", rec.AppName, "error", serr)
				}
				errs = append(errs, fmt.Errorf("instrument %s: %w", rec.AppName, err))
			} else if serr := i.store.ClearContainerAppInstrumentationError(ctx, rec.ResourceID); serr != nil {
				log.Warn("clear container-apps instrumentation error failed", "app", rec.AppName, "error", serr)
			}
			continue
		}
		if !rec.Instrumented {
			continue
		}
		if err := i.rollback(ctx, client, rec); err != nil {
			log.Error("rollback container-apps failed", "app", rec.AppName, "location", rec.Location, "error", err)
			if serr := i.store.SetContainerAppInstrumentationError(ctx, rec.ResourceID, err.Error()); serr != nil {
				log.Warn("set container-apps instrumentation error failed", "app", rec.AppName, "error", serr)
			}
			errs = append(errs, fmt.Errorf("rollback %s: %w", rec.AppName, err))
		} else if serr := i.store.ClearContainerAppInstrumentationError(ctx, rec.ResourceID); serr != nil {
			log.Warn("clear container-apps instrumentation error failed", "app", rec.AppName, "error", serr)
		}
	}
	return errors.Join(errs...)
}

func (i *ContainerAppsInstrumentor) instrument(ctx context.Context, client *armappcontainers.ContainerAppsClient, rec store.ContainerApp, src enabledSource, dest domaindest.OTLPTarget) error {
	serviceName := src.serviceName
	if serviceName == "" {
		serviceName = rec.AppName
	}

	rg := rec.ResourceGroup
	if rg == "" {
		rg = azureclient.ResourceGroupFromID(rec.ResourceID)
	}
	resp, err := client.Get(ctx, rg, rec.AppName, nil)
	if err != nil {
		return err
	}
	live := resp.ContainerApp

	lang := detectLanguage(src.language, rec.RuntimeLanguage, &live)
	if !SupportedLanguage(lang) {
		return fmt.Errorf("%w: could not determine a supported language for Container App %q (set a runtime language override on the source)", errUnsupportedLanguage, rec.AppName)
	}
	if lang != rec.RuntimeLanguage {
		if serr := i.store.SetContainerAppRuntime(ctx, rec.ResourceID, lang, rec.RuntimeVersion); serr != nil {
			log.Warn("set container-apps runtime failed", "app", rec.AppName, "error", serr)
		}
	}

	if alreadyInstrumented(&live, lang, i.image, serviceName, rec.Location, dest, i.debug()) {
		if err := i.store.SetContainerAppInstrumented(ctx, rec.ResourceID, true); err != nil {
			log.Warn("set container-apps instrumented flag failed", "app", rec.AppName, "error", err)
		}
		i.clearPhase(ctx, rec)
		return nil
	}

	if !rec.Instrumented {
		i.setPhase(ctx, rec, baseapi.PhaseVerifying)
	}
	if err := applyInstrumentation(&live, lang, i.image, serviceName, rec.Location, dest, i.debug()); err != nil {
		return err
	}

	i.setPhase(ctx, rec, baseapi.PhaseInstrumenting)
	log.Info("instrumenting container app", "app", rec.AppName, "location", rec.Location, "language", lang)
	poller, err := client.BeginUpdate(ctx, rg, rec.AppName, live, nil)
	if err != nil {
		return err
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return err
	}
	if err := i.store.SetContainerAppInstrumented(ctx, rec.ResourceID, true); err != nil {
		log.Warn("set container-apps instrumented flag failed", "app", rec.AppName, "error", err)
	}
	return nil
}

func (i *ContainerAppsInstrumentor) rollback(ctx context.Context, client *armappcontainers.ContainerAppsClient, rec store.ContainerApp) error {
	rg := rec.ResourceGroup
	if rg == "" {
		rg = azureclient.ResourceGroupFromID(rec.ResourceID)
	}
	resp, err := client.Get(ctx, rg, rec.AppName, nil)
	if err != nil {
		return err
	}
	live := resp.ContainerApp

	if !appOwned(&live) {
		if err := i.store.SetContainerAppInstrumented(ctx, rec.ResourceID, false); err != nil {
			log.Warn("set container-apps instrumented flag failed", "app", rec.AppName, "error", err)
		}
		return nil
	}
	if !rollbackInstrumentation(&live) {
		if err := i.store.SetContainerAppInstrumented(ctx, rec.ResourceID, false); err != nil {
			log.Warn("set container-apps instrumented flag failed", "app", rec.AppName, "error", err)
		}
		return nil
	}

	log.Info("rolling back container-apps instrumentation", "app", rec.AppName, "location", rec.Location)
	poller, err := client.BeginUpdate(ctx, rg, rec.AppName, live, nil)
	if err != nil {
		return err
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return err
	}
	if err := i.store.SetContainerAppInstrumented(ctx, rec.ResourceID, false); err != nil {
		log.Warn("set container-apps instrumented flag failed", "app", rec.AppName, "error", err)
	}
	i.clearPhase(ctx, rec)
	return nil
}

func (i *ContainerAppsInstrumentor) setPhase(ctx context.Context, rec store.ContainerApp, phase string) {
	if err := i.store.SetContainerAppInstrumentationPhase(ctx, rec.ResourceID, phase); err != nil {
		log.Warn("set container-apps instrumentation phase failed", "app", rec.AppName, "phase", phase, "error", err)
	}
}

func (i *ContainerAppsInstrumentor) clearPhase(ctx context.Context, rec store.ContainerApp) {
	if err := i.store.ClearContainerAppInstrumentationPhase(ctx, rec.ResourceID); err != nil {
		log.Warn("clear container-apps instrumentation phase failed", "app", rec.AppName, "error", err)
	}
}
