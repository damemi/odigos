package ruletests

import (
	"fmt"
	"testing"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

func TestMergeContinuesIntoStaticSuffix(t *testing.T) {
	paths := map[string]string{}
	for i := 0; i < 30; i++ {
		paths[fmt.Sprintf("/users/%d/orders", i)] = "5"
	}
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(paths), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/users/{id}/orders"})
	assertSegments(t, rules[0].Segments, []wantSegment{
		{typ: learning.SegmentTypeStatic, name: "users"},
		{typ: learning.SegmentTypeTemplate, name: "id"},
		{typ: learning.SegmentTypeStatic, name: "orders"},
	})
}
