package learning

import (
	"fmt"
	"sort"
)

func terminalRule(node *PathTrieNode, segments []Segment, cfg resolvedLearningParams) (LearnedRule, bool) {
	obs := node.TerminalObservationCount
	if obs <= cfg.minObservationsForRule {
		return LearnedRule{}, false
	}
	template := templateFromSegments(segments)
	return LearnedRule{
		Template: template,
		Reason:   terminalReason(node.TerminalPathCount, obs, segments),
		Examples: terminalExamplePaths(node.TerminalExamplePaths),
		Segments: copySegments(segments),
	}, true
}

// terminalExamplePaths returns the endpoint set as a sorted slice, capped at
// maxTerminalExamplePaths, so persisted rules stay stable.
func terminalExamplePaths(paths examplePaths) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	sort.Strings(out)
	if len(out) > maxTerminalExamplePaths {
		out = out[:maxTerminalExamplePaths]
	}
	return out
}

// terminalReason describes why a template was learned.
// paths is the number of distinct URLs merged into this template; obs is their
// combined request count.
func terminalReason(paths, obs int64, segments []Segment) string {
	requests := "requests"
	if obs == 1 {
		requests = "request"
	}
	urls := "different URLs"
	if paths == 1 {
		urls = "URL"
	}
	return fmt.Sprintf(
		"Learned the template %s from live traffic, based on %d %s from %d %s",
		templateFromSegments(segments), obs, requests, paths, urls,
	)
}

// addTerminalExamplePath records path unless it is already present or the cap is full.
// The set is created with room for maxTerminalExamplePaths.
func addTerminalExamplePath(existing examplePaths, path string) examplePaths {
	if _, ok := existing[path]; ok {
		return existing
	}
	if len(existing) >= maxTerminalExamplePaths {
		return existing
	}
	if existing == nil {
		existing = make(examplePaths, maxTerminalExamplePaths)
	}
	existing[path] = nil
	return existing
}

// mergeTerminalExamplePaths keeps up to maxTerminalExamplePaths endpoints.
// It balances path examples from the available nodes.
func mergeTerminalExamplePaths(examplesByNode []examplePaths) examplePaths {
	out := make(examplePaths, maxTerminalExamplePaths)
	for len(out) < maxTerminalExamplePaths {
		added := false
		for _, examples := range examplesByNode {
			for example := range examples {
				_, exists := out[example]
				if !exists {
					out[example] = nil
					added = true
					break
				}
			}
			if len(out) == maxTerminalExamplePaths {
				return out
			}
		}
		if !added {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
