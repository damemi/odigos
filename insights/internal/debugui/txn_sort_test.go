package debugui

import (
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

func TestBuildServiceTxnCards(t *testing.T) {
	now := time.Now()
	services := []repo.ServiceStat{
		{Namespace: "default", Service: "frontend", TransactionCount: 2, Volume: 100, LastSeen: now},
		{Namespace: "default", Service: "pricing", TransactionCount: 1, Volume: 10, LastSeen: now},
	}
	txs := []repo.TransactionStat{
		{Transaction: repo.Transaction{ID: 1, Namespace: "default", Service: "frontend", Operation: "GET /"}, Volume: 60},
		{Transaction: repo.Transaction{ID: 2, Namespace: "default", Service: "frontend", Operation: "POST /"}, Volume: 40},
		{Transaction: repo.Transaction{ID: 3, Namespace: "default", Service: "pricing", Operation: "GET /p"}, Volume: 10},
	}
	cards := buildServiceTxnCards(services, txs, "default", "pricing")
	if len(cards) != 2 {
		t.Fatalf("cards=%d", len(cards))
	}
	if len(cards[0].Transactions) != 2 || cards[0].Open {
		t.Fatalf("frontend card = %+v", cards[0])
	}
	if len(cards[1].Transactions) != 1 || !cards[1].Open {
		t.Fatalf("pricing card = %+v", cards[1])
	}
}
