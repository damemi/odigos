package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestParseCollectorTags pins the parser's leniency. In manual mode these values are authored by a customer's
// deployment definition, so an unknown tag, a missing tag or a junk value must degrade to "says nothing"
// rather than fail an observation.
func TestParseCollectorTags(t *testing.T) {
	got := ParseCollectorTags(map[string]string{
		TagComponent:   ComponentCollector,
		TagConnector:   "aws",
		TagConnectorID: "uid-1",
		TagRegion:      "us-east-1",
		TagNetwork:     "vpc-0abc",
		TagIaC:         `{"v":1,"source":"terraform"}`,
		"CostCenter":   "platform",
	})
	if !got.IsCollector() {
		t.Error("IsCollector = false for a fully tagged Collector object")
	}
	if got.HasOwnershipMarker() {
		t.Error("HasOwnershipMarker = true with no ownership tag set")
	}
	if !got.HasIaCMarker() {
		t.Error("HasIaCMarker = false with the artifact tag set")
	}
	if got.Connector != "aws" || got.ConnectorID != "uid-1" || got.Region != "us-east-1" || got.Network != "vpc-0abc" {
		t.Errorf("identity = %+v, want regional identity and host-network tags read through", got)
	}

	empty := ParseCollectorTags(nil)
	if empty.IsCollector() || empty.HasOwnershipMarker() || empty.HasIaCMarker() {
		t.Errorf("an untagged object reads as %+v, want a zero value that claims nothing", empty)
	}
}

// TestOwnershipMarkerMatchesOnValue: the delete gate keys on the VALUE, not on the key's presence. A document
// that sets the key to anything else — including "false" — must not be able to authorize a deletion, which
// presence-matching would let it do.
func TestOwnershipMarkerMatchesOnValue(t *testing.T) {
	for _, v := range []string{"false", "yes", "1", "True", ""} {
		if ParseCollectorTags(map[string]string{TagManaged: v}).HasOwnershipMarker() {
			t.Errorf("%s=%q reads as owned by Odigos", TagManaged, v)
		}
	}
	if !ParseCollectorTags(map[string]string{TagManaged: TagManagedTrue}).HasOwnershipMarker() {
		t.Errorf("%s=%q does not read as owned by Odigos", TagManaged, TagManagedTrue)
	}
}

// TestParseCollectorIaCMarker mirrors TestParseIaCMarker: a malformed payload degrades to "nothing recorded",
// which the conformance check answers by reporting itself incomplete, and never demotes a marked object to
// unmarked — that is decided by the tag's presence alone.
func TestParseCollectorIaCMarker(t *testing.T) {
	cases := []struct {
		name             string
		raw              string
		wantOK           bool
		wantSrc, wantImg string
	}{
		{"absent", "", false, "", ""},
		{"malformed", "{not json", false, "", ""},
		{"full payload", `{"v":1,"source":"cdk","version":"v0.0.11","image":"odigos/collector:v1.2.3"}`, true, "cdk", "odigos/collector:v1.2.3"},
		{"no image recorded", `{"v":1,"source":"manual"}`, true, "manual", ""},
	}
	for _, tc := range cases {
		m, ok := ParseCollectorIaCMarker(tc.raw)
		switch {
		case ok != tc.wantOK:
			t.Errorf("%s: ok = %v, want %v", tc.name, ok, tc.wantOK)
		case m.Source != tc.wantSrc:
			t.Errorf("%s: source = %q, want %q", tc.name, m.Source, tc.wantSrc)
		case m.Image != tc.wantImg:
			t.Errorf("%s: image = %q, want %q", tc.name, m.Image, tc.wantImg)
		}
	}
	// Presence, not parseability, is what attribution turns on.
	if !ParseCollectorTags(map[string]string{TagIaC: "{not json"}).HasIaCMarker() {
		t.Error("a malformed artifact payload demotes the object to un-attributed")
	}
}

// TestIdentityTagsCarryNoOwnership: the deployer writes identity in one call and ownership in another, so no
// code path can grant itself deletion authority as a side effect of tagging what it created.
func TestIdentityTagsCarryNoOwnership(t *testing.T) {
	tags := CollectorIdentityTags("aws", "uid-1", "us-east-1", "vpc-0abc")
	if _, ok := tags[TagManaged]; ok {
		t.Errorf("CollectorIdentityTags includes %s; identity and ownership must be written separately", TagManaged)
	}
	if _, ok := tags[TagIaC]; ok {
		t.Errorf("CollectorIdentityTags includes %s; only a customer's document writes it", TagIaC)
	}
	if got := len(tags); got != len(CollectorIdentityTagKeys())+len(CollectorHostMetadataTagKeys()) {
		t.Errorf("CollectorIdentityTags writes %d tags, identity + host metadata names %d", got, len(CollectorIdentityTagKeys())+len(CollectorHostMetadataTagKeys()))
	}
	for _, k := range append(CollectorIdentityTagKeys(), CollectorHostMetadataTagKeys()...) {
		if tags[k] == "" {
			t.Errorf("CollectorIdentityTags leaves %s empty; an object missing an identity tag cannot be observed or converged", k)
		}
	}
}

