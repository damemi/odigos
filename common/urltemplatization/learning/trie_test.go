package learning

import (
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/model"
	"github.com/odigos-io/odigos/common/urltemplate"
)

func TestBuildPathTrie(t *testing.T) {
	trie := BuildPathTrie(map[string]string{
		"/users/1":        "10",
		"/users/2":        "5",
		"/users/1/orders": "3",
		"/health":         "100",
	})

	users := trie.Root.Children["users"]
	if users == nil {
		t.Fatal("expected users node")
	}
	if users.Terminal {
		t.Fatal("users should not be terminal")
	}
	if users.TotalObservations != 18 {
		t.Fatalf("users totalObservations=%d, want 18", users.TotalObservations)
	}

	one := users.Children["1"]
	if one == nil || !one.Terminal {
		t.Fatal("expected /users/1 to be terminal")
	}
	if one.TerminalPathCount != 1 || one.TerminalObservationCount != 10 {
		t.Fatalf("/users/1 paths=%d obs=%d, want 1 and 10", one.TerminalPathCount, one.TerminalObservationCount)
	}
	if one.TotalObservations != 13 {
		t.Fatalf("/users/1 totalObservations=%d, want 13", one.TotalObservations)
	}

	orders := one.Children["orders"]
	if orders == nil || !orders.Terminal || orders.TerminalObservationCount != 3 {
		t.Fatalf("expected /users/1/orders terminal with obs=3, got %+v", orders)
	}

	health := trie.Root.Children["health"]
	if health == nil || !health.Terminal || health.TerminalObservationCount != 100 {
		t.Fatalf("expected /health terminal with obs=100, got %+v", health)
	}

	if trie.Root.TotalObservations != 118 {
		t.Fatalf("root totalObservations=%d, want 118", trie.Root.TotalObservations)
	}
	if trie.Root.TemplateProbability != 0 {
		t.Fatalf("root templateProbability=%v, want 0", trie.Root.TemplateProbability)
	}
}

func TestTerminalRuleCopiesExamplePaths(t *testing.T) {
	rule, ok := terminalRule(&PathTrieNode{
		Terminal:                 true,
		TerminalObservationCount: 101,
		TerminalExamplePaths: examplePaths{
			"/users/2": nil,
			"/users/1": nil,
			"/users/9": nil,
			"/users/4": nil,
			"/users/3": nil,
			"/users/8": nil,
		},
	}, []Segment{staticSegment("users", 0)}, resolvedLearningParams{minObservationsForRule: 100})
	if !ok {
		t.Fatal("expected a terminal rule")
	}
	want := []string{"/users/1", "/users/2", "/users/3", "/users/4", "/users/8"}
	if !reflect.DeepEqual(rule.Examples, want) {
		t.Fatalf("examples=%v, want %v", rule.Examples, want)
	}
}

func TestInsertStoresTerminalExamplePath(t *testing.T) {
	trie := NewPathTrie()
	trie.Insert("/users/1", 10)
	trie.Insert("/users/1", 4)
	trie.Insert("/users/1/orders", 3)

	users := trie.Root.Children["users"]
	if len(users.TerminalExamplePaths) != 0 {
		t.Fatalf("users examples=%v, want none", users.TerminalExamplePaths)
	}
	one := users.Children["1"]
	if one.TerminalPathCount != 1 || one.TerminalObservationCount != 14 {
		t.Fatalf("/users/1 paths=%d obs=%d, want 1 and 14", one.TerminalPathCount, one.TerminalObservationCount)
	}
	if got, want := one.TerminalExamplePaths, (examplePaths{"/users/1": nil}); !reflect.DeepEqual(got, want) {
		t.Fatalf("/users/1 examples=%v, want %v", got, want)
	}
	orders := one.Children["orders"]
	if got, want := orders.TerminalExamplePaths, (examplePaths{"/users/1/orders": nil}); !reflect.DeepEqual(got, want) {
		t.Fatalf("/users/1/orders examples=%v, want %v", got, want)
	}
}

