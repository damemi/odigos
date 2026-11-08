// Package instrumentor holds the MuleSoft workload instrumentation reconciler. A Mule application is
// instrumented by turning on Mule's Direct Telemetry Stream — the runtime's own OTLP exporter — through
// mule.openTelemetry.* application properties on its CloudHub 2.0 or Runtime Fabric deployment. There is no
// agent, image or sidecar: the properties ARE the instrumentation, which is why every function that decides
// what they should be lives in this file, as pure code, and the reconciler only reads and writes them.
package instrumentor

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/anypoint"
	domaindest "github.com/odigos-io/odigos-enterprise/connectors/base/domain/destination"
	store "github.com/odigos-io/odigos-enterprise/connectors/providers/mulesoft/pkg/workload/store"
)

// Direct Telemetry Stream property names (https://docs.mulesoft.com/mule-runtime/latest/otel-support).
const (
	propPrefix = "mule.openTelemetry."

	PropTracesEnabled  = "mule.openTelemetry.tracer.exporter.enabled"
	PropTracesType     = "mule.openTelemetry.tracer.exporter.type"
	PropTracesEndpoint = "mule.openTelemetry.tracer.exporter.endpoint"
	PropSampler        = "mule.openTelemetry.tracer.exporter.sampler"
	PropSamplerArg     = "mule.openTelemetry.tracer.exporter.sampler.arg"
	PropLogsEnabled    = "mule.openTelemetry.logging.exporter.enabled"
	PropLogsType       = "mule.openTelemetry.logging.exporter.type"
	PropLogsEndpoint   = "mule.openTelemetry.logging.exporter.endpoint"
	PropMetricsEnabled = "mule.openTelemetry.meter.exporter.enabled"
	PropMetricsType    = "mule.openTelemetry.meter.exporter.type"
	PropMetricsEnd     = "mule.openTelemetry.meter.exporter.endpoint"
	PropServiceName    = "mule.openTelemetry.exporter.resource.service.name"
	// PropResourceAttributes is the one property MERGED rather than set: a customer's own attributes stay, and
	// ours are appended after them with a comma.
	PropResourceAttributes = "mule.openTelemetry.exporter.resource.attributes"
	// PropLogCorrelation puts the trace and span ids in the log MDC, so exported logs carry them.
	PropLogCorrelation = "mule.put.trace.id.and.span.id.in.mdc"
)

const samplerParentBasedRatio = "parentbased_traceidratio"

// managedKeys is every property the connector may set, in addition to PropResourceAttributes and the marker.
// Their pre-Odigos values are snapshotted on first instrument; a key the current destination does not need is
// restored to that snapshot rather than left behind, so a destination that drops a signal turns it back off.
var managedKeys = []string{
	PropTracesEnabled, PropTracesType, PropTracesEndpoint, PropSampler, PropSamplerArg,
	PropLogsEnabled, PropLogsType, PropLogsEndpoint,
	PropMetricsEnabled, PropMetricsType, PropMetricsEnd,
	PropServiceName, PropLogCorrelation,
}

// Workload is what the desired properties depend on, besides the destination.
type Workload struct {
	ServiceName     string
	EnvironmentName string
	OrgID           string
	TargetType      string
	Version         anypoint.MuleVersion
	// API is the API Manager instance the application pairs with, or nil. Its id, asset and version ride on
	// every span as resource attributes, so traces can be found by the API they served.
	API *store.APIRef
}

// Desired is the property set a workload should carry: Props are set verbatim, Attributes is appended to the
// customer's own PropResourceAttributes.
type Desired struct {
	Props      map[string]string
	Attributes string
}

// metricsMinMinor is the Mule 4 minor that added metrics export; traces and logs need 4.11.
const (
	minMinor        = 11
	metricsMinMinor = 12
)

// Eligible reports whether a Mule runtime supports Direct Telemetry Stream at all.
func Eligible(v anypoint.MuleVersion) bool { return v.AtLeast(4, minMinor) }

