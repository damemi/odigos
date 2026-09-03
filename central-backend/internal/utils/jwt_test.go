package utils

import (
	"encoding/base64"
	"testing"
)

func TestExtractSubjectFromToken(t *testing.T) {
	t.Run("reads sub", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{"sub": "user-42", "iat": float64(1_700_000_000)})
		got, err := ExtractSubjectFromToken(token)
		if err != nil {
			t.Fatalf("ExtractSubjectFromToken: %v", err)
		}
		if got != "user-42" {
			t.Fatalf("sub = %q, want user-42", got)
		}
	})

	t.Run("rejects missing sub", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{"iat": float64(1)})
		if _, err := ExtractSubjectFromToken(token); err == nil {
			t.Fatal("expected error for missing sub")
		}
	})

	t.Run("rejects empty sub", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{"sub": ""})
		if _, err := ExtractSubjectFromToken(token); err == nil {
			t.Fatal("expected error for empty sub")
		}
	})
}

func TestExtractIssuedAtFromToken(t *testing.T) {
	t.Run("reads iat", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{"iat": float64(1_700_000_123)})
		got, err := ExtractIssuedAtFromToken(token)
		if err != nil {
			t.Fatalf("ExtractIssuedAtFromToken: %v", err)
		}
		if got != 1_700_000_123 {
			t.Fatalf("iat = %d, want 1700000123", got)
		}
	})

	t.Run("rejects missing iat", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{"sub": "u"})
		if _, err := ExtractIssuedAtFromToken(token); err == nil {
			t.Fatal("expected error for missing iat")
		}
	})

	t.Run("rejects string iat", func(t *testing.T) {
		token := makeTestJWT(t, map[string]any{"iat": "1700000123"})
		if _, err := ExtractIssuedAtFromToken(token); err == nil {
			t.Fatal("expected error for non-numeric iat")
		}
	})
}

func TestDecodeJWTClaimsRejectsMalformed(t *testing.T) {
	cases := []struct {
		name  string
		token string
	}{
		{"too few parts", "only.two"},
		{"too many parts", "a.b.c.d"},
		{"bad payload encoding", "hdr.@@@.sig"},
		{"non-json payload", "hdr." + base64.RawURLEncoding.EncodeToString([]byte("not-json")) + ".sig"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeJWTClaims(tc.token); err == nil {
				t.Fatal("expected decode error")
			}
			if _, err := ExtractSubjectFromToken(tc.token); err == nil {
				t.Fatal("expected subject extract error")
			}
			if _, err := ExtractIssuedAtFromToken(tc.token); err == nil {
				t.Fatal("expected iat extract error")
			}
		})
	}
}
