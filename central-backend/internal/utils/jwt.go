package utils

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func decodeJWTClaims(token string) (map[string]any, error) {
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
	return claims, nil
}

// ExtractSubjectFromToken returns the JWT "sub" claim (Keycloak user id).
func ExtractSubjectFromToken(token string) (string, error) {
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return "", err
	}
	sub, ok := claims["sub"].(string)
	if !ok || sub == "" {
		return "", fmt.Errorf("sub not found in token")
	}
	return sub, nil
}

// ExtractIssuedAtFromToken returns the JWT "iat" claim as unix seconds.
func ExtractIssuedAtFromToken(token string) (int64, error) {
	claims, err := decodeJWTClaims(token)
	if err != nil {
		return 0, err
	}
	iat, ok := claims["iat"].(float64)
	if !ok {
		return 0, fmt.Errorf("iat not found in token")
	}
	return int64(iat), nil
}
