package collectorgateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

// HeaderMetricsAuthorization carries a metrics-only credential from the gateway
// to the connector, which offers it to the supervisor over OpAMP.
const HeaderMetricsAuthorization = "X-Odigos-Metrics-Authorization"
const MetricsPath = "/v1/metrics"

// MetricsAuthorization derives a credential that cannot authenticate an OpAMP
// session. Its lifetime and binding follow the original credential's database row.
func MetricsAuthorization(identity Identity, tokenHash string) string {
	key, err := hex.DecodeString(tokenHash)
	if err != nil || len(key) != sha256.Size || tokenHash == collectorcredential.EmptyDigest ||
		!validMetricsID(identity.Binding.CollectorID) || !validMetricsID(identity.CredentialID) {
		return ""
	}
	message := "om1." + identity.Binding.CollectorID + "." + identity.CredentialID
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return "Bearer " + message + "." + hex.EncodeToString(mac.Sum(nil))
}

// ParseMetricsAuthorization extracts lookup IDs only. Callers must verify the
// MAC against the matching stored record before trusting either ID.
func ParseMetricsAuthorization(authorization string) (collectorID, credentialID string, ok bool) {
	scheme, token, found := strings.Cut(strings.TrimSpace(authorization), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 4 || parts[0] != "om1" || !validMetricsID(parts[1]) || !validMetricsID(parts[2]) {
		return "", "", false
	}
	mac, err := hex.DecodeString(parts[3])
	if err != nil || len(mac) != sha256.Size {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// VerifyMetricsAuthorization compares the signature in constant time. Database
// lifetime and connector binding authorization must additionally succeed.
func VerifyMetricsAuthorization(authorization string, identity Identity, tokenHash string) bool {
	collectorID, credentialID, ok := ParseMetricsAuthorization(authorization)
	if !ok || collectorID != identity.Binding.CollectorID || credentialID != identity.CredentialID {
		return false
	}
	expected := MetricsAuthorization(identity, tokenHash)
	if expected == "" {
		return false
	}
	_, suppliedToken, _ := strings.Cut(strings.TrimSpace(authorization), " ")
	_, expectedToken, _ := strings.Cut(expected, " ")
	return hmac.Equal([]byte(strings.TrimSpace(suppliedToken)), []byte(expectedToken))
}

func validMetricsID(id string) bool {
	if id == "" || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
