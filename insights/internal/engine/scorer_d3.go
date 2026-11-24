package engine

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/odigos-io/odigos/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// d3Latency scores a (sub-)transaction's entry-span latency (its end-to-end
// wall time) against a learned distribution: learning records the entry
// duration in microseconds into an exponential histogram, and after promotion a
// request slower than the baseline p99 deviates. The signal is the entry span,
// so a nested service entry is judged on its own latency, not the whole trace's.
//
// JSONB shape:
//
//	{"latency":{"buckets":[...]}}
type d3Latency struct{}

type d3Data struct {
	Latency histogram.Histogram `json:"latency"`
}

func (d3Latency) Class() repo.DeviationClass { return repo.D3Latency }

func (s d3Latency) Score(t *Trace, current json.RawMessage) (bool, json.RawMessage, error) {
	return s.scoreWithLayout(t, current, histogram.DefaultLayout())
}

// decodeBaseline implements baselineDecoder.
func (d3Latency) decodeBaseline(raw json.RawMessage) decodedBaseline {
	d, _ := decodeD3(raw)
	return d
}

func (s d3Latency) scoreWithLayout(t *Trace, current json.RawMessage, layout histogram.Layout) (bool, json.RawMessage, error) {
	baseline, _ := decodeD3(current)
	return baseline.scoreWithLayout(t, layout)
}

// score implements decodedBaseline; it only reads the histogram
// (Total/Percentile), never Observe, so the shared value is left untouched.
func (baseline d3Data) score(t *Trace) (bool, json.RawMessage, error) {
	return baseline.scoreWithLayout(t, histogram.DefaultLayout())
}

func (baseline d3Data) scoreWithLayout(t *Trace, layout histogram.Layout) (bool, json.RawMessage, error) {
	if baseline.Latency.Total() == 0 {
		return false, nil, nil
	}
	us, ok := entryLatencyMicros(t)
	if !ok {
		return false, nil, nil
	}
	p99, ok := baseline.Latency.Percentile(0.99, layout)
	if !ok || !aboveRatio(us, p99, d3DeviationRatio) {
		return false, nil, nil
	}
	ev, err := json.Marshal(map[string]any{
		"latency_us":  us,
		"latency_p99": p99,
	})
	if err != nil {
		return true, nil, err
	}
	return true, ev, nil
}

func (d3Latency) Merge(t *Trace, current json.RawMessage) (json.RawMessage, error) {
	return d3Latency{}.mergeWithLayout(t, current, histogram.DefaultLayout())
}

type d3Learning struct{ data d3Data }

func (d3Latency) decodeLearning(raw json.RawMessage) learningBaseline {
	d, _ := decodeD3(raw)
	return &d3Learning{data: d}
}

func (l *d3Learning) merge(t *Trace) (bool, bool) {
	us, ok := entryLatencyMicros(t)
	if !ok {
		return false, false
	}
	before := histogram.Histogram{Buckets: slices.Clone(l.data.Latency.Buckets)}
	l.data.Latency.Observe(us, histogram.DefaultLayout())
	return true, histGrew(before, l.data.Latency)
}

func (l *d3Learning) encode() (json.RawMessage, error) { return json.Marshal(l.data) }

func (l *d3Learning) members() int { return 0 }

func (d3Latency) mergeWithLayout(t *Trace, current json.RawMessage, layout histogram.Layout) (json.RawMessage, error) {
	baseline, _ := decodeD3(current)
	us, ok := entryLatencyMicros(t)
	if !ok {
		return current, nil
	}
	baseline.Latency.Observe(us, layout)
	return json.Marshal(baseline)
}

// grew implements stabilityComparator: the latency baseline has grown only when
// a previously-empty bucket becomes populated (a new latency band), not when
// counts rise within known bands.
func (d3Latency) grew(before, after json.RawMessage) bool {
	b, _ := decodeD3(before)
	a, _ := decodeD3(after)
	return histGrew(b.Latency, a.Latency)
}

func decodeD3(raw json.RawMessage) (d3Data, error) {
	if len(raw) == 0 {
		return d3Data{Latency: histogram.New()}, nil
	}
	var d d3Data
	if err := json.Unmarshal(raw, &d); err != nil {
		return d3Data{Latency: histogram.New()}, err
	}
	d.Latency.Ensure(histogram.LayoutForStored(d.Latency))
	return d, nil
}

// Explain implements Explainer: latency is a single-number metric finding, so
// it reports observed-vs-baseline p99 and highlights the entry (root) span that
// carries the end-to-end latency.
func (d3Latency) Explain(evidence json.RawMessage, spans []SpanView) Finding {
	f := Finding{
		Class:       repo.D3Latency,
		Kind:        FindingMetric,
		Title:       titleFor(repo.D3Latency),
		RawEvidence: evidence,
	}

	var ev struct {
		LatencyUs  int64 `json:"latency_us"`
		LatencyP99 int64 `json:"latency_p99"`
	}
	_ = json.Unmarshal(evidence, &ev)

	f.Metric = &MetricComparison{Observed: ev.LatencyUs, Baseline: ev.LatencyP99, Unit: "us"}
	f.Summary = fmt.Sprintf("Entry latency %s exceeded the learned p99 of %s.",
		humanMicros(ev.LatencyUs), humanMicros(ev.LatencyP99))

	if len(spans) > 0 {
		f.SpanHighlights = append(f.SpanHighlights, SpanHighlight{
			SpanID:   spans[0].SpanID,
			Severity: "medium",
			Reason:   "entry span slower than baseline p99",
		})
	}
	return f
}

// humanMicros renders a microsecond duration compactly (µs / ms / s).
func humanMicros(us int64) string {
	switch {
	case us < 1000:
		return fmt.Sprintf("%dµs", us)
	case us < 1_000_000:
		return fmt.Sprintf("%.1fms", float64(us)/1000)
	default:
		return fmt.Sprintf("%.2fs", float64(us)/1_000_000)
	}
}

// entryLatencyMicros returns the entry-span wall time in microseconds. ok=false
// when timing is absent or the span ends before it starts (clock skew), so such
// traces are neither scored nor learned rather than counted as zero-latency.
func entryLatencyMicros(t *Trace) (int64, bool) {
	if t == nil || t.Root == nil {
		return 0, false
	}
	s := t.Root
	if s.EndUnix <= s.StartUnix {
		return 0, false
	}
	return int64((s.EndUnix - s.StartUnix) / uint64(time.Microsecond)), true
}

// d3DeviationRatio is how far over its learned p99 a request's latency must be
// to deviate: latency is noisy, and the p99 is a power-of-two bucket edge.
const d3DeviationRatio = 1.5

// aboveRatio reports whether value exceeds baseline by more than ratio.
func aboveRatio(value, baseline int64, ratio float64) bool {
	return float64(value) > float64(baseline)*ratio
}
