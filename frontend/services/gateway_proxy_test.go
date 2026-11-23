package services

import (
	"testing"

	"github.com/odigos-io/odigos/common"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/yaml"
)

func TestValidateGatewayProxyAddress(t *testing.T) {
	for _, valid := range []string{"", "  ", "http://proxy.example.com:3128", "https://proxy:443", "proxy.example.com:3128", "http://user:pass@10.0.0.1:8080"} {
		assert.NoError(t, ValidateGatewayProxyAddress(&valid), valid)
	}
	for _, invalid := range []string{"http://a:1,http://b:2", "proxy example.com", "socks5://proxy:1080", "http://"} {
		assert.Error(t, ValidateGatewayProxyAddress(&invalid), invalid)
	}
	assert.NoError(t, ValidateGatewayProxyAddress(nil))
}

func TestApplyGatewayProxy(t *testing.T) {
	cfg := &common.OdigosConfiguration{}
	ApplyGatewayProxy(cfg, nil, nil)
	assert.Nil(t, cfg.CollectorGateway, "nothing to set: the gateway section isn't created")

	ApplyGatewayProxy(cfg, new(" http://proxy:3128 "), []string{" .example.com", "", "10.0.0.0/8 "})
	assert.Equal(t, "http://proxy:3128", *cfg.CollectorGateway.HttpsProxyAddress)
	assert.Equal(t, []string{".example.com", "10.0.0.0/8"}, *cfg.CollectorGateway.NoProxy)

	// Only the address: the no-proxy list stays.
	ApplyGatewayProxy(cfg, new(""), nil)
	assert.Equal(t, "", *cfg.CollectorGateway.HttpsProxyAddress, "an empty address is kept: no proxy")
	assert.Equal(t, []string{".example.com", "10.0.0.0/8"}, *cfg.CollectorGateway.NoProxy)

	// The list's last entry removed: the form sends one blank row, and an empty list is kept.
	ApplyGatewayProxy(cfg, nil, []string{""})
	assert.NotNil(t, cfg.CollectorGateway.NoProxy)
	assert.Empty(t, *cfg.CollectorGateway.NoProxy)
}

// The overlay ConfigMap round trip: an empty address survives it (no proxy over Helm's), and so does an empty list (no
// bypass list over Helm's or Central's); provenance names the overlay for both.
func TestGatewayProxyOverlayRoundTrip(t *testing.T) {
	cfg := &common.OdigosConfiguration{}
	ApplyGatewayProxy(cfg, new(""), []string{})

	data, err := yaml.Marshal(cfg)
	assert.NoError(t, err)
	var read common.OdigosConfiguration
	assert.NoError(t, yaml.Unmarshal(data, &read))

	assert.NotNil(t, read.CollectorGateway.HttpsProxyAddress)
	assert.Equal(t, "", *read.CollectorGateway.HttpsProxyAddress)
	assert.NotNil(t, read.CollectorGateway.NoProxy, "an empty list is written")
	assert.Empty(t, *read.CollectorGateway.NoProxy)

	provenance := map[string]string{}
	recordOverlayProvenance(&read, provenance, "odigos-local-ui-config")
	assert.Equal(t, "odigos-local-ui-config", provenance["collectorGateway.httpsProxyAddress"])
	assert.Equal(t, "odigos-local-ui-config", provenance["collectorGateway.noProxy"])
}
