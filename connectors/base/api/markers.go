package api

import "encoding/json"

// EnvOdigosIaC is the attribution marker an Odigos IaC artifact — the CDK constructs, the Terraform
// fragments this connector publishes — writes on a workload it instruments. It answers ONE question,
// "did Odigos tooling apply this instrumentation", and deliberately not the one the per-provider
// ownership marker answers.
//
// The two are not interchangeable, and the split is by WRITER:
//
//   - AUTOMATIC mode: the connector mutates the workload and writes the ownership marker, whose payload is a
//     rollback snapshot of everything it changed. Only that marker grants mutation rights, because only it
//     records what undoing the mutation means.
//   - MANUAL (IaC) mode: nothing mutates the workload, so there is no snapshot to take — and asking a
//     customer to hand-author one is asking them to describe a rollback that will never run. What their
//     deployment definition CAN state is that Odigos tooling put the instrumentation there, and which
//     version of it did.
//
// Readers therefore have to keep the questions apart. "Is this instrumented by Odigos" accepts either marker
// — discovery has no management mode at all and could not choose between them even if it wanted to — while
// "may I roll this workload back" accepts the ownership marker alone. A workload carrying only this marker is
// reported, never mutated.
//
// It lives in the base for the reason the component names do: four providers' IaC artifacts write it and four
// providers' observers read it, and a marker arriving under a different name per provider would make an
// artifact non-portable for no reason. Note that presence — not a parseable payload — is what attribution
// turns on, matching how each provider treats its ownership marker: a customer typo inside the value must not
// be able to make a correctly instrumented workload read as untouched.
const EnvOdigosIaC = "ODIGOS_IAC"

// IaCMarkerVersion is the schema version of the payload below. It is what tells a reader how to interpret
// the rest of the object, so an artifact writing a bumped version is a change here first.
const IaCMarkerVersion = 1

// IaCMarker is the JSON payload of EnvOdigosIaC.
//
// Source and Release are attribution: which artifact wrote this and at which release. They are what makes a
// support conversation about a manual-mode workload possible at all — the connector cannot see a customer's
// deployment definition, so the workload itself is the only place that fact can live.
//
// Layer is the load-bearing field, and it is optional only because not every platform has layers. Where one
// does (Lambda), the recorded artifact is what conformance compares the workload against, and preferring it
// over asking the resolver what Odigos ships TODAY is the whole semantics of manual-mode drift: the resolver
// would report every conformant workload as drifted the day a new layer is released, announcing an available
// upgrade as an error on every function until the customer re-applied. Manual mode also grants no permission
// to ask — a layer lookup is an instrumentation permission — so an unrecorded layer degrades to an explicitly
// incomplete check rather than to a wrong verdict.
type IaCMarker struct {
	Version int    `json:"v"`
	Source  string `json:"source,omitempty"`
	Release string `json:"version,omitempty"`
	Layer   string `json:"layer,omitempty"`
}

// ParseIaCMarker decodes the marker's payload, reporting false for an absent or malformed value.
//
// A malformed value is NOT an error here. This value is authored by a customer's deployment definition, so a
// typo in it must not be able to fail an observation; it degrades to "nothing recorded", which callers already
// have a correct answer for — the layer check reports itself incomplete rather than inventing drift. Whether
// the workload counts as Odigos-instrumented is a separate question, decided by the variable's PRESENCE, so a
// malformed payload never demotes a marked workload to unmarked.
func ParseIaCMarker(s string) (IaCMarker, bool) {
	if s == "" {
		return IaCMarker{}, false
	}
	var m IaCMarker
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return IaCMarker{}, false
	}
	return m, true
}
