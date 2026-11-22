package engine

import (
	"encoding/json"
	"testing"
)

func TestD5_EmptyScopeNameSkipped(t *testing.T) {
	tr := &Trace{Spans: []*Span{
		{ScopeName: "", ScopeVersion: "9.9"},
		{ScopeName: "postgresql", ScopeVersion: "42"},
		{ScopeName: "postgresql", ScopeVersion: "42"},
	}}
	got := libsFromTrace(tr)
	if len(got) != 1 || got[0] != (lib{Ns: "postgresql"}) {
		t.Fatalf("libsFromTrace = %+v, want only named scopes (deduped)", got)
	}
}

func TestD5_VersionChangeDoesNotDeviate(t *testing.T) {
	learned := &Trace{Spans: []*Span{{ScopeName: "okhttp3", ScopeVersion: "4.11.0"}}}
	baseline, err := (d5Libraries{}).Merge(learned, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if contains(string(baseline), "4.11.0") {
		t.Fatalf("baseline should not store versions: %s", baseline)
	}

	upgraded := &Trace{Spans: []*Span{{ScopeName: "okhttp3", ScopeVersion: "4.12.0"}}}
	if dev, ev, _ := (d5Libraries{}).Score(upgraded, baseline); dev {
		t.Fatalf("an upgraded library must not deviate: %s", ev)
	}
}

func TestD5_NewLibraryDeviatesOnUpgradedTrace(t *testing.T) {
	baseline, err := (d5Libraries{}).Merge(&Trace{Spans: []*Span{{ScopeName: "okhttp3", ScopeVersion: "4"}}}, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	tr := &Trace{Spans: []*Span{
		{ScopeName: "okhttp3", ScopeVersion: "5"},
		{ScopeName: "commons-text", ScopeVersion: "1.9"},
	}}
	dev, ev, err := (d5Libraries{}).Score(tr, baseline)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if !dev {
		t.Fatal("a library not in the baseline must deviate")
	}
	if !contains(string(ev), "commons-text") || contains(string(ev), "okhttp3") {
		t.Fatalf("evidence should name only the new library: %s", ev)
	}
}

// Baselines stored before libraries were matched by name hold versions.
func TestD5_BaselineWithVersionsMatchesByName(t *testing.T) {
	stored := json.RawMessage(`{"libs":[{"ns":"okhttp3","ver":"4.11.0"},{"ns":"okhttp3","ver":"4.12.0"}]}`)

	upgraded := &Trace{Spans: []*Span{{ScopeName: "okhttp3", ScopeVersion: "5.0.0"}}}
	if dev, ev, _ := (d5Libraries{}).Score(upgraded, stored); dev {
		t.Fatalf("a library stored with an older version must not deviate: %s", ev)
	}

	merged, err := (d5Libraries{}).Merge(upgraded, stored)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if string(merged) != `{"libs":[{"ns":"okhttp3"}]}` {
		t.Fatalf("merge should drop versions and dedupe by name: %s", merged)
	}
}

func TestD5_MergeDedupsAndSorts(t *testing.T) {
	tr := &Trace{Spans: []*Span{
		{ScopeName: "zlib", ScopeVersion: "2"},
		{ScopeName: "okhttp3", ScopeVersion: "4"},
		{ScopeName: "okhttp3", ScopeVersion: "4"},
	}}
	got, err := (d5Libraries{}).Merge(tr, nil)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var d d5Data
	if err := json.Unmarshal(got, &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(d.Libs) != 2 {
		t.Fatalf("libs = %+v, want 2 distinct", d.Libs)
	}
	if d.Libs[0].Ns != "okhttp3" || d.Libs[1].Ns != "zlib" {
		t.Fatalf("expected lexicographic ns order, got %+v", d.Libs)
	}

	again, err := (d5Libraries{}).Merge(tr, got)
	if err != nil {
		t.Fatalf("re-merge: %v", err)
	}
	if string(again) != string(got) {
		t.Fatalf("re-merge grew/changed baseline:\n first %s\n again %s", got, again)
	}
}

func TestD5_EmptyBaselineTreatsObservedLibsAsNew(t *testing.T) {
	tr := &Trace{Spans: []*Span{{ScopeName: "okhttp3", ScopeVersion: "4"}}}
	dev, ev, err := (d5Libraries{}).Score(tr, nil)
	if err != nil {
		t.Fatalf("score: %v", err)
	}
	if !dev {
		t.Fatal("empty baseline has no known libs, so observed libs are new")
	}
	if !contains(string(ev), "okhttp3") {
		t.Fatalf("evidence should name the new lib: %s", ev)
	}
}
