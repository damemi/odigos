package risk

import "github.com/odigos-io/odigos-enterprise/insights/internal/repo"

// Service-guardrail catalog. A guardrail is an operator-declared L7 micro-
// segmentation rule ("only these services may call me"), not learned behaviour.
// This catalog is to guardrails what enricher_catalog.go is to enrichers: one
// entry per rule type declares its id, violation signal, and editor metadata,
// so adding a rule type needs no schema or template change.
//
// To add a rule type: pick a repo.RuleKey and repo.SignalSource, add one
// GuardrailRule below, and teach the engine's guardrail evaluator to check it
// (see engine/guardrail.go). Editor, seeding, scoring, and badging are all
// catalog-driven.

// ProfileFacet names which observed-communication facet of a ServiceProfile
// pre-fills a rule's allowlist in the editor, so "adopt observed" starts the
// operator from reality instead of a blank box.
type ProfileFacet string

const (
	FacetCallers      ProfileFacet = "callers"      // services observed calling this service
	FacetCallees      ProfileFacet = "callees"      // internal services this service calls
	FacetEgress       ProfileFacet = "egress"       // external hosts this service reaches
	FacetTransactions ProfileFacet = "transactions" // transaction identities (kind + operation)
)

// Guardrail rule keys and their violation signal sources. Kept as typed
// constants so the engine, editor, and Findings badging reference the same ids.
const (
	RuleAllowedCallers      repo.RuleKey = "allowed_callers"
	RuleAllowedCallees      repo.RuleKey = "allowed_callees"
	RuleAllowedEgress       repo.RuleKey = "allowed_egress"
	RuleAllowedTransactions repo.RuleKey = "allowed_transactions"
	// RuleAttributeCorrelation asserts two captured span attributes stay
	// consistent within one trace. Transaction-scoped, no learning; the per-rule
	// severity on each spec drives the emitted score, not the catalog Weight.
	RuleAttributeCorrelation repo.RuleKey = "attribute_correlation"

	SignalForbiddenCaller      repo.SignalSource = "guardrail_forbidden_caller"
	SignalForbiddenCallee      repo.SignalSource = "guardrail_forbidden_callee"
	SignalForbiddenEgress      repo.SignalSource = "guardrail_forbidden_egress"
	SignalForbiddenTransaction repo.SignalSource = "guardrail_forbidden_transaction"
	SignalAttributeCorrelation repo.SignalSource = "guardrail_attribute_correlation"
)

// GuardrailRule is one rule type: its id, the signal a break emits, its risk
// weight/category, and the editor metadata. Weight is deliberately high - a
// violated rule is a declared-intent break, not a fuzzy anomaly - so an
// enforced violation lands in the "high" band on its own.
type GuardrailRule struct {
	Key       repo.RuleKey
	Source    repo.SignalSource
	Category  Category
	Weight    int
	Label     string       // editor heading, e.g. "Allowed callers"
	Desc      string       // one-line "what it enforces"
	Hint      string       // editor textarea hint
	SeedFacet ProfileFacet // which observed facet pre-fills the allowlist
}

// GuardrailRules is the rule-type catalog. One line per rule type.
var GuardrailRules = []GuardrailRule{
	{
		Key:       RuleAllowedCallers,
		Source:    SignalForbiddenCaller,
		Category:  CategoryLateralMovement,
		Weight:    60,
		Label:     "Allowed callers",
		Desc:      "Only these services may call this service; any other caller is a violation (inbound).",
		Hint:      "Comma- or newline-separated service names allowed to call this service.",
		SeedFacet: FacetCallers,
	},
	{
		Key:       RuleAllowedCallees,
		Source:    SignalForbiddenCallee,
		Category:  CategoryLateralMovement,
		Weight:    55,
		Label:     "Allowed callees",
		Desc:      "This service may only call these internal services; any other dependency is a violation (outbound).",
		Hint:      "Comma- or newline-separated internal service names this service is allowed to call.",
		SeedFacet: FacetCallees,
	},
	{
		Key:       RuleAllowedEgress,
		Source:    SignalForbiddenEgress,
		Category:  CategoryExfiltration,
		Weight:    55,
		Label:     "Allowed egress",
		Desc:      "This service may only reach these external destinations; any other egress is a violation.",
		Hint:      "Comma- or newline-separated external destinations. Use host (e.g. api.stripe.com) to allow any port, or host:port (e.g. api.stripe.com:443) to pin a port.",
		SeedFacet: FacetEgress,
	},
	{
		Key:       RuleAllowedTransactions,
		Source:    SignalForbiddenTransaction,
		Category:  CategoryRecon,
		Weight:    40,
		Label:     "Allowed transactions",
		Desc:      "This service may only expose these transactions; any new operation/kind is a violation.",
		Hint:      "One entry per line as KIND operation (e.g. HTTP GET /products, GRPC myapp.User/Login).",
		SeedFacet: FacetTransactions,
	},
	{
		Key:      RuleAttributeCorrelation,
		Source:   SignalAttributeCorrelation,
		Category: CategoryAccessAnomaly,
		// Fallback only; the per-rule Severity on each CorrelationSpec drives the
		// emitted score (see ScoreForSeverity).
		Weight: bandCritical,
		Label:  "Attribute correlation",
		Desc:   "Two captured span attributes must stay consistent within one trace; a mismatch is a violation (transaction-scoped).",
		Hint:   "Configured per transaction as span/attribute selectors with a relation and optional regex extract.",
	},
}

// ScoreForSeverity maps a per-rule severity tier to a representative score in
// that band, so a rule's chosen risk survives the round-trip through
// SeverityForScore on the finding. An empty/unknown tier defaults to critical.
func ScoreForSeverity(severity string) int {
	switch Severity(severity) {
	case SeverityLow:
		return bandLow
	case SeverityMedium:
		return bandMedium
	case SeverityHigh:
		return bandHigh
	default:
		return bandCritical
	}
}

// GuardrailRuleByKey returns the rule type for a key.
func GuardrailRuleByKey(key repo.RuleKey) (GuardrailRule, bool) {
	for _, rule := range GuardrailRules {
		if rule.Key == key {
			return rule, true
		}
	}
	return GuardrailRule{}, false
}

// IsGuardrailSource reports whether a signal source is a guardrail-violation
// signal (rather than a deviation-class or enricher signal). The Findings feed
// uses this to badge an item as a policy violation instead of an anomaly.
func IsGuardrailSource(source repo.SignalSource) bool {
	for _, rule := range GuardrailRules {
		if rule.Source == source {
			return true
		}
	}
	return false
}

// GuardrailSignal builds the risk signal for a violated guardrail rule at its
// catalog weight and full confidence (a declared break is certain, not a
// probabilistic deviation).
func GuardrailSignal(rule GuardrailRule, rationale string) Signal {
	return NewCategorySignal(rule.Source, rule.Category, rule.Weight, 1.0, rationale)
}
