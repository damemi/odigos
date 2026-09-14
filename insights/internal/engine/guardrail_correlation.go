package engine

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
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
		left, lok := extractSelectorValue(full, spec.Left)
		right, rok := extractSelectorValue(full, spec.Right)
		if !lok || !rok {
			continue
		}
		equal := left == right
		violated := (spec.Relation == repo.CorrelationEquals && !equal) ||
			(spec.Relation == repo.CorrelationNotEquals && equal)
		if !violated {
			continue
		}
		out = append(out, guardrailViolation{
			rule:  risk.RuleAttributeCorrelation,
			item:  correlationOffending(spec, equal),
			score: risk.ScoreForSeverity(spec.Severity),
		})
	}
	return out
}

// extractSelectorValue finds the first span matching the selector, reads the
// attribute, and applies the optional regex extract. ok is false when no span
// matches or the attribute is absent.
func extractSelectorValue(full *Trace, sel repo.CorrelationSelector) (string, bool) {
	for _, s := range full.Spans {
		if !spanMatchesSelector(s, sel) {
			continue
		}
		raw := s.Attr(sel.Attr)
		if raw == "" {
			continue
		}
		return applyExtract(raw, sel.Extract), true
	}
	return "", false
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

// applyExtract returns capture group 1 of the regex (or the whole match when the
// pattern has no groups); an empty pattern or a non-match returns the value
// verbatim. The pattern is validated on write, so a compile error is treated as
// "no extraction" rather than dropping the assertion.
func applyExtract(value, pattern string) string {
	if pattern == "" {
		return value
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
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
