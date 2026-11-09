package risk

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// ViolationSummary is the render-ready sentence for a guardrail violation. It
// deliberately omits the service: callers show service and title separately,
// so the sentence only adds what broke the rule. Shared by the product API
// and the notifier so a Slack message reads like the findings feed.
func ViolationSummary(ruleLabel, offending string) string {
	return fmt.Sprintf("%s broken by %s.", ruleLabel, offending)
}

// AnomalySummary is the render-ready sentence for an anomaly finding: the
// human labels of the triggered classes (deduped, catalog order preserved).
// It omits service and operation, which callers show separately, and carries
// no per-instance values so it is cheap on list feeds.
func AnomalySummary(classes []repo.DeviationClass) string {
	labels := make([]string, 0, len(classes))
	seen := make(map[string]bool, len(classes))
	for _, c := range classes {
		l := ClassLabel(c)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		labels = append(labels, l)
	}
	if len(labels) == 0 {
		return "Transaction deviated from its baseline."
	}
	return fmt.Sprintf("Transaction deviated from its baseline: %s.", joinHuman(labels))
}

// joinHuman joins labels for prose: "a", "a and b", "a, b and c".
func joinHuman(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}
