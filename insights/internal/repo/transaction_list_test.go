package repo

import (
	"errors"
	"testing"
)

func TestTransactionListParamsDefaults(t *testing.T) {
	p := TransactionListParams{}.WithDefaults()
	if p.SortBy != SortByVolume || p.SortOrder != SortDesc || p.Limit != 1000 {
		t.Fatalf("defaults = %+v", p)
	}
	p = TransactionListParams{SortBy: SortByLastSeen}.WithDefaults()
	if p.SortOrder != SortDesc {
		t.Fatalf("last_seen default order = %s, want desc", p.SortOrder)
	}
	p = TransactionListParams{SortBy: SortByService}.WithDefaults()
	if p.SortOrder != SortAsc {
		t.Fatalf("service default order = %s, want asc", p.SortOrder)
	}
}

func TestNormalizeTransactionSortBy(t *testing.T) {
	if _, err := NormalizeTransactionSortBy("not_a_column"); !errors.Is(err, ErrInvalidSort) {
		t.Fatalf("expected ErrInvalidSort, got %v", err)
	}
	got, err := NormalizeTransactionSortBy("last_seen")
	if err != nil || got != SortByLastSeen {
		t.Fatalf("got %q err=%v", got, err)
	}
}
