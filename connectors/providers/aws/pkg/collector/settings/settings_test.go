package collectorsettings

import (
	"testing"

	base "github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorsettings"
)

func TestExecutionRoleARNIsReadFromProviderSettings(t *testing.T) {
	want := "arn:aws:iam::123456789012:role/OdigosCollectorExecutionRole"
	got := FromBase(base.Settings{Defaults: base.Network{
		Provider: map[string]any{KeyExecutionRoleARN: want},
	}}).Defaults.ExecutionRoleARN
	if got != want {
		t.Fatalf("execution role ARN = %q, want %q", got, want)
	}
}
