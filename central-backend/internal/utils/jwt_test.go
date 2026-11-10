package utils_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/odigos-io/odigos/central-backend/internal/utils"
	"github.com/odigos-io/odigos/central-backend/internal/utils/tokentest"
)

func newVerifier(t *testing.T, realm *tokentest.Realm, edit func(*utils.TokenVerifierConfig)) *utils.TokenVerifier {
	t.Helper()
	cfg := realm.VerifierConfig()
	if edit != nil {
		edit(&cfg)
	}
	return utils.NewTokenVerifier(cfg)
}

func unsignedToken(t *testing.T, header map[string]any, claims jwt.MapClaims) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(header) + "." + enc(claims) + "."
}

func TestVerifyAcceptsValidTokenForEveryRole(t *testing.T) {
	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, nil)

	for _, role := range []string{"owner", "admin", "maintainer", "viewer", "no-access"} {
		t.Run(role, func(t *testing.T) {
			claims, err := v.Verify(realm.Token(t, "user-"+role, role))
			if err != nil {
				t.Fatalf("Verify() error = %v", err)
			}
			if claims.Subject != "user-"+role {
				t.Fatalf("sub = %q, want user-%s", claims.Subject, role)
			}
			if len(claims.RealmAccess.Roles) != 1 || claims.RealmAccess.Roles[0] != role {
				t.Fatalf("roles = %v, want [%s]", claims.RealmAccess.Roles, role)
			}
		})
	}
}

func TestVerifyRejectsInvalidTokens(t *testing.T) {
	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, nil)
	attackerKey := tokentest.NewKey(t)

	withClaim := func(key string, value any) string {
		claims := realm.Claims("u", "admin")
		if value == nil {
			delete(claims, key)
		} else {
			claims[key] = value
		}
		return realm.Sign(t, claims)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"forged signature", tokentest.SignWith(t, attackerKey, "key-1", realm.Claims("u", "admin"))},
		{"alg none", unsignedToken(t, map[string]any{"alg": "none", "typ": "JWT", "kid": "key-1"}, realm.Claims("u", "admin"))},
		{"alg none without kid", unsignedToken(t, map[string]any{"alg": "none", "typ": "JWT"}, realm.Claims("u", "admin"))},
		// Algorithm confusion: HMAC keyed with the realm's own public key.
		{"HS256 keyed with the realm public key", tokentest.SignWithMethod(t, jwt.SigningMethodHS256, realm.PublicKey().N.Bytes(), "key-1", realm.Claims("u", "admin"))},
		{"HS256 keyed with the realm public key PEM", tokentest.SignWithMethod(t, jwt.SigningMethodHS256, publicKeyPEM(t, realm.PublicKey()), "key-1", realm.Claims("u", "admin"))},
		{"PS256 with an RS256 key", tokentest.SignWithMethod(t, jwt.SigningMethodPS256, attackerKey, "key-1", realm.Claims("u", "admin"))},
		{"tampered payload", func() string {
			parts := strings.Split(realm.Token(t, "u", "viewer"), ".")
			forged := strings.Split(unsignedToken(t, map[string]any{}, realm.Claims("u", "admin")), ".")
			return parts[0] + "." + forged[1] + "." + parts[2]
		}()},
		{"unknown key id", tokentest.SignWith(t, attackerKey, "attacker-key", realm.Claims("u", "admin"))},
		{"no key id", tokentest.SignWith(t, attackerKey, "", realm.Claims("u", "admin"))},
		{"wrong issuer", withClaim("iss", "https://evil.example.com/realms/master")},
		{"missing issuer", withClaim("iss", nil)},
		{"expired", withClaim("exp", time.Now().Add(-time.Minute).Unix())},
		{"missing expiry", withClaim("exp", nil)},
		{"not yet valid", withClaim("nbf", time.Now().Add(time.Hour).Unix())},
		{"other client", withClaim("azp", "admin-cli")},
		{"id token", withClaim("typ", "ID")},
		{"refresh token", withClaim("typ", "Refresh")},
		{"missing subject", withClaim("sub", nil)},
		{"missing issued-at", withClaim("iat", nil)},
		{"not a jwt", "not-a-jwt"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if claims, err := v.Verify(tc.token); err == nil {
				t.Fatalf("Verify() accepted token, claims = %+v", claims)
			}
		})
	}
}

