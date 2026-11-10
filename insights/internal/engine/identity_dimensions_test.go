package engine

import (
	"testing"

	"github.com/odigos-io/odigos/insights/internal/engine/semconv"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestApplyIdentityDimensions_StatusSplitsIdentity(t *testing.T) {
	dims := []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
	}
	op200 := applyIdentityDimensions("POST /buy", &Span{
		Name:      "POST /buy",
		SpanAttrs: map[string]string{semconv.HTTPResponseStatusCode: "200"},
	}, dims)
	op403 := applyIdentityDimensions("POST /buy", &Span{
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
	id200 := repo.HashTransactionID("svc", "ns", op200, repo.KindHTTP)
	id403 := repo.HashTransactionID("svc", "ns", op403, repo.KindHTTP)
	if id200 == id403 {
		t.Fatal("expected different transaction ids for 200 vs 403")
	}
}

func TestApplyIdentityDimensions_MissingStatusOmits(t *testing.T) {
	dims := []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
		{Key: repo.IdentityDimRPCGRPCStatus, Enabled: true},
	}
	got := applyIdentityDimensions("POST /buy", &Span{
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
	got := applyIdentityDimensions("POST /buy", &Span{
		SpanAttrs: map[string]string{semconv.HTTPResponseStatusCode: "500"},
	}, dims)
	if got != "POST /buy" {
		t.Fatalf("disabled dim should not append, got %q", got)
	}
	gotEmpty := applyIdentityDimensions("POST /buy", &Span{
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
	ab := applyIdentityDimensions("op", root, []repo.TransactionIdentityDimension{
		{Key: "a", Enabled: true},
		{Key: "b", Enabled: true},
	})
	ba := applyIdentityDimensions("op", root, []repo.TransactionIdentityDimension{
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
	got := applyIdentityDimensions("GET /x", &Span{
		SpanAttrs: map[string]string{semconv.LegacyHTTPStatusCode: "404"},
	}, []repo.TransactionIdentityDimension{
		{Key: repo.IdentityDimHTTPStatusCode, Enabled: true},
	})
	if got != "GET /x [http.response.status_code=404]" {
		t.Fatalf("got %q", got)
	}
}

func TestEscapeIdentityDimToken(t *testing.T) {
	got := applyIdentityDimensions("op", &Span{
		SpanAttrs: map[string]string{"weird": "a=b]c"},
	}, []repo.TransactionIdentityDimension{
		{Key: "weird", Enabled: true},
	})
	if got != `op [weird=a\=b\]c]` {
		t.Fatalf("got %q", got)
	}
}
