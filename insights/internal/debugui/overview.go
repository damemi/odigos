package debugui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// overviewTopN caps rows in each Overview summary table; the full lists live on
// the dedicated Findings and Transactions pages (reached via the view-all link).
const overviewTopN = 8

// overviewFinding is one row of the Overview "Top findings" table: a policy
// violation or an anomaly issue flattened to a shared shape so both kinds rank
// together by severity. Kind is "Anomaly" or "Violation".
type overviewFinding struct {
	Kind        string
	Service     string
	Namespace   string
	Detail      string       // anomaly: operation; violation: rule label
	Offending   string       // raw offending string, rendered as chips by the view
	Chips       []signalChip // anomaly: one chip per triggered class (nil for violations)
	Score       int
	Severity    string
	Events      int64
	LastSeen    time.Time
	Investigate string // drill-down URL: anomaly → issue compare, violation → evidence
}

// signalChip is a compact, hover-explained label for one contributing signal in
// a findings row. Tone (hi/mid/lo, from the class's default weight) drives color
// so a heavier class reads at a glance; Tip is the "why" shown on hover.
type signalChip struct {
	Label string
	Tip   string
	Tone  string
}

// classChips renders one chip per triggered deviation class of an anomaly issue,
// so a D7-only finding and a D7+D3 one look different at a glance. Registered as a
// template func (used by anomalies.html) and also called here for the Overview
// "Top findings" rows. The tooltip is the class's catalog description plus its
// pretty-printed offending value(s) when present.
func classChips(issue repo.AnomalyIssue) []signalChip {
	valsByClass := map[string][]string{}
	for _, seg := range strings.Split(issue.Offending, "; ") {
		if classID, values, ok := strings.Cut(strings.TrimSpace(seg), "="); ok {
			valsByClass[classID] = prettyOffendingItems(values)
		}
	}
	chips := make([]signalChip, 0, len(issue.TriggeredClasses))
	for _, class := range issue.TriggeredClasses {
		tip := risk.ClassDesc(class)
		if items := valsByClass[string(class)]; len(items) > 0 {
			tip = strings.TrimSpace(tip + " · " + strings.Join(items, ", "))
		}
		chips = append(chips, signalChip{Label: risk.ClassLabel(class), Tip: tip, Tone: chipTone(class)})
	}
	return chips
}

// chipTone buckets a class by its default catalog weight for coloring.
func chipTone(class repo.DeviationClass) string {
	w, _ := risk.DefaultWeight(class)
	switch {
	case w >= 35:
		return "hi"
	case w >= 25:
		return "mid"
	default:
		return "lo"
	}
}

// index is the HTTP handler for GET /debug: it renders the Overview - a Findings
// summary (metric boxes + top findings) and a Transactions summary (metric boxes
// + busiest transactions), each with a view-all link to its dedicated page.
// Read-only, cold path.
func (u *ui) index(c *gin.Context) {
	ctx := c.Request.Context()
	timeRange, err := findingsTimeRangeFromRequest(c)
	if err != nil {
		u.render(c, "index", "index", "", map[string]any{"Error": err.Error()})
		return
	}
	rangeView := findingsTimeRangeViewFor("/debug", nil, timeRange)

	params := listFindingsParamsFromRequest(c)
	params.Status = apitypes.StatusFilter(repo.AnomalyIssueOpen)

	topFindings, openFindings, criticalHigh, openViolations, openAnomalies, findingsErr := u.overviewFindings(ctx, params)

	services, _ := u.repo.ListServiceStats(ctx, 1000)
	serviceCount := len(services)
	transactionCount := 0
	var volumeTotal int64
	for _, s := range services {
		transactionCount += int(s.TransactionCount)
		volumeTotal += s.Volume
	}
	// One aggregate query backs both the promotion metric and the busiest
	// table's baseline column, avoiding an N+1 baseline read per transaction.
	var promotedClasses, totalClasses int
	if stats, err := u.repo.ListBaselinePromotion(ctx); err == nil {
		for _, p := range stats {
			promotedClasses += p.Promoted
			totalClasses += p.Total
		}
	}
	promotedPct := 0
	if totalClasses > 0 {
		promotedPct = promotedClasses * 100 / totalClasses
	}
	busiest := u.busiestTransactions(ctx, overviewTopN)

	viewAllFindings := "/debug/anomalies"
	if rangeView.Query != "" {
		viewAllFindings += "?" + rangeView.Query
	}

	data := map[string]any{
		"OpenFindings":     openFindings,
		"CriticalHigh":     criticalHigh,
		"PolicyViolations": openViolations,
		"GroupedAnomalies": openAnomalies,
		"TopFindings":      topFindings,

		"TransactionCount": transactionCount,
		"ServiceCount":     serviceCount,
		"VolumeTotal":      volumeTotal,
		"PromotedPct":      promotedPct,
		"Busiest":          busiest,

		"TimeRange":       rangeView,
		"ViewAllFindings": viewAllFindings,
	}
	if findingsErr != "" {
		data["Error"] = "Couldn't load some findings (try refresh): " + findingsErr
	}
	u.render(c, "index", "index", c.Query("flash"), data)
}

