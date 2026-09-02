// Package risk is the threat-scoring layer: it turns deviation signals into a
// single 0-100 risk score and a severity tier. The engine detects deviations,
// hands this package a list of Signals, and gets back an Assessment to persist
// and display.
//
// It is pure and dependency-light (only repo, for DeviationClass). Keeping the
// weights, categories, and bands here in one catalog is what makes the layer
// extensible: a new deviation class or enricher is one catalog entry that every
// policy picks up without further code changes.
package risk

import (
	"math"
	"sort"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Category is an outcome-oriented grouping of signals (what an attacker is
// trying to achieve), stable across class additions so a new class joins an
// existing category. Each maps to a MITRE/OWASP tag for the UI.
type Category string

const (
	CategoryInjection       Category = "injection"        // malformed input reaching an interpreter (SQLi, cmd, JNDI)
	CategoryExfiltration    Category = "exfiltration"     // data / connections leaving to unexpected destinations
	CategoryAccessAnomaly   Category = "access_anomaly"   // auth / authz / tenant-boundary breaks
	CategorySupplyChain     Category = "supply_chain"     // new/unexpected code, libraries, versions
	CategoryLateralMovement Category = "lateral_movement" // new internal paths / new backend dependencies
	CategoryRecon           Category = "recon"            // probing / enumeration behavior
	CategoryResourceAbuse   Category = "resource_abuse"   // availability / cost anomalies (latency, payload)
	CategoryImpact          Category = "impact"           // destructive / integrity-affecting actions (DELETE, DROP)
)

// CategoryInfo carries a category's default weight ("base"), a human label, and
// its threat-intel tags. Base is the fallback weight for signals in the
// category; a class can override it with its own catalog weight.
type CategoryInfo struct {
	Base  int
	Label string
	Mitre string
	Owasp string
}

// Categories is the category taxonomy (insights plan §9). Bikeshed the numbers
// here — this is the single source of truth.
var Categories = map[Category]CategoryInfo{
	CategoryInjection:       {Base: 40, Label: "Injection", Mitre: "T1190", Owasp: "A03"},
	CategoryExfiltration:    {Base: 35, Label: "Exfiltration", Mitre: "TA0010"},
	CategoryAccessAnomaly:   {Base: 35, Label: "Access anomaly", Mitre: "TA0004", Owasp: "A01"},
	CategorySupplyChain:     {Base: 30, Label: "Supply chain", Mitre: "T1195", Owasp: "A06"},
	CategoryLateralMovement: {Base: 25, Label: "Lateral movement", Mitre: "TA0008"},
	CategoryRecon:           {Base: 15, Label: "Recon", Mitre: "TA0043"},
	CategoryResourceAbuse:   {Base: 10, Label: "Resource abuse", Mitre: "T1499"},
	CategoryImpact:          {Base: 45, Label: "Impact", Mitre: "TA0040"},
}

// CategoryLabel is the human name for a category ("Lateral movement"), or the
// raw category string if it has no label.
func CategoryLabel(c Category) string {
	if info, ok := Categories[c]; ok && info.Label != "" {
		return info.Label
	}
	return string(c)
}

// CatalogEntry maps a deviation class to its primary category, default weight,
// and display text. Weight is the class's own contribution ceiling and can
// differ from the category base. Label/Desc are finding-oriented (what went
// wrong); BaselineLabel/BaselineDesc name what the class learns on a
// transaction baseline.
type CatalogEntry struct {
	Category      Category
	Weight        int
	Label         string // finding title, e.g. "New database access"
	Desc          string // one-line "what it detects"
	BaselineLabel string // transaction baseline name, e.g. "Database access"
	BaselineDesc  string // tooltip: what this class learns for a transaction
}

// Catalog maps every deviation class to its risk meaning. Adding a class = add
// one line here (plus its Scorer); it then contributes to every score and is
// covered by every policy with no other change.
var Catalog = map[repo.DeviationClass]CatalogEntry{
	repo.D1CallEdges: {
		Category: CategoryLateralMovement, Weight: 20,
		Label: "New call edge", Desc: "A service→service call path not seen during learning.",
		BaselineLabel: "Service call edges", BaselineDesc: "Learned set of service→service call paths for this transaction.",
	},
	repo.D2Egress: {
		Category: CategoryExfiltration, Weight: 35,
		Label: "New egress destination", Desc: "An outbound destination (host/port) not seen during learning.",
		BaselineLabel: "Outbound peers", BaselineDesc: "Learned set of external destinations (host:port) this transaction reaches.",
	},
	repo.D3Latency: {
		Category: CategoryResourceAbuse, Weight: 10,
		Label: "Latency above baseline", Desc: "Request latency well above the learned p99.",
		BaselineLabel: "Request latency", BaselineDesc: "Learned latency distribution for this transaction's entry request (e.g. p99).",
	},
	repo.D4ArgsReturns: {
		Category: CategoryInjection, Weight: 40,
		Label: "Argument / return anomaly", Desc: "Arguments or return values outside the learned shape.",
		BaselineLabel: "Args & returns", BaselineDesc: "Learned shape of captured arguments and return values.",
	},
	repo.D5Libraries: {
		Category: CategorySupplyChain, Weight: 30,
		Label: "New instrumentation library", Desc: "An instrumentation library/runtime not seen during learning.",
		BaselineLabel: "Libraries", BaselineDesc: "Learned set of instrumentation libraries and runtimes seen on this transaction.",
	},
	repo.D6AttrRelations: {
		Category: CategoryAccessAnomaly, Weight: 35,
		Label: "Attribute-relation anomaly", Desc: "Span attribute relationships outside the learned pattern.",
		BaselineLabel: "Attribute relations", BaselineDesc: "Learned relationships between span attributes for this transaction.",
	},
	repo.D7DBAccess: {
		Category: CategoryLateralMovement, Weight: 25,
		Label: "New database access", Desc: "A database table/operation not accessed during learning.",
		BaselineLabel: "Database access", BaselineDesc: "Learned set of database systems, tables, and operations touched.",
	},
	repo.D8PayloadSize: {
		Category: CategoryResourceAbuse, Weight: 20,
		Label: "Payload size above baseline", Desc: "Request/response payload larger than the learned baseline.",
		BaselineLabel: "Payload size", BaselineDesc: "Learned request/response body-size distribution for this transaction.",
	},
}

// ClassLabel is the finding-oriented human name for a deviation class
// ("New database access"), or the raw class string if unknown.
func ClassLabel(class repo.DeviationClass) string {
	if e, ok := Catalog[class]; ok && e.Label != "" {
		return e.Label
	}
	return string(class)
}

// ClassDesc is the one-line "what it detects" for a class, or "" if unknown.
func ClassDesc(class repo.DeviationClass) string { return Catalog[class].Desc }

// ClassBaselineLabel is the transaction-baseline name for a class
// ("Database access"), falling back to ClassLabel when unset.
func ClassBaselineLabel(class repo.DeviationClass) string {
	if e, ok := Catalog[class]; ok && e.BaselineLabel != "" {
		return e.BaselineLabel
	}
	return ClassLabel(class)
}

// ClassBaselineDesc is the tooltip for what a class learns on a transaction, or "" if unknown.
func ClassBaselineDesc(class repo.DeviationClass) string { return Catalog[class].BaselineDesc }

// tagDescriptions maps a MITRE ATT&CK or OWASP Top 10 tag code to a short plain
// description, shown as a UI tooltip. Covers every code referenced in
// Categories; add a line when a new category introduces a new code.
var tagDescriptions = map[string]string{
	// MITRE ATT&CK
	"T1190":  "Exploit Public-Facing Application",
	"T1195":  "Supply Chain Compromise",
	"T1499":  "Endpoint Denial of Service",
	"TA0004": "Privilege Escalation (tactic)",
	"TA0008": "Lateral Movement (tactic)",
	"TA0010": "Exfiltration (tactic)",
	"TA0040": "Impact (tactic)",
	"TA0043": "Reconnaissance (tactic)",
	// OWASP Top 10 (2021)
	"A01": "Broken Access Control",
	"A03": "Injection",
	"A06": "Vulnerable and Outdated Components",
}

// TagDescription returns a short description for a MITRE/OWASP tag code, or ""
// if the code is unknown.
func TagDescription(code string) string { return tagDescriptions[code] }

// Severity is the human tier derived from a score.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

// Score band cutoffs (inclusive lower bound): info 0-19, low 20-34,
// medium 35-49, high 50-74, critical 75-100. Default fire-at is High.
const (
	bandLow      = 20
	bandMedium   = 35
	bandHigh     = 50
	bandCritical = 75

	// CorrelationBonus is added once when signals span >= 2 kill-chain
	// categories — a multi-stage attack story is worse than the sum of parts.
	// Exported so the UI can show it as its own line and reconcile the total.
	CorrelationBonus = 15
)

// SeverityForScore maps a 0-100 score to its tier.
func SeverityForScore(score int) Severity {
	switch {
	case score >= bandCritical:
		return SeverityCritical
	case score >= bandHigh:
		return SeverityHigh
	case score >= bandMedium:
		return SeverityMedium
	case score >= bandLow:
		return SeverityLow
	default:
		return SeverityInfo
	}
}

// killChain is the set of categories that, when two or more co-occur, form an
// attack narrative and earn the correlation bonus. Symptom-only categories
// (resource_abuse, recon) are excluded so noise doesn't self-amplify.
var killChain = map[Category]bool{
	CategoryInjection:       true,
	CategoryExfiltration:    true,
	CategoryAccessAnomaly:   true,
	CategorySupplyChain:     true,
	CategoryLateralMovement: true,
	CategoryImpact:          true,
}

// Signal is one contribution to a risk assessment, from a deviation class or a
// context enricher, where Contribution = round(Weight * Confidence). The JSON
// tags are the stored/wire shape rendered by the UI.
type Signal struct {
	Source       repo.SignalSource `json:"source"`     // e.g. "D2_egress"
	Category     Category          `json:"category"`   // for grouping + correlation
	Weight       int               `json:"weight"`     // this signal's ceiling
	Confidence   float64           `json:"confidence"` // 0-1, scales with magnitude
	Contribution int               `json:"contribution"`
	Rationale    string            `json:"rationale"`
	Mitre        string            `json:"mitre,omitempty"`
	Owasp        string            `json:"owasp,omitempty"`
}

// Assessment is the scored result for one anomaly: the aggregate score, its
// severity tier, and the signals that produced it (kept for explainability).
type Assessment struct {
	Score      int        `json:"score"`
	Severity   Severity   `json:"severity"`
	Signals    []Signal   `json:"signals"`
	Categories []Category `json:"categories"`
	Correlated bool       `json:"correlated"`
}

// DefaultWeight returns the catalog's default weight for a deviation class. ok
// is false for a class with no catalog entry (a placeholder), so callers — e.g.
// the policy editor rendering per-class weight fields — can skip it.
func DefaultWeight(class repo.DeviationClass) (int, bool) {
	entry, ok := Catalog[class]
	return entry.Weight, ok
}

// NewSignal builds a signal for a deviation class at its catalog default weight,
// scaled by confidence (0-1). ok is false for a class with no catalog entry, so
// callers skip unknown/placeholder classes. Per-scope weight overrides are
// applied later by ApplyWeightOverrides. Called by the engine's scorers.
func NewSignal(class repo.DeviationClass, confidence float64, rationale string) (Signal, bool) {
	entry, ok := Catalog[class]
	if !ok {
		return Signal{}, false
	}
	return NewCategorySignal(repo.SignalSource(class), entry.Category, entry.Weight, confidence, rationale), true
}

// ApplyWeightOverrides rewrites each signal's weight and contribution when the
// policy supplies an override for its Source (0 mutes the signal). weights is
// keyed by Source, covering class and enricher signals uniformly. Mutates and
// returns the slice. Called by the engine before Assess.
func ApplyWeightOverrides(signals []Signal, weights map[repo.SignalSource]int) []Signal {
	if len(weights) == 0 {
		return signals
	}
	for i := range signals {
		w, ok := weights[signals[i].Source]
		if !ok {
			continue
		}
		if w < 0 {
			w = 0
		}
		signals[i].Weight = w
		signals[i].Contribution = int(math.Round(float64(w) * signals[i].Confidence))
	}
	return signals
}

// NewCategorySignal builds a signal not tied to a deviation class — the output
// of a context enricher or guardrail. The category supplies the threat-intel
// tags; weight and confidence work as for a class signal. Unlike NewSignal
// there is no catalog lookup, so it can model a signal the class catalog can't.
func NewCategorySignal(source repo.SignalSource, category Category, weight int, confidence float64, rationale string) Signal {
	if weight < 0 {
		weight = 0
	}
	confidence = clamp01(confidence)
	info := Categories[category]
	return Signal{
		Source:       source,
		Category:     category,
		Weight:       weight,
		Confidence:   confidence,
		Contribution: int(math.Round(float64(weight) * confidence)),
		Rationale:    rationale,
		Mitre:        info.Mitre,
		Owasp:        info.Owasp,
	}
}

// Assess aggregates signals into a capped 0-100 score and severity tier. It
// sums contributions (a quiet signal never dilutes a loud one), adds a one-time
// correlation bonus when >= 2 kill-chain categories co-occur, then clamps to
// 100. Empty input scores 0 / info. Called by the engine per fired anomaly.
func Assess(signals []Signal) Assessment {
	a := Assessment{Signals: signals}
	if len(signals) == 0 {
		a.Severity = SeverityInfo
		return a
	}

	total := 0
	seen := map[Category]bool{}
	killChainHit := 0
	for _, s := range signals {
		total += s.Contribution
		if !seen[s.Category] {
			seen[s.Category] = true
			a.Categories = append(a.Categories, s.Category)
			if killChain[s.Category] {
				killChainHit++
			}
		}
	}
	if killChainHit >= 2 {
		total += CorrelationBonus
		a.Correlated = true
	}

	a.Score = clampScore(total)
	a.Severity = SeverityForScore(a.Score)
	sort.Slice(a.Categories, func(i, j int) bool { return a.Categories[i] < a.Categories[j] })
	return a
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

func clampScore(n int) int {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return n
}
