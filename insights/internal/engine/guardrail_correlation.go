package engine

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// correlationViolations evaluates the transaction-scoped attribute_correlation
// rule over the FULL trace and returns one violation per spec whose asserted
// relation between two captured span attributes does not hold. It is no-learning
// and additive: it never touches baselines and layers on top of the service
// allowlist. Suggest/off mode is observe-only. A spec whose left or right span
// (or attribute) is absent emits nothing - the assertion cannot be evaluated.
func correlationViolations(full *Trace, guardrail repo.ServiceGuardrail) []guardrailViolation {
	if full == nil || !enforced(guardrail, risk.RuleAttributeCorrelation) {
		return nil
	}
	specs := guardrail.RuleCorrelations[risk.RuleAttributeCorrelation]
	if len(specs) == 0 {
		return nil
	}
	var out []guardrailViolation
	for _, spec := range specs {
		res := evaluateCorrelation(full, spec)
		if !res.evaluated || res.held {
			continue
		}
		out = append(out, guardrailViolation{
			rule:  risk.RuleAttributeCorrelation,
			item:  correlationOffending(spec, spec.Relation == repo.CorrelationNotEquals),
			score: risk.ScoreForSeverity(spec.Severity),
		})
	}
	return out
}

// One rule checked on one trace. left/right are the pair that decided it: the
// breaking pair on a violation, the first pair otherwise.
type correlationResult struct {
	// false when a side has no matching span: the rule says nothing here.
	evaluated   bool
	held        bool
	left, right string
}

// Every span matching a side is checked, not just the first: when an operation
// runs twice in one trace (a legitimate lookup and an attacker-induced one)
// the rule breaks whichever ran first. Mining, preview, shadow counting and
// enforcement share this, so a recommendation's numbers are what applying it
// enforces.
func evaluateCorrelation(full *Trace, spec repo.CorrelationSpec) correlationResult {
	lefts := selectorValues(full, spec.Left)
	rights := selectorValues(full, spec.Right)
	if len(lefts) == 0 || len(rights) == 0 {
		return correlationResult{}
	}
	res := correlationResult{evaluated: true, held: true, left: lefts[0], right: rights[0]}
	wantEqual := spec.Relation != repo.CorrelationNotEquals
	for _, l := range lefts {
		for _, r := range rights {
			if (l == r) != wantEqual {
				return correlationResult{evaluated: true, held: false, left: l, right: r}
			}
		}
	}
	return res
}

// The attribute's value on every span the selector matches, extract applied.
func selectorValues(full *Trace, sel repo.CorrelationSelector) []string {
	var out []string
	for _, s := range full.Spans {
		if !spanMatchesSelector(s, sel) {
			continue
		}
		raw := s.Attr(sel.Attr)
		if raw == "" {
			continue
		}
		out = append(out, applyExtract(raw, sel.Extract))
	}
	return out
}

// spanMatchesSelector matches a span by owning service and by span identity: the
// span name, the captured code.function.name site, or a suffix of it (so a short
// selector like "resolvePrincipal" matches a fully-qualified
// "com.rowan.edge.JwtService.resolvePrincipal").
func spanMatchesSelector(s *Span, sel repo.CorrelationSelector) bool {
	if sel.Service != "" && !strings.EqualFold(s.Service, sel.Service) {
		return false
	}
	if sel.Span == "" {
		return false
	}
	if s.Name == sel.Span {
		return true
	}
	if site := d4SiteOf(s); site != "" {
		if site == sel.Span || strings.HasSuffix(site, "."+sel.Span) {
			return true
		}
	}
	if fn := s.Attr(semconv.CodeFunctionName); fn != "" {
		if fn == sel.Span || strings.HasSuffix(fn, "."+sel.Span) {
			return true
		}
	}
	return false
}

// extractRegexes caches compiled extract patterns. Patterns come from a small,
// operator-authored set (guardrail specs and open recommendations), so the
// cache stays tiny while sparing the per-trace path a regexp compile.
var extractRegexes sync.Map // pattern string -> *regexp.Regexp (nil when invalid)

func compiledExtract(pattern string) *regexp.Regexp {
	if cached, ok := extractRegexes.Load(pattern); ok {
		re, _ := cached.(*regexp.Regexp)
		return re
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		re = nil
	}
	extractRegexes.Store(pattern, re)
	return re
}

// applyExtract returns capture group 1 of the regex (or the whole match when the
// pattern has no groups); an empty pattern or a non-match returns the value
// verbatim. The pattern is validated on write, so a compile error is treated as
// "no extraction" rather than dropping the assertion.
func applyExtract(value, pattern string) string {
	if pattern == "" {
		return value
	}
	re := compiledExtract(pattern)
	if re == nil {
		return value
	}
	m := re.FindStringSubmatch(value)
	if m == nil {
		return value
	}
	if len(m) > 1 {
		return m[1]
	}
	return m[0]
}

// correlationOffending renders the violation's offending string. It is the
// stable grouping key for a spec: the correlation name plus the two
// service/span sites and the (relation-determined) operator, with no concrete
// values, so every trace that breaks the same spec aggregates into one
// violation. The observed values live in the captured evidence trace and its
// span highlights, not in this identity.
func correlationOffending(spec repo.CorrelationSpec, equal bool) string {
	op := correlationOpNotEqual
	if equal {
		op = correlationOpEqual
	}
	return fmt.Sprintf("%s: %s/%s %s %s/%s",
		spec.Name, spec.Left.Service, spec.Left.Span, op, spec.Right.Service, spec.Right.Span)
}

const (
	correlationOpEqual    = "equal"
	correlationOpNotEqual = "not equal"
)
