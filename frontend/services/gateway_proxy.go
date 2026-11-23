package services

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/odigos-io/odigos/common"
)

// ValidateGatewayProxyAddress checks an https proxy address set from the UI or Central: empty (no proxy), or a
// host with an optional scheme (http or https) and port, as HTTPS_PROXY takes it.
func ValidateGatewayProxyAddress(address *string) error {
	if address == nil {
		return nil
	}
	value := strings.TrimSpace(*address)
	if value == "" {
		return nil
	}
	if strings.ContainsAny(value, " \t\n,") {
		return fmt.Errorf("invalid https proxy address %q: it must be a single address, e.g. http://proxy.example.com:3128", value)
	}
	parsed, err := url.Parse(value)
	if err != nil || !strings.Contains(value, "://") {
		// No scheme (proxy.example.com:3128): HTTPS_PROXY takes it as http://.
		parsed, err = url.Parse("http://" + value)
	}
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("invalid https proxy address %q: expected e.g. http://proxy.example.com:3128", value)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid https proxy address %q: the scheme must be http or https", value)
	}
	return nil
}

// ApplyGatewayProxy sets the gateway's https proxy settings in a config overlay (the local UI config or the remote
// config). An empty address is kept: it means no proxy, over one set in Helm. No-proxy entries are trimmed and empty
// ones dropped; an empty list is kept too: nothing bypasses the proxy, over a list set in Helm or Central.
func ApplyGatewayProxy(cfg *common.OdigosConfiguration, httpsProxyAddress *string, noProxy []string) {
	if httpsProxyAddress == nil && noProxy == nil {
		return
	}
	if cfg.CollectorGateway == nil {
		cfg.CollectorGateway = &common.CollectorGatewayConfiguration{}
	}
	if httpsProxyAddress != nil {
		cfg.CollectorGateway.HttpsProxyAddress = new(strings.TrimSpace(*httpsProxyAddress))
	}
	if noProxy != nil {
		entries := make([]string, 0, len(noProxy))
		for _, entry := range noProxy {
			if entry = strings.TrimSpace(entry); entry != "" {
				entries = append(entries, entry)
			}
		}
		cfg.CollectorGateway.NoProxy = &entries
	}
}
