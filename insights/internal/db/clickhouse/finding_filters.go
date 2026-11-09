package clickhouse

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

const decisionEpochSQL = `toDateTime64('1970-01-01 00:00:00', 3)`

// anomalyWindowWhere narrows raw anomaly rows before aggregation. WHERE (not
// PREWHERE) so column names are never confused with SELECT aliases like
// max(last_seen) AS newest_seen.
const anomalyWindowWhere = `WHERE last_seen >= ? OR accepted_at >= ? OR dismissed_at >= ? OR reopened_at >= ?`

// guardrailWindowWhere narrows raw violation rows before aggregation.
const guardrailWindowWhere = `WHERE last_seen >= ? OR accepted_at >= ? OR dismissed_at >= ? OR reopened_at >= ?`

func sinceArgs(since any) []any {
	return []any{since, since, since, since}
}

// appendActivityWindowFilter adds the outer post-aggregation time bounds on the
// greatest(last_seen, decision timestamps) expression.
func appendActivityWindowFilter(where *strings.Builder, args *[]any, f repo.FindingListFilter, greatestExpr string) {
	where.WriteString(`WHERE `)
	where.WriteString(greatestExpr)
	where.WriteString(` >= ?`)
	*args = append(*args, f.Since)
	if !f.Until.IsZero() {
		where.WriteString(` AND `)
		where.WriteString(greatestExpr)
		where.WriteString(` <= ?`)
		*args = append(*args, f.Until)
	}
}

func appendTxnIdentityFilter(where *strings.Builder, args *[]any, f repo.FindingListFilter, alias string) {
	if f.Service != "" {
		where.WriteString(fmt.Sprintf(" AND %s.service = ?", alias))
		*args = append(*args, f.Service)
	}
	if f.Namespace != "" {
		where.WriteString(fmt.Sprintf(" AND %s.namespace = ?", alias))
		*args = append(*args, f.Namespace)
	}
}

func appendScopeKeyFilter(where *strings.Builder, args *[]any, f repo.FindingListFilter, alias string) {
	if f.Namespace != "" {
		where.WriteString(fmt.Sprintf(` AND (position(%[1]s, '/') > 0 ? splitByChar('/', %[1]s)[1] : '') = ?`, alias))
		*args = append(*args, f.Namespace)
	}
	if f.Service != "" {
		where.WriteString(fmt.Sprintf(` AND (position(%[1]s, '/') > 0 ? splitByChar('/', %[1]s)[2] : %[1]s) = ?`, alias))
		*args = append(*args, f.Service)
	}
}

func appendAnomalyStatusFilter(where *strings.Builder, status string) {
	switch status {
	case string(repo.AnomalyIssueOpen):
		where.WriteString(fmt.Sprintf(` AND NOT (%s) AND NOT (%s)`, anomalyTerminalExpr("a"), anomalyDismissedExpr("a")))
	case string(repo.AnomalyIssueAccepted):
		where.WriteString(fmt.Sprintf(` AND (%s)`, anomalyTerminalExpr("a")))
	case string(repo.AnomalyIssueDismissed):
		where.WriteString(fmt.Sprintf(` AND (%s)`, anomalyDismissedExpr("a")))
	}
}

func appendGuardrailStatusFilter(where *strings.Builder, status string) {
	switch status {
	case string(repo.GuardrailViolationOpen):
		where.WriteString(fmt.Sprintf(` AND NOT (%s) AND NOT (%s)`, guardrailTerminalExpr("g"), guardrailDismissedExpr("g")))
	case string(repo.GuardrailViolationAccepted):
		where.WriteString(fmt.Sprintf(` AND (%s)`, guardrailTerminalExpr("g")))
	case string(repo.GuardrailViolationDismissed):
		where.WriteString(fmt.Sprintf(` AND (%s)`, guardrailDismissedExpr("g")))
	}
}

func anomalyTerminalExpr(alias string) string {
	return fmt.Sprintf(`%[1]s.latest_accepted > %[2]s AND %[1]s.latest_accepted >= %[1]s.latest_dismissed AND %[1]s.latest_accepted >= %[1]s.latest_reopened`, alias, decisionEpochSQL)
}

func anomalyDismissedExpr(alias string) string {
	return fmt.Sprintf(`%[1]s.latest_dismissed > %[2]s AND %[1]s.latest_dismissed > %[1]s.latest_reopened AND NOT (%[3]s)`, alias, decisionEpochSQL, anomalyTerminalExpr(alias))
}

func guardrailTerminalExpr(alias string) string {
	return fmt.Sprintf(`%[1]s.latest_accepted > %[2]s AND %[1]s.latest_accepted >= %[1]s.latest_dismissed AND %[1]s.latest_accepted >= %[1]s.latest_reopened`, alias, decisionEpochSQL)
}

func guardrailDismissedExpr(alias string) string {
	return fmt.Sprintf(`%[1]s.latest_dismissed > %[2]s AND %[1]s.latest_dismissed > %[1]s.latest_reopened AND NOT (%[3]s)`, alias, decisionEpochSQL, guardrailTerminalExpr(alias))
}