// DesiredProperties computes the Direct Telemetry Stream properties for a workload and destination.
//
// gRPC is used whenever the destination has a gRPC endpoint, with the scheme-qualified form Mule's exporter
// needs (a bare host:port is accepted and then exports nowhere); an HTTP-only destination gets OTLP/HTTP with
// the per-signal path Mule's HTTP exporter expects.
func DesiredProperties(w Workload, dest domaindest.OTLPTarget, sampleRatio float64) Desired {
	props := map[string]string{
		PropServiceName:    w.ServiceName,
		PropLogCorrelation: "true",
	}
	typ, endpoint := exporterFor(dest)
	signals := []struct {
		on                 bool
		enabled, kind, url string
		path               string
	}{
		{dest.Traces, PropTracesEnabled, PropTracesType, PropTracesEndpoint, "/v1/traces"},
		{dest.Logs, PropLogsEnabled, PropLogsType, PropLogsEndpoint, "/v1/logs"},
		{dest.Metrics && w.Version.AtLeast(4, metricsMinMinor), PropMetricsEnabled, PropMetricsType, PropMetricsEnd, "/v1/metrics"},
	}
	for _, s := range signals {
		if !s.on {
			continue
		}
		props[s.enabled] = "true"
		props[s.kind] = typ
		if typ == "HTTP" {
			props[s.url] = endpoint + s.path
		} else {
			props[s.url] = endpoint
		}
	}
	if dest.Traces {
		props[PropSampler] = samplerParentBasedRatio
		props[PropSamplerArg] = formatRatio(sampleRatio)
	}
	return Desired{Props: props, Attributes: resourceAttributes(w)}
}

// formatRatio renders a sampling ratio the way the property carries it: always with a decimal point, so 1
// reads as "1.0" and the manual instructions and the automatic writer agree byte for byte.
func formatRatio(r float64) string {
	s := strconv.FormatFloat(r, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func exporterFor(dest domaindest.OTLPTarget) (typ, endpoint string) {
	if dest.Endpoint != "" {
		scheme := "https://"
		if dest.Insecure {
			scheme = "http://"
		}
		ep := dest.Endpoint
		if strings.Contains(ep, "://") {
			return "GRPC", ep
		}
		return "GRPC", scheme + ep
	}
	return "HTTP", strings.TrimRight(dest.HTTPEndpoint, "/")
}

func resourceAttributes(w Workload) string {
	attrs := []string{"deployment.environment.name=" + w.EnvironmentName}
	if w.OrgID != "" {
		attrs = append(attrs, "mulesoft.org.id="+w.OrgID)
	}
	if w.TargetType != "" {
		attrs = append(attrs, "mulesoft.target.type="+w.TargetType)
	}
	if w.API != nil {
		attrs = append(attrs, "mulesoft.api.id="+attributeValue(w.API.ID))
		if w.API.AssetID != "" {
			attrs = append(attrs, "mulesoft.api.asset.id="+attributeValue(w.API.AssetID))
		}
		if w.API.Version != "" {
			attrs = append(attrs, "mulesoft.api.version="+attributeValue(w.API.Version))
		}
	}
	return strings.Join(attrs, ",")
}

// attributeValue keeps a value from breaking the key=value,key=value list it is written into.
func attributeValue(v string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case ',', '=', ' ', '\t', '\n':
			return '_'
		}
		return r
	}, strings.TrimSpace(v))
}

// ConflictError is returned when an application already carries telemetry configuration Odigos did not write.
// Overwriting it would silently redirect telemetry the customer configured on purpose, so the application is
// reported and left alone.
type ConflictError struct{ Keys []string }

func (e *ConflictError) Error() string {
	return fmt.Sprintf("the application already sets %s; Odigos does not overwrite Direct Telemetry Stream "+
		"configuration it did not write — remove those properties to let Odigos instrument it",
		strings.Join(e.Keys, ", "))
}

// conflicts lists the customer-owned properties that block a FIRST instrumentation: any export setting we
// would write that is already present (as a property or a secure property), and the customer's own IaC marker.
// PropResourceAttributes is merged, and PropLogCorrelation is harmless to snapshot, so neither blocks.
func conflicts(props map[string]string, secureKeys []string) []string {
	present := map[string]bool{}
	for k := range props {
		present[k] = true
	}
	for _, k := range secureKeys {
		present[k] = true
	}
	var out []string
	if present[store.PropOdigosIaC] {
		out = append(out, store.PropOdigosIaC)
	}
	for _, k := range managedKeys {
		if strings.HasPrefix(k, propPrefix) && present[k] {
			out = append(out, k)
		}
	}
	return out
}

