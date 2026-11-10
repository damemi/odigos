package store

import "testing"

func TestPendingLambdaSnapshotNeedsCleanupWithoutClaimingInstrumentation(t *testing.T) {
	for _, tags := range []any{
		map[string]string{TagLambdaPending: "v2:snapshot"},
		map[string]any{TagLambdaPending: "v2:snapshot"},
	} {
		fn := LambdaFunction{Metadata: map[string]any{"odigos_tags": tags}}
		if fn.Instrumented || !LambdaHasPendingInstrumentation(fn) {
			t.Fatal("unapplied pending snapshot must need cleanup without claiming instrumentation")
		}
	}
	if LambdaHasPendingInstrumentation(LambdaFunction{}) {
		t.Fatal("unmanaged function must not need pending cleanup")
	}
	env := LambdaAttributionEnvironment(nil, map[string]string{TagLambdaPending: "v2:snapshot"})
	if OdigosInstrumented(env) {
		t.Fatal("pending ownership alone must not claim applied instrumentation")
	}
}
