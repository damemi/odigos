package repo

import (
	"fmt"
	"time"
)

const (
	DefaultFindingsLookbackHours = 7 * 24
	MaxFindingsLookbackHours     = 120 * 24
)

// FindingsTimeRange is the inclusive lookback used when listing findings.
type FindingsTimeRange struct {
	From        time.Time
	Until       time.Time
	WindowHours int  // set for relative presets; 0 for custom absolute ranges
	Custom      bool // true when resolved from from/to query params
}

// ResolveFindingsTimeRange builds a findings lookback from optional query inputs.
// Absolute from/to wins over windowHours. Omitted inputs use the 7d default.
// Spans longer than MaxFindingsLookbackHours are clamped by advancing From.
func ResolveFindingsTimeRange(now time.Time, windowHours int, fromRaw, toRaw string) (FindingsTimeRange, error) {
	if fromRaw != "" {
		from, err := parseFindingsTimeBound(fromRaw, false)
		if err != nil {
			return FindingsTimeRange{}, fmt.Errorf("from: %w", err)
		}
		until := now
		if toRaw != "" {
			until, err = parseFindingsTimeBound(toRaw, true)
			if err != nil {
				return FindingsTimeRange{}, fmt.Errorf("to: %w", err)
			}
		}
		if !from.Before(until) {
			return FindingsTimeRange{}, fmt.Errorf("from must be before to")
		}
		maxSpan := time.Duration(MaxFindingsLookbackHours) * time.Hour
		if until.Sub(from) > maxSpan {
			from = until.Add(-maxSpan)
		}
		return FindingsTimeRange{From: from, Until: until, Custom: true}, nil
	}

	hours := DefaultFindingsLookbackHours
	if windowHours > 0 {
		hours = windowHours
	}
	if hours > MaxFindingsLookbackHours {
		hours = MaxFindingsLookbackHours
	}
	return FindingsTimeRange{
		From:        now.Add(-time.Duration(hours) * time.Hour),
		Until:       now,
		WindowHours: hours,
	}, nil
}

// parseFindingsTimeBound accepts RFC3339 or YYYY-MM-DD. Date-only from is start
// of UTC day; date-only to (endOfDay) is end of UTC day.
func parseFindingsTimeBound(raw string, endOfDay bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, nil
	}
	day, err := time.Parse("2006-01-02", raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("want RFC3339 or YYYY-MM-DD")
	}
	if endOfDay {
		return day.Add(24*time.Hour - time.Nanosecond), nil
	}
	return day, nil
}
