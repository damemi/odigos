package engine

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/odigos-io/odigos/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Compile-time check that the engine satisfies the backend-neutral seam.
var _ repo.BaselineCodec = baselineCodec{}

// NewBaselineCodec returns the engine's implementation of repo.BaselineCodec.
// It is the bridge between the scorers' per-class JSON shapes and the flat
// (member / counter) rows the database backend stores and merges. Keeping it
// here means the backend never needs to understand a single class's JSON.
func NewBaselineCodec() repo.BaselineCodec { return baselineCodec{} }

type baselineCodec struct{}

// D7 histogram buckets are stored as counters keyed by a one-letter side prefix
// plus the bucket index: "q3" is request bucket 3, "s7" is response bucket 7.
const (
	d7RequestPrefix  = "q"
	d7ResponsePrefix = "s"
)

// D3 stores a single latency histogram; its bucket counters carry an "l"
// prefix ("l3" is latency bucket 3) to keep the counter-key scheme uniform.
const d3LatencyPrefix = "l"

// D6 mixes two sets (accesses, query shapes) in one class, so its members carry
// a one-letter prefix to tell them apart on decode.
const (
	d6AccessPrefix = "a"
	d6ShapePrefix  = "s"
)

func (baselineCodec) Encode(class repo.DeviationClass, data json.RawMessage) ([]string, []repo.Counter, error) {
	switch class {
	case repo.D1CallEdges:
		d, err := decodeD1(data)
		if err != nil {
			return nil, nil, err
		}
		return marshalMembers(d.Edges, "")
	case repo.D2Egress:
		d, err := decodeD2(data)
		if err != nil {
			return nil, nil, err
		}
		return marshalMembers(d.Peers, "")
	case repo.D3Latency:
		d, err := decodeD3(data)
		if err != nil {
			return nil, nil, err
		}
		counters := appendHistogramCounters(make([]repo.Counter, 0, histogram.BucketCount), d3LatencyPrefix, d.Latency)
		return nil, counters, nil
	case repo.D4ArgsReturns:
		d, err := decodeD4(data)
		if err != nil {
			return nil, nil, err
		}
		members, err := encodeD4Members(d)
		if err != nil {
			return nil, nil, err
		}
		return members, nil, nil
	case repo.D5Libraries:
		d, err := decodeD5(data)
		if err != nil {
			return nil, nil, err
		}
		return marshalMembers(d.Libs, "")
	case repo.D6DBAccess:
		d, err := decodeD6(data)
		if err != nil {
			return nil, nil, err
		}
		members := make([]string, 0, len(d.Accesses)+len(d.QueryShapes))
		acc, _, err := marshalMembers(d.Accesses, d6AccessPrefix)
		if err != nil {
			return nil, nil, err
		}
		shapes, _, err := marshalMembers(d.QueryShapes, d6ShapePrefix)
		if err != nil {
			return nil, nil, err
		}
		members = append(members, acc...)
		members = append(members, shapes...)
		return members, nil, nil
	case repo.D7PayloadSize:
		d, err := decodeD7(data)
		if err != nil {
			return nil, nil, err
		}
		counters := make([]repo.Counter, 0, histogram.BucketCount)
		counters = appendHistogramCounters(counters, d7RequestPrefix, d.Request)
		counters = appendHistogramCounters(counters, d7ResponsePrefix, d.Response)
		return nil, counters, nil
	default:
		return nil, nil, nil
	}
}

