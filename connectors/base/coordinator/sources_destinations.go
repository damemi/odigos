package coordinator

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	domainsrc "github.com/odigos-io/odigos-enterprise/connectors/base/domain/source"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	"github.com/odigos-io/odigos-enterprise/connectors/base/store"
)

var apiLog = connlog.Subsystem("coordinator-api")

// ErrDraining is returned by source create/update while a drain (CR-delete cleanup, shutdown, or manual
// RequestCleanup) is in progress. Accepting a mutation then could (re-)enable a source the rollback just
// disabled; because the reconcile loops are being torn down, that source would never be reconciled,
// leaving instrumentation live on a workload after the runtime reports drain-complete. Callers see a
// clear error rather than a silently-orphaned source.
var ErrDraining = errors.New("connector is draining; source create/update is blocked until cleanup completes")

// ErrRegionRequired is returned by CreateSource when no region was provided. A source must be pinned to a
// single regional resource; the connector no longer falls back to matching a discovered workload by
// name+kind alone (which is ambiguous across regions).
var ErrRegionRequired = errors.New("region is required for source creation")

func (c *Coordinator) GetSources(ctx context.Context) ([]domainsrc.Source, error) {
	apiLog.Debug("get sources")
	rows, err := c.store.ListSources(ctx)
	if err != nil {
		apiLog.Error("get sources failed", "error", err)
		return nil, err
	}
	out := make([]domainsrc.Source, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.SourceRecordToDomain(row))
	}
	return out, nil
}

func (c *Coordinator) GetSource(ctx context.Context, name, kind, region string) (*domainsrc.Source, error) {
	apiLog.Debug("get source", "name", name, "kind", kind, "region", region)
	row, err := c.store.FindSource(ctx, name, kind, region)
	if err != nil || row == nil {
		if err != nil {
			apiLog.Error("get source failed", "name", name, "kind", kind, "region", region, "error", err)
		}
		return nil, err
	}
	src := store.SourceRecordToDomain(*row)
	return &src, nil
}

func (c *Coordinator) CreateSource(ctx context.Context, src domainsrc.Source) error {
	apiLog.Info("create source", "name", src.Name, "kind", src.Kind)
	if c.draining.Load() {
		apiLog.Warn("create source rejected: connector is draining", "name", src.Name, "kind", src.Kind)
		return ErrDraining
	}
	if err := c.validateSourceKind(src.Kind); err != nil {
		apiLog.Error("create source validation failed", "name", src.Name, "error", err)
		return fmt.Errorf("createSource: %w", err)
	}
	// Region is required for source creation. Without it the connector cannot pin the source to a single
	// regional resource and previously fell back to matching a discovered workload by name+kind alone
	// (ambiguous across regions); reject instead of guessing.
	if src.Region == "" {
		apiLog.Error("create source rejected: region is required", "name", src.Name, "kind", src.Kind)
		return fmt.Errorf("createSource: %w (source %q kind %s)", ErrRegionRequired, src.Name, src.Kind)
	}
	if slices.Contains(c.computePlatformKinds, src.Kind) {
		return c.createComputePlatformAssignment(ctx, src)
	}
	return c.createWorkloadSource(ctx, src)
}

func (c *Coordinator) validateSourceKind(kind string) error {
	if slices.Contains(c.workloadKinds, kind) || slices.Contains(c.computePlatformKinds, kind) {
		return nil
	}
	return fmt.Errorf("unsupported source kind %q", kind)
}

func (c *Coordinator) createWorkloadSource(ctx context.Context, src domainsrc.Source) error {
	w, err := c.provider.LookupWorkload(ctx, src.Name, src.Kind, src.Region)
	if err != nil {
		apiLog.Error("create source workload lookup failed", "name", src.Name, "error", err)
		return err
	}
	if w == nil {
		apiLog.Warn("create source workload not found", "name", src.Name, "service", src.Name, "kind", src.Kind)
		return fmt.Errorf("createSource: discovered workload not found for service=%s kind=%s", src.Name, src.Kind)
	}
	if provider, ok := c.provider.(api.InstrumentationProvider); ok {
		if err := provider.ValidateSourceInstrumentation(ctx, src, w); err != nil {
			return err
		}
	}
	src.ExternalResourceID = w.ExternalID
	src.Region = w.Region
	if err := c.store.UpsertSource(ctx, src); err != nil {
		apiLog.Error("create source db failed", "name", src.Name, "error", err)
		return err
	}
	apiLog.Info("create source complete", "name", src.Name, "external_id", src.ExternalResourceID)
	c.TriggerCollectorReconcile()
	c.triggerReconcile()
	return nil
}

