package histogram

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Scheme names how bucket bounds are derived.
type Scheme string

const SchemeExponential Scheme = "exponential"

// Layout is the runtime bucket geometry used by Observe, Bars, and Percentile.
type Layout struct {
	Scheme      Scheme
	BucketCount int
}

// DefaultLayout returns the compiled-in default (32 exponential buckets).
func DefaultLayout() Layout {
	return Layout{Scheme: SchemeExponential, BucketCount: repo.DefaultHistogramBucketCount}
}

func (l Layout) withDefaults() Layout {
	if l.Scheme == "" {
		l.Scheme = SchemeExponential
	}
	if l.BucketCount <= 0 {
		l.BucketCount = repo.DefaultHistogramBucketCount
	}
	return l
}

func (l Layout) bucketCount() int {
	return l.withDefaults().BucketCount
}

// Fingerprint returns a short stable hash of this layout geometry.
func (l Layout) Fingerprint() string {
	l = l.withDefaults()
	b, _ := json.Marshal(struct {
		Scheme      Scheme `json:"scheme"`
		BucketCount int    `json:"bucket_count"`
	}{l.Scheme, l.BucketCount})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// LayoutForStored derives bucket geometry from a decoded histogram slice.
func LayoutForStored(h Histogram) Layout {
	n := len(h.Buckets)
	l := DefaultLayout()
	if n > l.BucketCount {
		l.BucketCount = n
	}
	return l
}

// MergeLayout returns a layout wide enough to hold both histograms.
func MergeLayout(a, b Histogram) Layout {
	la := LayoutForStored(a)
	lb := LayoutForStored(b)
	if lb.BucketCount > la.BucketCount {
		la.BucketCount = lb.BucketCount
	}
	return la
}
