package detector

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	computepb "cloud.google.com/go/compute/apiv1/computepb"
	"google.golang.org/api/iterator"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/computeplatform/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/gcpclient"
)

// GCEInstanceDetector discovers Compute Engine instances across every zone in the project via the
// aggregated list API (one call spans all zones).
type GCEInstanceDetector struct {
	store   *store.Store
	clients *gcpclient.Clients
}

// NewGCEInstanceDetector builds the GCE detector over the compute-platform store and shared client factory.
func NewGCEInstanceDetector(st *store.Store, clients *gcpclient.Clients) *GCEInstanceDetector {
	return &GCEInstanceDetector{store: st, clients: clients}
}

func (d *GCEInstanceDetector) Detect(ctx context.Context) (baseapi.DetectResult, error) {
	// The prune cutoff comes from the database clock, the one last_seen_at is written with.
	scanStart, err := d.store.Now(ctx)
	if err != nil {
		return baseapi.DetectResult{}, fmt.Errorf("read database clock: %w", err)
	}
	log.Info("gce instance detector starting", "project", d.clients.ProjectID())

	client, err := d.clients.GCEInstances(ctx)
	if err != nil {
		return baseapi.DetectResult{}, err
	}
	defer client.Close()

	it := client.AggregatedList(ctx, &computepb.AggregatedListInstancesRequest{Project: d.clients.ProjectID()})
	upsertFailed := false
	count := 0
	for {
		pair, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Error("gce aggregated list failed", "error", err)
			return baseapi.DetectResult{}, err
		}
		for _, inst := range pair.Value.GetInstances() {
			rec := store.GCEInstance{
				Location:     lastSegment(inst.GetZone()),
				ResourceName: inst.GetSelfLink(),
				InstanceName: inst.GetName(),
				InstanceID:   strconv.FormatUint(inst.GetId(), 10),
				Status:       inst.GetStatus(),
				Metadata: map[string]any{
					"cpuPlatform": inst.GetCpuPlatform(),
				},
			}
			if rec.ResourceName == "" {
				// Fall back to a stable composed identity if the API omits the self link.
				rec.ResourceName = "zones/" + rec.Location + "/instances/" + rec.InstanceName
			}
			if err := d.store.UpsertGCEInstance(ctx, rec); err != nil {
				log.Error("upsert gce instance failed", "instance", rec.InstanceName, "error", err)
				upsertFailed = true
				continue
			}
			count++
		}
	}

	_ = d.store.UpdateDiscoveryState(ctx, gcpclient.LocationWildcard, store.KindGCE, scanStart)
	if upsertFailed {
		log.Error("skipping gce prune after upsert failure")
	} else if err := d.store.PruneGCE(ctx, scanStart); err != nil {
		log.Error("prune gce inventory failed", "error", err)
	}

	log.Info("gce instance detector complete", "instances", count)
	return baseapi.DetectResult{Authenticated: true}, nil
}

// lastSegment returns the final path segment of a GCP resource URL (e.g. a zone or machine-type URL),
// which is the human-facing short name.
func lastSegment(url string) string {
	if i := strings.LastIndex(url, "/"); i >= 0 {
		return url[i+1:]
	}
	return url
}
