// Package histogram provides a configurable exponential histogram over a
// non-negative integer value, used by the engine's metric scorers: D7 observes
// payload byte sizes, D3 observes entry-span latency in microseconds. Counts
// are additive across disjoint observation streams (see Add), which lets the
// baseline store flush deltas and the database sum them across replicas.
//
// Bucket geometry comes from Layout (global system settings plus optional
// service/transaction overrides). Bucket 0 counts value==0; bucket k≥1 covers
// [2^(k-1), 2^k) of the observed unit for exponential layouts.
package histogram

import (
	"math"
	"math/bits"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// BucketCount is the default number of exponential buckets when Layout is unset.
const BucketCount = repo.DefaultHistogramBucketCount

// Histogram is a fixed exponential histogram over a non-negative integer value.
type Histogram struct {
	Buckets []uint64 `json:"buckets"`
}

// Bar is one chart-ready bucket: inclusive lo, exclusive hi (lo==hi==0 for the
// zero bucket), and the observation count.
type Bar struct {
	Lo    int64
	Hi    int64
	Count uint64
}

// New returns a Histogram with its bucket slice allocated for DefaultLayout.
func New() Histogram {
	return NewWithLayout(DefaultLayout())
}

// NewWithLayout returns a Histogram with buckets allocated for layout.
func NewWithLayout(layout Layout) Histogram {
	n := layout.bucketCount()
	return Histogram{Buckets: make([]uint64, n)}
}

// Ensure allocates or pads the bucket slice to layout.BucketCount. Call after
// JSON-decoding a Histogram whose buckets may be missing or short.
func (h *Histogram) Ensure(layout Layout) {
	n := layout.bucketCount()
	if len(h.Buckets) == 0 {
		h.Buckets = make([]uint64, n)
	}
	if len(h.Buckets) < n {
		padded := make([]uint64, n)
		copy(padded, h.Buckets)
		h.Buckets = padded
	}
}

func bucketFor(value int64, layout Layout) int {
	if value <= 0 {
		return 0
	}
	n := layout.bucketCount()
	k := bits.Len64(uint64(value))
	if k >= n {
		return n - 1
	}
	return k
}

// Observe records a single value into its bucket.
func (h *Histogram) Observe(value int64, layout Layout) {
	h.Ensure(layout)
	h.Buckets[bucketFor(value, layout)]++
}

// Add folds another histogram into this one bucket-wise, combining two disjoint
// observation streams (e.g. a replica's flushed delta into the stored
// distribution) - additive because each side counted different traces.
func (h *Histogram) Add(o Histogram, layout Layout) {
	h.Ensure(layout)
	n := layout.bucketCount()
	for i := 0; i < n && i < len(o.Buckets); i++ {
		h.Buckets[i] += o.Buckets[i]
	}
}

// Total returns the number of observations across all buckets.
func (h *Histogram) Total() uint64 {
	var n uint64
	for _, c := range h.Buckets {
		n += c
	}
	return n
}

// barBounds returns inclusive lo and exclusive hi for bucket index i.
func barBounds(i int, layout Layout) (lo, hi int64) {
	if i == 0 {
		return 0, 0
	}
	return int64(1) << (i - 1), int64(1) << i
}

// Bars returns chart bars in index order for layout.
func (h Histogram) Bars(layout Layout) []Bar {
	layout = layout.withDefaults()
	n := layout.bucketCount()
	h.Ensure(layout)
	out := make([]Bar, n)
	for i := 0; i < n; i++ {
		c := uint64(0)
		if i < len(h.Buckets) {
			c = h.Buckets[i]
		}
		lo, hi := barBounds(i, layout)
		out[i] = Bar{Lo: lo, Hi: hi, Count: c}
	}
	return out
}

// Percentile returns the inclusive upper bound of the bucket that contains the
// given quantile (e.g. 0.99 for p99). The second return is false when empty.
func (h *Histogram) Percentile(p float64, layout Layout) (int64, bool) {
	layout = layout.withDefaults()
	total := h.Total()
	if total == 0 {
		return 0, false
	}
	target := uint64(math.Ceil(p * float64(total)))
	if target < 1 {
		target = 1
	}
	var cum uint64
	for i, c := range h.Buckets {
		cum += c
		if cum >= target {
			if i == 0 {
				return 0, true
			}
			return (int64(1) << i) - 1, true
		}
	}
	return 0, false
}