func TestVerifyRejectsAlgorithmOutsideConfiguredSet(t *testing.T) {
	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) { cfg.Algorithms = []string{"PS256"} })
	if _, err := v.Verify(realm.Token(t, "u", "admin")); err == nil {
		t.Fatal("Verify() accepted an RS256 token when only PS256 is configured")
	}
}

func TestVerifyRefreshesKeysOnUnknownKeyID(t *testing.T) {
	realm := tokentest.NewRealm(t)
	// Rate limiting is covered below; here every unknown kid may refresh.
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) { cfg.MinRefreshInterval = time.Nanosecond })

	if _, err := v.Verify(realm.Token(t, "u", "admin")); err != nil {
		t.Fatalf("Verify() before rotation: %v", err)
	}
	if _, err := v.Verify(realm.Token(t, "u", "admin")); err != nil {
		t.Fatalf("Verify() cached key: %v", err)
	}
	if got := realm.JWKSRequests.Load(); got != 1 {
		t.Fatalf("JWKS requests = %d, want 1 (known kid served from cache)", got)
	}

	realm.RotateKey(t)
	if _, err := v.Verify(realm.Token(t, "u", "admin")); err != nil {
		t.Fatalf("Verify() after rotation: %v", err)
	}
	if got := realm.JWKSRequests.Load(); got != 2 {
		t.Fatalf("JWKS requests = %d, want 2 (one refresh for the new kid)", got)
	}
}

func TestVerifyRateLimitsKeyRefresh(t *testing.T) {
	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) { cfg.MinRefreshInterval = time.Hour })
	attackerKey := tokentest.NewKey(t)

	for i := range 20 {
		token := tokentest.SignWith(t, attackerKey, fmt.Sprintf("random-%d", i), realm.Claims("u", "admin"))
		if _, err := v.Verify(token); err == nil {
			t.Fatal("Verify() accepted a token signed with an unknown key")
		}
	}
	if got := realm.JWKSRequests.Load(); got != 1 {
		t.Fatalf("JWKS requests = %d, want 1 (unknown kids must not trigger a fetch each)", got)
	}
	// Known keys keep working while refreshes are rate-limited.
	if _, err := v.Verify(realm.Token(t, "u", "admin")); err != nil {
		t.Fatalf("Verify() valid token: %v", err)
	}
}

func TestVerifyAcceptsConfiguredIssuers(t *testing.T) {
	realm := tokentest.NewRealm(t)
	const browserIssuer = "https://central.example.com/realms/master"
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) {
		cfg.DiscoveryURL = ""
		cfg.Issuers = []string{browserIssuer + "/"}
	})

	claims := realm.Claims("u", "admin")
	claims["iss"] = browserIssuer
	if _, err := v.Verify(realm.Sign(t, claims)); err != nil {
		t.Fatalf("Verify() configured issuer: %v", err)
	}
	// Without discovery, the realm's own URL is not an accepted issuer.
	if _, err := v.Verify(realm.Token(t, "u", "admin")); err == nil {
		t.Fatal("Verify() accepted an issuer that is not configured")
	}
}

func TestClaimReadsRequireVerifiedToken(t *testing.T) {
	realm := tokentest.Install(t)
	forged := tokentest.SignWith(t, tokentest.NewKey(t), "key-1", realm.Claims("u", "admin"))

	if _, err := utils.ExtractRolesFromToken(forged); err == nil {
		t.Fatal("ExtractRolesFromToken() read claims from a forged token")
	}
	if _, err := utils.ExtractEmailFromToken(forged); err == nil {
		t.Fatal("ExtractEmailFromToken() read claims from a forged token")
	}
	roles, err := utils.ExtractRolesFromToken(realm.Token(t, "u", "admin"))
	if err != nil || len(roles) != 1 || roles[0] != "admin" {
		t.Fatalf("ExtractRolesFromToken() = %v, %v; want [admin]", roles, err)
	}
}

