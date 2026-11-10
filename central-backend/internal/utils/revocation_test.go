package utils

import (
	"testing"
	"time"
)

func TestTokenRevocationStore(t *testing.T) {
	store := NewTokenRevocationStore()
	const userID = "user-1"

	if store.IsRevoked(userID, time.Now().Unix()) {
		t.Fatal("expected no revocation before Revoke")
	}

	notBefore := time.Now().Unix()
	store.Revoke(userID, notBefore)

	if !store.IsRevoked(userID, notBefore-1) {
		t.Fatal("expected token issued before not-before to be revoked")
	}
	if store.IsRevoked(userID, notBefore) {
		t.Fatal("expected token issued at not-before to remain valid")
	}
	if store.IsRevoked(userID, notBefore+10) {
		t.Fatal("expected token issued after not-before to remain valid")
	}
}

func TestTokenRevocationStoreIgnoresEmptyUser(t *testing.T) {
	store := NewTokenRevocationStore()
	store.Revoke("", time.Now().Unix())
	if store.NotBefore("") != 0 {
		t.Fatal("empty userID must not store a not-before")
	}
}

func TestTokenRevocationStoreDoesNotLowerNotBefore(t *testing.T) {
	store := NewTokenRevocationStore()
	store.Revoke("user-1", 200)
	store.Revoke("user-1", 100)
	if got := store.NotBefore("user-1"); got != 200 {
		t.Fatalf("NotBefore() = %d, want 200 (must keep the later revoke)", got)
	}
}
