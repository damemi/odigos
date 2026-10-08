// Package collectorownership decides what a connector may do to a managed Collector's
// cloud objects: mutate, delete, or leave alone. Pure — no I/O. Provider deployers
// must not re-derive these decisions from raw tags.
//
// OpAMP config is gated by identity, not tags. Cloud writes are gated here by tags.
//
//	                | origin=odigos             | origin=customer
//	mode=automatic  | managed: converge, delete | adopted: converge, never delete
//	mode=manual     | handed over: observe only | customer's: observe only
//
// Only the top-left cell may delete. TagManaged is never written by artifacts or on adoption.
package collectorownership

import (
	"github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/spec"
)

// Origin is who created a cloud object. Orthogonal to mode.
type Origin string

const (
	OriginOdigos   Origin = "odigos"   // deployer-created (TagManaged); only deletable origin
	OriginCustomer Origin = "customer" // artifact-applied, hand-built, or customer-named
)

// Attribution is which connector a Collector object belongs to.
type Attribution string

const (
	AttributionNone         Attribution = ""            // not a Collector object
	AttributionMine         Attribution = "mine"        // TagConnectorID matches this connector
	AttributionPredecessor  Attribution = "predecessor" // name matches, id does not; adoptable
	AttributionForeign      Attribution = "foreign"     // different connector; never touched
	AttributionUnattributed Attribution = "unattributed" // Collector with no connector tags
)

// Identity is this connector's identity as config.Config carries it.
type Identity struct {
	ConnectorName string // stable; adoption key
	ConnectorID   string // unique; authority key
}

// Object is one cloud object under consideration.
type Object struct {
	Ref           string // provider-native id; reporting and record matching only
	Tags          api.CollectorTags
	CustomerNamed bool // pointed at via operational config; never mutated or deleted
	Recorded      bool // managed_collectors holds Ref; required for delete
}

// OriginOf reports who created an object.
// TagIaC presence disqualifies even a pasted TagManaged → OriginCustomer.
func OriginOf(t api.CollectorTags) Origin {
	if t.HasOwnershipMarker() && !t.HasIaCMarker() {
		return OriginOdigos
	}
	return OriginCustomer
}

// AttributionOf decides which connector an object belongs to.
// Unique id wins over stable name.
func AttributionOf(t api.CollectorTags, self Identity) Attribution {
	if !t.IsCollector() {
		return AttributionNone
	}
	if t.ConnectorID != "" && t.ConnectorID == self.ConnectorID {
		return AttributionMine
	}
	if t.Connector != "" && t.Connector == self.ConnectorName {
		return AttributionPredecessor
	}
	if t.Connector == "" && t.ConnectorID == "" {
		return AttributionUnattributed
	}
	return AttributionForeign
}

// Mine reports whether this connector may act on the attribution at all.
func (a Attribution) Mine() bool { return a == AttributionMine || a == AttributionPredecessor }

// MayMutate reports whether the connector may write to an object.
// Requires automatic mode and local attribution; origin is excluded so adoption can converge.
func MayMutate(o Object, self Identity, mode string) bool {
	if mode != spec.ModeAutomatic || o.CustomerNamed {
		return false
	}
	return AttributionOf(o.Tags, self).Mine()
}

// MayDelete reports whether the connector may destroy an object.
// Requires MayMutate, OriginOdigos, and Recorded.
func MayDelete(o Object, self Identity, mode string) bool {
	return MayMutate(o, self, mode) && OriginOf(o.Tags) == OriginOdigos && o.Recorded
}

// Class is an object's full standing for reporting.
type Class struct {
	Attribution Attribution
	Origin      Origin
	Mode        string
	Mutable     bool
	Deletable   bool
}

// Classify computes standing so reporting and the gates cannot drift.
func Classify(o Object, self Identity, mode string) Class {
	return Class{
		Attribution: AttributionOf(o.Tags, self),
		Origin:      OriginOf(o.Tags),
		Mode:        mode,
		Mutable:     MayMutate(o, self, mode),
		Deletable:   MayDelete(o, self, mode),
	}
}

// ClassifyRecorded computes standing from what a managed_collectors ROW persists, rather than from an
// object's live tags.
//
// The two are for different moments. Classify answers "may I touch this cloud object I just found",
// which needs the tags in front of it. This answers "what may Odigos do to the Collector I have a
// record of", which is what a report is asked and what a teardown request has to decide — and at that
// point the observer has already read the tags and written its conclusion into mode and origin.
// Re-deriving from tags here would need a provider call on every report.
//
// Attribution is AttributionMine by construction: rows are read back by connector_name, so a row
// reaching this function is this connector's own. Adoption is visible separately (origin stays
// customer, and adopted_at is set), which is what keeps an adopted stack from being reported as
// Odigos-created and deleted at teardown.
//
// The gates themselves are the same ones MayMutate/MayDelete apply, deliberately spelled out in the
// same order so the two cannot drift apart silently.
func ClassifyRecorded(mode, origin string, recordedRefs bool) Class {
	mutable := mode == spec.ModeAutomatic
	return Class{
		Attribution: AttributionMine,
		Origin:      Origin(origin),
		Mode:        mode,
		Mutable:     mutable,
		Deletable:   mutable && Origin(origin) == OriginOdigos && recordedRefs,
	}
}

// Partition splits teardown candidates into deletable and retained.
func Partition(objs []Object, self Identity, mode string) (deletable, retained []Object) {
	for _, o := range objs {
		if MayDelete(o, self, mode) {
			deletable = append(deletable, o)
			continue
		}
		retained = append(retained, o)
	}
	return deletable, retained
}
