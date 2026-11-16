package http

import (
	"testing"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// A transaction guardrail's delete must reach the transaction scope: the
// handler used to pin the service scope, so the delete matched no row, still
// answered 204, and the guardrail stayed.
func TestGuardrailScopeFromQuery(t *testing.T) {
	cases := []struct {
		raw     string
		want    repo.PolicyScope
		wantErr bool
	}{
		{raw: "", want: repo.ScopeService},
		{raw: "service", want: repo.ScopeService},
		{raw: " transaction ", want: repo.ScopeTransaction},
		{raw: "transaction", want: repo.ScopeTransaction},
		{raw: "global", wantErr: true},
		{raw: "bogus", wantErr: true},
	}
	for _, tc := range cases {
		got, err := guardrailScopeFromQuery(tc.raw)
		if tc.wantErr {
			if err == nil {
				t.Errorf("guardrailScopeFromQuery(%q) = %q, want an error", tc.raw, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("guardrailScopeFromQuery(%q) = %q, %v; want %q", tc.raw, got, err, tc.want)
		}
	}
}
