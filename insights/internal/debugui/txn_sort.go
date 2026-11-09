package debugui

import (
	"net/url"
	"time"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// serviceTxnCard is one expandable service row on the by-service transactions view.
type serviceTxnCard struct {
	Namespace          string
	Service            string
	TransactionCount   int64
	Volume             int64
	LastSeen           time.Time
	Transactions       []repo.TransactionStat
	Open               bool
	InventoryStatus    string // learning | promoted
	PromotedCount      int
	ActiveCount        int
	InventoryProtected bool
	InventoryAuto      bool // auto-enabled transaction guardrail
	TransactionLimit   int
	// TransactionLimitReached: TransactionCount hit TransactionLimit, so unseen
	// operations of this service are dropped.
	TransactionLimitReached bool
}

func resolveTransactionSort(params apitypes.ListTransactionsParams) (sortBy, sortOrder string) {
	sortBy = string(params.SortBy)
	if sortBy == "" {
		sortBy = string(repo.SortByVolume)
	}
	sortOrder = string(params.SortOrder)
	if sortOrder == "" {
		switch repo.TransactionSortBy(sortBy) {
		case repo.SortByVolume, repo.SortByLastSeen:
			sortOrder = string(repo.SortDesc)
		case repo.SortByService, repo.SortByNamespace:
			sortOrder = string(repo.SortAsc)
		default:
			sortOrder = string(repo.SortDesc)
		}
	}
	return sortBy, sortOrder
}

// buildServiceTxnCards joins service rollups with flat transaction stats, keeping
// the service list's volume order and nesting each service's transactions.
func buildServiceTxnCards(services []repo.ServiceStat, txs []repo.TransactionStat, openNS, openSvc string) []serviceTxnCard {
	return buildServiceTxnCardsWithGuardrails(services, txs, nil, openNS, openSvc, 0)
}

func buildServiceTxnCardsWithGuardrails(services []repo.ServiceStat, txs []repo.TransactionStat, guardrails []repo.ServiceGuardrail, openNS, openSvc string, transactionLimit int) []serviceTxnCard {
	byKey := map[string][]repo.TransactionStat{}
	for _, tx := range txs {
		key := repo.ServiceScopeKey(tx.Namespace, tx.Service)
		byKey[key] = append(byKey[key], tx)
	}
	protected := map[string]bool{}
	autoProtected := map[string]bool{}
	for _, g := range guardrails {
		if g.Scope != repo.ScopeService {
			continue
		}
		if repo.InventoryProtected(g, risk.RuleAllowedTransactions) {
			protected[g.ScopeKey] = true
			if g.RuleOrigins[risk.RuleAllowedTransactions] == repo.GuardrailOriginAutoTransactionGuardrail {
				autoProtected[g.ScopeKey] = true
			}
		}
	}
	out := make([]serviceTxnCard, 0, len(services))
	for _, s := range services {
		key := repo.ServiceScopeKey(s.Namespace, s.Service)
		txns := byKey[key]
		promoted, active := 0, len(txns)
		for _, tx := range txns {
			if tx.Promoted {
				promoted++
			}
		}
		status := "learning"
		if active > 0 && promoted == active {
			status = "promoted"
		}
		out = append(out, serviceTxnCard{
			Namespace:               s.Namespace,
			Service:                 s.Service,
			TransactionCount:        s.TransactionCount,
			Volume:                  s.Volume,
			LastSeen:                s.LastSeen,
			Transactions:            txns,
			Open:                    openNS != "" && openSvc != "" && s.Namespace == openNS && s.Service == openSvc,
			InventoryStatus:         status,
			PromotedCount:           promoted,
			ActiveCount:             active,
			InventoryProtected:      protected[key],
			InventoryAuto:           autoProtected[key],
			TransactionLimit:        transactionLimit,
			TransactionLimitReached: transactionLimit > 0 && s.TransactionCount >= int64(transactionLimit),
		})
	}
	return out
}

// txnSortLink is one sortable column header on the transactions table.
type txnSortLink struct {
	Column string
	Label  string
	Href   string
	Active bool
	Asc    bool // true when current order is ascending
}

func transactionSortLinks(path string, baseQuery url.Values, sortBy, sortOrder string) map[string]txnSortLink {
	cols := []struct {
		Column string
		Label  string
	}{
		{string(repo.SortByNamespace), "Namespace"},
		{string(repo.SortByService), "Service"},
		{string(repo.SortByVolume), "Volume"},
		{string(repo.SortByLastSeen), "Last seen"},
	}
	out := make(map[string]txnSortLink, len(cols))
	for _, col := range cols {
		q := cloneQuery(baseQuery)
		active := sortBy == col.Column
		nextOrder := string(repo.SortDesc)
		if active {
			if sortOrder == string(repo.SortDesc) {
				nextOrder = string(repo.SortAsc)
			} else {
				nextOrder = string(repo.SortDesc)
			}
		} else if col.Column == string(repo.SortByService) || col.Column == string(repo.SortByNamespace) {
			nextOrder = string(repo.SortAsc)
		}
		if col.Column == string(repo.SortByVolume) && nextOrder == string(repo.SortDesc) {
			q.Del("sort_by")
			q.Del("sort_order")
		} else {
			q.Set("sort_by", col.Column)
			q.Set("sort_order", nextOrder)
		}
		out[col.Column] = txnSortLink{
			Column: col.Column,
			Label:  col.Label,
			Href:   pathWithQuery(path, q),
			Active: active,
			Asc:    active && sortOrder == string(repo.SortAsc),
		}
	}
	return out
}
