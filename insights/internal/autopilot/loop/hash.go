package loop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

func hashAnalysisInput(userJSON string) string {
	h := sha256.New()
	fmt.Fprintf(h, "promptRevision=%s\n%s", schema.PromptRevision, userJSON)
	return hex.EncodeToString(h.Sum(nil))
}
