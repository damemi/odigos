// Package collectordestination is the vocabulary of the destinations a managed Collector owns: which
// types it offers, what a valid one looks like, and how one crosses the GraphQL boundary.
//
// These are not the workload destinations (domain/destination's Catalog and the connector's
// destinations table). A workload destination says where a workload's telemetry goes — possibly to a
// regional Collector, through the route marker. A Collector destination says where that Collector sends
// what it receives. The value type is shared, destination.Destination with Region naming the Collector,
// because the shape is the same; the rules are not.
//
// Pure — no I/O. The catalog is the Odigos destination catalog compiled into this binary, limited to the
// types collectorconfig can render into components the Collector image ships, so a type is offered only
// when a destination of it would actually run.
package collectordestination

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/odigos-io/agents-api/go/server/graph/model"
	"github.com/odigos-io/odigos/common"
	odigosdest "github.com/odigos-io/odigos/destinations"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorconfig"
	"github.com/odigos-io/odigos/connectors/base/domain/destination"
)

// namePattern is the grammar of a Collector component id suffix. The name becomes part of every
// exporter and pipeline the destination renders to (e.g. otlp_grpc/honeycomb-<name>), and a name the
// Collector cannot parse would make it refuse the whole document.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Signals a managed Collector can export. PROFILES is absent on purpose: its receivers accept traces,
// metrics and logs, so a profiles pipeline would be declared and never fed.
var collectorSignals = map[string]bool{
	string(common.TracesObservabilitySignal):  true,
	string(common.MetricsObservabilitySignal): true,
	string(common.LogsObservabilitySignal):    true,
}

// Types returns every destination type a managed Collector can export to, ordered by type key: the Odigos
// destination catalog, limited to the types collectorconfig.SupportsType allows and that export at least
// one signal a Collector carries.
//
// A type the Collector cannot run would be accepted, stored, and then left out of its configuration —
// so it is not offered at all.
func Types() ([]*model.DestinationType, error) {
	if err := odigosdest.Load(); err != nil {
		return nil, fmt.Errorf("load destination catalog: %w", err)
	}
	entries := odigosdest.Get()
	out := make([]*model.DestinationType, 0, len(entries))
	for _, entry := range entries {
		if !collectorconfig.SupportsType(string(entry.Metadata.Type)) {
			continue
		}
		// A profiles-only type (pyroscope) could never be created here: PROFILES is not a signal a
		// Collector exports, so its every destination would be refused.
		signals := entry.Spec.Signals
		if !signals.Traces.Supported && !signals.Metrics.Supported && !signals.Logs.Supported {
			continue
		}
		out = append(out, destination.DestinationTypeFromCatalog(entry))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

// NormalizeType is the stored form of a type key: the catalog's keys are lower case, and a
// case-different spelling of the same type must not become a second one.
func NormalizeType(t string) string { return strings.ToLower(strings.TrimSpace(t)) }

// Validate checks one Collector destination completely: its address, its type, its fields and its
// signals. Whether its region actually has a Collector is the caller's question — this package holds no
// state.
func Validate(d destination.Destination) error {
	if strings.TrimSpace(d.Region) == "" {
		return fmt.Errorf("a managed Collector destination needs the region of its Collector")
	}
	if strings.TrimSpace(d.Region) != d.Region {
		return fmt.Errorf("region %q must not contain leading or trailing whitespace", d.Region)
	}
	if !namePattern.MatchString(d.Name) {
		return fmt.Errorf("destination name %q may contain only letters, digits, '-' and '_': it names the "+
			"Collector components this destination renders to", d.Name)
	}
	if destination.IsManagedCollectorType(d.Type) {
		return fmt.Errorf("destination type %q routes workloads to a Collector; it is not something a "+
			"Collector exports to", d.Type)
	}
	if len(d.Signals) == 0 {
		return fmt.Errorf("destination %q needs at least one signal", d.Name)
	}
	for _, s := range d.Signals {
		if !collectorSignals[s] {
			return fmt.Errorf("signal %q is not exported by a managed Collector (supported: TRACES, METRICS, LOGS)", s)
		}
	}
	if !collectorconfig.SupportsType(NormalizeType(d.Type)) {
		return fmt.Errorf("destination type %q is not available to a managed Collector", d.Type)
	}
	if err := destination.ValidateOdigosCatalogFields(d); err != nil {
		return err
	}
	// Last, because it is the one check that renders: configurers reject bad field values only inside
	// ModifyConfig, so a value the catalog accepts can still be one the Collector never could run.
	return collectorconfig.CheckRenderable(d)
}

// FromCreateInput builds a Collector destination from GraphQL create input.
func FromCreateInput(in model.CreateManagedCollectorDestinationInput) destination.Destination {
	return destination.Destination{
		Region:  in.Region,
		Name:    in.Name,
		Type:    NormalizeType(in.Type),
		Config:  copyConfig(in.Config),
		Signals: signals(in.Signals),
	}
}

// ApplyUpdateInput merges a partial update onto an existing destination. An omitted field keeps its
// value; a given config replaces the whole map, because merging key by key would make a field
// impossible to clear.
func ApplyUpdateInput(d destination.Destination, in model.UpdateManagedCollectorDestinationInput) destination.Destination {
	if in.Type != nil {
		d.Type = NormalizeType(*in.Type)
	}
	if in.Config != nil {
		d.Config = copyConfig(in.Config)
	}
	if in.Signals != nil {
		d.Signals = signals(in.Signals)
	}
	return d
}

// ToDestroyPayload is the GraphQL answer to a destroy.
func ToDestroyPayload(region, name string, deleted bool) *model.DestroyManagedCollectorDestinationPayload {
	return &model.DestroyManagedCollectorDestinationPayload{Region: region, Name: name, Deleted: deleted}
}

func copyConfig(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func signals(in []model.TelemetrySignal) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, string(s))
	}
	return out
}