// Apply returns the properties an application should end up with: its current properties, with every managed
// key set or restored, our resource attributes present beside the customer's, and the ownership marker carrying
// the snapshot rollback needs. existingMarker is the current PropOdigosManaged value ("" on first instrument);
// its snapshot is reused so our own earlier values are never mistaken for the customer's. A present marker that
// is unreadable or belongs to another deployment is refused (see ownedBy).
//
// The resource attributes are merged against the LIVE value, never the snapshot: a customer who adds an
// attribute after instrumentation keeps it, and an application already carrying ours is left as it is — a
// rebuilt string would differ in order, and any difference is a redeploy.
func Apply(current map[string]string, d Desired, existingMarker, deploymentID string) (map[string]string, error) {
	var m manifest
	if existingMarker == "" {
		m = snapshot(current, deploymentID)
	} else {
		if err := ownedBy(existingMarker, deploymentID); err != nil {
			return nil, err
		}
		m, _ = parseManifest(existingMarker)
	}
	next := maps.Clone(current)
	if next == nil {
		next = map[string]string{}
	}
	for _, k := range managedKeys {
		if v, ok := d.Props[k]; ok {
			next[k] = v
		} else {
			restore(next, k, m.PrevProps[k])
		}
	}
	cur, has := current[PropResourceAttributes]
	// Kept as it is only while it carries exactly our current pairs: one we no longer want — an application whose
	// API Manager pairing went away still carrying mulesoft.api.id — is stale, and the value is rebuilt.
	if !(has && existingMarker != "" && containsAll(cur, d.Attributes, ",") && noStaleAttributes(cur, d.Attributes)) {
		next[PropResourceAttributes] = joinAttributes(stripAttributeKeys(cur, ourAttributeKeys), d.Attributes)
	}
	marker, err := marshalManifest(m)
	if err != nil {
		return nil, err
	}
	next[store.PropOdigosManaged] = marker
	return next, nil
}

// Rollback returns the properties with every change Odigos recorded undone and the marker removed. ok is false
// when the marker is missing, unreadable or another deployment's — then nothing proves what the prior values
// were, and the caller must not touch the application.
//
// The resource attributes are rebuilt from the live value with our attributes taken out, so anything the
// customer added since is kept; any of our keys the customer had set before instrumentation is put back from
// the snapshot.
func Rollback(current map[string]string, deploymentID string) (map[string]string, bool) {
	marker := current[store.PropOdigosManaged]
	if marker == "" || ownedBy(marker, deploymentID) != nil {
		return nil, false
	}
	m, _ := parseManifest(marker)
	next := maps.Clone(current)
	for _, k := range slices.Sorted(maps.Keys(m.PrevProps)) {
		if k == PropResourceAttributes {
			continue
		}
		restore(next, k, m.PrevProps[k])
	}
	attrs := stripAttributeKeys(current[PropResourceAttributes], ourAttributeKeys)
	prior := m.PrevProps[PropResourceAttributes]
	switch {
	case prior != nil && attrs == stripAttributeKeys(*prior, ourAttributeKeys):
		// The customer changed nothing since: restore their value exactly, order included.
		attrs = *prior
	case prior != nil:
		for _, pair := range splitAttributes(*prior) {
			if ourAttributeKeys[attributeKey(pair)] {
				attrs = joinAttributes(attrs, pair)
			}
		}
	}
	if attrs == "" && prior == nil {
		delete(next, PropResourceAttributes)
	} else {
		next[PropResourceAttributes] = attrs
	}
	delete(next, store.PropOdigosManaged)
	return next, true
}

// ourAttributeKeys are the resource attribute keys Odigos writes (see resourceAttributes).
var ourAttributeKeys = map[string]bool{
	"deployment.environment.name": true,
	"mulesoft.org.id":             true,
	"mulesoft.target.type":        true,
	"mulesoft.api.id":             true,
	"mulesoft.api.asset.id":       true,
	"mulesoft.api.version":        true,
}

func splitAttributes(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func attributeKey(pair string) string {
	k, _, _ := strings.Cut(pair, "=")
	return strings.TrimSpace(k)
}

// stripAttributeKeys removes the pairs whose key is in keys, keeping the rest in order.
func stripAttributeKeys(v string, keys map[string]bool) string {
	var kept []string
	for _, p := range splitAttributes(v) {
		if !keys[attributeKey(p)] {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, ",")
}

func restore(props map[string]string, key string, prev *string) {
	if prev == nil {
		delete(props, key)
		return
	}
	props[key] = *prev
}

// noStaleAttributes reports whether every pair of ours in the live value is one we still want.
func noStaleAttributes(live, desired string) bool {
	want := map[string]bool{}
	for _, p := range splitAttributes(desired) {
		want[p] = true
	}
	for _, p := range splitAttributes(live) {
		if ourAttributeKeys[attributeKey(p)] && !want[p] {
			return false
		}
	}
	return true
}

func joinAttributes(customer, ours string) string {
	switch {
	case customer == "":
		return ours
	case ours == "":
		return customer
	}
	return customer + "," + ours
}
