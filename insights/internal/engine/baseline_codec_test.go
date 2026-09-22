package engine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// TestCodecRoundTripSets checks that each set class survives Encode → (store
// would union the members) → Decode unchanged, independent of ClickHouse.
func TestCodecRoundTripSets(t *testing.T) {
	c := NewBaselineCodec()
	cases := []struct {
		class repo.DeviationClass
		data  string
	}{
		{repo.D1CallEdges, `{"edges":[{"from":{"svc":"a","op":"x"},"to":{"svc":"b","op":"y"}},{"from":{"svc":"a","op":"x"},"to":{"svc":"c","op":"z"}}]}`},
		// Decode sorts sets deterministically (host asc), so the expected value
		// is written already sorted.
		{repo.D2Egress, `{"peers":[{"host":"db","port":5432},{"host":"stripe.com","port":443}]}`},
		{repo.D5Libraries, `{"libs":[{"ns":"okhttp3","ver":"4"},{"ns":"pg","ver":"42"}]}`},
		{repo.D6DBAccess, `{"accesses":[{"system":"postgresql","ns":"app","table":"users","op":"SELECT"}],"query_shapes":[{"op":"SELECT","tables":"users"}]}`},
	}
	for _, tc := range cases {
		members, counters, err := c.Encode(tc.class, json.RawMessage(tc.data))
		if err != nil {
			t.Fatalf("%s encode: %v", tc.class, err)
		}
		if len(counters) != 0 {
			t.Fatalf("%s: set class produced counters", tc.class)
		}
		got, err := c.Decode(tc.class, members, nil)
		if err != nil {
			t.Fatalf("%s decode: %v", tc.class, err)
		}
		if !jsonEqual(t, tc.data, string(got)) {
			t.Fatalf("%s round-trip mismatch:\n want %s\n got  %s", tc.class, tc.data, got)
		}
	}
}

// TestCodecHistogramSumsAcrossContributions mirrors how the SummingMergeTree
// adds delta contributions: concatenating two Encode outputs and summing equal
// buckets must reproduce the sum.
func TestCodecHistogramSumsAcrossContributions(t *testing.T) {
	c := NewBaselineCodec()
	d7 := func(reqIdx int, n uint64) json.RawMessage {
		req := make([]uint64, histogram.BucketCount)
		req[reqIdx] = n
		b, _ := json.Marshal(d7Data{Request: histogram.Histogram{Buckets: req}, Response: histogram.New()})
		return b
	}

	_, c1, _ := c.Encode(repo.D7PayloadSize, d7(3, 2))
	_, c2, _ := c.Encode(repo.D7PayloadSize, d7(3, 5))

	// Emulate SummingMergeTree: sum counts of identical buckets.
	sum := map[string]int64{}
	for _, x := range append(append([]repo.Counter{}, c1...), c2...) {
		sum[x.Bucket] += x.Count
	}
	merged := make([]repo.Counter, 0, len(sum))
	for b, n := range sum {
		merged = append(merged, repo.Counter{Bucket: b, Count: n})
	}

	got, err := c.Decode(repo.D7PayloadSize, nil, merged)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var d d7Data
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Request.Buckets[3] != 7 {
		t.Fatalf("expected bucket 3 == 7 (2+5), got %d", d.Request.Buckets[3])
	}
}

// TestCodecD3HistogramSumsAcrossContributions mirrors how the SummingMergeTree
// adds delta contributions for the single D3 latency histogram.
func TestCodecD3HistogramSumsAcrossContributions(t *testing.T) {
	c := NewBaselineCodec()
	d3 := func(bucketIdx int, n uint64) json.RawMessage {
		buckets := make([]uint64, histogram.BucketCount)
		buckets[bucketIdx] = n
		b, _ := json.Marshal(d3Data{Latency: histogram.Histogram{Buckets: buckets}})
		return b
	}

	_, c1, _ := c.Encode(repo.D3Latency, d3(5, 3))
	_, c2, _ := c.Encode(repo.D3Latency, d3(5, 4))

	summed := map[string]int64{}
	for _, cc := range append(append([]repo.Counter{}, c1...), c2...) {
		summed[cc.Bucket] += cc.Count
	}
	merged := make([]repo.Counter, 0, len(summed))
	for bucket, count := range summed {
		merged = append(merged, repo.Counter{Bucket: bucket, Count: count})
	}

	got, err := c.Decode(repo.D3Latency, nil, merged)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var d d3Data
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Latency.Buckets[5] != 7 {
		t.Fatalf("expected bucket 5 == 7 (3+4), got %d", d.Latency.Buckets[5])
	}
}

// TestCodecUnknownClass: a class without a scorer (e.g. one retired from an
// older build) must encode to nothing rather than fail the flush.
func TestCodecUnknownClass(t *testing.T) {
	c := NewBaselineCodec()
	class := repo.DeviationClass("D9_retired")
	members, counters, err := c.Encode(class, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("%s encode: %v", class, err)
	}
	if len(members) != 0 || len(counters) != 0 {
		t.Fatalf("%s: unknown class produced data (%d members, %d counters)", class, len(members), len(counters))
	}
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var av, bv any
	if err := json.Unmarshal([]byte(a), &av); err != nil {
		t.Fatalf("bad json a: %v", err)
	}
	if err := json.Unmarshal([]byte(b), &bv); err != nil {
		t.Fatalf("bad json b: %v", err)
	}
	return reflect.DeepEqual(av, bv)
}
