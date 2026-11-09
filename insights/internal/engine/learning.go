package engine

import (
	"encoding/json"
	"sort"
)

// learner is an optional Scorer capability for the classes whose learning cost
// dominates: it parses a learning baseline once so processOne merges each trace
// into the parsed value, instead of decoding the stored JSON per trace (Merge,
// the per-set cap and baselineGrew each decoded it). The JSON stays the stored
// form, and encode must return exactly the bytes Merge would.
type learner interface {
	decodeLearning(raw json.RawMessage) learningBaseline
}

// learningBaseline is one class's learning baseline held parsed. It is only
// touched under the transaction's entry.mu.
type learningBaseline interface {
	// merge folds t in place. changed reports that the stored JSON differs
	// now and must be re-encoded; grew is the histogram classes'
	// stabilityComparator answer for this observation.
	merge(t *Trace) (changed, grew bool)
	encode() (json.RawMessage, error)
	// members is the set size checked against the per-set cap; 0 for histograms.
	members() int
}

// setLearning holds a grow-only set (D1, D5, and each of D6's two). synced is
// false until the first encode, because Merge canonicalizes (sorts, dedups)
// whatever was stored, so the first observation must re-encode even when it
// adds nothing. fromTrace and wrap make it a whole learningBaseline; a class
// holding several sets leaves them nil and drives add/sorted itself.
type setLearning[T comparable] struct {
	set       map[T]struct{}
	synced    bool
	fromTrace func(*Trace) []T
	less      func(a, b T) bool
	wrap      func([]T) any
}

func newSetLearning[T comparable](members []T, fromTrace func(*Trace) []T, less func(a, b T) bool, wrap func([]T) any) *setLearning[T] {
	set := make(map[T]struct{}, len(members))
	for _, m := range members {
		set[m] = struct{}{}
	}
	return &setLearning[T]{set: set, fromTrace: fromTrace, less: less, wrap: wrap}
}

// add inserts items and reports whether the encoded set changes.
func (s *setLearning[T]) add(items []T) bool {
	changed := !s.synced
	for _, m := range items {
		if _, ok := s.set[m]; !ok {
			s.set[m] = struct{}{}
			changed = true
		}
	}
	return changed
}

// sorted is the set in Merge's canonical order, never nil so an empty set
// encodes as [] like Merge's.
func (s *setLearning[T]) sorted() []T {
	out := make([]T, 0, len(s.set))
	for m := range s.set {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return s.less(out[i], out[j]) })
	return out
}

func (s *setLearning[T]) merge(t *Trace) (bool, bool) {
	return s.add(s.fromTrace(t)), false
}

func (s *setLearning[T]) encode() (json.RawMessage, error) {
	out, err := json.Marshal(s.wrap(s.sorted()))
	if err == nil {
		s.synced = true
	}
	return out, err
}

func (s *setLearning[T]) members() int { return len(s.set) }
