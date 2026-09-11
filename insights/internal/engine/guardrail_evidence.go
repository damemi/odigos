package engine

import (
	"strconv"
	"strings"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// GuardrailSpanHighlights marks the spans in a captured evidence trace that
// witness a guardrail violation, so the Findings evidence waterfall can point
// the operator straight at the offending edge. It mirrors the extractors in
// guardrail.go (forbiddenCallers/Callees/Egress) but works on the rendered
// SpanView list rather than the live Trace:
//
//   - caller/callee rules: the offending item is a service name, so every span
//     owned by that service is highlighted;
//   - egress rule: the offending item is "host" or "host:port", so the CLIENT
//     span(s) whose destination matches are highlighted.
//
// Adding a rule type here is one case, matching its extractor.
func GuardrailSpanHighlights(rule repo.RuleKey, offending string, spans []SpanView) []SpanHighlight {
	switch rule {
	case risk.RuleAllowedCallers:
		return highlightByService(spans, offending, "unauthorized caller")
	case risk.RuleAllowedCallees:
		return highlightByService(spans, offending, "unauthorized callee")
	case risk.RuleAllowedEgress:
		return highlightEgress(spans, offending)
	case risk.RuleAttributeCorrelation:
		return highlightCorrelation(spans, offending)
	default:
		return nil
	}
}

// highlightCorrelation flags the two spans that witness a cross-span correlation
// violation. The offending string leads with the two "service/span" sites (see
// correlationOffending), so we parse them back out and highlight every span that
// matches a site by service + name/code.function.name. Values trail in a quoted
// parenthetical and are ignored here.
func highlightCorrelation(spans []SpanView, offending string) []SpanHighlight {
	lsite, rsite, ok := parseCorrelationSites(offending)
	if !ok {
		return nil
	}
	var out []SpanHighlight
	for _, s := range spans {
		if spanViewMatchesSite(s, lsite) || spanViewMatchesSite(s, rsite) {
			out = append(out, SpanHighlight{SpanID: s.SpanID, Severity: "high", Reason: "correlation mismatch"})
		}
	}
	return out
}

// correlationSite is a (service, span) selector parsed from an offending string.
type correlationSite struct{ service, span string }

// parseCorrelationSites extracts the two service/span sites from a correlation
// offending string of the form
// "<name>: <lsvc>/<lspan> <op> <rsvc>/<rspan> (\"lval\" vs \"rval\")".
func parseCorrelationSites(offending string) (correlationSite, correlationSite, bool) {
	i := strings.Index(offending, ": ")
	if i < 0 {
		return correlationSite{}, correlationSite{}, false
	}
	region := offending[i+2:]
	if p := strings.LastIndex(region, " ("); p >= 0 {
		region = region[:p]
	}
	var sides []string
	switch {
	case strings.Contains(region, " "+correlationOpNotEqual+" "):
		sides = strings.SplitN(region, " "+correlationOpNotEqual+" ", 2)
	case strings.Contains(region, " "+correlationOpEqual+" "):
		sides = strings.SplitN(region, " "+correlationOpEqual+" ", 2)
	default:
		return correlationSite{}, correlationSite{}, false
	}
	if len(sides) != 2 {
		return correlationSite{}, correlationSite{}, false
	}
	return parseSite(sides[0]), parseSite(sides[1]), true
}

// parseSite splits "service/span" on the first slash; span may itself contain
// slashes (e.g. an HTTP span name), so only the first separator is used.
func parseSite(s string) correlationSite {
	if j := strings.Index(s, "/"); j >= 0 {
		return correlationSite{service: s[:j], span: s[j+1:]}
	}
	return correlationSite{service: s}
}

// spanViewMatchesSite mirrors spanMatchesSelector for the rendered SpanView.
func spanViewMatchesSite(s SpanView, site correlationSite) bool {
	if site.span == "" {
		return false
	}
	if site.service != "" && !strings.EqualFold(s.Service, site.service) {
		return false
	}
	if s.Name == site.span {
		return true
	}
	attrs := s.AttrMap()
	if fqn := attrs[semconv.CodeFunctionName]; fqn != "" {
		if fqn == site.span || strings.HasSuffix(fqn, "."+site.span) {
			return true
		}
	}
	if ns, fn := attrs[semconv.CodeNamespace], attrs[semconv.CodeFunction]; fn != "" {
		site2 := fn
		if ns != "" {
			site2 = ns + "." + fn
		}
		if site2 == site.span || strings.HasSuffix(site2, "."+site.span) {
			return true
		}
	}
	return false
}

// highlightByService flags every span owned by the named service.
func highlightByService(spans []SpanView, service, reason string) []SpanHighlight {
	var out []SpanHighlight
	for _, s := range spans {
		if strings.EqualFold(s.Service, service) {
			out = append(out, SpanHighlight{SpanID: s.SpanID, Severity: "high", Reason: reason})
		}
	}
	return out
}

// highlightEgress flags outbound (CLIENT/PRODUCER) spans whose destination
// matches the offending "host" or "host:port". A bare host matches any port,
// mirroring the allowlist semantics in forbiddenEgress. Destination attributes
// are read with the same semconv keys peersFromTrace uses so the match is
// consistent with what fired the violation.
func highlightEgress(spans []SpanView, offending string) []SpanHighlight {
	host, port := splitOffendingHostPort(offending)
	var out []SpanHighlight
	for _, s := range spans {
		if s.Kind != "CLIENT" && s.Kind != "PRODUCER" {
			continue
		}
		attrs := s.AttrMap()
		spanHost := firstNonEmpty(
			attrs[semconv.ServerAddress],
			attrs[semconv.LegacyNetPeerName],
			attrs[semconv.LegacyHTTPHost],
			attrs[semconv.LegacyDBConnectionString],
		)
		if !strings.EqualFold(spanHost, host) {
			continue
		}
		if port > 0 {
			spanPort := atoiOrZero(firstNonEmpty(attrs[semconv.ServerPort], attrs[semconv.LegacyNetPeerPort]))
			if spanPort != port {
				continue
			}
		}
		out = append(out, SpanHighlight{SpanID: s.SpanID, Severity: "high", Reason: "egress to unauthorized destination"})
	}
	return out
}

// splitOffendingHostPort splits a "host:port" destination into its parts,
// returning port 0 when the item is a bare host (or the suffix isn't numeric).
func splitOffendingHostPort(s string) (host string, port int) {
	if i := strings.LastIndex(s, ":"); i >= 0 {
		if p, err := strconv.Atoi(s[i+1:]); err == nil {
			return s[:i], p
		}
	}
	return s, 0
}
