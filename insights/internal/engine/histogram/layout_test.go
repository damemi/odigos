package histogram

import (
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestDefaultLayout(t *testing.T) {
	l := DefaultLayout()
	if l.Scheme != SchemeExponential || l.BucketCount != repo.DefaultHistogramBucketCount {
		t.Fatalf("DefaultLayout = %+v, want exponential/%d", l, repo.DefaultHistogramBucketCount)
	}
}

func TestLayoutFingerprintStableAfterDefaults(t *testing.T) {
	empty := Layout{}
	def := DefaultLayout()
	if empty.Fingerprint() != def.Fingerprint() {
		t.Fatal("zero layout must fingerprint as the compiled-in default so replicas agree")
	}
	if empty.Fingerprint() != empty.Fingerprint() {
		t.Fatal("fingerprint must be stable across calls")
	}
	wider := Layout{Scheme: SchemeExponential, BucketCount: 48}
	if wider.Fingerprint() == def.Fingerprint() {
		t.Fatal("a wider bucket count must change the fingerprint")
	}
}

func TestLayoutForStoredWidens(t *testing.T) {
	short := Histogram{Buckets: make([]uint64, 16)}
	if got := LayoutForStored(short); got.BucketCount != repo.DefaultHistogramBucketCount {
		t.Fatalf("short stored hist layout = %+v, want at least default %d", got, repo.DefaultHistogramBucketCount)
	}
	wide := Histogram{Buckets: make([]uint64, 48)}
	if got := LayoutForStored(wide); got.BucketCount != 48 {
		t.Fatalf("wide stored hist layout = %+v, want 48", got)
	}
}

func TestMergeLayoutTakesMaxBucketCount(t *testing.T) {
	a := Histogram{Buckets: make([]uint64, 16)}
	b := Histogram{Buckets: make([]uint64, 48)}
	got := MergeLayout(a, b)
	if got.BucketCount != 48 || got.Scheme != SchemeExponential {
		t.Fatalf("MergeLayout = %+v, want exponential/48 so both deltas fit", got)
	}
	if MergeLayout(b, a).BucketCount != 48 {
		t.Fatal("MergeLayout must be commutative")
	}
}
