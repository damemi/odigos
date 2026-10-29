package utils

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/odigos-io/central-backend/internal/logger"
	"golang.org/x/sync/singleflight"
)

// ErrTokenVerifierNotConfigured is returned when no TokenVerifier is installed,
// so every token is treated as unverified.
var ErrTokenVerifierNotConfigured = errors.New("token verifier not configured")

const (
	// keycloakAccessTokenType is the "typ" claim Keycloak puts on access tokens
	// (ID tokens carry "ID", refresh tokens "Refresh").
	keycloakAccessTokenType = "Bearer"

	defaultMinKeyRefreshInterval = 10 * time.Second
	defaultMaxKeyAge             = 10 * time.Minute
	maxStaleKeyAge               = 24 * time.Hour
	clockSkewLeeway              = 5 * time.Second
	keyFetchTimeout              = 5 * time.Second
	maxKeyDocumentBytes          = 1 << 20
)

// TokenClaims are the claims of a Keycloak access token whose signature,
// issuer, authorized party, type and expiry were verified.
type TokenClaims struct {
	jwt.RegisteredClaims
	Type            string `json:"typ"`
	AuthorizedParty string `json:"azp"`
	Email           string `json:"email"`
	RealmAccess     struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`
}

// TokenVerifierConfig describes the Keycloak realm whose tokens are trusted.
type TokenVerifierConfig struct {
	// JWKSURL is the realm's certs endpoint, reached over the backchannel.
	JWKSURL string
	// DiscoveryURL is the realm's OIDC discovery document. Its "issuer" is
	// accepted alongside Issuers. Optional.
	DiscoveryURL string
	// Issuers are the accepted "iss" values.
	Issuers []string
	// ClientID is the required "azp" (the client the token was issued to).
	ClientID string
	// Algorithms are the accepted RSA signing algorithms. Defaults to RS256.
	Algorithms []string
	// MinRefreshInterval rate-limits key fetches triggered by unknown key IDs.
	MinRefreshInterval time.Duration
	// MaxKeyAge triggers a background refresh so keys disabled in Keycloak stop
	// being trusted.
	MaxKeyAge  time.Duration
	HTTPClient *http.Client
}

type verificationKey struct {
	alg string
	key *rsa.PublicKey
}

// keySet is one successful fetch of the realm's keys and discovery issuer.
type keySet struct {
	keys      map[string]verificationKey
	issuer    string
	fetchedAt time.Time
}

// TokenVerifier verifies Keycloak access tokens against the realm's JWKS.
// Keys are cached; a token with an unknown key ID triggers a refresh, at most
// once per MinRefreshInterval, and tokens with known keys never wait on one.
type TokenVerifier struct {
	cfg    TokenVerifierConfig
	parser *jwt.Parser

	current   atomic.Pointer[keySet]
	refreshes singleflight.Group

	attemptMu        sync.Mutex
	lastFetchAttempt time.Time

	warnMu           sync.Mutex
	warnedBadIssuers map[string]struct{}
}

func NewTokenVerifier(cfg TokenVerifierConfig) *TokenVerifier {
	if len(cfg.Algorithms) == 0 {
		cfg.Algorithms = []string{jwt.SigningMethodRS256.Alg()}
	}
	if cfg.MinRefreshInterval <= 0 {
		cfg.MinRefreshInterval = defaultMinKeyRefreshInterval
	}
	if cfg.MaxKeyAge <= 0 {
		cfg.MaxKeyAge = defaultMaxKeyAge
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: keyFetchTimeout}
	}
	issuers := make([]string, 0, len(cfg.Issuers))
	for _, iss := range cfg.Issuers {
		if iss = normalizeIssuer(iss); iss != "" {
			issuers = append(issuers, iss)
		}
	}
	cfg.Issuers = issuers
	return &TokenVerifier{
		cfg: cfg,
		parser: jwt.NewParser(
			jwt.WithValidMethods(cfg.Algorithms),
			jwt.WithExpirationRequired(),
			jwt.WithLeeway(clockSkewLeeway),
		),
		warnedBadIssuers: map[string]struct{}{},
	}
}

// Verify checks the token's signature against the realm keys, then its expiry,
// issuer, authorized party and type, and returns its claims.
func (v *TokenVerifier) Verify(token string) (*TokenClaims, error) {
	claims := &TokenClaims{}
	if _, err := v.parser.ParseWithClaims(token, claims, v.keyFor); err != nil {
		return nil, err
	}
	if !v.isAcceptedIssuer(claims.Issuer) {
		return nil, fmt.Errorf("untrusted token issuer %q", claims.Issuer)
	}
	if claims.AuthorizedParty != v.cfg.ClientID {
		return nil, fmt.Errorf("token was issued to client %q", claims.AuthorizedParty)
	}
	if claims.Type != keycloakAccessTokenType {
		return nil, fmt.Errorf("token type %q is not an access token", claims.Type)
	}
	if claims.Subject == "" {
		return nil, errors.New("token has no subject")
	}
	if claims.IssuedAt == nil {
		return nil, errors.New("token has no issued-at time")
	}
	return claims, nil
}

// Warm fetches the realm keys ahead of the first request.
func (v *TokenVerifier) Warm() error {
	return v.refresh(true)
}

// usableKeys returns the cached key set, or nil when there is none or it is
// older than maxStaleKeyAge (Keycloak unreachable for that long: fail closed).
func (v *TokenVerifier) usableKeys() *keySet {
	ks := v.current.Load()
	if ks == nil || time.Since(ks.fetchedAt) > maxStaleKeyAge {
		return nil
	}
	return ks
}

func (v *TokenVerifier) lookup(kid string) (verificationKey, bool) {
	ks := v.usableKeys()
	if ks == nil {
		return verificationKey{}, false
	}
	key, ok := ks.keys[kid]
	return key, ok
}

func (v *TokenVerifier) keyFor(token *jwt.Token) (any, error) {
	kid, _ := token.Header["kid"].(string)
	if kid == "" {
		return nil, errors.New("token has no key id")
	}

	key, known := v.lookup(kid)
	if !known {
		// Rate-limited and shared between callers, so a flood of made-up key IDs
		// costs at most one fetch per MinRefreshInterval.
		_ = v.refresh(false)
		key, known = v.lookup(kid)
	} else if ks := v.current.Load(); time.Since(ks.fetchedAt) > v.cfg.MaxKeyAge {
		go func() { _ = v.refresh(false) }()
	}
	if !known {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	if key.alg != "" && key.alg != token.Method.Alg() {
		return nil, fmt.Errorf("signing key %q is for %s, token uses %s", kid, key.alg, token.Method.Alg())
	}
	return key.key, nil
}

func (v *TokenVerifier) isAcceptedIssuer(iss string) bool {
	iss = normalizeIssuer(iss)
	if iss == "" {
		return false
	}
	accepted := slices.Clone(v.cfg.Issuers)
	if ks := v.usableKeys(); ks != nil && ks.issuer != "" {
		accepted = append(accepted, ks.issuer)
	}
	if slices.Contains(accepted, iss) {
		return true
	}
	// Only correctly signed tokens get here, so an unexpected issuer means the
	// accepted issuers don't match Keycloak's hostname settings.
	v.warnMu.Lock()
	defer v.warnMu.Unlock()
	if _, warned := v.warnedBadIssuers[iss]; !warned && len(v.warnedBadIssuers) < 16 {
		v.warnedBadIssuers[iss] = struct{}{}
		slogWarn("Rejected Keycloak-signed token with an unexpected issuer", "issuer", iss, "accepted", accepted)
	}
	return false
}

// refresh fetches the realm keys unless a fetch was attempted within
// MinRefreshInterval (force skips that limit). Concurrent callers share one fetch.
func (v *TokenVerifier) refresh(force bool) error {
	_, err, _ := v.refreshes.Do("keys", func() (any, error) {
		v.attemptMu.Lock()
		if !force && time.Since(v.lastFetchAttempt) < v.cfg.MinRefreshInterval {
			v.attemptMu.Unlock()
			return nil, nil
		}
		v.lastFetchAttempt = time.Now()
		v.attemptMu.Unlock()
		return nil, v.fetch()
	})
	return err
}

// fetch reloads the discovery issuer and the realm keys. On failure the cached
// keys stay in use (until maxStaleKeyAge) so a Keycloak blip doesn't sign
// everyone out.
func (v *TokenVerifier) fetch() error {
	var errs []error
	issuer := ""
	if previous := v.current.Load(); previous != nil {
		issuer = previous.issuer
	}
	if v.cfg.DiscoveryURL != "" {
		var discovery struct {
			Issuer string `json:"issuer"`
		}
		if err := v.getJSON(v.cfg.DiscoveryURL, &discovery); err != nil {
			errs = append(errs, err)
		} else if iss := normalizeIssuer(discovery.Issuer); iss != "" {
			issuer = iss
		}
	}

	var set struct {
		Keys []jsonWebKey `json:"keys"`
	}
	if err := v.getJSON(v.cfg.JWKSURL, &set); err != nil {
		return errors.Join(append(errs, err)...)
	}
	keys := make(map[string]verificationKey, len(set.Keys))
	for _, k := range set.Keys {
		if k.Kid == "" || k.Kty != "RSA" || (k.Use != "" && k.Use != "sig") {
			continue
		}
		if k.Alg != "" && !slices.Contains(v.cfg.Algorithms, k.Alg) {
			continue
		}
		pub, err := k.rsaPublicKey()
		if err != nil {
			errs = append(errs, fmt.Errorf("key %q: %w", k.Kid, err))
			continue
		}
		keys[k.Kid] = verificationKey{alg: k.Alg, key: pub}
	}
	v.current.Store(&keySet{keys: keys, issuer: issuer, fetchedAt: time.Now()})
	return errors.Join(errs...)
}

func (v *TokenVerifier) getJSON(url string, out any) error {
	ctx, cancel := context.WithTimeout(context.Background(), keyFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := v.cfg.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxKeyDocumentBytes)).Decode(out); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

type jsonWebKey struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

func (k jsonWebKey) rsaPublicKey() (*rsa.PublicKey, error) {
	n, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil || len(n) == 0 {
		return nil, errors.New("invalid modulus")
	}
	e, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil || len(e) == 0 || len(e) > 4 {
		return nil, errors.New("invalid exponent")
	}
	exponent := int(new(big.Int).SetBytes(e).Int64())
	if exponent < 3 {
		return nil, errors.New("invalid exponent")
	}
	modulus := new(big.Int).SetBytes(n)
	if modulus.BitLen() < 2048 {
		return nil, errors.New("modulus shorter than 2048 bits")
	}
	return &rsa.PublicKey{N: modulus, E: exponent}, nil
}

func slogWarn(msg string, args ...any) {
	if logger.Log != nil {
		logger.Log.Warn(msg, args...)
	}
}

func normalizeIssuer(iss string) string {
	return strings.TrimRight(strings.TrimSpace(iss), "/")
}

var (
	tokenVerifierMu sync.RWMutex
	tokenVerifier   *TokenVerifier
)

// SetTokenVerifier installs the process-wide verifier and returns a function
// that restores the previous one. Until one is installed every token is rejected.
func SetTokenVerifier(v *TokenVerifier) (restore func()) {
	tokenVerifierMu.Lock()
	defer tokenVerifierMu.Unlock()
	previous := tokenVerifier
	tokenVerifier = v
	return func() {
		tokenVerifierMu.Lock()
		defer tokenVerifierMu.Unlock()
		tokenVerifier = previous
	}
}

// VerifyAccessToken verifies token with the installed verifier. It is the only
// way central-backend reads claims from a caller's token.
func VerifyAccessToken(token string) (*TokenClaims, error) {
	tokenVerifierMu.RLock()
	v := tokenVerifier
	tokenVerifierMu.RUnlock()
	if v == nil {
		return nil, ErrTokenVerifierNotConfigured
	}
	return v.Verify(token)
}
