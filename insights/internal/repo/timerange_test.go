package repo

import (
	"testing"
	"time"
)

func TestResolveFindingsTimeRangeDefault(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	got, err := ResolveFindingsTimeRange(now, 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Custom || got.WindowHours != DefaultFindingsLookbackHours {
		t.Fatalf("got %+v", got)
	}
	if !got.From.Equal(now.Add(-7 * 24 * time.Hour)) || !got.Until.Equal(now) {
		t.Fatalf("range %v..%v", got.From, got.Until)
	}
}

func TestResolveFindingsTimeRangeClampsWindowHours(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	got, err := ResolveFindingsTimeRange(now, 10_000, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.WindowHours != MaxFindingsLookbackHours {
		t.Fatalf("hours=%d", got.WindowHours)
	}
}

func TestResolveFindingsTimeRangeCustomClampsSpan(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	got, err := ResolveFindingsTimeRange(now, 0, "2026-01-01", "2026-08-01")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Custom {
		t.Fatal("expected custom")
	}
	maxSpan := time.Duration(MaxFindingsLookbackHours) * time.Hour
	if got.Until.Sub(got.From) > maxSpan {
		t.Fatalf("span %v > max", got.Until.Sub(got.From))
	}
}

func TestResolveFindingsTimeRangeRejectsInverted(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	if _, err := ResolveFindingsTimeRange(now, 0, "2026-08-01", "2026-07-01"); err == nil {
		t.Fatal("expected error")
	}
}

func TestParseWindowHoursQuery(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"24", 24},
		{"0", 0},
		{"-3", 0},
		{"abc", 0},
		{"12.5", 0},
	}
	for _, tc := range cases {
		if got := ParseWindowHoursQuery(tc.in); got != tc.want {
			t.Fatalf("ParseWindowHoursQuery(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
