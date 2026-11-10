package repo

import "time"

// FindingListFilter scopes list queries for anomalies and guardrail violations.
// Service, namespace, and status are optional; empty means no filter.
// Until zero means no upper bound (open-ended through "now" for relative windows).
type FindingListFilter struct {
	Since     time.Time
	Until     time.Time
	Limit     int
	Service   string
	Namespace string
	Status    string
}

// FindingListSince builds a filter with only the lower time bound and limit.
func FindingListSince(since time.Time, limit int) FindingListFilter {
	if limit <= 0 {
		limit = 500
	}
	return FindingListFilter{Since: since, Limit: limit}
}

// FindingListInRange builds a filter with inclusive [from, until] bounds.
func FindingListInRange(timeRange FindingsTimeRange, limit int) FindingListFilter {
	f := FindingListSince(timeRange.From, limit)
	f.Until = timeRange.Until
	return f
}