func (c *Coordinator) createComputePlatformAssignment(ctx context.Context, src domainsrc.Source) error {
	svc, err := c.provider.LookupComputePlatform(ctx, src.Name, src.Kind, src.Region)
	if err != nil {
		apiLog.Error("create source compute-platform lookup failed", "name", src.Name, "error", err)
		return err
	}
	if svc == nil {
		apiLog.Warn("create source service not found", "name", src.Name, "service", src.Name, "kind", src.Kind)
		return fmt.Errorf("createSource: discovered compute platform not found for name=%s kind=%s", src.Name, src.Kind)
	}
	src.ExternalResourceID = svc.ExternalID
	src.Region = svc.Region
	if err := c.store.UpsertSource(ctx, src); err != nil {
		apiLog.Error("create source db failed", "name", src.Name, "error", err)
		return err
	}
	apiLog.Info("create compute-platform assignment complete", "name", src.Name, "external_id", src.ExternalResourceID)
	c.triggerReconcile()
	return nil
}

func (c *Coordinator) UpdateSource(ctx context.Context, src domainsrc.Source) error {
	apiLog.Info("update source", "name", src.Name)
	if c.draining.Load() {
		apiLog.Warn("update source rejected: connector is draining", "name", src.Name, "kind", src.Kind)
		return ErrDraining
	}
	if err := c.validateSourceKind(src.Kind); err != nil {
		apiLog.Error("update source validation failed", "name", src.Name, "error", err)
		return fmt.Errorf("updateSource: %w", err)
	}
	// Resolve the existing row by its source-name handle (+ kind/region to disambiguate). source_name is
	// the display handle; the identity the row is keyed by is kind+external_resource_id.
	current, err := c.store.FindSource(ctx, src.Name, src.Kind, src.Region)
	if err != nil {
		apiLog.Error("update source lookup failed", "name", src.Name, "error", err)
		return err
	}
	if current == nil {
		apiLog.Warn("update source not found", "name", src.Name)
		return fmt.Errorf("updateSource: source not found: name=%s", src.Name)
	}
	// Preserve discovery-derived fields and default any identity component the input omitted.
	src.ExternalResourceID = current.ExternalResourceID
	if src.Kind == "" {
		src.Kind = current.Kind
	}
	if src.Region == "" {
		src.Region = current.Region
	}
	if src.ServiceName == "" {
		src.ServiceName = current.ServiceName
	}
	if provider, ok := c.provider.(api.InstrumentationProvider); ok && !slices.Contains(c.computePlatformKinds, src.Kind) {
		w, err := c.provider.LookupWorkload(ctx, src.Name, src.Kind, src.Region)
		if err != nil {
			return err
		}
		if err := provider.ValidateSourceInstrumentation(ctx, src, w); err != nil {
			return err
		}
	}
	// With identity now keyed on the stable external_resource_id (preserved from the current row above), a
	// service_name/region/enabled change is an in-place update — no move. Only a changed kind (or, in
	// principle, external_resource_id) repoints to a different resource; that move is done in a single
	// transaction (delete-old + upsert-new) so a crash between the two can never orphan the intent.
	if sourceIdentityChanged(*current, src) {
		apiLog.Info("update source identity moved; relocating row",
			"name", src.Name, "old_kind", current.Kind, "new_kind", src.Kind,
			"old_external_id", current.ExternalResourceID, "new_external_id", src.ExternalResourceID)
		if err := c.store.MoveSource(ctx, current.Kind, current.ExternalResourceID, src); err != nil {
			apiLog.Error("update source move failed", "name", src.Name, "error", err)
			return err
		}
	} else if err := c.store.UpsertSource(ctx, src); err != nil {
		apiLog.Error("update source db failed", "name", src.Name, "error", err)
		return err
	}
	apiLog.Info("update source complete", "name", src.Name)
	c.TriggerCollectorReconcile()
	c.triggerReconcile()
	return nil
}

