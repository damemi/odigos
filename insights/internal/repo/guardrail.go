package repo

import "time"

// RuleKey identifies a service-guardrail rule type (e.g. "allowed_callers").
// The risk package's guardrail catalog is the source of truth for valid keys.
type RuleKey string

// RuleMode is how one guardrail rule behaves. The zero value normalizes to
// RuleModeSuggest, so a freshly seeded rule observes-and-recommends until the
// operator flips it to enforce.
type RuleMode string

const (
	// RuleModeSuggest records the profile and recommends a rule but never fires
	// a finding. It's the safe default so turning a service into a guardrail
	// can't page anyone before the allowlist has been curated.
	RuleModeSuggest RuleMode = "suggest"
	// RuleModeEnforce fires a policy-violation finding whenever the rule breaks.
	RuleModeEnforce RuleMode = "enforce"
	// RuleModeOff disables a rule without deleting the guardrail row. Used as the
	// per-service disable for auto-enabled transaction guardrails so reconcile does
	// not immediately recreate enforcement after the operator turns it off.
	RuleModeOff RuleMode = "off"
)

// NormalizeRuleMode maps an empty/unknown mode to RuleModeSuggest so a blank DB
// value or a partially-filled form always resolves to the safe default. Explicit
// off is preserved so operators can opt out of auto-enable.
func NormalizeRuleMode(m RuleMode) RuleMode {
	switch m {
	case RuleModeEnforce:
		return RuleModeEnforce
	case RuleModeOff:
		return RuleModeOff
	default:
		return RuleModeSuggest
	}
}

// TransactionIdentity is the stable allowlist string for one (kind, operation).
func TransactionIdentity(kind TransactionKind, operation string) string {
	return string(kind) + "\t" + operation
}

// InventoryRuleMode returns the normalized mode for allowed_transactions
// (suggest when the rule is absent).
func InventoryRuleMode(g ServiceGuardrail, rule RuleKey) RuleMode {
	if g.RuleModes == nil {
		return RuleModeSuggest
	}
	mode, ok := g.RuleModes[rule]
	if !ok {
		return RuleModeSuggest
	}
	return NormalizeRuleMode(mode)
}

// InventoryProtected reports whether the given rule is enforced on the guardrail.
func InventoryProtected(g ServiceGuardrail, rule RuleKey) bool {
	return InventoryRuleMode(g, rule) == RuleModeEnforce
}

// InventoryOptedOut reports whether the rule is explicitly off (operator opt-out).
func InventoryOptedOut(g ServiceGuardrail, rule RuleKey) bool {
	return InventoryRuleMode(g, rule) == RuleModeOff
}

// ServiceGuardrail is an operator-declared, service-scoped policy about how a
// service may communicate - L7 micro-segmentation of intent, not learned
// "normal". A break is a policy violation surfaced in the Findings feed.
//
// Rules are open-ended: each RuleKey names a rule type in the risk guardrail
// catalog, so adding one needs no schema change. RuleLists holds each rule's
// allowlist and RuleModes its per-rule mode; both are sparse (an absent rule is
// unconfigured). RuleOrigins records how a rule was created (e.g. auto
// transaction guardrail). Scope/ScopeKey use the shared scope stack (see scope.go).
type ServiceGuardrail struct {
	Scope       PolicyScope
	ScopeKey    string
	RuleLists   map[RuleKey][]string
	RuleModes   map[RuleKey]RuleMode
	RuleOrigins map[RuleKey]string
	// RuleFingerprints records the config a rule's allowlist was built under, so
	// a reader can tell whether the list still describes the world it is
	// policing. Only allowed_transactions uses it: its entries are whole
	// transaction identities, which change shape when an operator edits the
	// identity dimensions (see IdentityDimensionsFingerprint). The other rules
	// list service and host names, which no setting reshapes, so they leave it
	// unset.
	RuleFingerprints map[RuleKey]string
	// RuleCorrelations holds structured cross-span correlation specs per rule,
	// used by the attribute_correlation rule (transaction-scoped). Sparse: an
	// absent rule has no correlations. Unlike RuleLists, these carry span
	// selectors, a relation, and a per-rule severity rather than plain strings.
	RuleCorrelations map[RuleKey][]CorrelationSpec
	// IntegrationIDs routes this guardrail's violations to those backends.
	// Empty falls back to the global alert policy's list (the default route).
	IntegrationIDs []string
}

// CorrelationSelector locates one attribute on one span within a trace. Span
// matches either the span name or the captured code.function.name (e.g.
// resolvePrincipal, getAccount). Extract is an optional regex applied to the
// attribute value; capture group 1 is used when present, else the whole match.
type CorrelationSelector struct {
	Service string
	Span    string
	Attr    string
	Extract string
}

// CorrelationSpec is one cross-span consistency assertion for the
// attribute_correlation rule: compare Left vs Right within a single trace and
// fire a violation when they do not satisfy Relation. Severity is the per-rule
// risk of a violation (defaults to critical when empty).
type CorrelationSpec struct {
	Name     string
	Left     CorrelationSelector
	Right    CorrelationSelector
	Relation string
	Severity string
	Why      string
}

