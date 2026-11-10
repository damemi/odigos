package mcp

import (
	"strings"
	"testing"
)

func TestAccessControlToolsAreExempt(t *testing.T) {
	for _, name := range []string{getAccessModeToolName, enableWriteModeToolName, disableWriteModeToolName} {
		if !isAccessControlTool(name) {
			t.Fatalf("%q must be exempt so the mode can always be read and toggled", name)
		}
	}
	if isAccessControlTool("delete_destination") {
		t.Fatal("a regular write tool must not be treated as access-control")
	}
}

func TestEscalationMessageGuidesToEnableWrite(t *testing.T) {
	msg := escalationMessage("delete_destination")
	for _, want := range []string{"delete_destination", "read-only", enableWriteModeToolName} {
		if !strings.Contains(msg, want) {
			t.Fatalf("escalation message must mention %q; got: %s", want, msg)
		}
	}
}