// TestCollectorArtifactTagKeysForbidTheOwnershipTag is the artifact guard's contract in miniature: every
// published variant must set the identity set plus the artifact tag, and no variant may mention the ownership
// tag. A customer copying a snippet that sets it would be claiming a creation Odigos never performed, which is
// also what authorizes Odigos to delete the stack.
func TestCollectorArtifactTagKeysForbidTheOwnershipTag(t *testing.T) {
	want := map[string]bool{}
	for _, k := range CollectorIdentityTagKeys() {
		want[k] = true
	}
	for _, k := range CollectorHostMetadataTagKeys() {
		want[k] = true
	}
	want[TagIaC] = true
	got := CollectorArtifactTagKeys()
	if len(got) != len(want) {
		t.Fatalf("CollectorArtifactTagKeys = %v, want the identity set plus %s", got, TagIaC)
	}
	for _, k := range got {
		if k == TagManaged {
			t.Fatalf("%s is on the artifact sweep list; the guard must forbid it, never require it", TagManaged)
		}
		if !want[k] {
			t.Errorf("unexpected artifact tag %q", k)
		}
	}
}

// TestCollectorTagsAreNotTheWorkloadMarkers: the two ownership records must stay distinct names, for the
// reason TestIaCMarkerIsNotTheOwnershipMarker gives about the env markers. Collapsing the Collector's
// attribution tag into its ownership tag would make an artifact-applied stack eligible for deletion.
func TestCollectorTagsAreNotTheWorkloadMarkers(t *testing.T) {
	if TagIaC == TagManaged {
		t.Fatal("the artifact tag and the ownership tag are the same key, so attribution now grants deletion")
	}
	for _, k := range []string{TagComponent, TagConnector, TagConnectorID, TagRegion, TagNetwork, TagIaC, TagManaged} {
		if k == EnvOdigosIaC || k == "ODIGOS_MANAGED" {
			t.Errorf("tag key %q collides with a workload env marker", k)
		}
	}
}

// TestEveryCollectorTagKeyIsWritableOnEveryProvider is the guard that makes the single-spelling decision
// hold. The canonical `odigos.io/...` form these keys started as is invalid on two of the three clouds —
// Azure rejects the slash, GCP rejects the slash and the dot — and the failure mode is not a build error
// but an object that quietly never receives its ownership tag, which is an object teardown then refuses
// to delete. A key added later without this rule in mind fails here instead.
func TestEveryCollectorTagKeyIsWritableOnEveryProvider(t *testing.T) {
	keys := CollectorTagKeys()
	if len(keys) != 7 {
		t.Fatalf("CollectorTagKeys returned %d keys; every key must be covered by this guard", len(keys))
	}
	for _, k := range keys {
		if !ValidCollectorTagKey(k) {
			t.Errorf("tag key %q is not writable on every provider: keys must match ^[a-z][a-z0-9_-]{0,62}$", k)
		}
		if strings.ContainsAny(k, "/.") {
			t.Errorf("tag key %q contains a dot or a slash: invalid as an Azure tag name and a GCP label key", k)
		}
		if strings.ToLower(k) != k {
			t.Errorf("tag key %q is not lowercase: GCP label keys reject uppercase", k)
		}
	}
}

// TestFixedCollectorTagValuesArePortable covers the values this package chooses itself. The ones it does
// not choose — connector name, connector id, network id — are ValidateCollectorTags's job at write time.
func TestFixedCollectorTagValuesArePortable(t *testing.T) {
	for _, v := range []string{ComponentCollector, TagManagedTrue} {
		if !ValidCollectorTagValue(v) {
			t.Errorf("fixed tag value %q is not writable on every provider", v)
		}
	}
}

