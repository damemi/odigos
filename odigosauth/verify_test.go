package odigosauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The token generator still lives in odigos-enterprise; tests that need it skip until it moves here.
var errTokenGeneratorNotFound = errors.New("token generator not found")

func generateKeyPair() (*rsa.PrivateKey, string, error) {
	// Generate a new RSA private key
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate private key: %w", err)
	}

	// Encode the private key to PEM format
	privateKeyDER := x509.MarshalPKCS1PrivateKey(privateKey)
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: privateKeyDER,
	})

	return privateKey, string(privateKeyPEM), nil
}

func generateToken(privateKeyPEM string, expiryDuration time.Duration, audience string) (string, error) {
	// Get the path to the token generator
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %w", err)
	}

	// Assuming we're in odigosauth directory, go up to root and then to onprem-token-generator
	workspaceRoot := filepath.Join(cwd, "..")
	tokenGeneratorDir := filepath.Join(workspaceRoot, "onprem-token-generator")
	mainPath := filepath.Join(tokenGeneratorDir, "main.go")
	tokenGeneratorDir, err = filepath.Abs(tokenGeneratorDir)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	mainPath, err = filepath.Abs(mainPath)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute path: %w", err)
	}
	if _, err := os.Stat(mainPath); os.IsNotExist(err) {
		return "", fmt.Errorf("%w at %s", errTokenGeneratorNotFound, mainPath)
	}

	// Set up environment variables for the token generator
	env := os.Environ()
	env = append(env, fmt.Sprintf("PRIVATE_KEY=%s", privateKeyPEM))
	env = append(env, fmt.Sprintf("TOKEN_EXPIRY_DURATION=%s", expiryDuration.String()))
	env = append(env, fmt.Sprintf("JWT_AUDIENCE=%s", audience))

	// Run the token generator from its module directory so its go.mod/go.sum are used
	cmd := exec.Command("go", "run", "main.go")
	cmd.Dir = tokenGeneratorDir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to run token generator: %w, output: %s", err, string(output))
	}

	// Extract the token from the output
	outputStr := string(output)
	lines := strings.Split(outputStr, "\n")
	for _, line := range lines {
		// Look for "Token: " anywhere in the line (to handle timestamp prefix)
		if idx := strings.Index(line, "Token: "); idx != -1 {
			token := strings.TrimSpace(line[idx+len("Token: "):])
			if token != "" {
				return token, nil
			}
		}
	}

	return "", fmt.Errorf("token not found in output: %s", outputStr)
}

func TestTokenExpirationChannel(t *testing.T) {
	// Generate key pair
	privateKey, privateKeyPEM, err := generateKeyPair()
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}

	// Extract public key in PEM format for testing
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal public key: %v", err)
	}
	publicKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyDER,
	}))

	// Set the test public key
	originalTestKey := testPublicKeyString
	testPublicKeyString = publicKeyPEM
	defer func() {
		testPublicKeyString = originalTestKey
	}()

	// Generate token with 5 second expiration
	expiryDuration := 5 * time.Second
	audience := "test-audience"
	token, err := generateToken(privateKeyPEM, expiryDuration, audience)
	if errors.Is(err, errTokenGeneratorNotFound) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatalf("Failed to generate token: %v", err)
	}

	// Set the token in environment
	t.Setenv("ODIGOS_ONPREM_TOKEN", token)

	// Check the generated token through the docs validator too.
	expectedExpiry, err := VerifyOnpremTokenWithExpiry(token)
	if err != nil {
		t.Fatal(err)
	}
	expChan, err := TokenExpirationChannel()
	if err != nil {
		t.Fatalf("TokenExpirationChannel() returned error: %v", err)
	}

	// Wait for the channel to receive the expiration time
	// Use a generous timeout to avoid flakiness (5s + 3s buffer)
	select {
	case expTime := <-expChan:
		// JWT exp is rounded to seconds before the generator subprocess exits.
		// Compare against that timestamp instead of the original five-second duration.
		if expTime.Before(expectedExpiry) || expTime.After(expectedExpiry.Add(2*time.Second)) {
			t.Errorf("expiration fired at %v, expected %v", expTime, expectedExpiry)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Channel did not receive expiration time within 10 seconds")
	}
}

func TestTokenExpirationChannel_NoToken(t *testing.T) {
	// Ensure no token is set (empty string is treated as no token)
	t.Setenv("ODIGOS_ONPREM_TOKEN", "")

	_, err := TokenExpirationChannel()
	if err == nil {
		t.Error("Expected error when no token is present, got nil")
	}
	if err.Error() != "no token is present in env" {
		t.Errorf("Expected error message 'no token is present in env', got: %v", err)
	}
}

func TestTokenExpirationChannel_ExpiredToken(t *testing.T) {
	// Generate key pair
	privateKey, privateKeyPEM, err := generateKeyPair()
	if err != nil {
		t.Fatalf("Failed to generate key pair: %v", err)
	}

	// Extract public key for testing
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("Failed to marshal public key: %v", err)
	}
	publicKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyDER,
	}))

	// Set the test public key
	originalTestKey := testPublicKeyString
	testPublicKeyString = publicKeyPEM
	defer func() {
		testPublicKeyString = originalTestKey
	}()

	// Generate an expired token (expired 1 hour ago)
	expiredDuration := -1 * time.Hour
	token, err := generateToken(privateKeyPEM, expiredDuration, "test-audience")
	if errors.Is(err, errTokenGeneratorNotFound) {
		t.Skip(err)
	}
	if err != nil {
		t.Fatalf("Failed to generate expired token: %v", err)
	}

	// Set the token in environment
	t.Setenv("ODIGOS_ONPREM_TOKEN", token)

	// Try to get the expiration channel
	_, err = TokenExpirationChannel()
	if err == nil {
		t.Error("Expected error for expired token, got nil")
	}
	if !strings.Contains(err.Error(), "token is expired") {
		t.Errorf("Expected error message 'token is expired', got: %v", err)
	}
}
