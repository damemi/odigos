package debugui

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Findings tabs are the lifecycle buckets SHARED by both finding kinds: a policy
// violation's accept/dismiss and an anomaly's accept/dismiss fold into the same
// three buckets, so the tabs (and their counts) speak to the whole feed rather
// than to violations alone. "accepted" is the shared affirmative term: it unifies
// a violation's allowlist decision and an anomaly's accept-as-baseline (both mean
// "this is now normal").
const (
	findingsOpen      = "open"
	findingsDismissed = "dismissed"
	findingsAccepted  = "accepted"
)

// findingCounts is the per-tab tally across BOTH kinds, so a tab shows the real
// number of findings (violations + anomaly issues), not the number of services.
type findingCounts struct {
	Open      int
	Dismissed int
	Accepted  int
}

// listAnomalies is the HTTP handler for GET /debug/anomalies (the Findings feed).
// It mirrors the product UI's Findings page: one unified GET /api/v1/findings
// call (anomalies + policy violations, ranked worst-first by the server) rendered
// as a flat list, with the open/dismissed/accepted tabs bucketed client-side from
// that same feed. Each row carries its render-ready summary from the API and the
// kind-specific drill-down keys the product UI uses for investigate/resolve.
func (u *ui) listAnomalies(c *gin.Context) {
	ctx := c.Request.Context()
	timeRange, err := findingsTimeRangeFromRequest(c)
	if err != nil {
		u.render(c, "anomalies", "anomalies", "", map[string]any{"Error": err.Error()})
		return
	}
	tab := normalizeFindingsTab(c.Query("tab"))
	baseQuery := url.Values{}
	if tab != "" && tab != findingsOpen {
		baseQuery.Set("tab", tab)
	}
	rangeView := findingsTimeRangeViewFor("/debug/anomalies", baseQuery, timeRange)

	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	var loadErr string
	var all []findingRow
	if listed, err := api.ListFindings(ctx, listFindingsParamsFromRequest(c)); err != nil {
		loadErr = err.Error()
	} else {
		all = findingRowsFromWire(listed.Items)
	}

	tabQuery := findingsTimeRangeQuery(timeRange)
	data := map[string]any{
		"Findings":     filterFindingRowsForTab(all, tab),
		"Tab":          tab,
		"Counts":       countFindingRows(all),
		"TimeRange":    rangeView,
		"TabOpen":      pathWithQuery("/debug/anomalies", mergeQuery(tabQuery, url.Values{"tab": {"open"}})),
		"TabDismissed": pathWithQuery("/debug/anomalies", mergeQuery(tabQuery, url.Values{"tab": {"dismissed"}})),
		"TabAccepted":  pathWithQuery("/debug/anomalies", mergeQuery(tabQuery, url.Values{"tab": {"accepted"}})),
	}
	if loadErr != "" {
		data["Error"] = "Couldn't load findings (try refresh): " + loadErr
	}
	u.render(c, "anomalies", "anomalies", c.Query("flash"), data)
}

// findingRow is one row of the unified Findings feed, mapped straight from the
// product Finding wire type so the debug UI shows exactly what the product UI
// gets from GET /api/v1/findings. Both kinds share the visible columns; the
// kind-specific identity keys (transaction_id+signature for anomalies,
// scope_key+rule_key+offending for violations) ride along so investigate and
// accept/dismiss resolve the same way the product UI does.
type findingRow struct {
	Kind        string
	Service     string
	Namespace   string
	Title       string
	Summary     string
	Severity    string
	Score       int
	Occurrences int64
	LastSeen    time.Time
	Status      string
	// Tab is the lifecycle bucket derived from Status, so the tabs filter one
	// feed client-side the way the product UI does instead of querying per tab.
	Tab string

	TransactionID int64
	Signature     string
	ScopeKey      string
	RuleKey       string
	Offending     string
}

func findingRowsFromWire(items []apitypes.Finding) []findingRow {
	out := make([]findingRow, len(items))
	for i, f := range items {
		out[i] = findingRow{
			Kind:          string(f.Kind),
			Service:       f.Service,
			Namespace:     f.Namespace,
			Title:         f.Title,
			Summary:       f.Summary,
			Severity:      string(f.Severity),
			Score:         f.Score,
			Occurrences:   f.Occurrences,
			LastSeen:      f.LastSeen,
			Status:        f.Status,
			Tab:           findingTab(f.Status),
			TransactionID: f.TransactionId,
			Signature:     f.Signature,
			ScopeKey:      f.ScopeKey,
			RuleKey:       f.RuleKey,
			Offending:     f.Offending,
		}
	}
	return out
}

