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
	default:
		return nil
	}
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
