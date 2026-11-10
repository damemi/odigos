package odigosauth

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	commonlogger "github.com/odigos-io/odigos/common/logger"
)

// RsaPublicKeyString is the RSA public key used to verify Odigos enterprise tokens
const RsaPublicKeyString = `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAqjGWuv6zbpSmSA5Ygaw5
7ZLIO7U9bNjyWYzdVyToeayRT7sfKY313W0vbRgZySxEOwyeiHYrF/vvKn2MQx4e
V9d7oNDnL8otNzNSp7S6BsXxZCALlk8YtVPQOV2a7uzrtNK4AS/EB6NuqZHJwvH9
DmqEr0dkIbvoBjtWUo70ez6lBE1sQEShoORb11CVnGQwDBaeIXTL6+ajCFzyea0D
l9NYI4XJhhdZGedb44mhNNiigJ09z+5KHemhwUHwrLvnZKrAKkUtZlNvu8JR9eU8
daEeTbWsOQsnXuxSqxTlEaRddXbDNZjRyP7KtlMIrTII+MYEoigxHkO2nZmCBYhP
wQIDAQAB
-----END PUBLIC KEY-----`

// testPublicKeyString is used for testing purposes to override the default public key
var testPublicKeyString string

func parseToken(tokenString string, options ...jwt.ParserOption) (*jwt.Token, error) {
	// Use test public key if set, otherwise use the default
	publicKeyStr := RsaPublicKeyString
	if testPublicKeyString != "" {
		publicKeyStr = testPublicKeyString
	}

	// Parse the RSA public key
	publicKey, err := jwt.ParseRSAPublicKeyFromPEM([]byte(publicKeyStr))
	if err != nil {
		return nil, err
	}

	// Parse the token
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		return publicKey, nil
	}, options...)
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			expTime, expTimeErr := token.Claims.GetExpirationTime()
			if expTimeErr != nil {
				return nil, fmt.Errorf("failed to get expiration time from token: %w", errors.Join(err, expTimeErr))
			}

			now := time.Now()
			expirationDuration := now.Sub(expTime.Time)
			roundedDuration := expirationDuration.Round(time.Minute)
			return nil, fmt.Errorf("token is expired for %v, contact Odigos support to issue a new one", roundedDuration)
		}

		return nil, err
	}

	return token, nil
}

func VerifyOnpremToken(tokenString string) (string, error) {
	token, err := parseToken(tokenString)
	if err != nil {
		return "", err
	}
	return verifyOnpremClaims(token)
}

// VerifyOnpremTokenWithExpiry verifies a generated enterprise key and returns the
// latest time at which a dependent session may remain valid.
func VerifyOnpremTokenWithExpiry(tokenString string) (time.Time, error) {
	token, err := parseToken(strings.TrimSpace(tokenString),
		jwt.WithValidMethods([]string{jwt.SigningMethodRS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return time.Time{}, err
	}
	if _, err := verifyOnpremClaims(token); err != nil {
		return time.Time{}, err
	}
	expiry, err := token.Claims.GetExpirationTime()
	if err != nil || expiry == nil {
		return time.Time{}, errors.New("missing or invalid exp claim")
	}
	return expiry.Time, nil
}

func verifyOnpremClaims(token *jwt.Token) (string, error) {

	// Check if the token is valid
	claims, claimsok := token.Claims.(jwt.MapClaims)
	if !claimsok {
		return "", fmt.Errorf("invalid claims")
	}

	if !token.Valid {
		return "", fmt.Errorf("invalid token")
	}

	iss, ok := claims["iss"].(string)
	if !ok || iss != "https://odigos.io" {
		return "", fmt.Errorf("invalid iss")
	}

	sub, ok := claims["sub"].(string)
	if !ok || sub != "https://odigos.io/onprem" {
		return "", fmt.Errorf("invalid sub")
	}

	aud, ok := claims["aud"].(string)
	if !ok || aud == "" {
		return "", fmt.Errorf("missing aud claim")
	}

	return aud, nil
}

func verifyCloudApiKey(cloudApiKey string) error {
	url := fmt.Sprintf("https://compute-platform.keyval.dev/cloudtoken/%s/verify", cloudApiKey)

	// Make the GET request
	response, err := http.Get(url)
	if err != nil {
		// Handle error from the GET request
		return err
	}
	defer response.Body.Close()

	// Check if the status code is 200 OK
	if response.StatusCode == http.StatusOK {
		return nil
	} else {
		// Return an error for any status code other than 200
		return errors.New("invalid cloud api key or error in verification")
	}
}

// TokenExpirationChannel returns a channel that will receive a time.Time value when the token expires
// or an error if the token is not present or invalid.
// The caller can use this channel to trigger actions when the token expires.
func TokenExpirationChannel() (<-chan time.Time, error) {
	onpremToken := os.Getenv("ODIGOS_ONPREM_TOKEN")
	if onpremToken == "" {
		return nil, errors.New("no token is present in env")
	}

	// Trim any whitespace that might have been added
	trimmedOnpremToken := strings.TrimSpace(onpremToken)

	token, err := parseToken(trimmedOnpremToken)
	if err != nil {
		return nil, fmt.Errorf("failed to parse token: %w", err)
	}

	expTime, err := token.Claims.GetExpirationTime()
	if err != nil {
		return nil, fmt.Errorf("failed to get expiration time from token: %w", err)
	}

	now := time.Now()

	if expTime.Before(now) {
		expirationDuration := now.Sub(expTime.Time)
		roundedDuration := expirationDuration.Round(time.Minute)
		return nil, fmt.Errorf("token is expired for %v, contact Odigos support to issue a new one", roundedDuration)
	}

	durationUntilExpiration := time.Until(expTime.Time)
	return time.After(durationUntilExpiration), nil
}

// verify that this pro component is authorized to run.
// this is done by examining environment variables and os.Exit if not authorized
func AssertEnterpriseOdigosToken(logger *commonlogger.OdigosLogger) {
	onpremToken := os.Getenv("ODIGOS_ONPREM_TOKEN")
	if onpremToken != "" {

		// Trim any whitespace that might have been added
		trimmedOnpremToken := strings.TrimSpace(onpremToken)

		aud, err := VerifyOnpremToken(trimmedOnpremToken)
		if err != nil {
			logger.Error("Failed to verify onprem token", "error", err)
			os.Exit(1)
		}

		logger.Info("Odigos onprem token verified", "audience", aud)
		return
	}

	cloudToken := os.Getenv("ODIGOS_CLOUD_TOKEN")
	if cloudToken != "" {

		err := verifyCloudApiKey(cloudToken)
		if err != nil {
			logger.Error("Failed to verify cloud token", "error", err)
			os.Exit(-1)
		}

		logger.Info("Odigos cloud token verified")
		return
	}

	logger.Error("did not find odigos pro token in environment variables", "error", fmt.Errorf("missing Odigos Pro token"))
	os.Exit(-1)
}
