package collectorcredential

import (
	"testing"
	"time"
)

func TestSupersededCanRevokeOnlyAfterReplacementConnectionAndGrace(t *testing.T) {
	now := time.Now()
	old := Record{CredentialID: "old", State: StateSuperseded, SupersededBy: "new"}
	newRecord := Record{CredentialID: "new", State: StateActive}
	if SupersededCanRevoke(old, newRecord, now, 5*time.Minute) {
		t.Fatal("replacement has not connected")
	}
	connected := now.Add(-4 * time.Minute)
	newRecord.ActivatedAt = &connected
	if SupersededCanRevoke(old, newRecord, now, 5*time.Minute) {
		t.Fatal("grace period has not elapsed")
	}
	connected = now.Add(-6 * time.Minute)
	if !SupersededCanRevoke(old, newRecord, now, 5*time.Minute) {
		t.Fatal("connected replacement should retire the old credential")
	}
	newRecord.State = StateRevoked
	if SupersededCanRevoke(old, newRecord, now, 5*time.Minute) {
		t.Fatal("revoked replacement cannot retire the surviving old credential")
	}
}