func TestVerifyAccessTokenWithoutVerifier(t *testing.T) {
	realm := tokentest.NewRealm(t)
	t.Cleanup(utils.SetTokenVerifier(nil))
	if _, err := utils.VerifyAccessToken(realm.Token(t, "u", "admin")); !errors.Is(err, utils.ErrTokenVerifierNotConfigured) {
		t.Fatalf("VerifyAccessToken() = %v, want ErrTokenVerifierNotConfigured", err)
	}
}

func publicKeyPEM(t *testing.T, key *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// A JWK published with alg RS256 must not verify a PS256 signature, even when
// both algorithms are configured.
func TestVerifyRejectsAlgorithmOtherThanTheKeys(t *testing.T) {
	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) { cfg.Algorithms = []string{"RS256", "PS256"} })
	realmKey := realmPrivateKey(t, realm)
	if _, err := v.Verify(tokentest.SignWithMethod(t, jwt.SigningMethodPS256, realmKey, "key-1", realm.Claims("u", "admin"))); err == nil {
		t.Fatal("Verify() accepted PS256 with a key published for RS256")
	}
}

// realmPrivateKey rotates in a key the test holds, so it can sign arbitrary
// algorithms with a key the realm really publishes.
func realmPrivateKey(t *testing.T, realm *tokentest.Realm) *rsa.PrivateKey {
	t.Helper()
	key := tokentest.NewKey(t)
	realm.UseKey(t, "key-1", key)
	return key
}

func TestVerifyIgnoresUnusableJWKs(t *testing.T) {
	encKey, shortKey := tokentest.NewKey(t), shortRSAKey(t)
	jwk := func(kid, use, alg string, key *rsa.PublicKey) map[string]string {
		return map[string]string{
			"kid": kid, "kty": "RSA", "use": use, "alg": alg,
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			jwk("enc", "enc", "RSA-OAEP", &encKey.PublicKey),
			jwk("enc-no-alg", "enc", "", &encKey.PublicKey),
			jwk("hs", "sig", "HS256", &encKey.PublicKey),
			jwk("short", "sig", "RS256", &shortKey.PublicKey),
		}})
	}))
	t.Cleanup(server.Close)

	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) { cfg.JWKSURL = server.URL })
	for kid, key := range map[string]*rsa.PrivateKey{"enc": encKey, "enc-no-alg": encKey, "hs": encKey, "short": shortKey} {
		t.Run(kid, func(t *testing.T) {
			if _, err := v.Verify(tokentest.SignWith(t, key, kid, realm.Claims("u", "admin"))); err == nil {
				t.Fatalf("Verify() accepted a token signed with JWK %q", kid)
			}
		})
	}
}

func shortRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatalf("generate RSA key: %v", err)
	}
	return key
}

func TestVerifyKeepsCachedKeysWhileKeycloakIsDown(t *testing.T) {
	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) {
		cfg.MinRefreshInterval = time.Nanosecond
		cfg.MaxKeyAge = time.Nanosecond
	})
	if _, err := v.Verify(realm.Token(t, "u", "admin")); err != nil {
		t.Fatalf("Verify(): %v", err)
	}

	realm.FailJWKS.Store(true)
	if _, err := v.Verify(realm.Token(t, "u", "admin")); err != nil {
		t.Fatalf("Verify() with Keycloak down: %v (cached key must still work)", err)
	}
	if _, err := v.Verify(tokentest.SignWith(t, tokentest.NewKey(t), "new-key", realm.Claims("u", "admin"))); err == nil {
		t.Fatal("Verify() accepted an unknown key while Keycloak is down")
	}
}

// Past MaxKeyAge a token with a known key is served from cache while the keys
// refresh in the background.
func TestVerifyRefreshesOldKeysInBackground(t *testing.T) {
	realm := tokentest.NewRealm(t)
	v := newVerifier(t, realm, func(cfg *utils.TokenVerifierConfig) {
		cfg.MinRefreshInterval = time.Nanosecond
		cfg.MaxKeyAge = time.Nanosecond
	})
	for range 2 {
		if _, err := v.Verify(realm.Token(t, "u", "admin")); err != nil {
			t.Fatalf("Verify(): %v", err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for realm.JWKSRequests.Load() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("JWKS requests = %d, want a background refresh", realm.JWKSRequests.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
