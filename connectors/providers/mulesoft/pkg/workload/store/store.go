// Package store (workloadstore) is the WORKLOAD inventory persistence for the MuleSoft connector: Mule
// applications on CloudHub 2.0, Runtime Fabric and hybrid servers, in one connector_mulesoft.applications table
// with a kind column. It runs over the shared base store's pool, which also owns sources and destinations.
package store

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	connlog "github.com/odigos-io/odigos-enterprise/connectors/base/log"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/types"
)

var inventoryLog = connlog.Subsystem("mulesoft-workload-store")

// Workload kinds. Kind strings match the spec type names so the coordinator resolves a workload's
// instrumentation availability from the spec.
const (
	KindCloudHub2    = types.CloudHub2
	KindPrivateSpace = types.PrivateSpace
	KindRTF          = types.RTF
	KindHybrid       = types.Hybrid
	KindOmniGateway  = types.OmniGateway
)

// SupportedKinds returns the workload kinds this connector publishes.
func SupportedKinds() []string {
	return []string{KindCloudHub2, KindPrivateSpace, KindRTF, KindHybrid, KindOmniGateway}
}

// Gateway target types: who runs an Omni Gateway, which decides whether Odigos can change its configuration.
const (
	GatewayManaged     = "managed"
	GatewaySelfManaged = "self-managed"
)

// Omni Gateway markers are literal tracing labels, the only thing on a gateway's tracing configuration that
// carries a name Odigos chooses. A literal label is also added to every span the gateway emits, so the values
// are short.
const (
	// GatewayManagedLabel is the label Odigos adds when it turns tracing on; its presence is the right to undo it.
	// The configuration it replaced is kept in the connector's own database (Application.GatewaySnapshot).
	GatewayManagedLabel = "odigos.managed"
	// GatewayIaCLabel is the label the published manual instructions add.
	GatewayIaCLabel = "odigos.iac"
)

// GatewayOrigin reports which Odigos label a gateway's tracing configuration carries (see MarkerOrigin for
// applications). Labels on a tracing configuration that is turned off count for nothing.
func GatewayOrigin(tracing map[string]any) string {
	if enabled, _ := tracing["enabled"].(bool); !enabled {
		return ""
	}
	switch {
	case GatewayHasLabel(tracing, GatewayManagedLabel):
		return OriginConnector
	case GatewayHasLabel(tracing, GatewayIaCLabel):
		return OriginExternal
	}
	return ""
}

// GatewayHasLabel reports whether a tracing configuration carries a label of this name.
func GatewayHasLabel(tracing map[string]any, name string) bool {
	labels, _ := tracing["labels"].([]any)
	for _, l := range labels {
		if m, ok := l.(map[string]any); ok && m["name"] == name {
			return true
		}
	}
	return false
}

// The two markers a Mule application may carry, as APPLICATION PROPERTIES (Mule apps have no environment the
// connector can write). They keep the names the env-var platforms use so every connector reads them the same way.
const (
	// PropOdigosManaged marks an application instrumented by THIS connector and carries, in its JSON value, the
	// pre-Odigos value of every property the connector changed so rollback restores the application exactly. It
	// is the single authority on ownership: the right to change the application's telemetry properties and to
	// roll them back. A customer must never write it.
	PropOdigosManaged = "ODIGOS_MANAGED"
	// PropOdigosIaC is the marker a CUSTOMER writes when they apply the published manual instructions. It
	// answers "is this instrumented by Odigos" and never "may Odigos change this".
	PropOdigosIaC = api.EnvOdigosIaC
)

// Instrumentation origins recorded on a row, matching api.DiscoveredWorkload.InstrumentationOrigin.
const (
	OriginConnector = "connector"
	OriginExternal  = "external"
)

// Store is the workload inventory store.
type Store struct {
	*basestore.Store
}

// New builds the workload store over an already-open base store.
func New(b *basestore.Store) *Store { return &Store{Store: b} }

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// MarkerOrigin reports which Odigos marker an application's properties carry: OriginConnector for this
// connector's PropOdigosManaged record for this deployment, OriginExternal for PropOdigosIaC, "" for neither.
// A marker set as a secure property still counts. Either marker means instrumented; only the connector's own
// grants the right to change the application.
//
// A PropOdigosManaged value that is not this deployment's record counts as OriginExternal: one copied by
// promoting an instrumented application, one someone edited until it no longer parses, or one set as a secure
// property, whose value cannot be read. The instrumentor refuses to change or roll back any of these, so
// calling them the connector's would only have the reconciler re-read the deployment every pass and flip the
// instrumented flag back after each discovery.
func MarkerOrigin(props map[string]string, secureKeys []string, deploymentID string) string {
	has := func(k string) bool {
		if _, ok := props[k]; ok {
			return true
		}
		for _, s := range secureKeys {
			if s == k {
				return true
			}
		}
		return false
	}
	switch {
	case has(PropOdigosManaged) && markerIsFor(props[PropOdigosManaged], deploymentID):
		return OriginConnector
	case has(PropOdigosManaged), has(PropOdigosIaC):
		return OriginExternal
	}
	return ""
}

// markerIsFor reports whether a PropOdigosManaged value is the connector's manifest for this deployment. It
// reads only the two fields that decide it, the same way the instrumentor's ownership check does: a manifest
// without prevProps records nothing, and one naming another deployment was copied. A manifest naming no
// deployment predates the field and is accepted.
func markerIsFor(v, deploymentID string) bool {
	var m struct {
		Deployment string             `json:"dep"`
		PrevProps  map[string]*string `json:"prevProps"`
	}
	if json.Unmarshal([]byte(v), &m) != nil || m.PrevProps == nil {
		return false
	}
	return m.Deployment == "" || m.Deployment == deploymentID
}
