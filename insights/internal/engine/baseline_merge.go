package engine

import (
	"encoding/json"
	"sort"

	"github.com/odigos-io/odigos/insights/internal/engine/histogram"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// classMerger knows how to combine two stored baseline values of the same
// class into one: set classes take the union, the D3/D7 histograms add buckets.
// The DB layer calls this during a flush so it can merge values without
// needing to understand each class's JSON shape.
type classMerger struct{}

func (classMerger) MergeData(class repo.DeviationClass, stored, contribution json.RawMessage) (json.RawMessage, error) {
	switch class {
	case repo.D1CallEdges:
		return mergeD1Values(stored, contribution)
	case repo.D2Egress:
		return mergeD2Values(stored, contribution)
	case repo.D4ArgsReturns:
		return mergeD4Values(stored, contribution)
	case repo.D3Latency:
		return mergeD3Values(stored, contribution)
	case repo.D5Libraries:
		return mergeD5Values(stored, contribution)
	case repo.D6DBAccess:
		return mergeD6Values(stored, contribution)
	case repo.D7PayloadSize:
		return mergeD7Values(stored, contribution)
	default:
		return json.RawMessage(`{}`), nil
	}
}

// isAdditiveClass reports whether a class's Data accumulates (so its flushed
// contribution must be a since-last-flush DELTA, added by MergeData) versus
// being an idempotent set (flushed as the full value, unioned). The D3 and D7
// histogram classes are additive.
func isAdditiveClass(c repo.DeviationClass) bool {
	return c == repo.D3Latency || c == repo.D7PayloadSize
}

// D1CallEdges: union the set of edges.
func mergeD1Values(a, b json.RawMessage) (json.RawMessage, error) {
	da, _ := decodeD1(a)
	db, _ := decodeD1(b)
	set := make(map[d1Edge]struct{}, len(da.Edges)+len(db.Edges))
	for _, e := range da.Edges {
		set[e] = struct{}{}
	}
	for _, e := range db.Edges {
		set[e] = struct{}{}
	}
	merged := make([]d1Edge, 0, len(set))
	for e := range set {
		merged = append(merged, e)
	}
	sort.Slice(merged, func(i, j int) bool { return lessD1Edge(merged[i], merged[j]) })
	return json.Marshal(d1Data{Edges: merged})
}

// D2Egress: union peers by {host,port}, coalescing Owner/External annotations.
func mergeD2Values(a, b json.RawMessage) (json.RawMessage, error) {
	da, _ := decodeD2(a)
	db, _ := decodeD2(b)
	peers := make([]peer, 0, len(da.Peers)+len(db.Peers))
	peers = append(peers, da.Peers...)
	peers = append(peers, db.Peers...)
	return json.Marshal(d2Data{Peers: coalesceD2Peers(peers)})
}

// D5Libraries: union the set of instrumented libraries.
func mergeD5Values(a, b json.RawMessage) (json.RawMessage, error) {
	da, _ := decodeD5(a)
	db, _ := decodeD5(b)
	set := make(map[lib]struct{}, len(da.Libs)+len(db.Libs))
	for _, l := range da.Libs {
		set[l] = struct{}{}
	}
	for _, l := range db.Libs {
		set[l] = struct{}{}
	}
	merged := make([]lib, 0, len(set))
	for l := range set {
		merged = append(merged, l)
	}
	sort.Slice(merged, func(i, j int) bool { return lessLib(merged[i], merged[j]) })
	return json.Marshal(d5Data{Libs: merged})
}

// D6DBAccess: union the set of DB accesses and query shapes.
func mergeD6Values(a, b json.RawMessage) (json.RawMessage, error) {
	da, _ := decodeD6(a)
	db, _ := decodeD6(b)
	accSet := make(map[dbAccess]struct{}, len(da.Accesses)+len(db.Accesses))
	for _, x := range da.Accesses {
		accSet[x] = struct{}{}
	}
	for _, x := range db.Accesses {
		accSet[x] = struct{}{}
	}
	shapeSet := make(map[queryShape]struct{}, len(da.QueryShapes)+len(db.QueryShapes))
	for _, s := range da.QueryShapes {
		shapeSet[s] = struct{}{}
	}
	for _, s := range db.QueryShapes {
		shapeSet[s] = struct{}{}
	}
	mergedAcc := make([]dbAccess, 0, len(accSet))
	for x := range accSet {
		mergedAcc = append(mergedAcc, x)
	}
	sort.Slice(mergedAcc, func(i, j int) bool { return lessDBAccess(mergedAcc[i], mergedAcc[j]) })
	mergedShapes := make([]queryShape, 0, len(shapeSet))
	for s := range shapeSet {
		mergedShapes = append(mergedShapes, s)
	}
	sort.Slice(mergedShapes, func(i, j int) bool {
		if mergedShapes[i].Op != mergedShapes[j].Op {
			return mergedShapes[i].Op < mergedShapes[j].Op
		}
		return mergedShapes[i].Tables < mergedShapes[j].Tables
	})
	return json.Marshal(d6Data{Accesses: mergedAcc, QueryShapes: mergedShapes})
}

// D4ArgsReturns: union the per-callsite value profiles (sites, formats, enum
// values), honoring the enum cap, and OR the Seen marker (mergeD4Data). A
// no-site union with neither side seen marshals to the canonical "{}"; once
// either side has observed traffic it stays {"seen":true} so the zero-value
// promotion survives the flush.
func mergeD4Values(a, b json.RawMessage) (json.RawMessage, error) {
	da, _ := decodeD4(a)
	db, _ := decodeD4(b)
	return json.Marshal(mergeD4Data(da, db))
}

// D3Latency: add the histograms.
func mergeD3Values(a, b json.RawMessage) (json.RawMessage, error) {
	da, _ := decodeD3(a)
	db, _ := decodeD3(b)
	layout := histogram.MergeLayout(da.Latency, db.Latency)
	da.Latency.Add(db.Latency, layout)
	return json.Marshal(da)
}

// D7PayloadSize: add the histograms.
func mergeD7Values(a, b json.RawMessage) (json.RawMessage, error) {
	da, _ := decodeD7(a)
	db, _ := decodeD7(b)
	reqLayout := histogram.MergeLayout(da.Request, db.Request)
	respLayout := histogram.MergeLayout(da.Response, db.Response)
	da.Request.Add(db.Request, reqLayout)
	da.Response.Add(db.Response, respLayout)
	return json.Marshal(da)
}

// capClassData caps a grow-only set baseline at max, a safety valve so one
// high-cardinality transaction (e.g. span names that embed IDs, minting a new
// D1 edge per request) can't grow an unbounded row. The set is already sorted
// by Merge, so keeping the first max is deterministic; it returns whether it
// trimmed. Additive (D3/D7 histograms) classes are fixed-size and pass through
// unchanged.
func capClassData(class repo.DeviationClass, data json.RawMessage, max int) (json.RawMessage, bool) {
	if max <= 0 || len(data) == 0 {
		return data, false
	}
	// Each class keeps its set under a differently-typed struct field, so
	// decode the class-specific shape, trim its slice(s), and re-encode.
	switch class {
	case repo.D1CallEdges:
		d, _ := decodeD1(data)
		if len(d.Edges) <= max {
			return data, false
		}
		d.Edges = d.Edges[:max]
		return reencode(data, d)
	case repo.D2Egress:
		d, _ := decodeD2(data)
		if len(d.Peers) <= max {
			return data, false
		}
		d.Peers = d.Peers[:max]
		return reencode(data, d)
	case repo.D4ArgsReturns:
		d, _ := decodeD4(data)
		if len(d.Sites) <= max {
			return data, false
		}
		// Deterministically keep the first max sites (probed methods are few;
		// this only guards against method names that embed identifiers).
		keep := sortedKeys(d.Sites)[:max]
		trimmed := d4Data{Sites: make(map[string]d4Site, max), Seen: d.Seen}
		for _, site := range keep {
			trimmed.Sites[site] = d.Sites[site]
		}
		return reencode(data, trimmed)
	case repo.D5Libraries:
		d, _ := decodeD5(data)
		if len(d.Libs) <= max {
			return data, false
		}
		d.Libs = d.Libs[:max]
		return reencode(data, d)
	case repo.D6DBAccess:
		d, _ := decodeD6(data)
		if len(d.Accesses) <= max && len(d.QueryShapes) <= max {
			return data, false
		}
		if len(d.Accesses) > max {
			d.Accesses = d.Accesses[:max]
		}
		if len(d.QueryShapes) > max {
			d.QueryShapes = d.QueryShapes[:max]
		}
		return reencode(data, d)
	default:
		return data, false
	}
}

// reencode marshals a trimmed baseline value, reporting (result, true). If
// marshaling somehow fails it keeps the original bytes and reports "not
// trimmed" so the caller never persists a broken value.
func reencode(original json.RawMessage, trimmed any) (json.RawMessage, bool) {
	out, err := json.Marshal(trimmed)
	if err != nil {
		return original, false
	}
	return out, true
}
