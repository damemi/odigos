package collectorgateway

import (
	"net/http"
	"strings"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

// bearerScheme is the only scheme accepted. The Supervisor sends the whole header value verbatim from
// ODIGOS_OPAMP_AUTHORIZATION (collector/managed/supervisor.yaml), documented as "Bearer <credential>".
const bearerScheme = "bearer"

// AuthorizationHeader renders a credential as the complete header value the Supervisor is given.
//
// It lives beside PresentedCredential, which parses it, because the two are one contract: the
// Supervisor copies ODIGOS_OPAMP_AUTHORIZATION into the header verbatim, so what a deployer writes to
// the secret store has to be the WHOLE header value and not the bare token — a bare token arrives with
// no scheme and is refused as RefusalNoCredential, which reads as "deployed but never connected" and
// is the one failure the connector cannot diagnose, because it does not read the secret back.
//
// The result stays wrapped, so a deployer can hand it to a secret-store write without ever revealing
// a value itself. The hash a credential is verified by remains the hash of the BARE token: this adds
// a transport scheme, not a new secret.
func AuthorizationHeader(credential collectorcredential.Secret) collectorcredential.Secret {
	if credential.Empty() {
		// Empty in, empty out. Prefixing a scheme onto nothing would turn a failed mint into a
		// well-formed header carrying no credential, which the gateway would refuse for a reason that
		// names the scheme rather than the emptiness.
		return credential
	}
	return collectorcredential.NewSecret("Bearer " + credential.Reveal())
}

// PresentedCredential extracts the credential a Collector presented on the OpAMP handshake.
//
// It returns the hash and never the value: past this function the gateway has no reason to hold the
// credential, and the shortest possible life for it in memory is the point. The Secret wrapper carries
// it the few lines in between, so an added log line renders "<redacted>" instead of a working
// credential.
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
