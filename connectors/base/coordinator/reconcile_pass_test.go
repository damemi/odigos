package coordinator

import (
	"context"
	"testing"
)

type testPassKey struct{}

type passContextProvider struct{ fakeProvider }

func (passContextProvider) ReconcilePassContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, testPassKey{}, "same-pass")
}

type passContextReconciler struct{ saw *int }

func (r passContextReconciler) Reconcile(ctx context.Context) error {
	if ctx.Value(testPassKey{}) == "same-pass" {
		(*r.saw)++
	}
	return nil
}

func TestRunReconcileSharesProviderPassContext(t *testing.T) {
	seen := 0
	c := &Coordinator{provider: passContextProvider{}}
	c.runReconcile(context.Background(), []namedReconciler{
		{name: "first", rec: passContextReconciler{saw: &seen}},
		{name: "second", rec: passContextReconciler{saw: &seen}},
	})
	if seen != 2 {
		t.Fatalf("%d reconcilers received the shared pass context, want 2", seen)
	}
}