// overviewFindings loads open findings via ProductAPI.ListFindings (same contract
// as GET /api/v1/findings) and folds them into Overview metric boxes + top rows.
func (u *ui) overviewFindings(ctx context.Context, params apitypes.ListFindingsParams) (top []overviewFinding, open, criticalHigh, openViolations, openAnomalies int, loadErr string) {
	api := u.api.Load()
	if api == nil || *api == nil {
		return nil, 0, 0, 0, 0, "product API not ready"
	}
	listed, err := (*api).ListFindings(ctx, params)
	if err != nil {
		return nil, 0, 0, 0, 0, err.Error()
	}

	findings := make([]overviewFinding, 0, len(listed.Items))
	for _, item := range listed.Items {
		if item.Status != string(repo.AnomalyIssueOpen) && item.Status != string(repo.GuardrailViolationOpen) {
			continue
		}
		open++
		if isHighOrAbove(item.Score) {
			criticalHigh++
		}
		row := overviewFinding{
			Service:   item.Service,
			Namespace: item.Namespace,
			Detail:    item.Title,
			Offending: item.Offending,
			Score:     item.Score,
			Severity:  string(item.Severity),
			Events:    item.Occurrences,
			LastSeen:  item.LastSeen,
		}
		switch item.Kind {
		case apitypes.Violation:
			openViolations++
			row.Kind = "Violation"
			ev := url.Values{"scope_key": {item.ScopeKey}, "rule_key": {item.RuleKey}, "offending": {item.Offending}}
			row.Investigate = "/debug/guardrails/violations/evidence?" + ev.Encode()
		default:
			openAnomalies++
			row.Kind = "Anomaly"
			row.Chips = classChipsFromFinding(item)
			iv := url.Values{
				"transaction_id": {strconv.FormatInt(item.TransactionId, 10)},
				"signature":      {item.Signature},
			}
			row.Investigate = "/debug/anomalies/issue?" + iv.Encode()
		}
		findings = append(findings, row)
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Score != findings[j].Score {
			return findings[i].Score > findings[j].Score
		}
		return findings[i].LastSeen.After(findings[j].LastSeen)
	})
	if len(findings) > overviewTopN {
		findings = findings[:overviewTopN]
	}
	return findings, open, criticalHigh, openViolations, openAnomalies, ""
}

// busiestTransactions returns the API's top-volume transaction summaries,
// including the same promotion state shown on the full Transactions page.
func (u *ui) busiestTransactions(ctx context.Context, n int) []repo.TransactionStat {
	stats, err := u.listTransactionStats(ctx, apitypes.ListTransactionsParams{})
	if err != nil {
		return nil
	}
	if n > 0 && len(stats) > n {
		stats = stats[:n]
	}
	return stats
}

// isHighOrAbove reports whether a risk score lands in the high or critical tier,
// the "needs attention now" cut used by the Critical/High metric box.
func isHighOrAbove(score int) bool {
	s := risk.SeverityForScore(score)
	return s == risk.SeverityHigh || s == risk.SeverityCritical
}

// humanCount renders a large count compactly (1234 → 1.2k, 3400000 → 3.4M) for
// the metric boxes and volume column; smaller numbers print as-is.
func humanCount(n any) string {
	v := toInt64(n)
	scaled := func(x float64, suffix string) string {
		return strings.TrimSuffix(fmt.Sprintf("%.1f", x), ".0") + suffix
	}
	switch {
	case v >= 1_000_000:
		return scaled(float64(v)/1e6, "M")
	case v >= 1_000:
		return scaled(float64(v)/1e3, "k")
	default:
		return fmt.Sprintf("%d", v)
	}
}

func toInt64(n any) int64 {
	switch x := n.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case uint64:
		if x > uint64(^uint64(0)>>1) {
			return int64(^uint64(0) >> 1)
		}
		return int64(x)
	case float64:
		return int64(x)
	default:
		return 0
	}
}
