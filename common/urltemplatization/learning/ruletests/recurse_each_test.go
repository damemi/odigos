package ruletests

import (
	"testing"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

// With few distinct child segments, learning keeps each child as a static literal
// rather than collapsing them into "*" / "{id}". The recurse cutoff scales with
// template probability. Here three non-pattern siblings under /a each become
// their own exact-path rule once observation gates are met.
func TestRecurseEachUpToFive(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/a/x": "101",
		"/a/y": "101",
		"/a/z": "101",
	}), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/a/x", "/a/y", "/a/z"})
}
