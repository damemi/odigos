package utils

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type contextKey string

const (
	tokenKey contextKey = "token"
)

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
