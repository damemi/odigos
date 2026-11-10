package engine

import (
	"bytes"
	"encoding/json"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// Scorer is the per-class strategy the engine dispatches to, one per
// DeviationClass (see Engine.scoreEntry / processOne). Score compares a trace
// to the current baseline and reports whether it deviates plus a render-ready
// evidence blob; it MUST NOT mutate current. Merge folds a non-anomalous trace
// into the baseline and returns the bytes to persist; the engine never calls it
// for anomaly-flagged traces (poisoning protection).
type Scorer interface {
	Class() repo.DeviationClass
	Score(t *Trace, current json.RawMessage) (deviated bool, evidence json.RawMessage, err error)
	Merge(t *Trace, current json.RawMessage) (updated json.RawMessage, err error)
}

// decodedBaseline is one class's baseline parsed into the form its scorer
// compares traces against (d1Data, d3Data, ...), able to score a trace itself.
// Holding the parsed value behind this interface is what lets the engine keep
// it: classState caches one per class and every trace scores against it,
// instead of re-parsing the same JSON per trace.
//
// score MUST NOT mutate the receiver's contents: one value is shared by every
// trace of the transaction until the class's data changes. A value receiver
// copies only the top level, so the danger is writing through what it points at
// - an element of a set slice, a map entry, or a histogram bucket (Observe/Add,
// which belong to Merge). Doing so would teach the baseline the very thing it
// just flagged, and the anomaly would fire once and never again.
type decodedBaseline interface {
	score(t *Trace) (deviated bool, evidence json.RawMessage, err error)
}

// baselineDecoder is an optional Scorer capability: it exposes the decode half
// of Score so the engine can do it once and score many traces against the
// result. That is safe because a promoted baseline is frozen - processOne stops
// merging into it, and only promoted classes are scored - so the value being
// reused is one that has stopped changing. Consumed by classState.decodedFor
// and Engine.scoreEntry; a scorer without this capability still works and is
// decoded per trace as before.
type baselineDecoder interface {
	decodeBaseline(raw json.RawMessage) decodedBaseline
}

// stabilityComparator is an optional Scorer capability reporting whether
// folding a trace structurally GREW the baseline (a new set member or
// newly-populated bucket) rather than just reinforcing existing counts. The
// histogram classes implement it because their Merge rewrites byte content
// every observation, so a plain byte compare would never look stable. Consumed
// by baselineGrew.
type stabilityComparator interface {
	grew(before, after json.RawMessage) bool
}

// baselineGrew reports whether an observation added something new to the
// baseline, the signal behind stability promotion. Set classes have canonical
// (sorted) Merge output so a byte compare is exact; other classes implement
// stabilityComparator. Called by Engine.processOne while learning.
func baselineGrew(sc Scorer, before, after json.RawMessage) bool {
	if c, ok := sc.(stabilityComparator); ok {
		return c.grew(before, after)
	}
	return !bytes.Equal(before, after)
}

// Explainer is an optional Scorer capability that turns the scorer's own Score
// evidence into a render-ready Finding, attributing highlights to concrete
// spans of the anomalous trace. It lives on the scorer so the evidence producer
// and reader evolve together; scorers without it fall back to a minimal
// finding. Dispatched by Explain (finding.go).
type Explainer interface {
	Explain(evidence json.RawMessage, spans []SpanView) Finding
}

// RiskEnricher is an optional Scorer capability: after its class deviates, it
// inspects its OWN evidence and emits EXTRA risk signals for dangerous
// sub-cases (e.g. a new DB access that is a DELETE, or one on a sensitive
// table). The base class signal stays low while these add on top, so a benign
// new read scores "low" and a destructive one "critical" - same class,
// different severity. Dispatched by Engine.enrichRisk. Signals that correlate
// ACROSS classes belong in the cross-class registry in enrich.go instead.
type RiskEnricher interface {
	EnrichRisk(evidence json.RawMessage, cfg enrichConfig) []risk.Signal
}

// defaultScorers returns the fixed registry of scorers, used by the Engine and
// the finding/explainer dispatch. A class only learns, promotes and scores if
// its scorer is listed here.
func defaultScorers() []Scorer {
	return []Scorer{
		&d1CallEdges{},
		&d2Egress{},
		&d3Latency{},
		&d4ArgsReturns{},
		&d5Libraries{},
		&d6DBAccess{},
		&d7PayloadSize{},
	}
}

// ScoredClasses lists the deviation classes the engine actively learns and
// scores (one per default scorer), in registry order. It is derived from the
// scorer registry rather than repo.AllDeviationClasses so a class added to the
// repo without a scorer never gets a learning placeholder it can't fill.
func ScoredClasses() []repo.DeviationClass {
	scorers := defaultScorers()
	classes := make([]repo.DeviationClass, len(scorers))
	for i, sc := range scorers {
		classes[i] = sc.Class()
	}
	return classes
}

// FillScoredPlaceholders augments a transaction's stored baseline rows with an
// empty placeholder for every scored class that has no row yet, returned in
// ScoredClasses order followed by any stored class the engine no longer scores
// (so nothing is ever hidden).
//
// A class has no row right after an operator "re-learn" wipes its baseline, or
// before the transaction's first trace re-seeds it. Callers that present a
// per-class view (the HTTP API and the debug UI) use this so a class never
// blinks out of the list during that window: a placeholder is unpromoted with a
// zero observation count and empty data - exactly "learning, nothing yet".
func FillScoredPlaceholders(transactionID int64, rows []repo.BaselineClass) []repo.BaselineClass {
	byClass := make(map[repo.DeviationClass]repo.BaselineClass, len(rows))
	for _, bc := range rows {
		byClass[bc.Class] = bc
	}
	out := make([]repo.BaselineClass, 0, len(rows)+len(defaultScorers()))
	seen := make(map[repo.DeviationClass]bool, len(rows))
	for _, class := range ScoredClasses() {
		seen[class] = true
		if bc, ok := byClass[class]; ok {
			out = append(out, bc)
			continue
		}
		out = append(out, repo.BaselineClass{
			TransactionID:     transactionID,
			Class:             class,
			Data:              json.RawMessage("{}"),
			DataSchemaVersion: 1,
		})
	}
	for _, bc := range rows {
		if !seen[bc.Class] {
			out = append(out, bc)
		}
	}
	return out
}
