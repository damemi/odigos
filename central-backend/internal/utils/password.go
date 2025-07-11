package utils

import (
	"crypto/rand"
	"encoding/base64"
)

// GenerateRandomPassword generates a random password of the specified length
func GenerateRandomPassword(length int) string {
	// Calculate how many bytes we need to generate to get the desired length after base64 encoding
	// base64 encoding increases size by 4/3, so we need to generate 3/4 of the desired length
	bytes := make([]byte, (length*3)/4)
	rand.Read(bytes)
	str := base64.URLEncoding.EncodeToString(bytes)
	// Trim to exact length and ensure it contains at least one special character
	return str[:length] + "!@"
}
