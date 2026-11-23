package clustercollector

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNoProxyValue(t *testing.T) {
	ui := "ui.odigos-system:4317"

	assert.Equal(t, ui, noProxyValue("odigos-system", nil), "the UI service is always there")
	assert.Equal(t, ui+",.example.com,10.0.0.0/8", noProxyValue("odigos-system", []string{" .example.com ", "", "10.0.0.0/8"}), "entries are trimmed, empty ones dropped")
	assert.Equal(t, ui+",vault.internal", noProxyValue("odigos-system", []string{"vault.internal", ui, "vault.internal"}), "repeated entries are dropped")
}
