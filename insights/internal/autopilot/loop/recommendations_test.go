package loop

import (
	"reflect"
	"testing"

	"github.com/odigos-io/odigos/insights/internal/autopilot/db/clickhouse"
	"github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

func TestStoredRecommendationsRoundTrip(t *testing.T) {
	source := clickhouse.SourceKey{Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: "app", ContainerName: "c"}
	recs := []schema.Recommendation{
		{
			Category: schema.CategorySecurity,
			Function: "com.acme.Auth.check",
			Language: schema.LangJava,
			Why:      " Auth.check is called from Api.handle. ",
			Probe:    schema.Probe{ClassName: "com.acme.Auth", MethodName: "check"},
		},
		{
			Category: schema.CategoryPerformance,
			Function: "acme/pricing.(*Svc).Total",
			Language: schema.LangGo,
			Why:      "Total fans out to the cache.",
			Probe:    schema.Probe{PackageName: "acme/pricing", ReceiverName: "Svc", ReceiverMethodName: "Total"},
		},
	}

	stored := storedRecommendations(source, "tx1", "rev", recs)
	if len(stored) != 2 {
		t.Fatalf("len = %d", len(stored))
	}
	if got := stored[0]; got.Target != "com.acme.Auth.check" || got.TransactionID != "tx1" ||
		got.AnalysisRevision != "rev" || got.ContainerName != "c" || got.Why != "Auth.check is called from Api.handle." {
		t.Fatalf("stored[0] = %+v", got)
	}
	if got := stored[1].Target; got != "acme/pricing.(*Svc).Total" {
		t.Fatalf("stored[1].Target = %q", got)
	}

	for i, s := range stored {
		want := recs[i]
		want.Why = s.Why
		if got := recommendationFromStored(s); !reflect.DeepEqual(got, want) {
			t.Fatalf("round trip %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestUnstoredRecommendations(t *testing.T) {
	rec := func(lang, target string) clickhouse.Recommendation {
		return clickhouse.Recommendation{Language: lang, Target: target}
	}
	existing := []clickhouse.Recommendation{rec("java", "A.b")}
	recs := []clickhouse.Recommendation{
		rec("java", "A.b"), // already stored
		rec("go", "p.F"),
		rec("go", "p.F"),  // repeated in the same response
		rec("cpp", "A.b"), // same target, different language
	}
	got := unstoredRecommendations(recs, existing)
	want := []clickhouse.Recommendation{rec("go", "p.F"), rec("cpp", "A.b")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
