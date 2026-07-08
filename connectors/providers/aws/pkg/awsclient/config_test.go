package awsclient

import (
	"context"
	"strings"
	"testing"

	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/config"
)

// An empty expected account id means the boundary cannot be enforced — VerifyAccount must reject it
// outright (before any STS call) rather than silently allowing the connector to manage any account.
func TestVerifyAccountRejectsEmptyExpected(t *testing.T) {
	c := NewCache(config.Credentials{})
	err := c.VerifyAccount(context.Background(), "")
	if err == nil {
		t.Fatal("expected an error for an empty account id, got nil")
	}
	if !strings.Contains(err.Error(), "account id") {
		t.Errorf("error = %q, want it to mention the missing account id", err.Error())
	}
}
