package debugui

import (
	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func listTransactionsParamsFromRequest(c *gin.Context) apitypes.ListTransactionsParams {
	return apitypes.ListTransactionsParams{
		Namespace:   c.Query("namespace"),
		Service:     c.Query("service"),
		Kind:        apitypes.TransactionKind(c.Query("kind")),
		WindowHours: apitypes.WindowHours(repo.ParseWindowHoursQuery(c.Query("window_hours"))),
		From:        apitypes.FromTime(c.Query("from")),
		To:          apitypes.ToTime(c.Query("to")),
		SortBy:      apitypes.TransactionSortBy(c.Query("sort_by")),
		SortOrder:   apitypes.SortOrder(c.Query("sort_order")),
	}
}

func listFindingsParamsFromRequest(c *gin.Context) apitypes.ListFindingsParams {
	return apitypes.ListFindingsParams{
		WindowHours: apitypes.WindowHours(repo.ParseWindowHoursQuery(c.Query("window_hours"))),
		From:        apitypes.FromTime(c.Query("from")),
		To:          apitypes.ToTime(c.Query("to")),
		Service:     apitypes.ServiceFilter(c.Query("service")),
		Namespace:   apitypes.NamespaceFilter(c.Query("namespace")),
		Status:      apitypes.StatusFilter(c.Query("status")),
		Kind:        apitypes.FindingKind(c.Query("kind")),
	}
}

func anomalyIssuesFromWire(items []apitypes.AnomalyIssueSummary) []repo.AnomalyIssue {
	out := make([]repo.AnomalyIssue, len(items))
	for i, item := range items {
		classes := make([]repo.DeviationClass, len(item.TriggeredClasses))
		for j, class := range item.TriggeredClasses {
			classes[j] = repo.DeviationClass(class)
		}
		out[i] = repo.AnomalyIssue{
			TransactionID:    item.TransactionId,
			Signature:        item.Signature,
			Service:          item.Service,
			Namespace:        item.Namespace,
			Operation:        item.Operation,
			Kind:             repo.TransactionKind(item.Kind),
			TriggeredClasses: classes,
			Offending:        item.Offending,
			PolicyID:         item.PolicyId,
			Occurrences:      item.Occurrences,
			MaxScore:         item.MaxScore,
			FirstSeen:        item.FirstSeen,
			LastSeen:         item.LastSeen,
			LastTraceID:      item.LastTraceId,
			Status:           repo.AnomalyIssueStatus(item.Status),
		}
	}
	return out
}

func classChipsFromFinding(finding apitypes.Finding) []signalChip {
	issue := repo.AnomalyIssue{
		Offending: finding.Offending,
	}
	for _, class := range finding.TriggeredClasses {
		issue.TriggeredClasses = append(issue.TriggeredClasses, repo.DeviationClass(class))
	}
	return classChips(issue)
}
