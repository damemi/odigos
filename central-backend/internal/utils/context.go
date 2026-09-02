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
	tokenKey    contextKey = "token"
	liveRoleKey contextKey = "liveRole"
)

// ErrForbidden is returned when a caller lacks the required role for an action.
var ErrForbidden = errors.New("forbidden: you do not have permission to perform this action")

// ErrUnauthenticated is returned when a caller presents no valid credentials.
var ErrUnauthenticated = errors.New("unauthenticated: a valid session is required")

func GetTokenFromContext(ctx context.Context) string {
	if token, ok := ctx.Value(tokenKey).(string); ok {
		return token
	}
	return ""
}

func SetTokenInContext(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, tokenKey, token)
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

func ExtractRolesFromToken(token string) ([]string, error) {
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return nil, err
	}

	if realmAccess, ok := claims["realm_access"].(map[string]any); ok {
		if roles, ok := realmAccess["roles"].([]any); ok {
			var result []string
			for _, role := range roles {
				if roleStr, ok := role.(string); ok {
					result = append(result, roleStr)
				}
			}
			return result, nil
		}
	}

	return []string{}, nil
}

func ExtractEmailFromToken(token string) (string, error) {
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return "", err
	}

	if email, ok := claims["email"].(string); ok {
		return email, nil
	}

	return "", fmt.Errorf("email not found in token")
}

func IsTokenExpired(token string) (bool, error) {
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return true, err
	}

	exp, ok := claims["exp"].(float64)
	if !ok {
		return true, fmt.Errorf("exp not found in token")
	}

	return time.Now().Unix() >= int64(exp), nil
}

// IsTokenRevoked reports whether the access token was issued before a privilege
// revoke (not-before). Used for immediate invalidation after role downgrades.
func IsTokenRevoked(token string) bool {
	sub, err := ExtractSubjectFromToken(token)
	if err != nil {
		return false
	}
	iat, err := ExtractIssuedAtFromToken(token)
	if err != nil {
		return false
	}
	return GlobalTokenRevocations.IsRevoked(sub, iat)
}

func rolesForAuthz(ctx context.Context) []string {
	token := GetTokenFromContext(ctx)
	if token != "" && IsTokenRevoked(token) {
		return nil
	}
	if liveRole, ok := GetLiveRoleFromContext(ctx); ok {
		return []string{liveRole}
	}
	if token == "" {
		return nil
	}
	roles, err := ExtractRolesFromToken(token)
	if err != nil {
		return nil
	}
	return roles
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

// RequireAdmin returns ErrForbidden unless the caller carries an ADMIN or OWNER
// role. Use this to guard access-management mutations (user CRUD,
// password resets, identity providers) so they cannot be reached by lower-privilege
// roles such as MAINTAINER or VIEWER, even via a direct API call.
func RequireAdmin(ctx context.Context) error {
	if !IsUserAdmin(ctx) {
		return ErrForbidden
	}
	return nil
}

// RequireAuth returns ErrUnauthenticated unless the caller presents a non-expired,
// non-revoked JWT that includes at least one Odigos role. Anonymous callers and
// authenticated users with no mapped role are both rejected.
func RequireAuth(ctx context.Context) error {
	token := GetTokenFromContext(ctx)
	if token == "" {
		return ErrUnauthenticated
	}
	if expired, err := IsTokenExpired(token); err != nil || expired {
		return ErrUnauthenticated
	}
	if IsTokenRevoked(token) {
		return ErrUnauthenticated
	}
	if !HasOdigosRole(ctx) {
		return ErrUnauthenticated
	}
	return nil
}
