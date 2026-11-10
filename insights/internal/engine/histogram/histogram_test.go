package histogram

import "testing"

func TestBarsBounds(t *testing.T) {
	layout := DefaultLayout()
	h := NewWithLayout(layout)
	h.Observe(0, layout)
	h.Observe(1, layout)
	h.Observe(100, layout)
	h.Observe(100, layout)

	bars := h.Bars(layout)
	if len(bars) != layout.bucketCount() {
		t.Fatalf("len(bars)=%d, want %d", len(bars), layout.bucketCount())
	}
	if bars[0].Lo != 0 || bars[0].Hi != 0 || bars[0].Count != 1 {
		t.Fatalf("bucket0 = %+v", bars[0])
	}
	if bars[1].Lo != 1 || bars[1].Hi != 2 || bars[1].Count != 1 {
		t.Fatalf("bucket1 = %+v", bars[1])
	}
	if bars[7].Lo != 64 || bars[7].Hi != 128 || bars[7].Count != 2 {
		t.Fatalf("bucket7 = %+v", bars[7])
	}
}

func TestCustomBucketCount(t *testing.T) {
	layout := Layout{Scheme: SchemeExponential, BucketCount: 16}
	h := NewWithLayout(layout)
	h.Observe(50, layout)
	p99, ok := h.Percentile(0.99, layout)
	if !ok || p99 <= 0 {
		t.Fatalf("p99 = %d ok=%v", p99, ok)
	}
}