// TestTagIaCCarriesNoPayload: the marker JSON moved to EnvOdigosIaC on the container, and TagIaC is a
// flag. Regressing this puts braces, quotes and an image reference into a tag value — outside AWS's
// documented set and impossible on a GCP label, where the tag would simply not be written.
func TestTagIaCCarriesNoPayload(t *testing.T) {
	if _, ok := ParseCollectorIaCMarker("true"); ok {
		t.Fatal("the flag value decodes as a marker payload; flag and payload must stay distinct")
	}
	marker, err := json.Marshal(CollectorIaCMarker{
		Version: CollectorIaCMarkerVersion,
		Source:  "terraform",
		Release: "v1.2.3",
		Image:   "ghcr.io/odigos-io/odigos-enterprise-collector@sha256:" + strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if ValidCollectorTagValue(string(marker)) {
		t.Fatal("a marker payload passes the tag-value rule; the rule is wrong, not the payload")
	}
	if err := ValidateCollectorTags(map[string]string{TagIaC: string(marker)}); err == nil {
		t.Fatal("ValidateCollectorTags accepted a JSON payload as a tag value")
	}
}

// TestCollectorTagsParseCaseInsensitively: Azure tag names are case-insensitive, so an exact-match lookup
// would read Odigos's own object as untagged. That reads as unattributed, which means neither converged
// nor deleted — the Collector strands in the customer's account while everything reports healthy.
func TestCollectorTagsParseCaseInsensitively(t *testing.T) {
	got := ParseCollectorTags(map[string]string{
		"Odigos-Component":    ComponentCollector,
		"ODIGOS-CONNECTOR":    "prod",
		"odigos-Connector-Id": "col-1",
		"Odigos-Region":       "us-east-1",
		"Odigos-Network":      "vpc-1",
		"ODIGOS-MANAGED":      TagManagedTrue,
	})
	if !got.IsCollector() {
		t.Error("a Collector object written with Azure's casing did not parse as a Collector")
	}
	if got.Connector != "prod" || got.ConnectorID != "col-1" || got.Region != "us-east-1" || got.Network != "vpc-1" {
		t.Errorf("identity tags lost to casing: %+v", got)
	}
	if !got.HasOwnershipMarker() {
		t.Error("the ownership marker was lost to casing")
	}
}

// TestExactCaseWinsOverAFoldedMatch keeps the folded lookup deterministic: a set carrying two case
// variants must resolve to the canonical key rather than to whichever the map happens to yield.
func TestExactCaseWinsOverAFoldedMatch(t *testing.T) {
	for i := 0; i < 50; i++ {
		got := ParseCollectorTags(map[string]string{
			TagManaged:       TagManagedTrue,
			"ODIGOS-MANAGED": "false",
		})
		if got.Managed != TagManagedTrue {
			t.Fatalf("Managed = %q, want the exact-case value %q", got.Managed, TagManagedTrue)
		}
	}
}

// TestValidateCollectorTagsReportsEveryProblemAtOnce: a deployer puts this in status, so one run must not
// hide the second fault behind the first, and two runs must read identically.
func TestValidateCollectorTagsReportsEveryProblemAtOnce(t *testing.T) {
	tags := map[string]string{
		TagComponent:       ComponentCollector,
		TagConnector:       "my.connector",                  // a Kubernetes name may carry a dot
		TagNetwork:         "/subscriptions/x/vnets/vnet-1", // a full Azure resource id
		TagConnectorID:     strings.Repeat("a", 64),         // one over the limit
		"customer.own/tag": "Whatever They Like",            // not ours to police
	}
	err := ValidateCollectorTags(tags)
	if err == nil {
		t.Fatal("ValidateCollectorTags accepted a dotted name, a resource id and an over-long value")
	}
	msg := err.Error()
	for _, want := range []string{TagConnector, TagNetwork, TagConnectorID} {
		if !strings.Contains(msg, want) {
			t.Errorf("error does not mention %s, so a deployer would fix one fault at a time: %s", want, msg)
		}
	}
	if strings.Contains(msg, "customer.own/tag") {
		t.Errorf("a customer's own tag was policed: %s", msg)
	}
	if msg != ValidateCollectorTags(tags).Error() {
		t.Error("the message is not stable across runs")
	}
}

// TestCollectorIdentityTagsAreWritable: the set the deployer actually sends must pass its own rule.
func TestCollectorIdentityTagsAreWritable(t *testing.T) {
	tags := CollectorIdentityTags("prod-connector", "col-abc123", "us-east-1", "vpc-0abc123def")
	tags[TagManaged] = TagManagedTrue
	if err := ValidateCollectorTags(tags); err != nil {
		t.Fatalf("the deployer's own tag set is not portable: %v", err)
	}
}

// TestTheTwoMarkersMatchOnOppositeThings locks an asymmetry that looks like an oversight and is not.
//
// TagManaged GRANTS deletion, so it is matched on its value: `odigos-managed=false` must not hand Odigos
// authority over a customer's stack. TagIaC REMOVES deletion, so it is matched on presence: any value at
// all has to disqualify, because a reader that demanded one exact string would let `odigos-iac=yes`
// through and make a customer-applied stack deletable.
//
// Making these two consistent with each other is the refactor this test exists to fail. There is no
// spelling of "consistent" that is safe in both directions.
func TestTheTwoMarkersMatchOnOppositeThings(t *testing.T) {
	// TagManaged: only the exact value grants ownership.
	for _, v := range []string{"false", "TRUE", "1", "yes", ""} {
		if ParseCollectorTags(map[string]string{TagManaged: v}).HasOwnershipMarker() {
			t.Errorf("%s=%q reads as Odigos-created; only %q may", TagManaged, v, TagManagedTrue)
		}
	}
	if !ParseCollectorTags(map[string]string{TagManaged: TagManagedTrue}).HasOwnershipMarker() {
		t.Errorf("%s=%q does not read as Odigos-created", TagManaged, TagManagedTrue)
	}

	// TagIaC: any value disqualifies, because disqualifying is the safe direction.
	for _, v := range []string{"true", "yes", "1", "cloudformation", "{}", "whatever"} {
		if !ParseCollectorTags(map[string]string{TagIaC: v}).HasIaCMarker() {
			t.Errorf("%s=%q does not disqualify the object from deletion; presence is the gate", TagIaC, v)
		}
	}
	if ParseCollectorTags(map[string]string{TagIaC: ""}).HasIaCMarker() {
		t.Errorf("%s with an empty value disqualifies; absent and empty must read alike", TagIaC)
	}
}
