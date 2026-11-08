package ruletests

import (
	"testing"

	learning "github.com/odigos-io/odigos/common/urltemplatization/learning"
)

func TestChildrenNeedEnoughObservations(t *testing.T) {
	rules := learning.FindLiveTrafficLearningRules(learning.BuildPathTrie(map[string]string{
		"/api":    "101",
		"/api/v1": "50",
		"/api/v2": "50",
	}), learning.LiveTrafficLearningParams{})
	assertTemplates(t, templatesOf(rules), []string{"/api"})
}
