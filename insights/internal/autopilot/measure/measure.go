// Package measure holds the timings of probe and entry spans: what the
// ingest path records, the batcher aggregates and the leader reads back from
// Redis. It is a leaf package so the loop and interrogation can use it.
package measure

import (
	"cmp"
	"slices"
	"time"
	"unicode"

	"github.com/odigos-io/odigos/insights/internal/engine/histogram"
)

const (
	// BucketSeconds is the width of a time bucket, keyed by span start.
	BucketSeconds = 10
	// MaxErrors is how many distinct error messages a measure keeps.
	MaxErrors = 3
	// MaxMessage is the longest error message kept, in characters.
	MaxMessage = 160
	// minSpansForP95 is the sample count below which a p95 is not reported.
	minSpansForP95 = 20
)

// Sample is one measured span. Site is empty for a transaction's entry span,
// whose SelfUs is the time outside every child and whose Spans is the size
// of the sub-trace.
type Sample struct {
	Bucket int64
	Site   string
	DurUs  int64
	SelfUs int64
	Err    bool
	Msg    string
	Spans  int64
}

// ErrorCount is an error message and how many spans carried it.
type ErrorCount struct {
	Message string
	Count   int64
}

// Measure sums the samples of one site (or entry) in a time window.
type Measure struct {
	N      int64
	Err    int64
	SumUs  int64
	MaxUs  int64
	SelfUs int64
	Hist   histogram.Histogram
	Spans  int64
	Errors []ErrorCount
}

// Transaction is the entry measure of a transaction and its probe sites.
type Transaction struct {
	Entry Measure
	Sites map[string]*Measure
}

// Timings is keyed by transaction id.
type Timings map[string]*Transaction

// Add folds one sample in.
func (m *Measure) Add(s Sample) {
	m.N++
	m.SumUs += s.DurUs
	m.MaxUs = max(m.MaxUs, s.DurUs)
	m.SelfUs += s.SelfUs
	m.Spans += s.Spans
	m.Hist.Observe(s.DurUs, histogram.DefaultLayout())
	if s.Err {
		m.Err++
		m.AddError(s.Msg, 1)
	}
}

// Merge folds o in.
func (m *Measure) Merge(o *Measure) {
	m.N += o.N
	m.Err += o.Err
	m.SumUs += o.SumUs
	m.MaxUs = max(m.MaxUs, o.MaxUs)
	m.SelfUs += o.SelfUs
	m.Spans += o.Spans
	m.Hist.Add(o.Hist, histogram.DefaultLayout())
	for _, e := range o.Errors {
		m.AddError(e.Message, e.Count)
	}
}

// AddError counts msg, dropping it when MaxErrors other messages are held.
func (m *Measure) AddError(msg string, count int64) {
	if msg == "" {
		return
	}
	for i := range m.Errors {
		if m.Errors[i].Message == msg {
			m.Errors[i].Count += count
			return
		}
	}
	if len(m.Errors) < MaxErrors {
		m.Errors = append(m.Errors, ErrorCount{Message: msg, Count: count})
	}
}

// SortErrors puts the most frequent message first.
func (m *Measure) SortErrors() {
	slices.SortStableFunc(m.Errors, func(a, b ErrorCount) int { return cmp.Compare(b.Count, a.Count) })
}

// AvgMs is the mean duration in milliseconds.
func (m *Measure) AvgMs() float64 {
	if m.N == 0 {
		return 0
	}
	return float64(m.SumUs) / float64(m.N) / float64(time.Millisecond/time.Microsecond)
}

// SelfAvgMs is the mean time outside the measure's children, in milliseconds.
func (m *Measure) SelfAvgMs() float64 {
	if m.N == 0 {
		return 0
	}
	return float64(m.SelfUs) / float64(m.N) / float64(time.Millisecond/time.Microsecond)
}

// P95Ms is the upper bound of the histogram bucket holding the 95th
// percentile, in milliseconds. It is 0 below 20 samples.
func (m *Measure) P95Ms() float64 {
	if m.N < minSpansForP95 {
		return 0
	}
	us, _ := m.Hist.Percentile(0.95, histogram.DefaultLayout())
	return float64(us) / float64(time.Millisecond/time.Microsecond)
}

// CleanMessage removes control characters and cuts msg to MaxMessage
// characters.
func CleanMessage(msg string) string {
	r := []rune(msg)
	out := r[:0]
	for _, c := range r {
		if !unicode.IsControl(c) {
			out = append(out, c)
		}
	}
	return string(out[:min(len(out), MaxMessage)])
}
