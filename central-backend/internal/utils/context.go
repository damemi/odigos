package utils

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type contextKey string

const (
	claimsKey   contextKey = "verifiedClaims"
	liveRoleKey contextKey = "liveRole"
)

// ErrForbidden is returned when a caller lacks the required role for an action.
var ErrForbidden = errors.New("forbidden: you do not have permission to perform this action")

// ErrUnauthenticated is returned when a caller presents no valid credentials.
var ErrUnauthenticated = errors.New("unauthenticated: a valid session is required")

// AuthenticateToken verifies token and, if it is a valid access token, returns
// ctx carrying its claims. On error ctx is returned unchanged, so the caller
// stays anonymous. Any request path that reads a caller's token (header, query
// parameter, cookie) must come through here or SetTokenInContext.
func AuthenticateToken(ctx context.Context, token string) (context.Context, error) {
	claims, err := VerifyAccessToken(token)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, claimsKey, claims), nil
}

// SetTokenInContext is AuthenticateToken for callers that don't need the reason
// a token was rejected.
func SetTokenInContext(ctx context.Context, token string) context.Context {
	ctx, _ = AuthenticateToken(ctx, token)
	return ctx
}

func verifiedClaimsFromContext(ctx context.Context) *TokenClaims {
	claims, _ := ctx.Value(claimsKey).(*TokenClaims)
	return claims
}

// GetSubjectFromContext returns the verified caller's Keycloak user id.
func GetSubjectFromContext(ctx context.Context) (string, bool) {
	claims := verifiedClaimsFromContext(ctx)
	if claims == nil || claims.Subject == "" {
		return "", false
	}
	return claims.Subject, true
}

// SetLiveRoleInContext stores the user's current Keycloak realm role for this
// request so privilege checks do not rely solely on (possibly stale) JWT claims.
func SetLiveRoleInContext(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, liveRoleKey, role)
}

// GetLiveRoleFromContext returns the per-request live role when middleware set one.
func GetLiveRoleFromContext(ctx context.Context) (string, bool) {
	role, ok := ctx.Value(liveRoleKey).(string)
	return role, ok && role != ""
}

// ExtractRolesFromToken verifies token and returns its realm roles.
func ExtractRolesFromToken(token string) ([]string, error) {
	claims, err := VerifyAccessToken(token)
	if err != nil {
		return nil, err
	}
	return claims.RealmAccess.Roles, nil
}

// ExtractEmailFromToken verifies token and returns its email claim.
func ExtractEmailFromToken(token string) (string, error) {
	claims, err := VerifyAccessToken(token)
	if err != nil {
		return "", err
	}
	if claims.Email == "" {
		return "", fmt.Errorf("email not found in token")
	}
	return claims.Email, nil
}

func isExpired(claims *TokenClaims) bool {
	return claims.ExpiresAt == nil || !time.Now().Before(claims.ExpiresAt.Time)
}

// isRevoked reports whether the access token was issued before a privilege
// revoke (not-before). Used for immediate invalidation after role downgrades.
// Verify rejects tokens without iat; one without it is treated as revoked anyway.
func isRevoked(claims *TokenClaims) bool {
	if claims.IssuedAt == nil {
		return true
	}
	return GlobalTokenRevocations.IsRevoked(claims.Subject, claims.IssuedAt.Unix())
}

func rolesForAuthz(ctx context.Context) []string {
	claims := verifiedClaimsFromContext(ctx)
	if claims == nil || isRevoked(claims) {
		return nil
	}
	if liveRole, ok := GetLiveRoleFromContext(ctx); ok {
		return []string{liveRole}
	}
	return claims.RealmAccess.Roles
}

func IsUserAdmin(ctx context.Context) bool {
	for _, role := range rolesForAuthz(ctx) {
		if strings.EqualFold(role, "ADMIN") || strings.EqualFold(role, "OWNER") {
			return true
		}
	}
	return false
}

// IsUserEditor reports whether the requesting user may edit product configuration
// (sources, destinations, actions, etc.). This includes ADMIN, OWNER and MAINTAINER.
// User management and other admin-only operations should keep using IsUserAdmin.
func IsUserEditor(ctx context.Context) bool {
	for _, role := range rolesForAuthz(ctx) {
		if strings.EqualFold(role, "ADMIN") || strings.EqualFold(role, "OWNER") || strings.EqualFold(role, "MAINTAINER") {
			return true
		}
	}
	return false
}

// HasOdigosRole reports whether the caller has at least one Odigos realm role
// (owner, admin, maintainer, or viewer), preferring the live role when present.
func HasOdigosRole(ctx context.Context) bool {
	for _, role := range rolesForAuthz(ctx) {
		if strings.EqualFold(role, "OWNER") ||
			strings.EqualFold(role, "ADMIN") ||
			strings.EqualFold(role, "MAINTAINER") ||
			strings.EqualFold(role, "VIEWER") {
			return true
		}
	}
	return false
}

// RequireAdmin returns ErrUnauthenticated for anonymous, expired or revoked
// sessions, and ErrForbidden unless the caller carries an ADMIN or OWNER role.
// Use this to guard access-management mutations (user CRUD, password resets,
// identity providers) so they cannot be reached by lower-privilege roles such as
// MAINTAINER or VIEWER, even via a direct API call.
func RequireAdmin(ctx context.Context) error {
	if err := RequireAuth(ctx); err != nil {
		return err
	}
	if !IsUserAdmin(ctx) {
		return ErrForbidden
	}
	return nil
}

// RequireAuth returns ErrUnauthenticated unless the caller presents a verified,
// non-expired, non-revoked access token that includes at least one Odigos role.
// Anonymous callers and authenticated users with no mapped role are both rejected.
func RequireAuth(ctx context.Context) error {
	claims := verifiedClaimsFromContext(ctx)
	if claims == nil || isExpired(claims) || isRevoked(claims) {
		return ErrUnauthenticated
	}
	if !HasOdigosRole(ctx) {
		return ErrUnauthenticated
	}
	return nil
}