// findingTab maps a finding's status to the shared lifecycle bucket. Anomalies
// and violations use the same status literals, and anything not explicitly
// accepted/dismissed is open - the same rule as the product UI's isOpenFinding.
func findingTab(status string) string {
	switch status {
	case findingsAccepted:
		return findingsAccepted
	case findingsDismissed:
		return findingsDismissed
	default:
		return findingsOpen
	}
}

// normalizeFindingsTab clamps ?tab= to a known bucket, defaulting to the
// actionable "open".
func normalizeFindingsTab(s string) string {
	switch s {
	case findingsDismissed, findingsAccepted:
		return s
	default:
		return findingsOpen
	}
}

func countFindingRows(rows []findingRow) findingCounts {
	var c findingCounts
	for _, r := range rows {
		switch r.Tab {
		case findingsAccepted:
			c.Accepted++
		case findingsDismissed:
			c.Dismissed++
		default:
			c.Open++
		}
	}
	return c
}

// filterFindingRowsForTab keeps the tab's rows in the feed's server-side ranking.
func filterFindingRowsForTab(rows []findingRow, tab string) []findingRow {
	out := make([]findingRow, 0, len(rows))
	for _, r := range rows {
		if r.Tab == tab {
			out = append(out, r)
		}
	}
	return out
}

// --- anomaly issue actions -------------------------------------------------

// anomalyIssueFormKeys pulls the (transaction_id, signature) identity of an
// anomaly issue from a POST form. Returns ok=false (after failing the action)
// when either part is missing.
func anomalyIssueFormKeys(c *gin.Context, redirect string) (txnID int64, signature string, ok bool) {
	signature = strings.TrimSpace(c.PostForm("signature"))
	id, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("transaction_id")), 10, 64)
	if err != nil || signature == "" {
		actionFail(c, redirect, "missing anomaly identity (transaction or signature)")
		return 0, "", false
	}
	return id, signature, true
}

// acceptAnomaly folds an issue's offending trace into the baseline so it stops
// alerting, then marks the issue accepted (sticky).
func (u *ui) acceptAnomaly(c *gin.Context) {
	redirect := findingsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	txnID, signature, ok := anomalyIssueFormKeys(c, redirect)
	if !ok {
		return
	}
	if err := api.ResolveAnomalyIssue(c.Request.Context(), txnID, signature, "accept"); err != nil {
		actionFail(c, redirect, "accept failed: "+err.Error())
		return
	}
	actionOK(c, redirect, "accepted as baseline - this behaviour is now normal and won't re-alert")
}

// dismissAnomaly closes an issue without changing the baseline; it moves to the
// Dismissed tab and its count keeps rising if the pattern still fires.
func (u *ui) dismissAnomaly(c *gin.Context) {
	redirect := findingsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	txnID, signature, ok := anomalyIssueFormKeys(c, redirect)
	if !ok {
		return
	}
	if err := api.ResolveAnomalyIssue(c.Request.Context(), txnID, signature, "dismiss"); err != nil {
		actionFail(c, redirect, "dismiss failed: "+err.Error())
		return
	}
	actionOK(c, redirect, "dismissed - closed without changing the baseline; moved to the Dismissed tab")
}

// reopenAnomaly moves a dismissed issue back to Open for re-triage.
func (u *ui) reopenAnomaly(c *gin.Context) {
	redirect := findingsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	txnID, signature, ok := anomalyIssueFormKeys(c, redirect)
	if !ok {
		return
	}
	if err := api.ResolveAnomalyIssue(c.Request.Context(), txnID, signature, "reopen"); err != nil {
		actionFail(c, redirect, "reopen failed: "+err.Error())
		return
	}
	actionOK(c, redirect, "reopened - moved back to the Open tab")
}

// acceptAnomalies / dismissAnomalies apply a bulk action to the checkbox-selected
// issues of one transaction, then return to the same filtered view. transaction_id
// identifies the transaction; the selected signatures come from "signature".
func (u *ui) acceptAnomalies(c *gin.Context) {
	u.bulkAnomalyAction(c, "accepted", "these behaviours are now normal and won't re-alert", "accept")
}

