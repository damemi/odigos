package plan

import (
	"strings"
	"testing"
)

func TestSubstituteReplacesEveryOccurrence(t *testing.T) {
	got := Substitute(`name = "<YOUR_CONTAINER>" ; other = "<YOUR_CONTAINER>"`, map[string]string{PlaceholderContainer: "api"})
	if strings.Contains(got, PlaceholderContainer) {
		t.Errorf("a second occurrence survived: %s", got)
	}
	if strings.Count(got, "api") != 2 {
		t.Errorf("got %q", got)
	}
}

// TestSubstituteIgnoresEmptyValues is the difference between "Odigos does not know your container's name" and
// "your container has no name". The second renders as a syntactically valid snippet that instruments nothing,
// and nothing downstream would flag it.
func TestSubstituteIgnoresEmptyValues(t *testing.T) {
	got := Substitute(`name = "<YOUR_CONTAINER>"`, map[string]string{PlaceholderContainer: ""})
	if !strings.Contains(got, PlaceholderContainer) {
		t.Errorf("an empty value erased the placeholder: %s", got)
	}
}

// TestSubstituteIgnoresMalformedKeys keeps a resolved map from rewriting arbitrary substrings of a template.
// The keys are placeholders, not a general search-and-replace.
func TestSubstituteIgnoresMalformedKeys(t *testing.T) {
	got := Substitute(`image = "agents:v1"`, map[string]string{"agents": "REPLACED", "<lower_case>": "REPLACED"})
	if strings.Contains(got, "REPLACED") {
		t.Errorf("a non-placeholder key was substituted: %s", got)
	}
}

func TestSubstituteAllPreservesNil(t *testing.T) {
	if got := SubstituteAll(nil, map[string]string{PlaceholderRegion: "us-east-1"}); got != nil {
		t.Errorf("nil in should be nil out, got %#v", got)
	}
	got := SubstituteAll([]string{"in <REGION>"}, map[string]string{PlaceholderRegion: "us-east-1"})
	if len(got) != 1 || got[0] != "in us-east-1" {
		t.Errorf("got %#v", got)
	}
}

// TestUnresolvedDescribesWhatIsLeft covers the whole reason the token travels with a description: a customer
// looking at <ODIGOS_SOURCE_NAME> has nowhere else to learn that it must MATCH rather than merely look
// sensible.
func TestUnresolvedDescribesWhatIsLeft(t *testing.T) {
	got := Unresolved(`svc = "<ODIGOS_SOURCE_NAME>" ; c = "<YOUR_CONTAINER>"`, nil)
	if len(got) != 2 {
		t.Fatalf("got %d placeholders: %+v", len(got), got)
	}
	// Sorted by token: <ODIGOS_SOURCE_NAME> before <YOUR_CONTAINER>.
	if got[0].Token != PlaceholderServiceName || got[1].Token != PlaceholderContainer {
		t.Fatalf("unsorted or wrong tokens: %+v", got)
	}
	for _, ph := range got {
		if ph.Description == "" {
			t.Errorf("%s reached a customer with nothing saying what belongs there", ph.Token)
		}
	}
	if got[0].Description != ServiceNamePlaceholder.Description {
		t.Errorf("base description not used for %s: %q", got[0].Token, got[0].Description)
	}
}

// TestUnresolvedPrefersThePlansOwnDescription lets a provider that knows more about its token say so — the
// Lambda layer ARN is the case that motivated it.
func TestUnresolvedPrefersThePlansOwnDescription(t *testing.T) {
	declared := []Placeholder{{Token: PlaceholderContainer, Description: "the ECS container running your application"}}
	got := Unresolved(`name = "<YOUR_CONTAINER>"`, declared)
	if len(got) != 1 || got[0].Description != declared[0].Description {
		t.Errorf("plan declaration did not win: %+v", got)
	}
}

// TestUnresolvedFallsBackForArtifactOnlyTokens covers the tokens that exist only in hand-authored delivery
// snippets (<YOUR_RESOURCE_GROUP>, <YOUR_FUNCTION_NAME>) and are in no plan. They still have to arrive with
// something rather than as a bare token.
func TestUnresolvedFallsBackForArtifactOnlyTokens(t *testing.T) {
	got := Unresolved(`--resource-group <YOUR_RESOURCE_GROUP>`, nil)
	if len(got) != 1 || got[0].Description == "" {
		t.Fatalf("got %+v", got)
	}
}

// TestUnresolvedDedupes — one blank, however many times the snippet repeats it.
func TestUnresolvedDedupes(t *testing.T) {
	got := Unresolved(`<YOUR_CONTAINER> and <YOUR_CONTAINER>`, nil)
	if len(got) != 1 {
		t.Errorf("got %d entries for one token: %+v", len(got), got)
	}
}

func TestUnresolvedIgnoresMalformedTokens(t *testing.T) {
	got := Unresolved(`<lower_case> <With Space> <>`, nil)
	if len(got) != 0 {
		t.Errorf("malformed tokens were reported as blanks: %+v", got)
	}
}

// TestUnresolvedIsNilWhenEverythingIsFilled keeps a client from rendering a "you still need to supply"
// section with nothing in it.
func TestUnresolvedIsNilWhenEverythingIsFilled(t *testing.T) {
	if got := Unresolved(`name = "api"`, nil); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// TestExistingValuePlaceholderWarnsAgainstReplacingTheWholeValue — the append merge has no template syntax,
// so a flat snippet renders it as "<YOUR_EXISTING_VALUE> -javaagent:...". A customer who reads that token as
// "delete this" reintroduces exactly the clobbering the merge model prevents, and the description is the only
// thing standing between them and it.
func TestExistingValuePlaceholderWarnsAgainstReplacingTheWholeValue(t *testing.T) {
	got := Unresolved(`Value: "<YOUR_EXISTING_VALUE> -javaagent:/var/odigos/java/javaagent.jar"`, nil)
	if len(got) != 1 {
		t.Fatalf("got %+v", got)
	}
	d := strings.ToLower(got[0].Description)
	if !strings.Contains(d, "added to it") && !strings.Contains(d, "not instead of") {
		t.Errorf("description does not say the Odigos value is added rather than substituted: %q", got[0].Description)
	}
}
