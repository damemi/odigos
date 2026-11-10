package odigosauth

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestVerifyOnpremTokenWithExpiry(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	old := testPublicKeyString
	testPublicKeyString = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	t.Cleanup(func() { testPublicKeyString = old })
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		name   string
		mutate func(jwt.MapClaims)
		method jwt.SigningMethod
		key    any
		valid  bool
	}{
		{name: "generated format", valid: true},
		{
			name: "any customer and profiles",
			mutate: func(c jwt.MapClaims) {
				c["aud"] = "another-customer"
				c["profiles"] = []string{"kratos"}
			},
			valid: true,
		},
		{name: "expired", mutate: func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Second).Unix() }},
		{name: "missing expiration", mutate: func(c jwt.MapClaims) { delete(c, "exp") }},
		{name: "malformed expiration", mutate: func(c jwt.MapClaims) { c["exp"] = "tomorrow" }},
		{name: "wrong issuer", mutate: func(c jwt.MapClaims) { c["iss"] = "attacker" }},
		{name: "wrong subject", mutate: func(c jwt.MapClaims) { c["sub"] = "other-product" }},
		{name: "empty audience", mutate: func(c jwt.MapClaims) { c["aud"] = "" }},
		{name: "missing audience", mutate: func(c jwt.MapClaims) { delete(c, "aud") }},
		{name: "not active yet", mutate: func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(time.Hour).Unix() }},
		{name: "wrong signature", key: other},
		{name: "wrong RSA algorithm", method: jwt.SigningMethodRS384},
		{name: "HMAC confusion", method: jwt.SigningMethodHS256, key: []byte(testPublicKeyString)},
		{name: "unsigned", method: jwt.SigningMethodNone, key: jwt.UnsafeAllowNoneSignatureType},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claims := jwt.MapClaims{
				"iss": "https://odigos.io",
				"sub": "https://odigos.io/onprem",
				"aud": "customer",
				"exp": expiry.Unix(),
				"iat": time.Now().Unix(),
			}
			if tc.mutate != nil {
				tc.mutate(claims)
			}
			method := tc.method
			if method == nil {
				method = jwt.SigningMethodRS256
			}
			signingKey := tc.key
			if signingKey == nil {
				signingKey = key
			}
			token, err := jwt.NewWithClaims(method, claims).SignedString(signingKey)
			if err != nil {
				t.Fatal(err)
			}
			got, err := VerifyOnpremTokenWithExpiry(" \n" + token + "\n")
			if tc.valid {
				if err != nil || !got.Equal(expiry) {
					t.Fatalf("%v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid token")
			}
		})
	}
	if _, err := VerifyOnpremTokenWithExpiry("not.a.jwt"); err == nil {
		t.Fatal("accepted malformed JWT")
	}
}
