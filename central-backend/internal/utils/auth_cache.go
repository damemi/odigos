package utils

import (
	"sync"
	"time"
)

type liveRoleCacheEntry struct {
	role      string
	expiresAt time.Time
}

var (
	liveRoleCacheMu sync.RWMutex
	liveRoleCache   = map[string]liveRoleCacheEntry{}

	// notBeforeSynced tracks users whose Keycloak not-before was already loaded
	// (including a confirmed zero), avoiding repeated admin password grants.
	notBeforeSynced sync.Map
)

const LiveRoleCacheTTL = 15 * time.Second

// GetCachedLiveRole returns a non-expired cached live role for userID.
func GetCachedLiveRole(userID string) (string, bool) {
	liveRoleCacheMu.RLock()
	defer liveRoleCacheMu.RUnlock()
	entry, ok := liveRoleCache[userID]
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.role, true
}

// SetCachedLiveRole stores the live Keycloak role for userID with LiveRoleCacheTTL.
func SetCachedLiveRole(userID, role string) {
	liveRoleCacheMu.Lock()
	defer liveRoleCacheMu.Unlock()
	liveRoleCache[userID] = liveRoleCacheEntry{role: role, expiresAt: time.Now().Add(LiveRoleCacheTTL)}
}

// HasSyncedNotBefore reports whether Keycloak not-before was already loaded for userID.
func HasSyncedNotBefore(userID string) bool {
	_, ok := notBeforeSynced.Load(userID)
	return ok
}

// MarkNotBeforeSynced records that Keycloak not-before was checked for userID.
func MarkNotBeforeSynced(userID string) {
	notBeforeSynced.Store(userID, struct{}{})
}

// InvalidateAuthCaches clears per-user auth caches after a privilege revoke.
func InvalidateAuthCaches(userID string) {
	notBeforeSynced.Delete(userID)
	liveRoleCacheMu.Lock()
	delete(liveRoleCache, userID)
	liveRoleCacheMu.Unlock()
}
