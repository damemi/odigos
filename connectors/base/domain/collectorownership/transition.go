package collectorownership

import "github.com/odigos-io/odigos-enterprise/connectors/spec"

// Adoption is the decision to bring an object this process did not create under convergence.
// Covers predecessor stacks (recreated CR) and manual→automatic over a customer-applied stack.
type Adoption struct {
	Adopt                   bool   // record the ref and converge from the next cycle
	RestampConnectorID      bool   // rewrite TagConnectorID to this connector's PlatformID
	AcquiresOwnership       bool   // always false — adoption never writes TagManaged
	InheritsDeleteAuthority bool   // true only when the object already carried TagManaged
	Reason                  string // status message; branch on the booleans, never on this
}

// AdoptionOf decides whether a tag-scan hit is adopted.
func AdoptionOf(o Object, self Identity, mode string) Adoption {
	if mode != spec.ModeAutomatic {
		return Adoption{Reason: "manual mode converges nothing, so there is nothing to adopt into"}
	}
	if o.CustomerNamed {
		return Adoption{Reason: "named by you in the connector configuration; Odigos uses it and never writes to it"}
	}
	switch AttributionOf(o.Tags, self) {
	case AttributionNone:
		return Adoption{Reason: "not tagged as an Odigos Collector object"}
	case AttributionForeign:
		return Adoption{Reason: "tagged for connector " + o.Tags.Connector + "; another connector's Collector is never adopted"}
	case AttributionUnattributed:
		return Adoption{Reason: "tagged as a Collector but names no connector; Odigos will not claim it"}
	case AttributionMine:
		if o.Recorded {
			return Adoption{Reason: "already tracked by this connector"}
		}
		return Adoption{
			Adopt:                   true,
			InheritsDeleteAuthority: OriginOf(o.Tags) == OriginOdigos,
			Reason:                  "this connector's own object, missing from its state; re-recording it",
		}
	case AttributionPredecessor:
		return Adoption{
			Adopt:                   true,
			RestampConnectorID:      true,
			InheritsDeleteAuthority: OriginOf(o.Tags) == OriginOdigos,
			Reason:                  "deployed by an earlier incarnation of this connector; adopting and re-stamping its connector id",
		}
	}
	return Adoption{Reason: "unclassified"}
}

// Policy is what the runtime does for a Collector type in a given mode.
type Policy struct {
	Publish   bool // publish IaC artifacts into the Setup catalog
	Observe   bool // read cloud state into managed_collectors
	Configure bool // push desired config over OpAMP (both modes)
	Converge  bool // write to the customer's cloud account (automatic only)
	Adopt     bool // scan and record unowned tagged objects (automatic only)
	Delete    bool // teardown may destroy objects that pass MayDelete (automatic only)
}

// PolicyFor returns the policy for a derived collector mode (or empty when not allowed).
func PolicyFor(mode string) Policy {
	switch mode {
	case spec.ModeAutomatic:
		return Policy{Publish: true, Observe: true, Configure: true, Converge: true, Adopt: true, Delete: true}
	case spec.ModeManual:
		return Policy{Publish: true, Observe: true, Configure: true}
	default:
		return Policy{}
	}
}

// Transition is what happens at a mode boundary.
// Deletes and RewritesOwnershipTags are always false — asserted in tests.
type Transition struct {
	From, To              string
	Policy                Policy
	Deletes               bool // always false; mode change never destroys
	RewritesOwnershipTags bool // always false; TagManaged records creation, not mode
	Note                  string
}

// TransitionFor returns boundary behaviour for a capability change on one Collector type.
// automatic → "" does not delete: a removed capability looks like one an older image
// could not parse, so deleting on absence would turn a rollback into a disappearing Collector.
func TransitionFor(from, to string) Transition {
	t := Transition{From: from, To: to, Policy: PolicyFor(to)}
	switch {
	case from == to:
		t.Note = "no change"
	case from == "" && to == spec.ModeManual:
		t.Note = "publishing the deployment instructions; the Collector is yours to apply"
	case from == "" && to == spec.ModeAutomatic:
		t.Note = "Odigos will deploy and converge the Collector"
	case from == spec.ModeManual && to == spec.ModeAutomatic:
		t.Note = "Odigos will converge the Collector you deployed; it will never delete infrastructure it did not create"
	case from == spec.ModeAutomatic && to == spec.ModeManual:
		t.Note = "Odigos has stopped converging the Collector and will not delete it; it is yours to maintain"
	case to == "":
		t.Note = "Odigos has stopped managing this Collector; it keeps running until you remove it"
	}
	return t
}
