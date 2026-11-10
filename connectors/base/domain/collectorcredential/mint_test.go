package collectorcredential

import (
	"encoding/base64"
	"strings"
	"testing"
)

// TestMintProducesADistinctUnguessableValue: the credential is long-lived by design, so it has to stay
// infeasible to guess for as long as the Collector runs. There is no rotation clock shortening that.
func TestMintProducesADistinctUnguessableValue(t *testing.T) {
	const samples = 200

	seenTokens := map[string]bool{}
	seenHashes := map[string]bool{}
	for i := 0; i < samples; i++ {
		m, err := Mint()
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}

		token := m.Secret.Reveal()
		if !strings.HasPrefix(token, TokenPrefix) {
			t.Fatalf("token %q does not carry the prefix that makes it recognisable in a secret store", token)
		}
		body := strings.TrimPrefix(token, TokenPrefix)
		raw, err := base64.RawURLEncoding.DecodeString(body)
		if err != nil {
			t.Fatalf("token body is not base64url: %v", err)
		}
		if len(raw) != TokenRandomBytes {
			t.Fatalf("token carries %d random bytes, want %d", len(raw), TokenRandomBytes)
		}
		if seenTokens[token] {
			t.Fatalf("Mint repeated a value after %d samples", i)
		}
		seenTokens[token] = true

		if seenHashes[m.Hash] {
			t.Fatalf("Mint repeated a hash after %d samples", i)
		}
		seenHashes[m.Hash] = true
	}
}

// TestMintPairsTheHashWithItsOwnValue: a Record assembled around the hash of something else is a
// credential the gateway will never authenticate, reported as "deployed but never connected" — the
// failure lists as the one the connector cannot diagnose, because it never reads the secret back.
func TestMintPairsTheHashWithItsOwnValue(t *testing.T) {
	m, err := Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	want, err := m.Secret.Hash()
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if m.Hash != want {
		t.Errorf("Minted.Hash = %q, but hashing its own value gives %q", m.Hash, want)
	}
	if m.Hash == EmptyDigest {
		t.Error("Mint produced the digest of the empty string")
	}
}

// TestMintedCannotBeSerialized: Minted exists for the few lines between minting and writing to the
// secret store. It inherits Secret's refusal to marshal, so a Minted that reaches a log line or a
// report fails loudly instead of emitting a working credential.
func TestMintedCannotBeSerialized(t *testing.T) {
	m, err := Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, err := m.Secret.MarshalJSON(); err == nil {
		t.Error("a minted credential marshalled to JSON")
	}
	if got := m.Secret.String(); got != "<redacted>" {
		t.Errorf("a minted credential formatted as %q", got)
	}
}

// TestIDsAreDistinctAndLabelled: prefixes are what keep a Collector id from being passed where a
// credential id belongs, and both appear side by side in headers, logs and audit rows.
func TestIDsAreDistinctAndLabelled(t *testing.T) {
	const samples = 200

	for _, tc := range []struct {
		name   string
		mint   func() (string, error)
		prefix string
	}{
		{"collector id", NewCollectorID, CollectorIDPrefix},
		{"credential id", NewCredentialID, CredentialIDPrefix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]bool{}
			for i := 0; i < samples; i++ {
				id, err := tc.mint()
				if err != nil {
					t.Fatalf("mint: %v", err)
				}
				if !strings.HasPrefix(id, tc.prefix) {
					t.Fatalf("%q does not carry the %q prefix", id, tc.prefix)
				}
				raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, tc.prefix))
				if err != nil {
					t.Fatalf("id body is not base64url: %v", err)
				}
				if len(raw) != IDRandomBytes {
					t.Fatalf("id carries %d bytes of entropy, want %d", len(raw), IDRandomBytes)
				}
				if seen[id] {
					t.Fatalf("id repeated after %d samples", i)
				}
				seen[id] = true
			}
		})
	}

	// And the two vocabularies must not overlap, or a prefix check cannot tell them apart.
	if strings.HasPrefix(CollectorIDPrefix, CredentialIDPrefix) || strings.HasPrefix(CredentialIDPrefix, CollectorIDPrefix) {
		t.Errorf("id prefixes %q and %q are ambiguous", CollectorIDPrefix, CredentialIDPrefix)
	}
}

// TestTokenPrefixIsNotAnIDPrefix guards against a value being mistaken for an identifier in a log line
// or a support transcript, which is precisely where somebody would paste one.
func TestTokenPrefixIsNotAnIDPrefix(t *testing.T) {
	for _, idPrefix := range []string{CollectorIDPrefix, CredentialIDPrefix} {
		if strings.HasPrefix(TokenPrefix, idPrefix) || strings.HasPrefix(idPrefix, TokenPrefix) {
			t.Errorf("token prefix %q is confusable with id prefix %q", TokenPrefix, idPrefix)
		}
	}
}