func (c *Coordinator) DeleteSource(ctx context.Context, name, kind, region string) error {
	apiLog.Info("delete source", "name", name, "kind", kind, "region", region)
	// Resolve the exact row first so we delete by its true identity (kind, external_resource_id) — never by
	// assuming name == service_name (which breaks under a service-name override) and never the wrong
	// region's row. An ambiguous name (multiple matches) errors; a name that matches nothing is a no-op.
	current, err := c.store.FindSource(ctx, name, kind, region)
	if err != nil {
		apiLog.Error("delete source lookup failed", "name", name, "kind", kind, "region", region, "error", err)
		return err
	}
	if current == nil {
		apiLog.Info("delete source: no matching row, nothing to delete", "name", name, "kind", kind, "region", region)
		return nil
	}
	if err := c.store.DeleteSourceByKey(ctx, current.Kind, current.ExternalResourceID); err != nil {
		apiLog.Error("delete source db failed", "name", name, "kind", kind, "region", region, "error", err)
		return err
	}
	apiLog.Info("delete source complete", "name", name, "kind", current.Kind, "service", current.ServiceName, "region", current.Region)
	c.TriggerCollectorReconcile()
	c.triggerReconcile()
	return nil
}

// sourceIdentityChanged reports whether the desired source resolves to a different primary-key identity
// (kind, external_resource_id) than the current row. When it does, an update is a move: the old-key row
// must be deleted so the upsert under the new key does not leave an orphan. service_name and region are
// no longer part of the identity, so changing them is an in-place update, not a move.
func sourceIdentityChanged(current store.SourceRecord, desired domainsrc.Source) bool {
	return desired.Kind != current.Kind ||
		desired.ExternalResourceID != current.ExternalResourceID
}

func (c *Coordinator) GetDestinations(ctx context.Context) ([]domaindest.Destination, error) {
	apiLog.Debug("get destinations")
	rows, err := c.store.ListDestinations(ctx)
	if err != nil {
		apiLog.Error("get destinations failed", "error", err)
		return nil, err
	}
	out := make([]domaindest.Destination, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.DestinationRecordToDomain(row))
	}
	return out, nil
}

func (c *Coordinator) GetDestination(ctx context.Context, name string) (*domaindest.Destination, error) {
	apiLog.Debug("get destination", "name", name)
	row, err := c.store.GetDestination(ctx, name)
	if err != nil || row == nil {
		if err != nil {
			apiLog.Error("get destination failed", "name", name, "error", err)
		}
		return nil, err
	}
	dest := store.DestinationRecordToDomain(*row)
	return &dest, nil
}

func (c *Coordinator) CreateDestination(ctx context.Context, dest domaindest.Destination) error {
	apiLog.Info("create destination", "name", dest.Name, "type", dest.Type)
	if err := c.validateDestinationSet(ctx, dest); err != nil {
		apiLog.Error("create destination validation failed", "name", dest.Name, "error", err)
		return fmt.Errorf("createDestination: %w", err)
	}
	if err := c.store.UpsertDestination(ctx, dest); err != nil {
		apiLog.Error("create destination db failed", "name", dest.Name, "error", err)
		return err
	}
	apiLog.Info("create destination complete", "name", dest.Name)
	c.TriggerCollectorReconcile()
	c.triggerWorkloadValidation()
	return nil
}

func (c *Coordinator) UpdateDestination(ctx context.Context, dest domaindest.Destination) error {
	apiLog.Info("update destination", "name", dest.Name)
	if err := c.validateDestinationSet(ctx, dest); err != nil {
		apiLog.Error("update destination validation failed", "name", dest.Name, "error", err)
		return fmt.Errorf("updateDestination: %w", err)
	}
	if err := c.store.UpsertDestination(ctx, dest); err != nil {
		apiLog.Error("update destination db failed", "name", dest.Name, "error", err)
		return err
	}
	apiLog.Info("update destination complete", "name", dest.Name)
	c.TriggerCollectorReconcile()
	c.triggerWorkloadValidation()
	return nil
}

// validateDestinationSet validates the prospective complete set, replacing any existing row with the
// same name. Marker uniqueness is a set invariant and cannot be enforced by validating only the row
// being written. The database also has a partial unique index as the concurrent-write backstop.
func (c *Coordinator) validateDestinationSet(ctx context.Context, desired domaindest.Destination) error {
	rows, err := c.store.ListDestinations(ctx)
	if err != nil {
		return fmt.Errorf("list destinations for validation: %w", err)
	}
	dests := make([]domaindest.Destination, 0, len(rows)+1)
	for _, row := range rows {
		if row.Name != desired.Name {
			dests = append(dests, store.DestinationRecordToDomain(row))
		}
	}
	dests = append(dests, desired)
	catalog, err := c.destinationCatalog(ctx)
	if err != nil {
		return err
	}
	// Live installation state applies to the row being created or updated. An older marker is allowed
	// to remain after its Collector is removed so routing can perform the required direct-export rollback;
	// it must not prevent an unrelated terminal destination from being repaired meanwhile.
	if err := catalog.ValidateWithCatalog(desired); err != nil {
		return err
	}
	return c.destCatalog.ValidateDestinations(dests)
}