// CorrelationRelation values for CorrelationSpec.Relation.
const (
	CorrelationEquals    = "equals"
	CorrelationNotEquals = "not_equals"
)

// SetRuleFingerprint stamps a rule with the config its allowlist was built
// under, allocating the map on first use. An empty fingerprint clears the stamp,
// which leaves the rule unenforceable - the intended state for a list whose
// provenance is unknown.
func SetRuleFingerprint(guardrail *ServiceGuardrail, rule RuleKey, fingerprint string) {
	if fingerprint == "" {
		delete(guardrail.RuleFingerprints, rule)
		return
	}
	if guardrail.RuleFingerprints == nil {
		guardrail.RuleFingerprints = map[RuleKey]string{}
	}
	guardrail.RuleFingerprints[rule] = fingerprint
}

// GuardrailOriginAutoTransactionGuardrail marks allowed_transactions rules
// created by the auto-enable / reconcile path (vs manual Enable transaction guardrail).
const GuardrailOriginAutoTransactionGuardrail = "auto_transaction_guardrail"

// ServiceProfile is the learned communication summary of one service, derived
// from the D1 (call-edge) and D2 (egress) baselines. Returned by
// GetServiceProfile to pre-fill a guardrail's allowlist with observed traffic
// so the operator curates instead of typing from scratch. Caller/callee
// matching is by service name, so cross-namespace collisions are a v1 limitation.
//
// Callers/Callees/Egress are the observed neighborhood (default checkbox
// selection / "adopt observed"). Free-form UI fields cover callers/callees/
// egress beyond those observed edges.
type ServiceProfile struct {
	Namespace    string
	Service      string
	Callers      []string // service names observed calling this service (seeds allowed_callers)
	Callees      []string // internal service names this service calls
	Egress       []string // external hosts this service reaches
	Transactions []string // kind\toperation identities (seeds allowed_transactions)
}

// GuardrailViolationHit is a flush batch's worth of one (service, rule,
// offending item) guardrail break. The engine buffers per-trace occurrences and
// flushes them as summed increments via RecordGuardrailViolations, keeping a
// steadily-broken edge one finding with a rising count rather than one row per
// request. FirstSeen is the earliest break in the batch; LastSeen/LastTraceID
// are the newest.
type GuardrailViolationHit struct {
	Scope       PolicyScope
	ScopeKey    string // ServiceScopeKey(namespace, service) of the offending service
	RuleKey     RuleKey
	Offending   string
	Occurrences int64
	MaxScore    int
	FirstSeen   time.Time
	LastSeen    time.Time
	LastTraceID string
}

// GuardrailViolationStatus is where a violation group sits in its lifecycle,
// derived from the group's max-merged decision timestamps. It buckets the
// Findings feed's policy-violation tabs.
type GuardrailViolationStatus string

const (
	// GuardrailViolationOpen is unresolved: the operator hasn't accepted or
	// dismissed it. New violations start here.
	GuardrailViolationOpen GuardrailViolationStatus = "open"
	// GuardrailViolationDismissed was closed without changing the guardrail.
	// The count keeps rising if the edge still fires, so the operator can watch
	// a "known but not acted on" violation without it cluttering the open tab.
	GuardrailViolationDismissed GuardrailViolationStatus = "dismissed"
	// GuardrailViolationAccepted was accepted as legitimate - the offending edge
	// is allowlisted on the guardrail so it stops alerting. Sticky: once accepted
	// it stays here.
	GuardrailViolationAccepted GuardrailViolationStatus = "accepted"
)

// GuardrailResolution is how an operator closed a violation group. It selects
// which decision timestamp ResolveGuardrailViolation stamps, and thus the tab
// the group moves to.
type GuardrailResolution string

const (
	// GuardrailResolveAccept accepts the violation as legitimate (allowlists the
	// edge). Mirrors AnomalyResolveAccept; both land in the shared "accepted" tab.
	GuardrailResolveAccept  GuardrailResolution = "accept"  // → GuardrailViolationAccepted
	GuardrailResolveDismiss GuardrailResolution = "dismiss" // → GuardrailViolationDismissed
	GuardrailResolveReopen  GuardrailResolution = "reopen"  // dismissed → GuardrailViolationOpen (re-triage)
)

// GuardrailViolationGroup is one policy-violation finding: a (service, rule,
// offending item) rolled up across occurrences with how often/recently it broke
// and its lifecycle Status. Returned by ListGuardrailViolations for the debug
// UI Findings feed, which tabs between open, dismissed, and allowed.
type GuardrailViolationGroup struct {
	Service     string
	Namespace   string
	ScopeKey    string
	RuleKey     RuleKey
	Offending   string
	Occurrences int64
	MaxScore    int
	FirstSeen   time.Time
	LastSeen    time.Time
	LastTraceID string
	Status      GuardrailViolationStatus
}

// GuardrailEvidenceReason builds the sample_reason tag
// ("guardrail:<rule>:<offending>") stored on a guardrail evidence trace, so the
// debug UI / product API can fetch a violation group's trace via GetLatestObservationByReason.
// Shared by the engine (writer) and debug UI (reader) so they agree.
func GuardrailEvidenceReason(rule RuleKey, offending string) string {
	return "guardrail:" + string(rule) + ":" + offending
}
