package collectorgateway

import (
	"strings"
	"testing"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

func TestMetricsAuthorizationScope(t *testing.T) {
	id := Identity{Binding: collectorcredential.Binding{CollectorID: "collector-1"}, CredentialID: "credential-1"}
	hash := strings.Repeat("a", 64)
	auth := MetricsAuthorization(id, hash)
	collector, credential, ok := ParseMetricsAuthorization(auth)
	if !ok || collector != "collector-1" || credential != "credential-1" || !VerifyMetricsAuthorization(auth, id, hash) {
		t.Fatal("derived credential did not verify")
	}
	other := id
	other.CredentialID = "credential-2"
	for _, tc := range []struct {
		name, auth, hash string
		identity         Identity
	}{
		{"wrong hash", auth, strings.Repeat("b", 64), id},
		{"wrong credential", auth, hash, other},
		{"changed identity", strings.Replace(auth, "collector-1", "collector-2", 1), hash, id},
		{"malformed", "Bearer om1.collector-1.credential-1.bad", hash, id},
		{"missing", "", hash, id},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if VerifyMetricsAuthorization(tc.auth, tc.identity, tc.hash) {
				t.Fatal("invalid metrics credential accepted")
			}
		})
	}
	for _, hash := range []string{"", "invalid", collectorcredential.EmptyDigest} {
		if MetricsAuthorization(id, hash) != "" {
			t.Error("derived credential from invalid key")
		}
	}
}