func (c *Coordinator) DeleteDestination(ctx context.Context, name string) error {
	apiLog.Info("delete destination", "name", name)
	if err := c.store.DeleteDestination(ctx, name); err != nil {
		apiLog.Error("delete destination db failed", "name", name, "error", err)
		return err
	}
	apiLog.Info("delete destination complete", "name", name)
	c.TriggerCollectorReconcile()
	c.triggerWorkloadValidation()
	return nil
}

// RevalidateWorkloads asks both sides of the workload contract to re-evaluate their
// current route. Automatic workloads converge a changed endpoint; manual (IaC)
// workloads are read back and report collectorExport drift instead. It is intentionally
// asynchronous: destination operations must not wait for every cloud workload to roll.
//
// Destination tests use this too. A successful test can make a previously unavailable
// managed-Collector destination actionable, and a re-test is an explicit request to
// re-evaluate the route rather than wait for the periodic discovery interval.
func (c *Coordinator) RevalidateWorkloads(_ context.Context) error {
	// Refresh topology and converge sender admission too: a workload-only retry
	// would reuse the cached evidence that caused the unreachable verdict.
	c.TriggerCollectorReconcile()
	c.triggerWorkloadValidation()
	return nil
}

func (c *Coordinator) triggerWorkloadValidation() {
	c.triggerReconcile()
	c.triggerObserve()
}

func (c *Coordinator) DestinationCatalog() domaindest.Catalog {
	return c.destCatalog
}

// DestinationCatalogFor applies live Collector applicability to the synthetic regional marker.
func (c *Coordinator) DestinationCatalogFor(ctx context.Context) (domaindest.Catalog, error) {
	return c.destinationCatalog(ctx)
}

func (c *Coordinator) destinationCatalog(ctx context.Context) (domaindest.Catalog, error) {
	if !c.destCatalog.IsSupported(domaindest.CatalogTypeManagedCollector) {
		return c.destCatalog, nil
	}
	records, err := c.store.ListManagedCollectors(ctx)
	if err != nil {
		return c.destCatalog, fmt.Errorf("list managed Collectors for destination applicability: %w", err)
	}
	regions := make([]string, 0, len(records))
	for _, rec := range records {
		if rec.CollectorID != "" && (rec.EndpointGRPC != "" || rec.EndpointHTTP != "") {
			regions = append(regions, rec.Key.Region)
		}
	}
	return c.destCatalog.WithInstalledManagedCollectorRegions(regions), nil
}

// OTLPExportTarget resolves the connector's OTLP export target (endpoint + which signals the destination
// accepts). Connector-wide: provider instrumentation exports only the supported signals so it never sends
// a signal the destination doesn't implement (e.g. metrics/logs to a traces-only backend).
//
// Equivalent to OTLPRouting with no network. Prefer OTLPRouting where the workload's network is known.
func (c *Coordinator) OTLPExportTarget(ctx context.Context) (domaindest.OTLPTarget, bool, error) {
	return c.store.OTLPExportTarget(ctx)
}

// OTLPRouting resolves where a workload on `network` exports, and why.
//
// The one resolution plans, instrumentors and observers share. Reporting reads the same answer
// applying reads, so a workload can never be described as being on a route it is not on. Pass an empty
// network for a workload that has none — a Lambda without VpcConfig, which cannot reach an internal
// endpoint at all.
func (c *Coordinator) OTLPRouting(ctx context.Context, network string) (domaindest.Routing, error) {
	return c.store.OTLPRouting(ctx, network)
}

// OTLPRoutingForRegion is the routing contract for regional workload reconcilers. Cross-VPC callers
// must pass explicit provider reachability evidence; region equality alone is never sufficient.
func (c *Coordinator) OTLPRoutingForRegion(ctx context.Context, region, network string, reachability domaindest.Reachability) (domaindest.Routing, error) {
	return c.store.OTLPRoutingForRegion(ctx, region, network, reachability)
}