func (baselineCodec) Decode(class repo.DeviationClass, members []string, counters []repo.Counter) (json.RawMessage, error) {
	switch class {
	case repo.D1CallEdges:
		edges := make([]d1Edge, 0, len(members))
		if err := unmarshalMembers(members, "", &edges); err != nil {
			return nil, err
		}
		sort.Slice(edges, func(i, j int) bool { return lessD1Edge(edges[i], edges[j]) })
		return json.Marshal(d1Data{Edges: edges})
	case repo.D2Egress:
		peers := make([]peer, 0, len(members))
		if err := unmarshalMembers(members, "", &peers); err != nil {
			return nil, err
		}
		// Collapse legacy rows that stored the same {host,port} with different
		// Owner/External annotations as distinct members (see coalesceD2Peers).
		return json.Marshal(d2Data{Peers: coalesceD2Peers(peers)})
	case repo.D4ArgsReturns:
		d, err := decodeD4Members(members)
		if err != nil {
			return nil, err
		}
		// Marshal directly (no empty short-circuit) so the Seen marker survives:
		// a seen-but-siteless baseline decodes to {"seen":true}, and a truly
		// empty one to {} - both matching a fresh Merge byte-for-byte.
		return json.Marshal(d)
	case repo.D3Latency:
		d := d3Data{Latency: histogram.New()}
		for _, c := range counters {
			side, idx, ok := parseHistogramBucket(c.Bucket)
			if !ok || side != d3LatencyPrefix || idx < 0 || idx >= repo.MaxHistogramBucketCount {
				continue
			}
			d.Latency.Buckets[idx] = uint64(c.Count)
		}
		return json.Marshal(d)
	case repo.D5Libraries:
		libs := make([]lib, 0, len(members))
		if err := unmarshalMembers(members, "", &libs); err != nil {
			return nil, err
		}
		sort.Slice(libs, func(i, j int) bool {
			if libs[i].Ns != libs[j].Ns {
				return libs[i].Ns < libs[j].Ns
			}
			return libs[i].Ver < libs[j].Ver
		})
		return json.Marshal(d5Data{Libs: libs})
	case repo.D6DBAccess:
		var (
			accesses = []dbAccess{}
			shapes   = []queryShape{}
		)
		for _, m := range members {
			switch {
			case strings.HasPrefix(m, d6AccessPrefix):
				var a dbAccess
				if err := json.Unmarshal([]byte(m[len(d6AccessPrefix):]), &a); err != nil {
					return nil, err
				}
				accesses = append(accesses, a)
			case strings.HasPrefix(m, d6ShapePrefix):
				var s queryShape
				if err := json.Unmarshal([]byte(m[len(d6ShapePrefix):]), &s); err != nil {
					return nil, err
				}
				shapes = append(shapes, s)
			}
		}
		sort.Slice(accesses, func(i, j int) bool { return lessDBAccess(accesses[i], accesses[j]) })
		sort.Slice(shapes, func(i, j int) bool {
			if shapes[i].Op != shapes[j].Op {
				return shapes[i].Op < shapes[j].Op
			}
			return shapes[i].Tables < shapes[j].Tables
		})
		return json.Marshal(d6Data{Accesses: accesses, QueryShapes: shapes})
	case repo.D7PayloadSize:
		d := d7Data{Request: histogram.New(), Response: histogram.New()}
		for _, c := range counters {
			side, idx, ok := parseHistogramBucket(c.Bucket)
			if !ok || idx < 0 || idx >= repo.MaxHistogramBucketCount {
				continue
			}
			if side == d7RequestPrefix {
				d.Request.Buckets[idx] = uint64(c.Count)
			} else {
				d.Response.Buckets[idx] = uint64(c.Count)
			}
		}
		return json.Marshal(d)
	default:
		return json.RawMessage(`{}`), nil
	}
}

// marshalMembers JSON-encodes each element of a set slice, optionally prefixing
// it so a class that stores two sets (D6) can tell them apart on decode.
func marshalMembers[T any](elems []T, prefix string) ([]string, []repo.Counter, error) {
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		b, err := json.Marshal(e)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, prefix+string(b))
	}
	return out, nil, nil
}

// unmarshalMembers decodes a set slice from its members, stripping prefix.
func unmarshalMembers[T any](members []string, prefix string, out *[]T) error {
	for _, m := range members {
		if !strings.HasPrefix(m, prefix) {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(m[len(prefix):]), &v); err != nil {
			return err
		}
		*out = append(*out, v)
	}
	return nil
}

func appendHistogramCounters(dst []repo.Counter, prefix string, h histogram.Histogram) []repo.Counter {
	for i, c := range h.Buckets {
		if c == 0 {
			continue
		}
		dst = append(dst, repo.Counter{Bucket: prefix + strconv.Itoa(i), Count: int64(c)})
	}
	return dst
}

// parseHistogramBucket splits a "q3"/"s7"/"l5" bucket key back into
// (side, index). The side prefix tells D7's request/response apart and marks
// D3's single latency histogram.
func parseHistogramBucket(bucket string) (side string, idx int, ok bool) {
	if len(bucket) < 2 {
		return "", 0, false
	}
	side = bucket[:1]
	if side != d7RequestPrefix && side != d7ResponsePrefix && side != d3LatencyPrefix {
		return "", 0, false
	}
	n, err := strconv.Atoi(bucket[1:])
	if err != nil {
		return "", 0, false
	}
	return side, n, true
}
