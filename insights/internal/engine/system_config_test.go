package engine

import (
	"context"
	"testing"
	"time"

	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestSystemConfigReloadUpdatesSampling(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(5), nil)

	s := repo.DefaultSystemSettings()
	s.ExamplesPerTransaction = 3
	s.ExampleSampleIntervalSeconds = 60
	s.MaxResidentTransactions = 100
	s.MaxBaselineSetMembers = 50
	if err := r.UpsertSystemSettings(context.Background(), s); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := eng.ReloadSystemConfig(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	got := eng.SystemSettings()
	if got.ExamplesPerTransaction != 3 || got.ExampleSampleIntervalSeconds != 60 {
		t.Fatalf("sampling not reloaded: %+v", got)
	}
	if eng.store.perSetCap != 50 || eng.store.maxTxns != 100 {
		t.Fatalf("capacity not applied: perSet=%d maxTxns=%d", eng.store.perSetCap, eng.store.maxTxns)
	}
}

func TestSamplerUsesSystemInterval(t *testing.T) {
	r := newFakeRepo()
	eng := New(r, nil, countOnlyConfig(100), nil)
	s := repo.DefaultSystemSettings()
	s.ExampleSampleIntervalSeconds = 60
	s.ExamplesPerTransaction = 10
	_ = r.UpsertSystemSettings(context.Background(), s)
	_ = eng.ReloadSystemConfig(context.Background())

	now := time.Unix(0, 0).UTC()
	eng.clock = func() time.Time { return now }
	mk := func() *Trace {
		return mkTrace(withRoot("/x", "svc", tracepb.Span_SPAN_KIND_SERVER, nil))
	}
	eng.Process(context.Background(), mk())
	now = now.Add(30 * time.Second)
	eng.Process(context.Background(), mk())
	n := 0
	for _, o := range r.observations {
		if o.SampleReason == repo.ExampleSampleReason {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected throttle at 60s to keep 1 example, got %d", n)
	}
}