func (u *ui) dismissAnomalies(c *gin.Context) {
	u.bulkAnomalyAction(c, "dismissed", "closed without changing the baseline", "dismiss")
}

// bulkAnomalyAction resolves every selected signature of one transaction,
// tolerating per-issue failures (an accept can fail if the sample aged out) and
// reporting a summary. pastVerb/effect shape the flash.
func (u *ui) bulkAnomalyAction(c *gin.Context, pastVerb, effect, resolution string) {
	redirect := findingsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	txnID, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("transaction_id")), 10, 64)
	if err != nil {
		actionFail(c, redirect, "missing transaction")
		return
	}
	sigs := c.PostFormArray("signature")
	if len(sigs) == 0 {
		actionFail(c, redirect, "no anomalies selected")
		return
	}
	ctx := c.Request.Context()
	var done, failed int
	var lastErr string
	for _, sig := range sigs {
		if strings.TrimSpace(sig) == "" {
			failed++
			continue
		}
		if err := api.ResolveAnomalyIssue(ctx, txnID, sig, resolution); err != nil {
			failed++
			lastErr = err.Error()
			continue
		}
		done++
	}
	if failed > 0 {
		msg := fmt.Sprintf("%s %d, %d failed", pastVerb, done, failed)
		if lastErr != "" {
			msg += ": " + lastErr
		}
		actionFail(c, redirect, msg)
		return
	}
	actionOK(c, redirect, fmt.Sprintf("%s %d anomal%s - %s", pastVerb, done, pluralY(done), effect))
}

