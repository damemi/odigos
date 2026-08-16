package plan

import (
	"sort"
	"strings"
)

// Substitution and the accounting that goes with it.
//
// This is what turns a per-TYPE artifact into per-WORKLOAD instructions, and it happens here rather than in a
// client for one reason: <OTLP_ENDPOINT> and <OTLP_ENDPOINT_URL> are the same destination in two forms that
// are not interchangeable, and a client that substituted them would be a second place that has to know which
// variable takes which. Getting it wrong is invisible — the OpenTelemetry SDK accepts a bare host:port for
// the URL form and then exports nowhere — so the knowledge lives in exactly one place.
//
// Not every blank gets filled, and that is fine. A container name on a task definition packing several
// applications, a Lambda layer ARN for a layer nobody has published yet, a resource group that exists only in
// the customer's account: those are genuinely the customer's to supply. Unresolved reports them WITH what
// they mean, so an unfilled blank reads as a field rather than as a rendering bug.

// Substitute replaces every placeholder that has a value with that value, and leaves the rest verbatim.
//
// An empty value is treated as no value. Substituting one would turn "the connector does not know your
// container's name" into "your container has no name", which renders as a syntactically valid snippet that
// silently instruments nothing.
func Substitute(text string, values map[string]string) string {
	if text == "" || len(values) == 0 {
		return text
	}
	for token, value := range values {
		if value == "" || !placeholderRe.MatchString(token) {
			continue
		}
		text = strings.ReplaceAll(text, token, value)
	}
	return text
}

// SubstituteAll applies Substitute to a slice, returning a new one. nil in, nil out.
func SubstituteAll(texts []string, values map[string]string) []string {
	if texts == nil {
		return nil
	}
	out := make([]string, 0, len(texts))
	for _, t := range texts {
		out = append(out, Substitute(t, values))
	}
	return out
}

// Unresolved returns the placeholders still present in text after substitution, each with what belongs
// there, sorted by token so a rendering is stable.
//
// It reads the RENDERED text rather than the plan's declarations on purpose. Declarations describe the
// desired state; a delivery artifact is hand-authored per platform and may carry blanks the plan knows
// nothing about (a resource group, a function name) and may omit ones it does. Deriving from the text means a
// customer is never asked for a value the snippet in front of them does not contain, and never left to
// discover one it does.
//
// declared are the plan's own placeholders, which take precedence: a provider that describes its layer ARN
// more precisely than the base could should have that description win.
func Unresolved(text string, declared []Placeholder) []Placeholder {
	tokens := placeholdersIn(text)
	if len(tokens) == 0 {
		return nil
	}

	byToken := make(map[string]string, len(declared))
	for _, ph := range declared {
		if ph.Description != "" {
			byToken[ph.Token] = ph.Description
		}
	}

	seen := map[string]bool{}
	out := make([]Placeholder, 0, len(tokens))
	for _, tok := range tokens {
		if seen[tok] {
			continue
		}
		seen[tok] = true
		out = append(out, Placeholder{Token: tok, Description: describeToken(tok, byToken)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Token < out[j].Token })
	return out
}

// describeToken resolves what a placeholder means: the plan's own declaration first, then the base's
// canonical descriptions, then a generic one.
func describeToken(token string, declared map[string]string) string {
	if d, ok := declared[token]; ok {
		return d
	}
	if ph, ok := wellKnown[token]; ok {
		return ph.Description
	}
	// A token neither the plan nor the base knows comes from a hand-authored delivery artifact, and by
	// convention those name a resource in the customer's own account: <YOUR_FUNCTION_NAME>,
	// <YOUR_RESOURCE_GROUP>, <YOUR_APP_NAME>. The token already says which one, so the fallback says where to
	// look rather than inventing a definition for something the base does not actually know.
	return "A value from your own account or deployment definition — Odigos has no way to determine it."
}

// wellKnown is every placeholder the base defines, keyed by token.
//
// A provider declaring one of these on its own plan gets the base's wording unless it overrides it, which is
// the point: the same placeholder described three ways across three providers is the same documentation bug
// three times, and the two endpoint forms in particular are exactly where a reworded description turns into a
// customer pasting the wrong one.
var wellKnown = func() map[string]Placeholder {
	all := []Placeholder{
		ContainerPlaceholder,
		ServiceNamePlaceholder,
		OTLPEndpointPlaceholder,
		OTLPEndpointURLPlaceholder,
		OTLPInsecurePlaceholder,
		RegionPlaceholder,
		RuntimeVersionPlaceholder,
		ExistingValuePlaceholder,
	}
	m := make(map[string]Placeholder, len(all))
	for _, ph := range all {
		m[ph.Token] = ph
	}
	return m
}()

// PlaceholderExistingValue is how a hand-authored artifact renders the LEFT side of an append: the value the
// customer's own definition already carries.
//
// It has no counterpart in the plan model — there the same thing is expressed as MergeAppend with a
// separator — because a template is a flat string and cannot express "combine with whatever is there". Its
// description is the one that has to carry the warning, since a customer who reads the token as "delete this
// line" produces exactly the silent value-clobbering the merge model exists to prevent.
const PlaceholderExistingValue = "<YOUR_EXISTING_VALUE>"

// ExistingValuePlaceholder is the base's canonical description of PlaceholderExistingValue.
var ExistingValuePlaceholder = Placeholder{
	Token:       PlaceholderExistingValue,
	Description: "Whatever your workload already sets for this variable, kept as-is. The Odigos value is added to it, not instead of it — replacing the whole value discards your own settings, and Odigos has no record of what they were.",
}
