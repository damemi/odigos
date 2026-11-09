package debugui

import (
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

var findingsLookbackPresets = []struct {
	Label       string
	WindowHours int
}{
	{Label: "Last 24 hours", WindowHours: 24},
	{Label: "Last 7 days", WindowHours: repo.DefaultFindingsLookbackHours},
	{Label: "Last 30 days", WindowHours: 30 * 24},
	{Label: "Last 90 days", WindowHours: 90 * 24},
	{Label: "Last 120 days", WindowHours: repo.MaxFindingsLookbackHours},
}

type findingsTimeRangeView struct {
	Label        string
	WindowHours  int
	FromDate     string
	ToDate       string
	Custom       bool
	Presets      []findingsLookbackPresetView
	Query        string            // encoded range params for appending to other links
	HiddenFields map[string]string // preserved on custom-range GET (e.g. tab)
}

type findingsLookbackPresetView struct {
	Label       string
	WindowHours int
	Href        string
	Active      bool
}

func findingsTimeRangeFromRequest(c *gin.Context) (repo.FindingsTimeRange, error) {
	return repo.ResolveFindingsTimeRange(
		time.Now(),
		repo.ParseWindowHoursQuery(c.Query("window_hours")),
		c.Query("from"),
		c.Query("to"),
	)
}

func findingsTimeRangeLabel(timeRange repo.FindingsTimeRange) string {
	if timeRange.Custom {
		return fmt.Sprintf("%s – %s",
			timeRange.From.UTC().Format("2006-01-02"),
			timeRange.Until.UTC().Format("2006-01-02"))
	}
	switch timeRange.WindowHours {
	case 24:
		return "Last 24h"
	case repo.DefaultFindingsLookbackHours:
		return "Last 7d"
	case 30 * 24:
		return "Last 30d"
	case 90 * 24:
		return "Last 90d"
	case repo.MaxFindingsLookbackHours:
		return "Last 120d"
	default:
		return fmt.Sprintf("Last %dh", timeRange.WindowHours)
	}
}

func findingsTimeRangeQuery(timeRange repo.FindingsTimeRange) url.Values {
	q := url.Values{}
	if timeRange.Custom {
		q.Set("from", timeRange.From.UTC().Format(time.RFC3339))
		q.Set("to", timeRange.Until.UTC().Format(time.RFC3339))
		return q
	}
	if timeRange.WindowHours > 0 && timeRange.WindowHours != repo.DefaultFindingsLookbackHours {
		q.Set("window_hours", strconv.Itoa(timeRange.WindowHours))
	}
	return q
}

func findingsTimeRangeViewFor(path string, baseQuery url.Values, timeRange repo.FindingsTimeRange) findingsTimeRangeView {
	rangeQuery := findingsTimeRangeQuery(timeRange)
	presets := make([]findingsLookbackPresetView, 0, len(findingsLookbackPresets))
	for _, preset := range findingsLookbackPresets {
		q := cloneQuery(baseQuery)
		delete(q, "from")
		delete(q, "to")
		if preset.WindowHours == repo.DefaultFindingsLookbackHours {
			q.Del("window_hours")
		} else {
			q.Set("window_hours", strconv.Itoa(preset.WindowHours))
		}
		presets = append(presets, findingsLookbackPresetView{
			Label:       preset.Label,
			WindowHours: preset.WindowHours,
			Href:        pathWithQuery(path, q),
			Active:      !timeRange.Custom && timeRange.WindowHours == preset.WindowHours,
		})
	}
	hiddenFields := map[string]string{}
	for key, values := range baseQuery {
		if key == "window_hours" || key == "from" || key == "to" || len(values) == 0 {
			continue
		}
		hiddenFields[key] = values[0]
	}
	return findingsTimeRangeView{
		Label:        findingsTimeRangeLabel(timeRange),
		WindowHours:  timeRange.WindowHours,
		FromDate:     timeRange.From.UTC().Format("2006-01-02"),
		ToDate:       timeRange.Until.UTC().Format("2006-01-02"),
		Custom:       timeRange.Custom,
		Presets:      presets,
		Query:        rangeQuery.Encode(),
		HiddenFields: hiddenFields,
	}
}

func cloneQuery(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func pathWithQuery(path string, q url.Values) string {
	encoded := q.Encode()
	if encoded == "" {
		return path
	}
	return path + "?" + encoded
}

func mergeQuery(base url.Values, extra url.Values) url.Values {
	out := cloneQuery(base)
	for key, values := range extra {
		out[key] = append([]string(nil), values...)
	}
	return out
}
