package utils

import (
	"testing"
	"time"
)

func TestLiveRoleCacheRoundTrip(t *testing.T) {
	const userID = "cache-user-roundtrip"
	t.Cleanup(func() { InvalidateAuthCaches(userID) })

	if _, ok := GetCachedLiveRole(userID); ok {
		t.Fatal("expected empty cache")
	}

	SetCachedLiveRole(userID, "maintainer")
	got, ok := GetCachedLiveRole(userID)
	if !ok || got != "maintainer" {
		t.Fatalf("GetCachedLiveRole() = %q ok=%v, want maintainer", got, ok)
	}
}

func TestLiveRoleCacheExpiredEntryMisses(t *testing.T) {
	const userID = "cache-user-expired"
	t.Cleanup(func() { InvalidateAuthCaches(userID) })

	liveRoleCacheMu.Lock()
	liveRoleCache[userID] = liveRoleCacheEntry{role: "admin", expiresAt: time.Now().Add(-time.Second)}
	liveRoleCacheMu.Unlock()

	if role, ok := GetCachedLiveRole(userID); ok {
		t.Fatalf("expired entry should miss, got %q", role)
	}
}

func TestInvalidateAuthCachesClearsRoleAndNotBeforeSync(t *testing.T) {
	const userID = "cache-user-invalidate"
	t.Cleanup(func() { InvalidateAuthCaches(userID) })

	SetCachedLiveRole(userID, "viewer")
	MarkNotBeforeSynced(userID)
	if !HasSyncedNotBefore(userID) {
		t.Fatal("expected not-before sync mark")
	}

	InvalidateAuthCaches(userID)

	if _, ok := GetCachedLiveRole(userID); ok {
		t.Fatal("live role cache should be cleared after revoke")
	}
	if HasSyncedNotBefore(userID) {
		t.Fatal("not-before sync mark should be cleared after revoke")
	}
}

func TestNotBeforeSyncedIsPerUser(t *testing.T) {
	const a, b = "cache-user-a", "cache-user-b"
	t.Cleanup(func() {
		InvalidateAuthCaches(a)
		InvalidateAuthCaches(b)
	})

	MarkNotBeforeSynced(a)
	if !HasSyncedNotBefore(a) {
		t.Fatal("user A should be marked")
	}
	if HasSyncedNotBefore(b) {
		t.Fatal("user B should not inherit A's sync mark")
	}
}
