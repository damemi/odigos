package utils

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type contextKey string

const (
	tokenKey contextKey = "token"
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

func ExtractRolesFromToken(token string) ([]string, error) {

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT token format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT payload: %w", err)
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

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid JWT token format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("failed to parse JWT payload: %w", err)
	}

	if email, ok := claims["email"].(string); ok {
		return email, nil
	}

	return "", fmt.Errorf("email not found in token")
}

func IsTokenExpired(token string) (bool, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return true, fmt.Errorf("invalid JWT token format")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return true, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return true, fmt.Errorf("failed to parse JWT payload: %w", err)
	}

	exp, ok := claims["exp"].(float64)
	if !ok {
		return true, fmt.Errorf("exp not found in token")
	}

	return time.Now().Unix() >= int64(exp), nil
}

func IsUserAdmin(ctx context.Context) bool {
	isAdmin := false
	if token := GetTokenFromContext(ctx); token != "" {
		if roles, err := ExtractRolesFromToken(token); err == nil {
			for _, role := range roles {
				if strings.EqualFold(role, "ADMIN") || strings.EqualFold(role, "OWNER") {
					isAdmin = true
					break
				}
			}
		}
	}
	return isAdmin
}

// IsUserEditor reports whether the requesting user may edit product configuration
// (sources, destinations, actions, etc.). This includes ADMIN, OWNER and MAINTAINER.
// User management and other admin-only operations should keep using IsUserAdmin.
func IsUserEditor(ctx context.Context) bool {
	isEditor := false
	if token := GetTokenFromContext(ctx); token != "" {
		if roles, err := ExtractRolesFromToken(token); err == nil {
			for _, role := range roles {
				if strings.EqualFold(role, "ADMIN") || strings.EqualFold(role, "OWNER") || strings.EqualFold(role, "MAINTAINER") {
					isEditor = true
					break
				}
			}
		}
	}
	return isEditor
}

// RequireAdmin returns ErrForbidden unless the caller carries an ADMIN or OWNER
// role in their JWT. Use this to guard access-management mutations (user CRUD,
// password resets, identity providers) so they cannot be reached by lower-privilege
// roles such as MAINTAINER or VIEWER, even via a direct API call.
func RequireAdmin(ctx context.Context) error {
	if !IsUserAdmin(ctx) {
		return ErrForbidden
	}
	return nil
}

// RequireAuth returns ErrUnauthenticated unless the caller presents a non-expired
// JWT in context. Use it to guard read endpoints that should be visible to any
// signed-in user (any role) but not to anonymous callers.
func RequireAuth(ctx context.Context) error {
	token := GetTokenFromContext(ctx)
	if token == "" {
		return ErrUnauthenticated
	}
	if expired, err := IsTokenExpired(token); err != nil || expired {
		return ErrUnauthenticated
	}
	return nil
}
