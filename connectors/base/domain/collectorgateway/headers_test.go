package collectorgateway

import (
	"net/http"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

func identity() Identity {
	return Identity{Binding: binding(), CredentialID: "cred-1"}
}

// TestIdentityRoundTrip: what the gateway sets is what the connector reads. The two live in different
// processes and different modules, so nothing but this test holds the spellings together.
func TestIdentityRoundTrip(t *testing.T) {
	h := http.Header{}
	identity().SetHeaders(h)

	got, ok := ParseIdentity(h)
	if !ok {
		t.Fatalf("ParseIdentity rejected what SetHeaders wrote: %+v", h)
	}
	if got != identity() {
		t.Errorf("round trip changed the identity:\n got %+v\nwant %+v", got, identity())
	}
}

// TestParseIdentityRequiresACompleteBinding: a partial identity is a gateway that changed without the
// connector. Serving configuration on one means serving it to something whose account or network was
// never established.
func TestParseIdentityRequiresACompleteBinding(t *testing.T) {
	for _, drop := range []string{
		HeaderCollectorID, HeaderCredentialID, HeaderConnectorUID,
		HeaderProvider, HeaderAccountID, HeaderRegion, HeaderNetworkID,
	} {
		h := http.Header{}
		identity().SetHeaders(h)
		h.Del(drop)

		if _, ok := ParseIdentity(h); ok {
			t.Errorf("an identity missing %s was accepted", drop)
		}
	}
	if _, ok := ParseIdentity(http.Header{}); ok {
		t.Error("an empty header set was accepted as an identity")
	}
}

// TestApplyStripsForgedIdentity is the header contract's security property: a Collector that names its
// own connector must not be able to pick one. Everything in the namespace is replaced from
// authenticated state, including keys this contract does not define.
func TestApplyStripsForgedIdentity(t *testing.T) {
	h := http.Header{}
	h.Set(HeaderConnectorUID, "uid-someone-elses")
	h.Set(HeaderCollectorID, "col-someone-elses")
	h.Set(HeaderAccountID, "999999999999")
	// A key the contract does not define today. Prefix stripping is what keeps a header added later
	// from being smuggled through until somebody remembers to add it to a list.
	h.Set("X-Odigos-Something-Added-Later", "forged")
	// Lower-cased on the wire; http.Header canonicalizes on Set, so assert the raw-map form too.
	h["x-odigos-raw-lowercase"] = []string{"forged"}
	h.Set("Authorization", "Bearer the-token")
	h.Set("User-Agent", "opamp-supervisor/1.0")

	identity().Apply(h)

	got, ok := ParseIdentity(h)
	if !ok {
		t.Fatal("Apply did not leave a readable identity")
	}
	if got != identity() {
		t.Errorf("forged headers survived:\n got %+v\nwant %+v", got, identity())
	}
	if v := h.Get("X-Odigos-Something-Added-Later"); v != "" {
		t.Errorf("an undeclared X-Odigos- header survived: %q", v)
	}
	if _, present := h["x-odigos-raw-lowercase"]; present {
		t.Error("a non-canonical X-Odigos- header survived")
	}
	// The credential buys the customer's destination credential out of a configuration push, and the
	// connector has no use for it: it keeps hashes and never reads a value back.
	if v := h.Get("Authorization"); v != "" {
		t.Errorf("the credential was forwarded upstream: %q", v)
	}
	if h.Get("User-Agent") != "opamp-supervisor/1.0" {
		t.Error("Apply removed a header outside its namespace")
	}
}

// TestPresentedCredential covers what arrives on the handshake. The Supervisor sends the whole header
// value verbatim from ODIGOS_OPAMP_AUTHORIZATION, so a customer's misconfiguration reaches here as-is.
func TestPresentedCredential(t *testing.T) {
	want, err := collectorcredential.NewSecret("the-token").Hash()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, header, wantHash string
		wantRefusal            Refusal
	}{
		{name: "bearer token", header: "Bearer the-token", wantHash: want},
		{name: "scheme is case-insensitive per RFC 7235", header: "bearer the-token", wantHash: want},
		{name: "surrounding whitespace", header: "  Bearer   the-token  ", wantHash: want},
		{name: "absent", header: "", wantRefusal: RefusalNoCredential},
		{name: "scheme with no value", header: "Bearer", wantRefusal: RefusalNoCredential},
		{name: "scheme with an empty value", header: "Bearer ", wantRefusal: RefusalNoCredential},
		{name: "basic auth", header: "Basic dXNlcjpwYXNz", wantRefusal: RefusalNoCredential},
		{name: "bare token with no scheme", header: "the-token", wantRefusal: RefusalNoCredential},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			if tc.header != "" {
				h.Set("Authorization", tc.header)
			}
			hash, refusal := PresentedCredential(h)
			if refusal != tc.wantRefusal {
				t.Errorf("refusal = %q, want %q", refusal, tc.wantRefusal)
			}
			if hash != tc.wantHash {
				t.Errorf("hash = %q, want %q", hash, tc.wantHash)
			}
		})
	}
}

// TestPresentedCredentialNeverReturnsTheEmptyDigest: an empty value must not become a well-formed hash
// that a broken row could match. Hash refuses it; this asserts the gateway's entry point inherits that.
func TestPresentedCredentialNeverReturnsTheEmptyDigest(t *testing.T) {
	for _, header := range []string{"Bearer ", "Bearer   ", "bearer\t"} {
		h := http.Header{}
		h.Set("Authorization", header)
		hash, _ := PresentedCredential(h)
		if hash == collectorcredential.EmptyDigest {
			t.Errorf("header %q produced the empty digest", header)
		}
		if hash != "" {
			t.Errorf("header %q produced a hash %q", header, hash)
		}
	}
}