func TestMergeChildrenTrieBalancesTerminalExamplePaths(t *testing.T) {
	trie := NewPathTrie()
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7", "8"} {
		trie.Insert("/users/"+id, 1)
		trie.Insert("/users/"+id+"/orders", 1)
	}
	// One sibling is only a prefix, so it must not contribute a terminal example.
	trie.Insert("/users/9/profile", 1)

	users := trie.Root.Children["users"]
	merged := mergeChildrenTrie(sortedChildList(users), "{id}")
	wantTerminal := examplePaths{
		"/users/1": nil,
		"/users/2": nil,
		"/users/3": nil,
		"/users/4": nil,
		"/users/5": nil,
	}
	if merged.TerminalPathCount != 8 || merged.TerminalObservationCount != 8 {
		t.Fatalf("merged paths=%d obs=%d, want 8 and 8", merged.TerminalPathCount, merged.TerminalObservationCount)
	}
	if !reflect.DeepEqual(merged.TerminalExamplePaths, wantTerminal) {
		t.Fatalf("terminal examples=%v, want %v", merged.TerminalExamplePaths, wantTerminal)
	}

	orders := merged.Children["orders"]
	wantOrders := examplePaths{
		"/users/1/orders": nil,
		"/users/2/orders": nil,
		"/users/3/orders": nil,
		"/users/4/orders": nil,
		"/users/5/orders": nil,
	}
	if orders == nil || !reflect.DeepEqual(orders.TerminalExamplePaths, wantOrders) {
		t.Fatalf("orders examples=%v, want %v", orders.TerminalExamplePaths, wantOrders)
	}
	if profile := merged.Children["profile"]; profile == nil || !reflect.DeepEqual(profile.TerminalExamplePaths, examplePaths{"/users/9/profile": nil}) {
		t.Fatalf("profile examples=%v, want [/users/9/profile]", profile.TerminalExamplePaths)
	}
}

func TestMergeTerminalPathCountSumsDistinctPathsAndObservations(t *testing.T) {
	trie := NewPathTrie()
	trie.Insert("/foo/bar", 10)
	trie.Insert("/foo/baz", 50)

	foo := trie.Root.Children["foo"]
	merged := mergeChildrenTrie(sortedChildList(foo), "id")
	if merged.TerminalPathCount != 2 || merged.TerminalObservationCount != 60 {
		t.Fatalf("merged paths=%d obs=%d, want 2 and 60", merged.TerminalPathCount, merged.TerminalObservationCount)
	}

	rule, ok := terminalRule(merged, []Segment{
		staticSegment("foo", 0),
		{Type: SegmentTypeTemplate, Name: "id"},
	}, resolvedLearningParams{})
	if !ok {
		t.Fatal("expected a terminal rule")
	}
	want := `Learned the template /foo/{id} from live traffic, based on 60 requests from 2 different URLs`
	if rule.Reason != want {
		t.Fatalf("reason=%q, want %q", rule.Reason, want)
	}
}

func TestAnnotateDynamicReasonIdentifiesRepeatedWildcards(t *testing.T) {
	segments := []Segment{
		staticSegment("api", 0),
		staticSegment("v1", 0),
		{Type: SegmentTypeWildcard},
		{Type: SegmentTypeWildcard},
		staticSegment("pods", 0),
	}
	reason := annotateDynamicReason("learned", segments, 3, 9)
	reason = annotateDynamicReason(reason, segments, 2, 14)
	want := `learned; 9 distinct values found for "*" in "/api/v1/*/*" (0% probability of being a template) and merged to the same wildcard segment; 14 distinct values found for "*" in "/api/v1/*" (0% probability of being a template) and merged to the same wildcard segment`
	if reason != want {
		t.Fatalf("reason=%q, want %q", reason, want)
	}
}

func TestAnnotateDynamicReasonLeavesSingleWildcardUnqualified(t *testing.T) {
	segments := []Segment{
		staticSegment("api", 0),
		staticSegment("v1", 0),
		{Type: SegmentTypeWildcard},
	}
	got := annotateDynamicReason("learned", segments, 2, 14)
	want := `learned; 14 distinct values found for "*" (0% probability of being a template) and merged to the same wildcard segment`
	if got != want {
		t.Fatalf("reason=%q, want %q", got, want)
	}
}

