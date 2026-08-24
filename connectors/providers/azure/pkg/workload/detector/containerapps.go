// Package detector implements WORKLOAD discovery for the Azure connector — Container Apps, the
// resources instrumented directly via a Source. It enumerates every Container App in the subscription
// via list-by-subscription and persists the workload store inventory the base reports as
// WorkloadReports. Compute-platform (managed-infra) discovery lives in the sibling
// computeplatform/detector package.
package detector

import (
	"context"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appcontainers/armappcontainers/v3"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/azureclient"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/azure/pkg/workload/store"
)

var log = connlog.Subsystem("azure-workload-detector")

// The two markers a discovered app may carry, both defined in the workload store — shared with the
// instrumentor that writes one of them, so reader and writer cannot drift.
const (
	// envOdigosManaged is written by the connector when IT instruments an app (automatic mode).
	envOdigosManaged = store.EnvOdigosManaged
	// envOdigosIaC is written by the CUSTOMER's own deployment definition in manual (IaC) mode, from the
	// instructions this connector publishes. Nothing else about the app differs.
	envOdigosIaC = store.EnvOdigosIaC
)

// ContainerAppsDetector discovers Azure Container Apps across the subscription.
type ContainerAppsDetector struct {
	store   *store.Store
	clients *azureclient.Clients
}

// NewContainerAppsDetector builds the Container Apps detector over the workload store and shared client factory.
func NewContainerAppsDetector(st *store.Store, clients *azureclient.Clients) *ContainerAppsDetector {
	return &ContainerAppsDetector{store: st, clients: clients}
}

func (d *ContainerAppsDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	scanStart := time.Now()
	log.Info("container apps detector starting", "subscription", d.clients.SubscriptionID())

	client, err := d.clients.ContainerApps()
	if err != nil {
		return baseapi.DetectResult{}, err
	}

	pager := client.NewListBySubscriptionPager(nil)
	upsertFailed := false
	count := 0
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Error("container apps list failed", "error", err)
			return baseapi.DetectResult{}, err
		}
		for _, app := range page.Value {
			if app == nil {
				continue
			}
			rec := toContainerApp(app)
			if err := d.store.UpsertContainerApp(ctx, rec); err != nil {
				log.Error("upsert container app failed", "app", rec.AppName, "error", err)
				upsertFailed = true
				continue
			}
			count++
		}
	}

	_ = d.store.UpdateDiscoveryState(ctx, azureclient.LocationWildcard, store.KindContainerApps, scanStart)

	// Prune only when every discovered app persisted this cycle; pruning after a failed upsert would
	// soft-delete a still-live app whose last_seen_at wasn't refreshed this scan.
	if upsertFailed {
		log.Error("skipping container apps prune after upsert failure")
	} else if err := d.store.Prune(ctx, scanStart); err != nil {
		log.Error("prune container apps inventory failed", "error", err)
	}
	if err := d.store.PurgeDeleted(ctx, time.Now().Add(-store.SoftDeleteRetention)); err != nil {
		log.Error("purge deleted container apps inventory failed", "error", err)
	}

	log.Info("container apps detector complete", "count", count)
	return baseapi.DetectResult{Authenticated: true}, nil
}

// toContainerApp maps an ARM Container App to an inventory record.
func toContainerApp(app *armappcontainers.ContainerApp) store.ContainerApp {
	resourceID := ""
	if app.ID != nil {
		resourceID = *app.ID
	}
	name := ""
	if app.Name != nil {
		name = *app.Name
	}
	location := ""
	if app.Location != nil {
		location = *app.Location
	}
	provState := ""
	if app.Properties != nil && app.Properties.ProvisioningState != nil {
		provState = string(*app.Properties.ProvisioningState)
	}
	meta := map[string]any{}
	if app.Properties != nil && app.Properties.LatestReadyRevisionName != nil {
		meta["latestReadyRevision"] = *app.Properties.LatestReadyRevisionName
	}
	if app.Properties != nil && app.Properties.Configuration != nil && app.Properties.Configuration.Ingress != nil &&
		app.Properties.Configuration.Ingress.Fqdn != nil {
		meta["fqdn"] = *app.Properties.Configuration.Ingress.Fqdn
	}
	return store.ContainerApp{
		Location:          location,
		ResourceGroup:     azureclient.ResourceGroupFromID(resourceID),
		ResourceID:        resourceID,
		AppName:           name,
		ProvisioningState: provState,
		Instrumented:      odigosInstrumented(app),
		Metadata:          meta,
	}
}

// odigosInstrumented reports whether a Container App's template shows Odigos instrumentation. A customer's own
// OpenTelemetry setup is deliberately not treated as ours.
//
// EITHER marker counts, and the pair is exhaustive: the connector writes the ownership marker when it performs
// the mutation, and the customer's own deployment definition writes the IaC marker when they applied the
// published instructions. Recognizing only the first is what made manual mode unreportable — an app with every
// fragment correctly applied has no ownership marker and never will, so it read as untouched and the report told
// the customer to go and deploy what they had already deployed.
//
// Widening THIS does not widen the mutation gate (instrumentor.appOwned), which still requires the ownership
// marker alone. That asymmetry is the point rather than an inconsistency: the two answer different questions, so
// they cannot disagree in the way a second ownership signal would — discovery reports what is applied, the gate
// authorizes changing it, and an IaC-marked app is reported and never touched. Other odigos-namespaced vars we
// set (store.EnvCollectorEndpoint) are values we own, not proof of anything.
func odigosInstrumented(app *armappcontainers.ContainerApp) bool {
	if app.Properties == nil || app.Properties.Template == nil {
		return false
	}
	for _, c := range app.Properties.Template.Containers {
		if c == nil {
			continue
		}
		for _, e := range c.Env {
			if e == nil || e.Name == nil {
				continue
			}
			if *e.Name == envOdigosManaged || *e.Name == envOdigosIaC {
				return true
			}
		}
	}
	return false
}
