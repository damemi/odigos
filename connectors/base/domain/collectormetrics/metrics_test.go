package collectormetrics

import (
	"testing"
	"time"
)

func i(v int) *int { return &v }

// The distinction the whole type exists to preserve: a Collector nobody has heard from reported
// nothing, and a zero would claim it handled nothing. Both must survive the store round trip.
func TestEncodeDecodeKeepsAbsentAndZeroApart(t *testing.T) {
	snapshot := Snapshot{AcceptedSpans: i(0), SentSpans: i(4200)}

	raw, err := Encode(snapshot)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if got.AcceptedSpans == nil || *got.AcceptedSpans != 0 {
		t.Errorf("a reported zero came back as %v", got.AcceptedSpans)
	}
	if got.SentSpans == nil || *got.SentSpans != 4200 {
		t.Errorf("sentSpans = %v", got.SentSpans)
	}
	if got.RefusedSpans != nil {
		t.Errorf("an unreported counter came back as %v", *got.RefusedSpans)
	}
}

// An empty column is a Collector that has never reported, not a corrupt row.
func TestDecodeEmptyIsNotAnError(t *testing.T) {
	got, err := Decode(nil)
	if err != nil {
		t.Fatalf("Decode(nil): %v", err)
	}
	if got.AcceptedSpans != nil || got.QueueSize != nil {
		t.Errorf("an empty snapshot came back populated: %+v", got)
	}
}

func TestDecodeRejectsCorruptJSON(t *testing.T) {
	if _, err := Decode([]byte("{not json")); err == nil {
		t.Fatal("corrupt stored metrics decoded without complaint")
	}
}

func TestToModelReportsWhenTheSnapshotWasTaken(t *testing.T) {
	observed := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)
	got := ToModel("eu-west-1", Record{
		Snapshot:   Snapshot{QueueSize: i(12), QueueCapacity: i(1000)},
		ObservedAt: observed,
	})

	if got.Region != "eu-west-1" {
		t.Errorf("region = %q", got.Region)
	}
	// The Collector's own timestamp, not the read time — it is what lets a reader judge staleness, which
	// is the only reason serving a cached snapshot is safe at all.
	if got.ObservedAt != "2026-09-15T11:30:00Z" {
		t.Errorf("observedAt = %q", got.ObservedAt)
	}
	if got.Live {
		t.Error("a cached snapshot reported itself as live")
	}
	if got.QueueSize == nil || *got.QueueSize != 12 {
		t.Errorf("queueSize = %v", got.QueueSize)
	}
}

func TestToModelCarriesTheLiveFlag(t *testing.T) {
	got := ToModel("eu-west-1", Record{ObservedAt: time.Now(), Live: true})
	if !got.Live {
		t.Error("a live snapshot reported itself as cached")
	}
}