func TestLearnedSegmentsRecordTemplateProbability(t *testing.T) {
	trie := BuildPathTrie(map[string]string{
		"/users/101": "40",
		"/users/202": "40",
		"/users/303": "40",
	})
	rules := FindLiveTrafficLearningRules(trie, LiveTrafficLearningParams{})
	if len(rules) != 1 || len(rules[0].Segments) != 2 {
		t.Fatalf("rules=%+v, want one /users/{id} rule", rules)
	}
	users := trie.Root.Children["users"]
	if math.Abs(rules[0].Segments[0].TemplateProbability-users.TemplateProbability) > 1e-12 {
		t.Fatalf("users probability=%v, want %v", rules[0].Segments[0].TemplateProbability, users.TemplateProbability)
	}
	merged := mergeChildrenTrie(sortedChildList(users), "id")
	if math.Abs(rules[0].Segments[1].TemplateProbability-merged.TemplateProbability) > 1e-12 {
		t.Fatalf("{id} probability=%v, want %v", rules[0].Segments[1].TemplateProbability, merged.TemplateProbability)
	}
}

func TestRecurseEachMentionsStaticChoice(t *testing.T) {
	trie := BuildPathTrie(map[string]string{
		"/a/x": "101",
		"/a/y": "101",
		"/a/z": "101",
	})
	rules := FindLiveTrafficLearningRules(trie, LiveTrafficLearningParams{})
	got := map[string]string{}
	for _, rule := range rules {
		got[rule.Template] = rule.Reason
	}
	a := trie.Root.Children["a"]
	want := fmt.Sprintf(
		`Learned the template /a/x from live traffic, based on 101 requests from 1 URL. Static segment "x" was kept because only 2 other values were observed (%s), and a rule was created for each option`,
		templateProbabilityClause(a.Children["x"].TemplateProbability),
	)
	if got["/a/x"] != want {
		t.Fatalf("reason=%q, want %q", got["/a/x"], want)
	}
}

func TestExampleURLTemplates(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/api/users/123/orders", "/api/users/{id}/orders"},
		{"/users/1", "/users/{id}"},
		{"/users/1/orders", "/users/{id}/orders"},
		{"/health", "/health"},
		{"https://example.com/api/users/123/orders?x=1", "/api/users/{id}/orders"},
	}
	for _, tc := range cases {
		if got := model.Template(tc.in); got != tc.want {
			t.Errorf("Template(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPathTrieStoresSegmentTemplateProbability(t *testing.T) {
	paths := []string{
		"/api/users/123/orders",
		"/health",
		"/v1/accounts/acct_123/charges/ch_abc",
	}
	for _, path := range paths {
		trie := BuildPathTrie(map[string]string{path: "4"})
		segments, _ := urltemplate.SplitPath(path)
		node := trie.Root
		for i, segment := range segments {
			node = node.Children[segment]
			if node == nil {
				t.Fatalf("%s: missing segment %q", path, segment)
			}
			want := model.Probability(segments, i)
			if math.Abs(node.TemplateProbability-want) > 1e-12 {
				t.Errorf("%s segment %q probability=%v, want %v", path, segment, node.TemplateProbability, want)
			}
		}
	}
}

func TestPathTrieAveragesTemplateProbabilityAcrossSharedPaths(t *testing.T) {
	trie := BuildPathTrie(map[string]string{
		"/users/1":        "10",
		"/users/1/orders": "30",
	})

	shortSegs, _ := urltemplate.SplitPath("/users/1")
	longSegs, _ := urltemplate.SplitPath("/users/1/orders")

	id := trie.Root.Children["users"].Children["1"]
	wantID := weightedTemplateProbability(
		model.Probability(shortSegs, 1), 10,
		model.Probability(longSegs, 1), 30,
	)
	if math.Abs(id.TemplateProbability-wantID) > 1e-12 {
		t.Errorf("/users/1 probability=%v, want %v", id.TemplateProbability, wantID)
	}

	orders := id.Children["orders"]
	wantOrders := model.Probability(longSegs, 2)
	if math.Abs(orders.TemplateProbability-wantOrders) > 1e-12 {
		t.Errorf("/users/1/orders probability=%v, want %v", orders.TemplateProbability, wantOrders)
	}
}
