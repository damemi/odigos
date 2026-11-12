package utils

import (
	"sync"
	"time"
)

// TokenRevocationStore tracks per-user not-before timestamps so access JWTs
// issued before a privilege revoke are rejected immediately (authz is offline).
type TokenRevocationStore struct {
	mu    sync.RWMutex
	times map[string]int64 // userID -> unix not-before (seconds)
}

// GlobalTokenRevocations is the process-wide store used by auth checks.
var GlobalTokenRevocations = NewTokenRevocationStore()

func NewTokenRevocationStore() *TokenRevocationStore {
	return &TokenRevocationStore{times: make(map[string]int64)}
}

// Revoke marks all tokens for userID with iat < notBefore as revoked.
// If notBefore is 0, time.Now().Unix() is used.
func (s *TokenRevocationStore) Revoke(userID string, notBefore int64) {
	if userID == "" {
		return
	}
	if notBefore <= 0 {
		notBefore = time.Now().Unix()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.times[userID]; ok && existing >= notBefore {
		return
	}
	s.times[userID] = notBefore
}

// NotBefore returns the stored not-before for userID, or 0 if none.
func (s *TokenRevocationStore) NotBefore(userID string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.times[userID]
}

// IsRevoked reports whether a token issued at iat (unix seconds) for userID
// is before the user's not-before.
func (s *TokenRevocationStore) IsRevoked(userID string, iat int64) bool {
	nb := s.NotBefore(userID)
	if nb == 0 {
		return false
	}
	return iat < nb
}

// Clear removes revocation state for userID (tests).
func (s *TokenRevocationStore) Clear(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.times, userID)
}

// RevokeUserTokens marks access tokens issued before notBefore as revoked.
func RevokeUserTokens(userID string, notBefore int64) {
	GlobalTokenRevocations.Revoke(userID, notBefore)
}
