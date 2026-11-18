package postgres

import (
	"fmt"
	"strings"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// lastActivityExpr is a finding's newest timestamp: its last occurrence or the
// latest operator decision, whichever is newer.
func lastActivityExpr(alias string) string {
	return fmt.Sprintf(`GREATEST(%[1]s.last_seen, %[1]s.accepted_at, %[1]s.dismissed_at, %[1]s.reopened_at)`, alias)
}

// appendActivityWindowFilter bounds a finding's newest activity to the window.
func appendActivityWindowFilter(where *strings.Builder, a *args, f repo.FindingListFilter, alias string) {
	expr := lastActivityExpr(alias)
	where.WriteString(`WHERE ` + expr + ` >= ` + a.add(f.Since))
	if !f.Until.IsZero() {
		where.WriteString(` AND ` + expr + ` <= ` + a.add(f.Until))
	}
}

func appendTxnIdentityFilter(where *strings.Builder, a *args, f repo.FindingListFilter, alias string) {
	if f.Service != "" {
		where.WriteString(fmt.Sprintf(" AND %s.service = %s", alias, a.add(f.Service)))
	}
	if f.Namespace != "" {
		where.WriteString(fmt.Sprintf(" AND %s.namespace = %s", alias, a.add(f.Namespace)))
	}
}

// appendScopeKeyFilter matches a "namespace/service" scope key; a key with no
// slash is a bare service in the empty namespace.
func appendScopeKeyFilter(where *strings.Builder, a *args, f repo.FindingListFilter, col string) {
	if f.Namespace != "" {
		where.WriteString(fmt.Sprintf(` AND (CASE WHEN strpos(%[1]s, '/') > 0 THEN split_part(%[1]s, '/', 1) ELSE '' END) = %[2]s`, col, a.add(f.Namespace)))
	}
	if f.Service != "" {
		where.WriteString(fmt.Sprintf(` AND (CASE WHEN strpos(%[1]s, '/') > 0 THEN split_part(%[1]s, '/', 2) ELSE %[1]s END) = %[2]s`, col, a.add(f.Service)))
	}
}

// appendDecisionStatusFilter narrows findings to one lifecycle bucket, using
// the same rule as stickyDecisionStatus.
func appendDecisionStatusFilter(where *strings.Builder, alias, status, open, accepted, dismissed string) {
	switch status {
	case open:
		where.WriteString(fmt.Sprintf(` AND NOT (%s) AND NOT (%s)`, terminalExpr(alias), dismissedExpr(alias)))
	case accepted:
		where.WriteString(fmt.Sprintf(` AND (%s)`, terminalExpr(alias)))
	case dismissed:
		where.WriteString(fmt.Sprintf(` AND (%s)`, dismissedExpr(alias)))
	}
}

func appendAnomalyStatusFilter(where *strings.Builder, status string) {
	appendDecisionStatusFilter(where, "a", status,
		string(repo.AnomalyIssueOpen), string(repo.AnomalyIssueAccepted), string(repo.AnomalyIssueDismissed))
}

func appendGuardrailStatusFilter(where *strings.Builder, status string) {
	appendDecisionStatusFilter(where, "g", status,
		string(repo.GuardrailViolationOpen), string(repo.GuardrailViolationAccepted), string(repo.GuardrailViolationDismissed))
}

func terminalExpr(alias string) string {
	return fmt.Sprintf(`%[1]s.accepted_at > 'epoch' AND %[1]s.accepted_at >= %[1]s.dismissed_at AND %[1]s.accepted_at >= %[1]s.reopened_at`, alias)
}

func dismissedExpr(alias string) string {
	return fmt.Sprintf(`%[1]s.dismissed_at > 'epoch' AND %[1]s.dismissed_at > %[1]s.reopened_at AND NOT (%[2]s)`, alias, terminalExpr(alias))
}
