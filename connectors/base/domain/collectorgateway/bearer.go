package collectorgateway

import (
	"net/http"
	"strings"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
)

// bearerScheme is the only scheme accepted. The Supervisor sends the whole header value verbatim from
// ODIGOS_OPAMP_AUTHORIZATION (collector/managed/supervisor.yaml), documented as "Bearer <credential>".
const bearerScheme = "bearer"

// PresentedCredential extracts the credential a Collector presented on the OpAMP handshake.
//
// It returns the hash and never the value: past this function the gateway has no reason to hold the
// credential, and the shortest possible life for it in memory is the point. The Secret wrapper carries
// it the few lines in between, so an added log line renders "<redacted>" instead of a working
// credential (§6.3).
//
// A malformed header is reported as RefusalNoCredential rather than a distinct reason. The difference
// between "absent", "wrong scheme" and "empty" tells an operator nothing they cannot see in the
// request, and telling the caller apart is a probe.
func PresentedCredential(h http.Header) (hash string, refusal Refusal) {
	scheme, value, found := strings.Cut(strings.TrimSpace(h.Get("Authorization")), " ")
	if !found || !strings.EqualFold(scheme, bearerScheme) {
		return "", RefusalNoCredential
	}
	secret := collectorcredential.NewSecret(strings.TrimSpace(value))
	sum, err := secret.Hash()
	if err != nil {
		// The only error is an empty value, which Hash refuses rather than digesting.
		return "", RefusalNoCredential
	}
	return sum, RefusalNone
}
