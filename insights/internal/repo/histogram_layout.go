package repo

// Fixed exponential histogram geometry for D3 latency and D8 payload size.
// Bucket 0 counts value==0; bucket k≥1 covers [2^(k-1), 2^k) of the observed unit.
const (
	DefaultHistogramBucketCount = 32
	MaxHistogramBucketCount     = 64
)
