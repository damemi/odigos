package learning

import (
	"math"
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
	if one.TerminalObservationCount != 10 {
		t.Fatalf("/users/1 obs=%d, want 10", one.TerminalObservationCount)
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
