package repo

import "testing"

func TestNormalizePolicyScope(t *testing.T) {
	cases := []struct {
		in   PolicyScope
		want PolicyScope
	}{
		{ScopeService, ScopeService},
		{ScopeTransaction, ScopeTransaction},
		{ScopeGlobal, ScopeGlobal},
		{"", ScopeGlobal},
		{"unknown", ScopeGlobal},
	}
	for _, tc := range cases {
		if got := NormalizePolicyScope(tc.in); got != tc.want {
			t.Fatalf("NormalizePolicyScope(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestScopeIndex_MostSpecificWins(t *testing.T) {
	idx := &ScopeIndex[string]{}
	if !idx.Put(ScopeGlobal, "", "global") {
		t.Fatal("put global")
	}
	if !idx.Put(ScopeService, ServiceScopeKey("ns", "shop"), "service") {
		t.Fatal("put service")
	}
	if !idx.Put(ScopeTransaction, TransactionScopeKey(42), "txn") {
		t.Fatal("put txn")
	}

	if got, ok := idx.Resolve(42, ServiceScopeKey("ns", "shop")); !ok || got != "txn" {
		t.Fatalf("txn override = %q ok=%v, want txn", got, ok)
	}
	if got, ok := idx.Resolve(99, ServiceScopeKey("ns", "shop")); !ok || got != "service" {
		t.Fatalf("service fallback = %q ok=%v, want service", got, ok)
	}
	if got, ok := idx.Resolve(99, ServiceScopeKey("other", "svc")); !ok || got != "global" {
		t.Fatalf("global fallback = %q ok=%v, want global", got, ok)
	}
}

func TestScopeIndex_UnknownScopeStoredAsGlobal(t *testing.T) {
	idx := &ScopeIndex[string]{}
	if !idx.Put("", "ignored", "from-empty") {
		t.Fatal("empty scope should store as global")
	}
	got, ok := idx.Global()
	if !ok || got != "from-empty" {
		t.Fatalf("Global() = %q ok=%v", got, ok)
	}
}

func TestScopeIndex_InvalidTransactionKeyDropped(t *testing.T) {
	idx := &ScopeIndex[string]{}
	if idx.Put(ScopeTransaction, "not-an-id", "x") {
		t.Fatal("non-numeric transaction key must be dropped")
	}
	if _, ok := idx.Resolve(0, ""); ok {
		t.Fatal("dropped key must not resolve")
	}
}

func TestScopeIndex_NilAndEmpty(t *testing.T) {
	var idx *ScopeIndex[string]
	if _, ok := idx.Resolve(1, "ns/svc"); ok {
		t.Fatal("nil index must not resolve")
	}
	if _, ok := idx.Global(); ok {
		t.Fatal("nil index has no global")
	}

	empty := &ScopeIndex[string]{}
	if _, ok := empty.Resolve(1, "ns/svc"); ok {
		t.Fatal("empty index must not resolve")
	}
}

func TestServiceAndTransactionScopeKeys(t *testing.T) {
	if got := ServiceScopeKey("prod", "checkout"); got != "prod/checkout" {
		t.Fatalf("ServiceScopeKey = %q", got)
	}
	if got := TransactionScopeKey(123456789); got != "123456789" {
		t.Fatalf("TransactionScopeKey = %q", got)
	}
}
