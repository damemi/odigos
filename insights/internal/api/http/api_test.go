package http

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestTransactionActiveWindowFromParams(t *testing.T) {
	t.Run("no params defaults to 7d", func(t *testing.T) {
		since, until, err := transactionActiveWindowFromParams(0, "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if until.Before(since) {
			t.Fatalf("until before since: %v < %v", until, since)
		}
		want := time.Duration(repo.DefaultFindingsLookbackHours) * time.Hour
		if until.Sub(since) != want {
			t.Fatalf("span = %v, want %v", until.Sub(since), want)
		}
	})

	t.Run("window hours", func(t *testing.T) {
		since, until, err := transactionActiveWindowFromParams(24, "", "")
		if err != nil {
			t.Fatalf("resolve: err=%v", err)
		}
		if until.Before(since) {
			t.Fatalf("until before since: %v < %v", until, since)
		}
		if until.Sub(since) != 24*time.Hour {
			t.Fatalf("span = %v, want 24h", until.Sub(since))
		}
	})

	t.Run("invalid from", func(t *testing.T) {
		_, _, err := transactionActiveWindowFromParams(0, "not-a-date", "")
		if err == nil {
			t.Fatal("expected error for invalid from")
		}
	})

	t.Run("absolute range", func(t *testing.T) {
		since, until, err := transactionActiveWindowFromParams(0, apitypes.FromTime("2026-01-01"), apitypes.ToTime("2026-01-02"))
		if err != nil {
			t.Fatalf("resolve: err=%v", err)
		}
		if since.Format("2006-01-02") != "2026-01-01" {
			t.Fatalf("since = %v", since)
		}
		if until.Format("2006-01-02") != "2026-01-02" {
			t.Fatalf("until = %v", until)
		}
	})
}
