package engine

import (
	"reflect"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

func highlightedIDs(highlights []SpanHighlight) []string {
	var ids []string
	for _, h := range highlights {
		ids = append(ids, h.SpanID)
	}
	return ids
}

// The evidence trace of an allowed_transactions violation is the whole trace:
// here alloy's GET is called by notification-api, whose CLIENT span carries the
// same name. Only alloy's entry span is the transaction that broke the rule.
func TestGuardrailSpanHighlightsAllowedTransactions(t *testing.T) {
	const route = "GET /api/v0/web/components/discovery.kubernetes.pods"
	spans := []SpanView{
		{SpanID: "root", Name: "POST banking/documents", Service: "edge-gateway", Kind: "SERVER", Depth: 0},
		{SpanID: "notif-server", Name: "POST /documents", Service: "notification-api", Kind: "SERVER", Depth: 2},
		{SpanID: "notif-client", Name: route, Service: "notification-api", Kind: "CLIENT", Depth: 5},
		{SpanID: "alloy-server", Name: route, Service: "alloy", Kind: "SERVER", Depth: 6},
		// The same name inside alloy, but not an entry: not the transaction.
		{SpanID: "alloy-internal", Name: route, Service: "alloy", Kind: "INTERNAL", Depth: 7},
	}
	identity := repo.TransactionIdentity("HTTP", route+" [http.response.status_code=200]")

	got := highlightedIDs(GuardrailSpanHighlights(risk.RuleAllowedTransactions, "monitoring/alloy", identity, spans))
	if want := []string{"alloy-server"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("highlighted %v, want %v", got, want)
	}

	// No identity dimensions: the operation is the bare span name.
	got = highlightedIDs(GuardrailSpanHighlights(risk.RuleAllowedTransactions, "monitoring/alloy", repo.TransactionIdentity("HTTP", route), spans))
	if want := []string{"alloy-server"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("without dimensions: highlighted %v, want %v", got, want)
	}

	// The trace root is an entry whatever its kind (a CRON root is INTERNAL).
	cron := []SpanView{{SpanID: "job", Name: "nightly-report", Service: "reporter", Kind: "INTERNAL", Depth: 0}}
	got = highlightedIDs(GuardrailSpanHighlights(risk.RuleAllowedTransactions, "jobs/reporter", repo.TransactionIdentity("CRON", "nightly-report"), cron))
	if want := []string{"job"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("root entry: highlighted %v, want %v", got, want)
	}

	// A name that only shares a prefix is a different transaction.
	got = highlightedIDs(GuardrailSpanHighlights(risk.RuleAllowedTransactions, "monitoring/alloy", repo.TransactionIdentity("HTTP", route+"/extra"), spans))
	if len(got) != 0 {
		t.Fatalf("prefix-only name: highlighted %v, want none", got)
	}

	// Another service's guardrail, or a scope key that isn't a service: nothing.
	if got = highlightedIDs(GuardrailSpanHighlights(risk.RuleAllowedTransactions, "default/frontend", identity, spans)); len(got) != 0 {
		t.Fatalf("other service: highlighted %v, want none", got)
	}
	if got = highlightedIDs(GuardrailSpanHighlights(risk.RuleAllowedTransactions, "12345", identity, spans)); len(got) != 0 {
		t.Fatalf("transaction scope key: highlighted %v, want none", got)
	}
}
