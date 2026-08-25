package api

import "encoding/json"

// EnvOdigosIaC is the attribution marker IaC artifacts (CDK, Terraform) write on instrumented workloads.
// It answers "did Odigos tooling apply this", not "may I mutate / roll this back".
//
// Split by writer:
//   - Automatic: connector mutates and writes the ownership marker (rollback snapshot). Only that grants mutation.
//   - Manual (IaC): no mutation, no snapshot — this marker only records that Odigos tooling applied the instrumentation.
//
// Discovery accepts either marker; rollback accepts ownership alone. Presence (not a parseable payload)
// is what attribution turns on. Carrying this marker does not keep a workload out of instrumentation —
// manual-mode types simply have no reconciler (see coordinator activeReconcilers).
//
// Shared across providers so artifacts stay portable; AWS is currently the only reader with a conformance observer.
const EnvOdigosIaC = "ODIGOS_IAC"

// IaCMarkerVersion is the schema version of the payload below. It is what tells a reader how to interpret
// the rest of the object, so an artifact writing a bumped version is a change here first.
const IaCMarkerVersion = 1

// IaCMarker is the JSON payload of EnvOdigosIaC.
//
// Source / Release — which artifact wrote this and at which release (support / attribution).
//
// Layer — optional (Lambda only). Conformance compares against this recorded layer, not what Odigos
// ships today, so a new release is not reported as drift until the customer re-applies. Unrecorded
// layer → incomplete check, not a wrong verdict (manual mode must not look layers up).
//
// Image — optional (staged-agent platforms). The agents-bundle image the artifact staged the agent files
// from: the Fargate counterpart of Layer, with the same rule and for the same reason. Conformance holds
// the workload to the image RECORDED here rather than to the one this connector build happens to pin, so
// bumping the connector's artifacts version does not report every manually instrumented task as drifted
// the moment it lands. Unrecorded image → the check falls back from identity to signature (a staging
// container is present, non-essential and staging the right agents, whatever its tag), never to today's
// pin — which is exactly the fallback the Lambda layer check makes for an unrecorded layer.
//
// Language — optional (staged-agent platforms). A cross-check against freshly resolved language, not
// the desired state itself. Only ECS Fargate reads it today; Cloud Run / Container Apps key off
// presence. Missing language → skip the cross-check (omission is not drift).
type IaCMarker struct {
	Version  int    `json:"v"`
	Source   string `json:"source,omitempty"`
	Release  string `json:"version,omitempty"`
	Layer    string `json:"layer,omitempty"`
	Image    string `json:"image,omitempty"`
	Language string `json:"lang,omitempty"`
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
