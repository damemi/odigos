package learning

import "fmt"

func terminalRule(node *PathTrieNode, segments []Segment, cfg resolvedLearningParams) (LearnedRule, bool) {
	obs := node.TerminalObservationCount
	if obs <= cfg.minObservationsForRule {
		return LearnedRule{}, false
	}
	template := templateFromSegments(segments)
	return LearnedRule{
		Template: template,
		Reason:   terminalReason(segments, template, obs, cfg.minObservationsForRule),
		Segments: copySegments(segments),
	}, true
}

// terminalReason describes a rule for traffic that terminates at template.
func terminalReason(segments []Segment, template string, obs, threshold int64) string {
	kind := "literal path"
	for _, seg := range segments {
		if seg.Type != SegmentTypeStatic {
			kind = "templated path"
			break
		}
	}
	return fmt.Sprintf(
		"%s %q observed %d times as a complete endpoint (threshold > %d)",
		kind, template, obs, threshold,
	)
}
