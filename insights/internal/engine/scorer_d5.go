package engine

import (
	"encoding/json"
	"sort"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// d5Libraries scores against the set of OpenTelemetry instrumentation libraries
// (Scope name + version) seen in the trace; a new library reaching a learned
// transaction is a deviation, often the symptom of an unintended class load
// (log4shell-style).
//
// JSONB shape:
//
//	{"libs": [{"ns": "okhttp3", "ver": "4.x"}, ...]}
type d5Libraries struct{}

type lib struct {
	Ns  string `json:"ns"`
	Ver string `json:"ver"`
}

type d5Data struct {
	Libs []lib `json:"libs"`
}

func (d5Libraries) Class() repo.DeviationClass { return repo.D5Libraries }

func (s d5Libraries) Score(t *Trace, current json.RawMessage) (bool, json.RawMessage, error) {
	return s.decodeBaseline(current).score(t)
}

// decodeBaseline implements baselineDecoder.
func (d5Libraries) decodeBaseline(raw json.RawMessage) decodedBaseline {
	d, _ := decodeD5(raw)
	return d
}

// score implements decodedBaseline; it only reads baseline.
func (baseline d5Data) score(t *Trace) (bool, json.RawMessage, error) {
	known := map[lib]struct{}{}
	for _, l := range baseline.Libs {
		known[l] = struct{}{}
	}
	var newLibs []lib
	for _, l := range libsFromTrace(t) {
		if _, ok := known[l]; !ok {
			newLibs = append(newLibs, l)
			known[l] = struct{}{}
		}
	}
	if len(newLibs) == 0 {
		return false, nil, nil
	}
	ev, err := json.Marshal(map[string]any{"new_libs": newLibs})
	if err != nil {
		return true, nil, err
	}
	return true, ev, nil
}

func (d5Libraries) Merge(t *Trace, current json.RawMessage) (json.RawMessage, error) {
	baseline, _ := decodeD5(current)
	set := map[lib]struct{}{}
	for _, l := range baseline.Libs {
		set[l] = struct{}{}
	}
	for _, l := range libsFromTrace(t) {
		set[l] = struct{}{}
	}
	merged := make([]lib, 0, len(set))
	for l := range set {
		merged = append(merged, l)
	}
	sort.Slice(merged, func(i, j int) bool { return lessLib(merged[i], merged[j]) })
	return json.Marshal(d5Data{Libs: merged})
}

func (d5Libraries) decodeLearning(raw json.RawMessage) learningBaseline {
	d, _ := decodeD5(raw)
	return newSetLearning(d.Libs, libsFromTrace, lessLib, func(l []lib) any { return d5Data{Libs: l} })
}

func lessLib(a, b lib) bool {
	if a.Ns != b.Ns {
		return a.Ns < b.Ns
	}
	return a.Ver < b.Ver
}

func decodeD5(raw json.RawMessage) (d5Data, error) {
	if len(raw) == 0 {
		return d5Data{}, nil
	}
	var d d5Data
	err := json.Unmarshal(raw, &d)
	return d, err
}

// libsFromTrace collects the distinct (scope name, version) pairs across the
// trace's spans, skipping spans with no scope name (some manually-instrumented
// apps omit it; lumping them together would be noise). Used by Score and Merge.
func libsFromTrace(t *Trace) []lib {
	out := []lib{}
	seen := map[lib]struct{}{}
	for _, s := range t.Spans {
		if s.ScopeName == "" {
			continue
		}
		l := lib{Ns: s.ScopeName, Ver: s.ScopeVersion}
		if _, ok := seen[l]; ok {
			continue
		}
		seen[l] = struct{}{}
		out = append(out, l)
	}
	return out
}
