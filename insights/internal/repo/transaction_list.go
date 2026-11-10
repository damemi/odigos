package repo

import (
	"errors"
	"fmt"
	"time"
)

// ErrInvalidSort is returned when sort_by / sort_order is not in the whitelist.
var ErrInvalidSort = errors.New("invalid sort")

// TransactionSortBy is a whitelist of columns GET /transactions can ORDER BY.
type TransactionSortBy string

const (
	SortByVolume    TransactionSortBy = "volume"
	SortByLastSeen  TransactionSortBy = "last_seen"
	SortByService   TransactionSortBy = "service"
	SortByNamespace TransactionSortBy = "namespace"
)

// SortOrder is asc or desc for list endpoints.
type SortOrder string

const (
	SortAsc  SortOrder = "asc"
	SortDesc SortOrder = "desc"
)

// TransactionListParams scopes, sorts, and limits ListTransactionStats.
type TransactionListParams struct {
	Namespace string
	Service   string
	Kind      string
	SortBy    TransactionSortBy
	SortOrder SortOrder
	Limit     int
	// ActiveSince/ActiveUntil restrict to transactions whose last_seen falls in
	// [ActiveSince, ActiveUntil]. Zero values mean no bound on that end.
	ActiveSince time.Time
	ActiveUntil time.Time
}

// WithDefaults fills sort/limit defaults used by the product API.
func (p TransactionListParams) WithDefaults() TransactionListParams {
	if p.Limit <= 0 {
		p.Limit = 1000
	}
	if p.SortBy == "" {
		p.SortBy = SortByVolume
	}
	if p.SortOrder == "" {
		switch p.SortBy {
		case SortByVolume, SortByLastSeen:
			p.SortOrder = SortDesc
		default:
			p.SortOrder = SortAsc
		}
	}
	return p
}

// NormalizeTransactionSortBy maps an unknown value to an error (empty → volume
// via WithDefaults). Unknown non-empty values are rejected so SQL stays safe.
func NormalizeTransactionSortBy(v string) (TransactionSortBy, error) {
	if v == "" {
		return SortByVolume, nil
	}
	s := TransactionSortBy(v)
	switch s {
	case SortByVolume, SortByLastSeen, SortByService, SortByNamespace:
		return s, nil
	default:
		return "", fmt.Errorf("%w: sort_by %q", ErrInvalidSort, v)
	}
}

// NormalizeSortOrder maps an unknown value to an error.
func NormalizeSortOrder(v string) (SortOrder, error) {
	if v == "" {
		return "", nil
	}
	o := SortOrder(v)
	switch o {
	case SortAsc, SortDesc:
		return o, nil
	default:
		return "", fmt.Errorf("%w: sort_order %q", ErrInvalidSort, v)
	}
}
