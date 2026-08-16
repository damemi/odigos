// Package detector implements WORKLOAD discovery for the GCP connector — Cloud Run services, the
// resources instrumented directly via a Source. It enumerates every location in the project in a single
// call (locations wildcard) and persists the workload store inventory the base reports as
// WorkloadReports. Compute-platform (managed-infra) discovery lives in the sibling
// computeplatform/detector package.
package detector

import (
	"context"
	"strings"
	"time"

	runpb "cloud.google.com/go/run/apiv2/runpb"
	"google.golang.org/api/iterator"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/gcpclient"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/workload/store"
)

var log = connlog.Subsystem("gcp-workload-detector")

// envOdigosManaged is the sole ownership signal, defined in the workload store — shared with the
// instrumentor that writes it, so reader and writer cannot drift (see store.EnvOdigosManaged).
const envOdigosManaged = store.EnvOdigosManaged

// CloudRunDetector discovers Cloud Run services across every location in the project.
type CloudRunDetector struct {
	store   *store.Store
	clients *gcpclient.Clients
}

// NewCloudRunDetector builds the Cloud Run detector over the workload store and shared client factory.
func NewCloudRunDetector(st *store.Store, clients *gcpclient.Clients) *CloudRunDetector {
	return &CloudRunDetector{store: st, clients: clients}
}

func (d *CloudRunDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	scanStart := time.Now()
	log.Info("cloud run detector starting", "project", d.clients.ProjectID())

	client, err := d.clients.CloudRun(ctx)
	if err != nil {
		return baseapi.DetectResult{}, err
	}
	defer client.Close()

	it := client.ListServices(ctx, &runpb.ListServicesRequest{Parent: d.clients.CloudRunParent()})
	upsertFailed := false
	count := 0
	for {
		svc, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Error("cloud run list services failed", "error", err)
			return baseapi.DetectResult{}, err
		}
		rec := toCloudRunService(svc)
		if err := d.store.UpsertCloudRunService(ctx, rec); err != nil {
			log.Error("upsert cloud run service failed", "service", rec.ServiceID, "error", err)
			upsertFailed = true
			continue
		}
		count++
	}

	_ = d.store.UpdateDiscoveryState(ctx, gcpclient.LocationWildcard, store.KindCloudRun, scanStart)

	// Prune only when every discovered service persisted this cycle; pruning after a failed upsert would
	// soft-delete a still-live service whose last_seen_at wasn't refreshed this scan.
	if upsertFailed {
		log.Error("skipping cloud run prune after upsert failure")
	} else if err := d.store.Prune(ctx, scanStart); err != nil {
		log.Error("prune cloud run inventory failed", "error", err)
	}
	if err := d.store.PurgeDeleted(ctx, time.Now().Add(-store.SoftDeleteRetention)); err != nil {
		log.Error("purge deleted cloud run inventory failed", "error", err)
	}

	log.Info("cloud run detector complete", "count", count)
	return baseapi.DetectResult{Authenticated: true}, nil
}

// toCloudRunService maps a Cloud Run API service to an inventory record, parsing the location + service
// id from the resource name and deriving live instrumentation state from the revision template env.
func toCloudRunService(svc *runpb.Service) store.CloudRunService {
	location, serviceID := parseServiceName(svc.GetName())
	meta := map[string]any{
		"uri":            svc.GetUri(),
		"latestRevision": svc.GetLatestReadyRevision(),
	}
	return store.CloudRunService{
		Location:     location,
		ResourceName: svc.GetName(),
		ServiceID:    serviceID,
		UID:          svc.GetUid(),
		Ingress:      svc.GetIngress().String(),
		Instrumented: odigosInstrumented(svc),
		Metadata:     meta,
	}
}

// parseServiceName splits a Cloud Run resource name (projects/{p}/locations/{loc}/services/{svc}) into
// its location and short service id.
func parseServiceName(name string) (location, serviceID string) {
	parts := strings.Split(name, "/")
	for i := 0; i+1 < len(parts); i++ {
		switch parts[i] {
		case "locations":
			location = parts[i+1]
		case "services":
			serviceID = parts[i+1]
		}
	}
	return location, serviceID
}

// odigosInstrumented reports whether a Cloud Run service's revision template shows instrumentation owned by
// THIS connector. A customer's own OpenTelemetry setup is deliberately not treated as ours.
//
// The ownership marker is the ONLY signal, deliberately. It is also what gates every mutation
// (instrumentor.serviceOwned) and what rollback restores from, so deriving the flag from anything else would
// let discovery and the instrumentor disagree: a service discovery called ours but the mutation gate did not
// would flip between instrumented and not on every cycle, one write each, and never settle. Other
// odigos-namespaced vars we set (store.EnvCollectorEndpoint) are values we own, not proof of ownership.
func odigosInstrumented(svc *runpb.Service) bool {
	tmpl := svc.GetTemplate()
	if tmpl == nil {
		return false
	}
	for _, c := range tmpl.GetContainers() {
		for _, e := range c.GetEnv() {
			if e.GetName() == envOdigosManaged {
				return true
			}
		}
	}
	return false
}