// pluralY returns the plural suffix for the "anomaly/anomalies" wording.
func pluralY(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// violationFormKeys pulls the (scope_key, rule_key, offending) identity of a
// policy-violation group from a POST form, the key the aggregate counter is
// resolved by. Returns ok=false (after redirecting with an error) when any part
// is missing.
func violationFormKeys(c *gin.Context, redirect string) (scopeKey string, rule repo.RuleKey, offending string, ok bool) {
	scopeKey = strings.TrimSpace(c.PostForm("scope_key"))
	rule = repo.RuleKey(strings.TrimSpace(c.PostForm("rule_key")))
	offending = strings.TrimSpace(c.PostForm("offending"))
	if scopeKey == "" || rule == "" || offending == "" {
		actionFail(c, redirect, "missing violation identity (service, rule, or item)")
		return "", "", "", false
	}
	return scopeKey, rule, offending, true
}

// acceptViolation resolves a policy-violation group by accepting the offending
// item as legitimate: it adds the item to the matching rule's allowlist and
// closes the finding, so the once-forbidden edge stops firing and stays closed.
// Keyed by (scope_key, rule_key, offending), not a per-trace id.
func (u *ui) acceptViolation(c *gin.Context) {
	redirect := findingsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scopeKey, ruleKey, offending, ok := violationFormKeys(c, redirect)
	if !ok {
		return
	}
	if err := api.AcceptGuardrailViolation(c.Request.Context(), scopeKey, ruleKey, offending); err != nil {
		actionFail(c, redirect, "accept failed: "+err.Error())
		return
	}
	actionOK(c, redirect, fmt.Sprintf("accepted %q on %s - this edge is now normal and won't re-alert", offending, scopeKey))
}

// dismissViolation closes a policy-violation group WITHOUT allowlisting it: the
// rule stays enforced and the group moves to the Dismissed tab, where its count
// keeps rising if the edge fires. The dismissal is sticky (it won't return to
// Open on its own); reopenViolation brings it back for re-triage.
func (u *ui) dismissViolation(c *gin.Context) {
	redirect := findingsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scopeKey, ruleKey, offending, ok := violationFormKeys(c, redirect)
	if !ok {
		return
	}
	if err := api.ResolveGuardrailViolation(c.Request.Context(), scopeKey, ruleKey, offending, repo.GuardrailResolveDismiss); err != nil {
		actionFail(c, redirect, "dismiss failed: "+err.Error())
		return
	}
	actionOK(c, redirect, fmt.Sprintf("dismissed %q on %s - moved to the Dismissed tab", offending, scopeKey))
}

// reopenViolation moves a dismissed policy-violation group back to Open for
// re-triage, without allowlisting it. Used to undo a dismiss or to re-surface a
// muted violation that has become a sustained problem.
func (u *ui) reopenViolation(c *gin.Context) {
	redirect := findingsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scopeKey, ruleKey, offending, ok := violationFormKeys(c, redirect)
	if !ok {
		return
	}
	if err := api.ResolveGuardrailViolation(c.Request.Context(), scopeKey, ruleKey, offending, repo.GuardrailResolveReopen); err != nil {
		actionFail(c, redirect, "reopen failed: "+err.Error())
		return
	}
	actionOK(c, redirect, fmt.Sprintf("reopened %q on %s - moved back to the Open tab", offending, scopeKey))
}

// findingsRedirect is the Findings URL to return to after an action, preserving
// the tab the operator was on (?tab=). Used only for the no-JS fallback; the JS
// path resolves the row in place without navigating.
func findingsRedirect(c *gin.Context) string {
	return "/debug/anomalies?tab=" + normalizeFindingsTab(c.PostForm("tab"))
}

// addQuery appends key=value to a URL, choosing ? or & so a URL that already
// carries a query (e.g. the Findings tab) isn't corrupted.
func addQuery(u, key, val string) string {
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + key + "=" + url.QueryEscape(val)
}

// fetchWants reports whether the caller is the Findings JS (it sets
// X-Requested-With: fetch). Such a request wants a small status reply it can act
// on in place, not a 303 redirect to a freshly-rendered full page.
func fetchWants(c *gin.Context) bool { return c.GetHeader("X-Requested-With") == "fetch" }

// actionOK ends a successful action: 204 for a fetch (the JS grays the row in
// place), otherwise a redirect back to the feed with a flash.
func actionOK(c *gin.Context, redirect, flash string) {
	if fetchWants(c) {
		c.Status(http.StatusNoContent)
		return
	}
	c.Redirect(http.StatusSeeOther, addQuery(redirect, "flash", flash))
}

// actionFail ends a failed action: a plain-text error the JS surfaces verbatim
// (422 so r.ok is false), otherwise a redirect back to the feed with ?error=.
func actionFail(c *gin.Context, redirect, msg string) {
	if fetchWants(c) {
		c.String(http.StatusUnprocessableEntity, "%s", msg)
		return
	}
	c.Redirect(http.StatusSeeOther, addQuery(redirect, "error", msg))
}

// violationEvidence is the HTTP handler for GET /debug/guardrails/violations/evidence.
// It consumes GetGuardrailViolationDetail - the same investigate payload the
// product UI uses - and maps wire fields into the HTML template.
func (u *ui) violationEvidence(c *gin.Context) {
	scopeKey := strings.TrimSpace(c.Query("scope_key"))
	ruleKey := repo.RuleKey(strings.TrimSpace(c.Query("rule_key")))
	offending := strings.TrimSpace(c.Query("offending"))
	if scopeKey == "" || ruleKey == "" || offending == "" {
		c.String(http.StatusBadRequest, "missing scope_key, rule_key, or offending")
		return
	}

	detail, ok, err := u.getGuardrailViolationDetail(c.Request.Context(), scopeKey, ruleKey, offending)
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	if !ok {
		c.String(http.StatusNotFound, "guardrail violation not found")
		return
	}

	data := map[string]any{
		"ScopeKey":  detail.ScopeKey,
		"Namespace": detail.Namespace,
		"Service":   detail.Service,
		"RuleKey":   detail.RuleKey,
		"RuleLabel": detail.RuleLabel,
		"Offending": detail.Offending,
		"Summary":   detail.Summary,
	}

	if detail.EvidenceTrace != nil && len(detail.EvidenceTrace.RawOtlp) > 0 {
		rows := traceSpans(detail.EvidenceTrace.RawOtlp)
		highlights := map[string]engine.SpanHighlight{}
		for _, h := range detail.SpanHighlights {
			highlights[h.SpanId] = engine.SpanHighlight{SpanID: h.SpanId, Severity: h.Severity, Reason: h.Reason}
		}
		for i := range rows {
			if h, ok := highlights[rows[i].SpanID]; ok {
				rows[i].Highlight = h.Severity
				rows[i].HighlightReason = h.Reason
			}
		}
		data["Rows"] = rows
		data["TraceID"] = detail.EvidenceTrace.TraceId
		data["ObservedAt"] = detail.EvidenceTrace.ObservedAt
	}

	u.render(c, "guardrail_evidence", "anomalies", "", data)
}
