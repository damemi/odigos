package collectorimage

import (
	"encoding/base64"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorcredential"
)

// EnvBootstrapConfig is a base64-encoded Collector config for cold starts when OpAMP is unreachable.
// Must be a secret-store reference (contains exporter credentials), not a plain env value.
// Decoded by collector/cmd/odigosopampsupervisor into storage.directory as the startup fallback.
const EnvBootstrapConfig = "ODIGOS_COLLECTOR_BOOTSTRAP_CONFIG"

// EncodeBootstrapConfig base64-encodes a rendered config for EnvBootstrapConfig.
// Returns a Secret so the credential-bearing document cannot travel as a plain string.
// An empty document yields an empty Secret.
func EncodeBootstrapConfig(document string) collectorcredential.Secret {
	return collectorcredential.NewSecret(base64.StdEncoding.EncodeToString([]byte(document)))
}
