package engine

import (
	"slices"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestApplyIdentityDimensions_StatusSplitsIdentity(t *testing.T) {
	dims := []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
	}
	op200 := identityOperation("POST /buy", &Span{
		Name:      "POST /buy",
		SpanAttrs: map[string]string{semconv.HTTPResponseStatusCode: "200"},
	}, dims)
	op403 := identityOperation("POST /buy", &Span{
		Name:      "POST /buy",
		SpanAttrs: map[string]string{semconv.HTTPResponseStatusCode: "403"},
	}, dims)
	if op200 == op403 {
		t.Fatalf("expected different operations for 200 vs 403, both %q", op200)
	}
	if op200 != "POST /buy [http.response.status_code=200]" {
		t.Fatalf("200 op = %q", op200)
	}
	if op403 != "POST /buy [http.response.status_code=403]" {
		t.Fatalf("403 op = %q", op403)
	}
	id200 := repo.HashTransactionID("svc", "ns", op200, repo.KindHTTP, nil)
	id403 := repo.HashTransactionID("svc", "ns", op403, repo.KindHTTP, nil)
	if id200 == id403 {
		t.Fatal("expected different transaction ids for 200 vs 403")
	}
}

func TestApplyIdentityDimensions_MissingStatusOmits(t *testing.T) {
	dims := []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
		{Key: repo.IdentityDimRPCGRPCStatus, Enabled: true},
	}
	got := identityOperation("POST /buy", &Span{
		Name:      "POST /buy",
		SpanAttrs: map[string]string{},
	}, dims)
	if got != "POST /buy" {
		t.Fatalf("got %q, want unchanged operation", got)
	}
}

func TestApplyIdentityDimensions_DisabledConfig(t *testing.T) {
	dims := []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: false},
	}
	got := identityOperation("POST /buy", &Span{
		SpanAttrs: map[string]string{semconv.HTTPResponseStatusCode: "500"},
	}, dims)
	if got != "POST /buy" {
		t.Fatalf("disabled dim should not append, got %q", got)
	}
	gotEmpty := identityOperation("POST /buy", &Span{
		SpanAttrs: map[string]string{semconv.HTTPResponseStatusCode: "500"},
	}, []repo.TransactionIdentityDimension{})
	if gotEmpty != "POST /buy" {
		t.Fatalf("empty config should not append, got %q", gotEmpty)
	}
}

func TestApplyIdentityDimensions_OrderStable(t *testing.T) {
	root := &Span{SpanAttrs: map[string]string{
		"a": "1",
		"b": "2",
	}}
	ab := identityOperation("op", root, []repo.TransactionIdentityDimension{
		{Key: "a", Enabled: true},
		{Key: "b", Enabled: true},
	})
	ba := identityOperation("op", root, []repo.TransactionIdentityDimension{
		{Key: "b", Enabled: true},
		{Key: "a", Enabled: true},
	})
	if ab != "op [a=1][b=2]" {
		t.Fatalf("ab = %q", ab)
	}
	if ba != "op [b=2][a=1]" {
		t.Fatalf("ba = %q", ba)
	}
	if ab == ba {
		t.Fatal("order must affect identity suffix")
	}
}

func TestApplyIdentityDimensions_LegacyHTTPStatus(t *testing.T) {
	got := identityOperation("GET /x", &Span{
		SpanAttrs: map[string]string{semconv.LegacyHTTPStatusCode: "404"},
	}, []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
	})
	if got != "GET /x [http.response.status_code=404]" {
		t.Fatalf("got %q", got)
	}
}

func TestEscapeIdentityDimToken(t *testing.T) {
	got := identityOperation("op", &Span{
		SpanAttrs: map[string]string{"weird": "a=b]c"},
	}, []repo.TransactionIdentityDimension{
		{Key: "weird", Enabled: true},
	})
	if got != `op [weird=a\=b\]c]` {
		t.Fatalf("got %q", got)
	}
}

// identityOperation is the operation string the engine hashes for root.
func identityOperation(operation string, root *Span, dims []repo.TransactionIdentityDimension) string {
	return applyIdentityDimensions(operation, identityAttributes(root, dims))
}

func TestIdentityAttributes(t *testing.T) {
	root := &Span{SpanAttrs: map[string]string{
		semconv.LegacyHTTPStatusCode: "404",
		"tenant":                     "acme",
		"region":                     "eu",
	}}
	got := identityAttributes(root, []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
		{Key: "tenant", Enabled: true},
		{Key: "region", Enabled: false},
		{Key: "missing", Enabled: true},
	})
	want := []repo.TransactionIdentityValue{{Key: repo.IdentityDimHTTPStatusCode, Value: "404"}, {Key: "tenant", Value: "acme"}}
	if !slices.Equal(got, want) {
		t.Fatalf("attributes = %v, want %v", got, want)
	}
	if got := identityAttributes(nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("nil root = %#v, want empty list", got)
	}
}
