package ruletests

import (
	"testing"

	learning "github.com/odigos-io/odigos-enterprise/common-enterprise/urltemplatization/learning"
)

func templatesOf(rules []learning.LearnedRule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Template)
	}
	return out
}

func assertTemplates(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("templates=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("templates=%v, want %v", got, want)
		}
	}
}

type wantSegment struct {
	typ         learning.SegmentType
	name        string
	certainty   learning.RuleCertainty
	minExamples int
}

func assertSegments(t *testing.T, got []learning.Segment, want []wantSegment) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("segments=%+v, want %+v", got, want)
	}
	for i := range want {
		if got[i].Type != want[i].typ {
			t.Fatalf("segments[%d].Type=%q, want %q (full=%+v)", i, got[i].Type, want[i].typ, got)
		}
		if got[i].Name != want[i].name {
			t.Fatalf("segments[%d].Name=%q, want %q (full=%+v)", i, got[i].Name, want[i].name, got)
		}
		if want[i].typ != learning.SegmentTypeStatic {
			certainty := want[i].certainty
			if certainty == "" {
				certainty = learning.RuleCertaintyHigh
			}
			if got[i].Certainty != certainty {
				t.Fatalf("segments[%d].Certainty=%q, want %q", i, got[i].Certainty, certainty)
			}
		}
		if want[i].minExamples > 0 && len(got[i].Examples) != want[i].minExamples {
			t.Fatalf("segments[%d].Examples=%d, want %d", i, len(got[i].Examples), want[i].minExamples)
		}
	}
}
