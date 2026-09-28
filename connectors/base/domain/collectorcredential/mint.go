package collectorcredential

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Token shape. The value is opaque — it carries no claims, so nothing can be learned from it and
// nothing about it can be forged; every fact about a credential comes from its row.
const (
	// TokenPrefix marks a value as an Odigos managed-Collector credential.
	//
	// Not decoration. A credential's whole life is spent in places where somebody has to work out what
	// they are looking at: a provider secret store, a support transcript, a task definition somebody
	// pasted into a ticket. A recognisable prefix also lets automated secret scanners flag one that
	// reaches a repository, which is the leak path that no amount of care inside Odigos prevents.
	TokenPrefix = "ocol_"
	// TokenRandomBytes is the entropy behind the prefix. 256 bits: the credential is long-lived by
	// design, so it has to stay infeasible to guess for as long as the Collector runs, and there
	// is no rotation clock shortening that window.
	TokenRandomBytes = 32
)

// ID shape. Prefixed so an id in a log line, an audit row or a header says what it identifies without
// being looked up — a bare opaque string next to another bare opaque string is how a Collector id ends
// up passed where a credential id belongs.
const (
	CollectorIDPrefix  = "col_"
	CredentialIDPrefix = "cred_"
	// IDRandomBytes is 128 bits, which is what makes an id collision-free without coordination. Central
	// allocates collector ids today and a connector may mint them in the interim; neither can see
	// the other's, so uniqueness has to come from width rather than from a sequence.
	IDRandomBytes = 16
)

// Minted is a freshly created credential: the value and the hash of that exact value.
//
// They travel together so a Record cannot be assembled around a hash of something else. Getting that
// pairing wrong produces a credential the gateway will never authenticate, reported as "deployed but
// never connected" — the failure lists as the one the connector cannot diagnose, because it does
// not read the secret back.
//
// Marshalling a Minted fails, because Secret's marshallers fail. That is the intent: this struct exists
// for the few lines between minting and writing to the secret store.
type Minted struct {
	Secret Secret
	// Hash is Secret.Hash() — the only representation that is persisted.
	Hash string
}

// Mint creates a new credential value.
//
// This is the one function in this package that is not a pure computation, and it is here rather than
// in the store layer on purpose: minting is the moment a Secret comes into existence, and the type that
// keeps a credential out of logs and serialized structures should be the only thing that constructs
// one. A store that minted would have to build a Secret from a loose string, which is exactly the
// operation worth confining to one place.
func Mint() (Minted, error) {
	buf := make([]byte, TokenRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		// Never fall back to a weaker source. A credential nobody can guess is the only thing standing
		// between an attacker and a configuration push carrying the customer's destination credential.
		return Minted{}, fmt.Errorf("no secure randomness available to mint a Collector credential: %w", err)
	}

	secret := NewSecret(TokenPrefix + base64.RawURLEncoding.EncodeToString(buf))
	hash, err := secret.Hash()
	if err != nil {
		// Unreachable: the value always has at least the prefix. Checked anyway, because the failure it
		// guards against is persisting EmptyDigest as though a credential had been issued.
		return Minted{}, fmt.Errorf("hashing a freshly minted credential: %w", err)
	}
	return Minted{Secret: secret, Hash: hash}, nil
}

// NewCollectorID allocates a Collector identity. One per logical Collector, kept across every rotation
// of its credential — the identity is the Collector, not the credential it currently holds.
func NewCollectorID() (string, error) { return newID(CollectorIDPrefix) }

// NewCredentialID allocates an issuance identity. One per ISSUANCE, so a rotation produces a second id
// for the same Collector and revocation has something to name.
func NewCredentialID() (string, error) { return newID(CredentialIDPrefix) }

func newID(prefix string) (string, error) {
	buf := make([]byte, IDRandomBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("no secure randomness available to allocate a %q id: %w", prefix, err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}
