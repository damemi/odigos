// Package tokentest runs a fake Keycloak realm (discovery document and JWKS)
// and signs access tokens with its keys, for tests that need tokens
// central-backend accepts.
package tokentest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/odigos-io/central-backend/internal/consts"
	"github.com/odigos-io/central-backend/internal/utils"
)

var (
	defaultKeyOnce sync.Once
	defaultKey     *rsa.PrivateKey
)

// NewKey returns a fresh 2048-bit RSA key.
func NewKey(t testing.TB) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

// Realm is a fake Keycloak realm served over HTTP.
type Realm struct {
	// URL is the realm's issuer.
	URL string
	// JWKSRequests counts fetches of the key set.
	JWKSRequests atomic.Int32
	// FailJWKS makes the key set endpoint return 500, like an unreachable Keycloak.
	FailJWKS atomic.Bool

	mu     sync.Mutex
	keys   map[string]*rsa.PrivateKey
	signer string
}

// NewRealm starts a realm with one signing key.
func NewRealm(t testing.TB) *Realm {
	t.Helper()
	defaultKeyOnce.Do(func() { defaultKey = NewKey(t) })
	r := &Realm{keys: map[string]*rsa.PrivateKey{"key-1": defaultKey}, signer: "key-1"}

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	r.URL = server.URL + "/realms/" + consts.KeycloakRealm

	mux.HandleFunc("GET /realms/"+consts.KeycloakRealm+"/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": r.URL})
	})
	mux.HandleFunc("GET /realms/"+consts.KeycloakRealm+"/protocol/openid-connect/certs", func(w http.ResponseWriter, _ *http.Request) {
		r.JWKSRequests.Add(1)
		if r.FailJWKS.Load() {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		keys := make([]map[string]string, 0, len(r.keys))
		for kid, key := range r.keys {
			keys = append(keys, map[string]string{
				"kid": kid,
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	})
	return r
}

// VerifierConfig trusts this realm the way central-backend trusts Keycloak.
func (r *Realm) VerifierConfig() utils.TokenVerifierConfig {
	return utils.TokenVerifierConfig{
		JWKSURL:      r.URL + "/protocol/openid-connect/certs",
		DiscoveryURL: r.URL + "/.well-known/openid-configuration",
		ClientID:     consts.ClientID,
	}
}

// Install starts a realm and makes it the process-wide token verifier for the test.
func Install(t testing.TB) *Realm {
	t.Helper()
	r := NewRealm(t)
	t.Cleanup(utils.SetTokenVerifier(utils.NewTokenVerifier(r.VerifierConfig())))
	return r
}

// RotateKey adds a new signing key to the realm and signs with it from now on.
func (r *Realm) RotateKey(t testing.TB) string {
	t.Helper()
	key := NewKey(t)
	r.mu.Lock()
	defer r.mu.Unlock()
	kid := fmt.Sprintf("key-%d", len(r.keys)+1)
	r.keys[kid] = key
	r.signer = kid
	return kid
}

// PublicKey returns the realm's current signing key's public half.
func (r *Realm) PublicKey() *rsa.PublicKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	return &r.keys[r.signer].PublicKey
}

// UseKey publishes key under kid and signs with it from now on.
func (r *Realm) UseKey(t testing.TB, kid string, key *rsa.PrivateKey) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.keys[kid] = key
	r.signer = kid
}

// Claims returns the claims of a valid access token for sub with the given
// realm roles. Tests edit the map to build invalid tokens.
func (r *Realm) Claims(sub string, roles ...string) jwt.MapClaims {
	now := time.Now()
	return jwt.MapClaims{
		"iss":          r.URL,
		"azp":          consts.ClientID,
		"typ":          "Bearer",
		"sub":          sub,
		"iat":          now.Unix(),
		"exp":          now.Add(time.Hour).Unix(),
		"realm_access": map[string]any{"roles": roles},
	}
}

// Token signs a valid access token for sub with the given realm roles.
func (r *Realm) Token(t testing.TB, sub string, roles ...string) string {
	t.Helper()
	return r.Sign(t, r.Claims(sub, roles...))
}

// Sign signs claims with the realm's current key.
func (r *Realm) Sign(t testing.TB, claims jwt.MapClaims) string {
	t.Helper()
	r.mu.Lock()
	kid, key := r.signer, r.keys[r.signer]
	r.mu.Unlock()
	return SignWith(t, key, kid, claims)
}

// SignWith signs claims with RS256 using any key, e.g. one the realm doesn't serve.
func SignWith(t testing.TB, key *rsa.PrivateKey, kid string, claims jwt.MapClaims) string {
	t.Helper()
	return SignWithMethod(t, jwt.SigningMethodRS256, key, kid, claims)
}

// SignWithMethod signs claims with any signing method and key.
func SignWithMethod(t testing.TB, method jwt.SigningMethod, key any, kid string, claims jwt.MapClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	token.Header["kid"] = kid
	if kid == "" {
		delete(token.Header, "kid")
	}
	signed, err := token.SignedString(key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}
