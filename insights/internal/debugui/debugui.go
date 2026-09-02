// Package debugui mounts a tiny server-rendered HTML inspector under /debug
// on the same Gin engine the production HTTP API uses.
//
// It exists for developer debugging only — there is no auth, no pagination,
// no rate limiting. The Helm chart never sets DEBUG_UI=true; this package is
// gated entirely by the caller.
//
// All reads go through the same repo.Repository the engine uses, so what
// you see here is exactly what the engine sees at scoring time. A single
// write path exists for operator actions: manually promoting a baseline
// class out of the learning phase.
package debugui

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

//go:embed templates/*.html
var templatesFS embed.FS

// ProductAPI is the product HTTP service surface. Debug UI reads/writes call
// these methods — the same ones GET/PUT handlers use — so the frontend and
// debug UI share one contract instead of talking to the repository directly.
type ProductAPI interface {
	LoadSystemSettings(ctx context.Context) (apitypes.SystemSettings, error)
	ApplySystemSettings(ctx context.Context, body apitypes.SystemSettings) error
	LoadStorageHealth(ctx context.Context) (apitypes.StorageHealth, error)

	ListLearningPolicies(ctx context.Context) ([]repo.LearningPolicy, error)
	UpsertLearningPolicy(ctx context.Context, p repo.LearningPolicy) error
	DeleteLearningPolicy(ctx context.Context, class repo.DeviationClass, scope repo.PolicyScope, scopeKey string) error

	ListAlertPolicies(ctx context.Context) ([]repo.Policy, error)
	UpsertAlertPolicy(ctx context.Context, p repo.Policy) error
	DeleteAlertPolicy(ctx context.Context, scope repo.PolicyScope, scopeKey string) error

	ListServiceGuardrails(ctx context.Context) ([]repo.ServiceGuardrail, error)
	UpsertServiceGuardrail(ctx context.Context, g repo.ServiceGuardrail) error
	DeleteServiceGuardrail(ctx context.Context, scopeKey string) error
	SeedServiceGuardrail(ctx context.Context, req apitypes.GuardrailSeedRequest) error
	EnableTransactionGuardrail(ctx context.Context, namespace, service string) error
	DisableTransactionGuardrail(ctx context.Context, namespace, service string) error
	ForcePromoteService(ctx context.Context, namespace, service string) error
	AcceptGuardrailViolation(ctx context.Context, scopeKey string, ruleKey repo.RuleKey, offending string) error
	ResolveGuardrailViolation(ctx context.Context, scopeKey string, ruleKey repo.RuleKey, offending string, resolution repo.GuardrailResolution) error

	PromoteBaselineClass(ctx context.Context, txID int64, class repo.DeviationClass) error
	PromoteTransactionBaselines(ctx context.Context, txID int64) error
	ResetBaselineClass(ctx context.Context, txID int64, class repo.DeviationClass) error
	ResetTransactionBaselines(ctx context.Context, txID int64) error

	ResolveAnomalyIssue(ctx context.Context, txID int64, signature, resolution string) error
	LiveSystemSettings() repo.SystemSettings

	// Observations: same paths as GET …/observations and GET …/observations/{trace_id}.
	ListObservations(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error)
	GetObservation(ctx context.Context, transactionID int64, traceID string) (repo.Observation, bool, error)

	// Findings / anomalies / violations: same OpenAPI params and wire types as
	// GET /findings, GET /anomalies, GET /guardrails/violations.
	ListFindings(ctx context.Context, params apitypes.ListFindingsParams) (apitypes.FindingList, error)
	ListAnomalies(ctx context.Context, params apitypes.ListAnomaliesParams) (apitypes.AnomalyIssueList, error)
	// GetAnomalyIssueDetail is GET /anomalies/{transaction_id}/{signature} — the
	// full investigate payload (class_findings + compare traces).
	GetAnomalyIssueDetail(ctx context.Context, transactionID int64, signature string) (apitypes.AnomalyIssue, bool, error)

	// Transactions: same OpenAPI params/wire types as GET/DELETE/POST …/transactions
	// (including bulk delete/promote and per-txn baselines/promote).
	ListTransactions(ctx context.Context, params apitypes.ListTransactionsParams) (apitypes.TransactionStatList, error)
	GetTransaction(ctx context.Context, transactionID int64) (repo.Transaction, bool, error)
	DeleteTransaction(ctx context.Context, transactionID int64) error
	DeleteTransactions(ctx context.Context, transactionIDs []int64) (deleted int, err error)
	PromoteTransactions(ctx context.Context, transactionIDs []int64) (promoted int, err error)
	ListGuardrailViolations(ctx context.Context, params apitypes.ListGuardrailViolationsParams) (apitypes.GuardrailViolationList, error)
	// GetGuardrailViolationDetail is GET /guardrails/violations/evidence — the
	// investigate payload (summary + evidence trace).
	GetGuardrailViolationDetail(ctx context.Context, scopeKey string, ruleKey repo.RuleKey, offending string) (apitypes.GuardrailViolationDetail, bool, error)
}

// Handle is returned by Register so the caller can attach dependencies once
// the engine / product API are built.
type Handle struct{ ui *ui }

// SetProductAPI wires the shared product API used by the real UI. Required for
// mutating debug actions and for shared list/assess helpers.
func (h *Handle) SetProductAPI(api ProductAPI) {
	h.ui.api.Store(&api)
}

// Register attaches /debug/* routes to the given engine. Idempotent on a
// fresh engine; calling twice on the same engine will panic because of Gin's
// route-conflict rule. The returned Handle lets the caller supply the
// product API (SetProductAPI) after the engine is constructed.
func Register(engine *gin.Engine, r repo.Repository) *Handle {
	ui := &ui{
		repo:      r,
		templates: parseTemplates(),
	}

	g := engine.Group("/debug")
	g.GET("", ui.index)
	g.GET("/transactions", ui.listTransactions)
	g.GET("/transactions/:id", ui.transactionDetail)
	// HTML fragment: waterfall for one retained sample (loaded on "view trace").
	g.GET("/transactions/:id/traces/:trace_id", ui.transactionTraceFragment)
	g.POST("/transactions/delete-bulk", ui.deleteTransactions)
	g.POST("/transactions/promote-bulk", ui.promoteTransactions)
	g.POST("/transactions/:id/delete", ui.deleteTransaction)
	g.POST("/transactions/:id/promote", ui.promoteTransaction)
	g.POST("/transactions/:id/baseline/promote", ui.promoteBaselineClass)
	g.POST("/transactions/:id/baseline/reset", ui.resetBaselineClass)
	g.POST("/transactions/:id/baseline/reset-all", ui.resetTransactionBaselines)
	g.GET("/anomalies", ui.listAnomalies)
	g.GET("/anomalies/issue", ui.compareAnomalyIssue)
	g.POST("/anomalies/accept", ui.acceptAnomaly)
	g.POST("/anomalies/dismiss", ui.dismissAnomaly)
	g.POST("/anomalies/reopen", ui.reopenAnomaly)
	g.POST("/anomalies/accept-bulk", ui.acceptAnomalies)
	g.POST("/anomalies/dismiss-bulk", ui.dismissAnomalies)
	// Settings area: the tuning knobs and rule authoring, all under one nav
	// entry. The "threat detection" group holds detection policies + guardrail
	// rules; guardrail *violations* live in the Findings feed, not here.
	g.GET("/settings", ui.settingsIndex)
	g.GET("/settings/learning", ui.listLearningPolicies)
	g.POST("/settings/learning/:class", ui.saveLearningPolicy)
	g.POST("/settings/learning/:class/override", ui.addLearningOverride)
	g.POST("/settings/learning/:class/delete", ui.deleteLearningPolicy)
	g.GET("/settings/detection", ui.listAnomalyPolicies)
	g.POST("/settings/detection/save", ui.saveAnomalyPolicy)
	g.POST("/settings/detection/override", ui.addAnomalyOverride)
	g.POST("/settings/detection/delete", ui.deleteAnomalyPolicy)
	g.GET("/settings/guardrails", ui.listGuardrails)
	g.POST("/settings/guardrails/save", ui.saveGuardrail)
	g.POST("/settings/guardrails/seed", ui.seedGuardrail)
	g.POST("/settings/guardrails/delete", ui.deleteGuardrail)
	g.GET("/settings/risk-model", ui.settingsRiskModel)
	g.GET("/settings/system", ui.settingsSystem)
	g.POST("/settings/system", ui.saveSystemSettings)
	g.GET("/settings/storage", ui.settingsStorage)
	g.POST("/services/transaction-guardrail/enable", ui.enableTransactionGuardrail)
	g.POST("/services/transaction-guardrail/disable", ui.disableTransactionGuardrail)
	g.POST("/services/transaction-guardrail/force-promote", ui.forcePromoteServiceTransactions)
	// Guardrail violation actions are Findings-feed operations, kept on their
	// own path since the Findings page posts to them.
	g.POST("/guardrails/violations/accept", ui.acceptViolation)
	g.POST("/guardrails/violations/dismiss", ui.dismissViolation)
	g.POST("/guardrails/violations/reopen", ui.reopenViolation)
	g.GET("/guardrails/violations/evidence", ui.violationEvidence)

	return &Handle{ui: ui}
}

type ui struct {
	repo      repo.Repository
	templates map[string]*template.Template
	// api is the product HTTP service surface; set after the HTTP API handle
	// is constructed so debug mutations share that code path.
	api atomic.Pointer[ProductAPI]
}

func (u *ui) productAPI(c *gin.Context) (ProductAPI, bool) {
	if p := u.api.Load(); p != nil && *p != nil {
		return *p, true
	}
	c.String(http.StatusServiceUnavailable, "product API not ready yet, try again in a moment")
	return nil, false
}

// parseTemplates loads every page template alongside the base layout.
// Templates use {{define "title"}} and {{define "content"}} blocks, both
// referenced from base.html.
func parseTemplates() map[string]*template.Template {
	pages := []string{
		"index",
		"services",
		"transactions",
		"transaction_detail",
		"anomalies",
		"anomaly_compare",
		"anomaly_policies",
		"learning_policies",
		"settings_risk_model",
		"settings_system",
		"settings_storage",
		"guardrails",
		"guardrail_evidence",
	}
	funcs := template.FuncMap{
		"prettyJSON":        prettyJSON,
		"histBars":          histBars,
		"traceSpans":        traceSpans,
		"humanTime":         humanTime,
		"humanBytes":        humanBytes,
		"humanDuration":     humanDuration,
		"metricValue":       metricValue,
		"overPct":           overPct,
		"conditionText":     conditionText,
		"conditionsText":    conditionsText,
		"sinceChange":       sinceChange,
		"policyEditJSON":    policyEditJSON,
		"anomalyPolicyJSON": anomalyPolicyJSON,
		"anomalyPolicyText": anomalyPolicyText,
		"defaultWeight":     defaultWeight,
		"classLabel":         risk.ClassLabel,
		"classDesc":          risk.ClassDesc,
		"classBaselineLabel": risk.ClassBaselineLabel,
		"classBaselineDesc":  risk.ClassBaselineDesc,
		"sub": func(a, b int) int { return a - b },
		"classCategory":      classCategory,
		"classMitre":        classMitre,
		"enrichersForClass": risk.EnrichersForClass,
		"scoreBand":         scoreBand,
		"enricherList":      enricherListText,
		"tagDescription":    risk.TagDescription,
		"dict":              templateDict,
		"severityForScore":  severityForScore,
		"humanCount":        humanCount,
		"offendingChips":    offendingChips,
		"classChips":        classChips,
		"operationDisplay":  operationDisplay,
		"identityDimLabel":  identityDimLabel,
		"mul100":            func(f float64) float64 { return f * 100 },
	}
	out := make(map[string]*template.Template, len(pages))
	for _, page := range pages {
		files := []string{"templates/base.html", "templates/operation_display.html", "templates/" + page + ".html"}
		if page == "index" || page == "anomalies" {
			files = append(files, "templates/timerange.html")
		}
		if page == "transactions" || page == "services" {
			files = append(files, "templates/txn_sort.html")
		}
		out[page] = template.Must(template.New("").Funcs(funcs).ParseFS(templatesFS, files...))
	}
	return out
}

// prettyJSON re-marshals a raw JSON message with 2-space indentation for readable
// display. Registered as a template func and used by transaction_detail.html to
// show observation data in a <pre> block. Falls back to the raw bytes on a parse
// error so an invalid blob is still inspectable.
func prettyJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(b)
}

// humanMinutes renders a learning-window duration stored as whole minutes,
// preferring an hour form when it divides cleanly.
func humanMinutes(mins int64) string {
	if mins <= 0 {
		return "0"
	}
	if mins%60 == 0 {
		return fmt.Sprintf("%dh (%d min)", mins/60, mins)
	}
	return fmt.Sprintf("%d min", mins)
}

// offendingChip is one readable rendering of an anomaly's offending item: the
// deviation class's human label plus its pretty-printed offending values (e.g.
// "New egress destination" → ["currency:8080"]). Raw is the original segment,
// kept for a hover tooltip.
type offendingChip struct {
	Class string
	Items []string
	Raw   string
}

// offendingChips turns a signature's raw offending string into readable chips so
// no view shows raw JSON. Registered as a template func; base.html's "offChips"
// block calls it (rendered from anomalies.html, anomaly_compare.html, and
// transaction_detail.html). Each "class=values" segment becomes a chip labelled
// by the catalog class with pretty-printed values; unparseable segments fall back
// to the raw text.
func offendingChips(raw string) []offendingChip {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var chips []offendingChip
	for _, seg := range strings.Split(raw, "; ") {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		classID, values, found := strings.Cut(seg, "=")
		if !found {
			chips = append(chips, offendingChip{Class: seg, Raw: seg})
			continue
		}
		chips = append(chips, offendingChip{
			Class: risk.ClassLabel(repo.DeviationClass(classID)),
			Items: prettyOffendingItems(values),
			Raw:   seg,
		})
	}
	return chips
}

type operationDisplayData struct {
	Name string
	Dims []repo.TransactionIdentityValue
}

func operationDisplay(operation string) operationDisplayData {
	name, dims := repo.SplitOperationIdentity(operation)
	if dims == nil {
		dims = []repo.TransactionIdentityValue{}
	}
	return operationDisplayData{Name: name, Dims: dims}
}

func identityDimLabel(key, value string) string {
	return repo.IdentityDimensionDisplayLabel(key, value)
}

// prettyOffendingItems parses the comma-joined JSON elements of one class's
// offending values (as anomalySignature emits them) and pretty-prints each. On
// any parse failure it returns the raw string unchanged.
func prettyOffendingItems(values string) []string {
	var arr []any
	if err := json.Unmarshal([]byte("["+values+"]"), &arr); err != nil {
		return []string{values}
	}
	items := make([]string, 0, len(arr))
	for _, v := range arr {
		items = append(items, prettyOffendingItem(v))
	}
	return items
}

// prettyOffendingItem renders one offending element. It recognizes the common
// evidence shapes (egress peer, call edge, db access, library) and falls back to
// compact "key=value" pairs for anything else.
func prettyOffendingItem(v any) string {
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Sprint(v)
	}
	if host, ok := m["host"].(string); ok && host != "" {
		if port := numString(m["port"]); port != "" && port != "0" {
			return host + ":" + port
		}
		return host
	}
	if from, to := edgeSvc(m["from"]), edgeSvc(m["to"]); from != "" || to != "" {
		return strings.TrimSpace(from + " → " + to)
	}
	if table, ok := m["table"].(string); ok && table != "" {
		if op, ok := m["op"].(string); ok && op != "" {
			return op + " " + table
		}
		return table
	}
	if name := firstString(m["name"], m["library"], m["lib"]); name != "" {
		return name
	}
	return compactPairs(m)
}

// edgeSvc extracts a call edge's service name from a {"svc": "..."} side, or "".
func edgeSvc(v any) string {
	if m, ok := v.(map[string]any); ok {
		if svc, ok := m["svc"].(string); ok {
			return svc
		}
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// numString renders a JSON number (decoded as float64) without a trailing ".0".
func numString(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return ""
}

// firstString returns the first non-empty string among the candidates.
func firstString(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// compactPairs renders a map as "k=v" pairs (scalar values only), keys sorted,
// dropping noise keys — the readable fallback for an unrecognized shape.
func compactPairs(m map[string]any) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		if k == "external" || k == "owner" {
			continue // internal flags, not useful in a chip
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				pairs = append(pairs, k+"="+v)
			}
		case float64:
			pairs = append(pairs, k+"="+strconv.FormatFloat(v, 'f', -1, 64))
		case bool:
			if v {
				pairs = append(pairs, k)
			}
		}
	}
	return strings.Join(pairs, " ")
}

// severityForScore maps a risk score to its severity tier. Registered as a
// template func and used (via the "sevBadge" block) by anomalies.html and
// transaction_detail.html for rows that carry only a score.
func severityForScore(score int) string {
	return string(risk.SeverityForScore(score))
}

// conditionText renders one learning condition as a short human phrase.
func conditionText(c repo.LearningCondition) string {
	switch c.Type {
	case repo.ConditionMinObservations:
		return fmt.Sprintf("≥ %d observations", c.MinObservations)
	case repo.ConditionMinDuration:
		return fmt.Sprintf("learning for %s", humanMinutes(c.MinDurationMinutes))
	case repo.ConditionStableObservations:
		return fmt.Sprintf("unchanged for %d observations", c.StableObservations)
	case repo.ConditionStableDuration:
		return fmt.Sprintf("unchanged for %s", humanMinutes(c.StableMinutes))
	default:
		return string(c.Type)
	}
}

// sinceChange is how many observations a class has folded in since its baseline
// last grew — the live value the stability conditions test against. Clamped at
// zero (cross-replica adoption can briefly leave the marker ahead of the count).
func sinceChange(b repo.BaselineClass) int64 {
	if b.ObservationCountAtLastChange <= 0 {
		return b.ObservationCount // never grew: everything counts as "since change"
	}
	if s := b.ObservationCount - b.ObservationCountAtLastChange; s > 0 {
		return s
	}
	return 0
}

// --- learning stability view ----------------------------------------------

// stabilityView is the rendered "Learning stability" cell for one baseline class,
// shown on transaction_detail.html. It folds observations-since-last-change and
// time-since-last-change into one read of how converged a baseline is: a progress
// bar toward the class's configured stability target, else a coarse verdict pill.
// The coarse buckets deliberately tolerate the ±1 cross-replica jitter in the raw
// counters that a bare number would surface as noise.
type stabilityView struct {
	State    string            // "frozen" | "growing" | "settling" | "stable"
	Label    string            // pill/text label; "" when a bar is shown instead
	Detail   string            // muted line under the pill/bar
	Help     string            // hover text explaining this state (title attr)
	HasBar   bool              // render the progress bar instead of a pill
	BarStyle template.HTMLAttr // precomputed width:NN% for the bar fill
	BarLabel string            // e.g. "42 / 50 stable obs" or "38m / 60m unchanged"
}

// baselineRow pairs a baseline class with its rendered stability cell, so the
// transaction-detail template shows both without recomputing anything.
// Histogram is the same chart-ready payload the product API returns (nil for
// non-histogram classes).
type baselineRow struct {
	repo.BaselineClass
	Stability  stabilityView
	Histogram  *apitypes.BaselineHistogram
}

// histBarRender is one non-empty histogram bar sized for the debug chart.
type histBarRender struct {
	Label string
	Count int64
	Pct   float64
}

// histSeriesBars is the non-empty bars of one series plus axis labels.
type histSeriesBars struct {
	Bars  []histBarRender
	First string
	Last  string
}

// histBars returns non-empty bars from a series with Pct relative to the max
// count in that series (for bar-chart heights).
func histBars(s apitypes.BaselineHistogramSeries) histSeriesBars {
	var max int64 = 1
	out := make([]histBarRender, 0, len(s.Bars))
	for _, b := range s.Bars {
		if b.Count <= 0 {
			continue
		}
		if b.Count > max {
			max = b.Count
		}
		out = append(out, histBarRender{Label: b.Label, Count: b.Count})
	}
	for i := range out {
		out[i].Pct = float64(out[i].Count) / float64(max) * 100
	}
	v := histSeriesBars{Bars: out}
	if len(out) > 0 {
		v.First = out[0].Label
		v.Last = out[len(out)-1].Label
	}
	return v
}

// Stability defaults used only when a class has no explicit stable_* condition,
// so the verdict pill still reflects roughly how settled the baseline is.
const (
	defaultStableObservations int64 = 50
	defaultStableMinutes      int64 = 30
)

// learningStability builds the stability cell for one class against its
// resolved learning policy. A promoted class is frozen (stability no longer
// applies). A class whose policy names a stable_observations/stable_duration
// target gets a progress bar toward that exact target. Otherwise it gets a
// coarse Growing → Settling → Stable verdict inferred from the default window.
func learningStability(bc repo.BaselineClass, policy repo.LearningPolicy) stabilityView {
	if bc.Promoted {
		return stabilityView{State: "frozen", Label: "\U0001F512 Promoted", Detail: "baseline locked", Help: stabilityHelp("frozen", false)}
	}
	now := time.Now()
	since := sinceChange(bc)

	// Stability is only meaningful once the baseline has actually grown. A class
	// that has learned nothing yet (still empty) can't be "stable" and won't be
	// promoted on stability, so show that plainly instead of a bar that would
	// fill up and imply it's about to promote. ObservationCountAtLastChange is the
	// same "has ever grown" marker the promotion engine uses.
	if bc.ObservationCountAtLastChange <= 0 {
		return stabilityView{State: "growing", Label: "Learning", Detail: "no baseline data yet", Help: stabilityHelp("nodata", false)}
	}

	// Explicit count target: progress bar toward the exact obs threshold.
	if target := stableTarget(policy, repo.ConditionStableObservations); target > 0 {
		if since >= target {
			return stabilityView{State: "stable", Label: "✓ Stable", Detail: unchangedDetail(since, bc.LastChangedAt, now), Help: stabilityHelp("stable", true)}
		}
		return stabilityView{
			State: "settling", HasBar: true,
			BarStyle: barWidth(since, target),
			BarLabel: fmt.Sprintf("%d / %d stable obs", since, target),
			Detail:   grewDetail(bc.LastChangedAt),
			Help:     stabilityHelp("settling", true),
		}
	}

	// Explicit time target: progress bar toward the exact quiet-time threshold.
	if target := stableTarget(policy, repo.ConditionStableDuration); target > 0 {
		mins := minutesSinceChange(bc, now)
		if mins >= target {
			return stabilityView{State: "stable", Label: "✓ Stable", Detail: unchangedDetail(since, bc.LastChangedAt, now), Help: stabilityHelp("stable", true)}
		}
		return stabilityView{
			State: "settling", HasBar: true,
			BarStyle: barWidth(mins, target),
			BarLabel: fmt.Sprintf("%s / %s unchanged", compactMinutes(mins), compactMinutes(target)),
			Detail:   grewDetail(bc.LastChangedAt),
			Help:     stabilityHelp("settling", true),
		}
	}

	// No explicit target: coarse verdict from the default window. Take whichever
	// signal is further along, so a low-traffic class that has been quiet a long
	// time reads as settled even with few observations.
	progress := float64(since) / float64(defaultStableObservations)
	if !bc.LastChangedAt.IsZero() && bc.LastChangedAt.Unix() > 0 {
		if tf := now.Sub(bc.LastChangedAt).Minutes() / float64(defaultStableMinutes); tf > progress {
			progress = tf
		}
	}
	switch {
	case progress >= 1.0:
		return stabilityView{State: "stable", Label: "✓ Stable", Detail: unchangedDetail(since, bc.LastChangedAt, now), Help: stabilityHelp("stable", false)}
	case progress < 0.2:
		return stabilityView{State: "growing", Label: "Growing", Detail: growingDetail(since, bc.LastChangedAt), Help: stabilityHelp("growing", false)}
	default:
		return stabilityView{State: "settling", Label: "Settling", Detail: unchangedDetail(since, bc.LastChangedAt, now), Help: stabilityHelp("settling", false)}
	}
}

// stabilityHelp is the hover explanation for one stability state. policyBacked
// says whether the class actually promotes on a stable_* condition: if so the
// cell is a real progress bar toward that target; if not, the verdict is only an
// informational convergence estimate from the default window (a class can be
// "Stable" here yet still be learning, because it promotes on other conditions).
// Centralized so the wording stays in one place as states evolve.
func stabilityHelp(state string, policyBacked bool) string {
	// A baseline "grows" when a trace adds something new to it: a new call edge,
	// egress peer, library, DB table, or latency/payload band.
	switch state {
	case "frozen":
		return "Promoted: this class has graduated from learning, so its baseline is frozen and no longer changes. Stability no longer applies."
	case "nodata":
		return "Learning: this class hasn't recorded any baseline data yet (no calls, peers, libraries, DB access, or latency/size samples seen for it). Stability only starts counting once it learns something, so it stays in learning until then."
	case "growing":
		return "Growing: the baseline changed very recently, so it's still discovering new structure (a new edge, peer, library, table, or latency/size band). " +
			estimateSuffix(policyBacked)
	case "settling":
		if policyBacked {
			return "Settling: progress toward this class's configured stability target (unchanged observations or quiet time). It resets to zero whenever the baseline grows; at 100% the class promotes."
		}
		return "Settling: the baseline has been quiet for a bit but not long enough to look converged yet. " + estimateSuffix(false)
	case "stable":
		if policyBacked {
			return "Stable: the baseline has met this class's configured stability target — it hasn't grown for the required window."
		}
		return "Stable: the baseline hasn't changed in a while, so it looks converged. " + estimateSuffix(false)
	default:
		return ""
	}
}

// estimateSuffix appends the caveat that a verdict is only an informational
// estimate for classes that don't promote on stability.
func estimateSuffix(policyBacked bool) string {
	if policyBacked {
		return ""
	}
	return "This class doesn't promote on stability — this is only an informational estimate from a default window (50 unchanged observations or 30 min quiet); promotion is driven by its other conditions."
}

// stableTarget returns the largest target across a policy's conditions of the
// given stability type, or 0 when the policy has none.
func stableTarget(policy repo.LearningPolicy, conditionType repo.LearningConditionType) int64 {
	var target int64
	for _, condition := range policy.Conditions {
		if condition.Type != conditionType {
			continue
		}
		value := condition.StableObservations
		if conditionType == repo.ConditionStableDuration {
			value = condition.StableMinutes
		}
		if value > target {
			target = value
		}
	}
	return target
}

// minutesSinceChange is whole minutes since the baseline last grew, falling
// back to when learning started before the first growth. Clamped at zero.
func minutesSinceChange(bc repo.BaselineClass, now time.Time) int64 {
	anchor := bc.LastChangedAt
	if anchor.IsZero() {
		anchor = bc.LearningStartedAt
	}
	if anchor.IsZero() || anchor.Unix() <= 0 {
		return 0
	}
	if m := int64(now.Sub(anchor) / time.Minute); m > 0 {
		return m
	}
	return 0
}

// barWidth precomputes the CSS width for a progress bar fill, clamped to 0..100.
func barWidth(cur, target int64) template.HTMLAttr {
	var pct int64
	if target > 0 {
		pct = cur * 100 / target
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	return template.HTMLAttr(fmt.Sprintf(` style="width:%d%%"`, pct))
}

// grewDetail is the muted line while a baseline is still settling: when it last
// grew, e.g. "grew 3m ago" or "no growth yet".
func grewDetail(lastChanged time.Time) string {
	if lastChanged.IsZero() || lastChanged.Unix() <= 0 {
		return "no growth yet"
	}
	return "grew " + humanTime(lastChanged)
}

// growingDetail adds the observation count to grewDetail, e.g.
// "grew just now · 1 obs ago".
func growingDetail(since int64, lastChanged time.Time) string {
	return fmt.Sprintf("%s · %d obs ago", grewDetail(lastChanged), since)
}

// unchangedDetail is the muted line once a baseline has settled: how long it
// has been quiet, e.g. "unchanged 120 obs · 40m". Drops the time half when the
// baseline never grew (no timestamp to show).
func unchangedDetail(since int64, lastChanged, now time.Time) string {
	d := fmt.Sprintf("unchanged %d obs", since)
	if !lastChanged.IsZero() && lastChanged.Unix() > 0 {
		d += " · " + compactDuration(now.Sub(lastChanged))
	}
	return d
}

// compactMinutes renders a whole-minute count as a compact age ("38m", "2h").
func compactMinutes(mins int64) string {
	return compactDuration(time.Duration(mins) * time.Minute)
}

// compactDuration renders a duration without an "ago" suffix, for inline use in
// a detail line ("<1m", "40m", "2h", "3d").
func compactDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// learningPolicyIndex builds a per-class scope resolver from all learning
// policy rows, so a caller can resolve the effective policy for any
// (transaction, class). A load error yields a nil map — the page still renders,
// just without stability targets (classes fall back to the coarse verdict).
func learningPolicyIndex(ctx context.Context, r repo.Repository) map[repo.DeviationClass]*repo.ScopeIndex[repo.LearningPolicy] {
	policies, err := r.ListLearningPolicies(ctx)
	if err != nil {
		return nil
	}
	byClass := make(map[repo.DeviationClass]*repo.ScopeIndex[repo.LearningPolicy])
	for _, policy := range policies {
		idx := byClass[policy.Class]
		if idx == nil {
			idx = &repo.ScopeIndex[repo.LearningPolicy]{}
			byClass[policy.Class] = idx
		}
		idx.Put(policy.Scope, policy.ScopeKey, policy)
	}
	return byClass
}

// templateDict builds a map from alternating key/value args, so a template can
// pass several named values into a shared sub-template ({{template "polCard"
// (dict "Policy" .Global "Scope" "global")}}). Keys must be strings.
func templateDict(kv ...any) map[string]any {
	m := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, _ := kv[i].(string)
		m[key] = kv[i+1]
	}
	return m
}

// policyEditJSON serializes a policy into the shape the editor's JavaScript
// hydrates its rows from: {mode, minMatches, conditions:[...]}. It is emitted
// into a data-attribute, so it must be valid JSON the browser can parse.
func policyEditJSON(policy repo.LearningPolicy) string {
	type wire struct {
		Mode       string                   `json:"mode"`
		MinMatches int                      `json:"minMatches"`
		Conditions []repo.LearningCondition `json:"conditions"`
	}
	mode := string(policy.Mode)
	if mode == "" {
		mode = string(repo.LearningModeAny)
	}
	encoded, err := json.Marshal(wire{Mode: mode, MinMatches: policy.MinMatches, Conditions: policy.Conditions})
	if err != nil {
		return `{"mode":"any","minMatches":0,"conditions":[]}`
	}
	return string(encoded)
}

// anomalyPolicyJSON serializes an alert policy into the shape the anomaly-policy
// editor's JavaScript hydrates from: {name, fireAt, enabled, weights}. weights
// is the sparse per-class override map (class -> weight). Emitted into a
// data-attribute, so it must be valid JSON.
func anomalyPolicyJSON(policy repo.Policy) string {
	type wire struct {
		Name    string         `json:"name"`
		FireAt  int            `json:"fireAt"`
		Enabled bool           `json:"enabled"`
		Weights map[string]int `json:"weights"`
	}
	fireAt := policy.FireAtScore
	if fireAt < 0 {
		fireAt = 0
	}
	weights := make(map[string]int, len(policy.SignalWeights))
	for source, weight := range policy.SignalWeights {
		weights[string(source)] = weight
	}
	encoded, err := json.Marshal(wire{Name: policy.Name, FireAt: fireAt, Enabled: policy.Enabled, Weights: weights})
	if err != nil {
		return fmt.Sprintf(`{"name":"","fireAt":%d,"enabled":true,"weights":{}}`, repo.DefaultFireAtScore)
	}
	return string(encoded)
}

// anomalyPolicyText renders an alert policy's firing rule in one human line, e.g.
// "fire when risk score ≥ 20" plus a note when it carries per-class weight
// overrides. Registered as a template func and used by anomaly_policies.html for
// each policy's summary line; mirrors the engine's score gate.
func anomalyPolicyText(policy repo.Policy) string {
	fireAt := policy.FireAtScore
	if fireAt < 0 {
		fireAt = 0
	}
	text := fmt.Sprintf("fire when risk score ≥ %d", fireAt)
	if overrides := len(policy.SignalWeights); overrides > 0 {
		text += fmt.Sprintf(" · %d signal weight override(s)", overrides)
	}
	return text
}

// defaultWeight is the risk catalog's default weight for a class, shown in the
// editor as the value an operator starts from before overriding. Unknown
// (placeholder) classes report 0.
func defaultWeight(class repo.DeviationClass) int {
	w, _ := risk.DefaultWeight(class)
	return w
}

// classCategory is the human category label for a deviation class ("Lateral
// movement"), used to group and explain classes in the editor.
func classCategory(class repo.DeviationClass) string {
	return risk.CategoryLabel(risk.Catalog[class].Category)
}

// classMitre is the MITRE tag for a class's category (for the tooltip), or "".
func classMitre(class repo.DeviationClass) string {
	return risk.Categories[risk.Catalog[class].Category].Mitre
}

// scoreBand maps a weight/score to its severity-band name (info/low/medium/…),
// so the editor can chip each weight with the band it alone reaches.
func scoreBand(score int) string {
	return string(risk.SeverityForScore(score))
}

// enricherListText is the value shown in an enricher's list textarea: the
// scope's own list for that key if it set one, otherwise the catalog default
// (pre-filled so an operator edits from it). Comma-separated for a compact
// single control. Works for any enricher list key, so the editor needs no
// per-list code.
func enricherListText(policy repo.Policy, key repo.EnricherListKey) string {
	if override, ok := policy.EnricherLists[key]; ok && len(override) > 0 {
		return strings.Join(override, ", ")
	}
	return strings.Join(risk.DefaultList(key), ", ")
}

// conditionsText joins a policy's conditions with its combine mode, e.g.
// "≥ 50 observations OR learning for 24h". Registered as a template func and used
// by learning_policies.html for each policy's summary line.
func conditionsText(policy repo.LearningPolicy) string {
	if len(policy.Conditions) == 0 {
		return fmt.Sprintf("default (≥ %d observations)", engine.DefaultMinObservations)
	}
	separator := " OR "
	switch policy.Mode {
	case repo.LearningModeAll:
		separator = " AND "
	case repo.LearningModeKOf:
		separator = fmt.Sprintf(" / any %d of ", policy.MinMatches)
	}
	parts := make([]string, 0, len(policy.Conditions))
	for _, condition := range policy.Conditions {
		parts = append(parts, conditionText(condition))
	}
	return strings.Join(parts, separator)
}

// metricValue humanizes a finding metric value by unit: microseconds render as
// µs/ms/s, bytes as B/KB/MB, anything else as "<n><unit>". Registered as a
// template func and used by anomaly_compare.html for the metric finding row.
func metricValue(unit string, v int64) string {
	switch unit {
	case "us":
		switch {
		case v < 1000:
			return fmt.Sprintf("%dµs", v)
		case v < 1_000_000:
			return fmt.Sprintf("%.1fms", float64(v)/1000)
		default:
			return fmt.Sprintf("%.2fs", float64(v)/1_000_000)
		}
	case "bytes":
		switch {
		case v < 1024:
			return fmt.Sprintf("%dB", v)
		case v < 1024*1024:
			return fmt.Sprintf("%.1fKB", float64(v)/1024)
		default:
			return fmt.Sprintf("%.1fMB", float64(v)/(1024*1024))
		}
	default:
		return fmt.Sprintf("%d%s", v, unit)
	}
}

// overPct renders how far observed is above baseline, e.g. "+63%". Registered as
// a template func and used by anomaly_compare.html next to the metric values.
// Empty when baseline is non-positive.
func overPct(observed, baseline int64) string {
	if baseline <= 0 {
		return ""
	}
	return fmt.Sprintf("+%.0f%%", float64(observed-baseline)/float64(baseline)*100)
}

// humanTime renders a "last seen" timestamp as a compact relative age
// ("just now", "12s ago", "5m ago", "3h ago", "2d ago"). Zero/epoch means never seen.
func humanTime(t time.Time) string {
	if t.IsZero() || t.Unix() <= 0 {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < 2*time.Second:
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d/time.Second))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(d/(24*time.Hour)))
	}
}

func humanBytes(n any) string {
	v := uint64(toInt64(n))
	if toInt64(n) < 0 {
		v = 0
	}
	switch {
	case v < 1024:
		return fmt.Sprintf("%d B", v)
	case v < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(v)/1024)
	case v < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(v)/(1024*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(v)/(1024*1024*1024))
	}
}

func humanDuration(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 1:
		return fmt.Sprintf("%.0f ms", seconds*1000)
	case seconds < 60:
		return fmt.Sprintf("%.1f s", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%.0f m", seconds/60)
	default:
		h := int(seconds / 3600)
		m := int(seconds/60) % 60
		return fmt.Sprintf("%dh %dm", h, m)
	}
}

// listObservations loads sample metadata via the product API (same as
// GET …/observations).
func (u *ui) listObservations(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error) {
	return u.mustProductAPI().ListObservations(ctx, transactionID, sampleReason, limit)
}

// getObservation loads one sample including raw_otlp via the product API
// (same as GET …/observations/{trace_id}).
func (u *ui) getObservation(ctx context.Context, transactionID int64, traceID string) (repo.Observation, bool, error) {
	return u.mustProductAPI().GetObservation(ctx, transactionID, traceID)
}

// getTransaction loads one transaction's identity via the product API
// (same as GET …/transactions/{id}).
func (u *ui) getTransaction(ctx context.Context, transactionID int64) (*repo.Transaction, error) {
	tx, found, err := u.mustProductAPI().GetTransaction(ctx, transactionID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	return &tx, nil
}

// listTransactionStats loads activity and promotion summaries via the product
// API (same as GET …/transactions).
func (u *ui) listTransactionStats(ctx context.Context, params apitypes.ListTransactionsParams) ([]repo.TransactionStat, error) {
	listed, err := u.mustProductAPI().ListTransactions(ctx, params)
	if err != nil {
		return nil, err
	}
	return transactionStatsFromWire(listed.Items), nil
}

func transactionStatsFromWire(items []apitypes.TransactionStat) []repo.TransactionStat {
	out := make([]repo.TransactionStat, len(items))
	for i, item := range items {
		out[i] = repo.TransactionStat{
			Transaction: repo.Transaction{
				ID:        item.Id,
				Service:   item.Service,
				Namespace: item.Namespace,
				Operation: item.Operation,
				Kind:      repo.TransactionKind(item.Kind),
			},
			Volume:   item.Volume,
			LastSeen: item.LastSeen,
		}
		if item.HasBaseline != nil {
			out[i].HasBaseline = *item.HasBaseline
		}
		if item.Promoted != nil {
			out[i].Promoted = *item.Promoted
		}
	}
	return out
}

// getAnomalyIssueDetail loads the investigate payload via the product API
// (same as GET /api/v1/anomalies/{transaction_id}/{signature}).
func (u *ui) getAnomalyIssueDetail(ctx context.Context, transactionID int64, signature string) (apitypes.AnomalyIssue, bool, error) {
	return u.mustProductAPI().GetAnomalyIssueDetail(ctx, transactionID, signature)
}

func (u *ui) getGuardrailViolationDetail(ctx context.Context, scopeKey string, ruleKey repo.RuleKey, offending string) (apitypes.GuardrailViolationDetail, bool, error) {
	return u.mustProductAPI().GetGuardrailViolationDetail(ctx, scopeKey, ruleKey, offending)
}

// mustProductAPI returns the wired ProductAPI. Panics only if SetProductAPI was
// never called (programming error); main wires it before ListenAndServe.
func (u *ui) mustProductAPI() ProductAPI {
	if p := u.api.Load(); p != nil && *p != nil {
		return *p
	}
	panic("debugui: ProductAPI not set; call SetProductAPI before serving")
}

// obsSection is one tab of the transaction-detail observations view. Prefix
// namespaces the per-row dialog IDs so the "examples" and "anomalies" tabs
// don't collide when both render the shared observationSection sub-template.
// Observations are metadata-only; OTLP is fetched on "view trace".
type obsSection struct {
	Prefix        string
	TransactionID int64
	Observations  []repo.Observation
}

// KV and SpanEvent are re-exported from the engine's shared trace view so the
// templates keep their existing field access while both the debug UI and the
// product API decode traces through one code path.
type (
	KV        = engine.KV
	SpanEvent = engine.SpanEvent
)

// SpanRow is one row in the rendered modal trace view. *Style fields are
// precomputed CSS so the template doesn't have to do math or fight
// html/template's style-attribute escaper.
type SpanRow struct {
	Depth      int
	SpanID     string
	Name       string
	Service    string
	Kind       string // short form: SERVER / CLIENT / INTERNAL / ...
	DurationMs float64
	OffsetMs   float64
	RowStyle   template.HTMLAttr // padding-left on the row container
	BarStyle   template.HTMLAttr // margin-left + width on the inner bar
	StatusCode string            // "OK" | "ERROR" | "" (UNSET)
	StatusMsg  string
	Scope      string // "<name>@<version>", omitted version is fine
	Attrs      []KV
	Events     []SpanEvent
	// Highlight is the evidence severity ("high"|"medium"|"low") when a finding
	// flagged this span, else "". Only set on the anomaly side of the compare
	// view; drives the row tint.
	Highlight string
	// HighlightReason is the short human reason from the matching finding.
	HighlightReason string
	// Expand requests the row render pre-opened so its attributes (e.g. the
	// deviating arg.N / return.value) are visible without a click. Set on the
	// flagged spans of the anomaly trace and on the matching spans of the
	// baseline trace, so the offending vs. normal values sit side by side.
	Expand bool
}

// HasDetails returns true when there's anything worth expanding for this
// span. The template uses it to decide whether to render the
// click-to-expand affordance.
func (r SpanRow) HasDetails() bool {
	return r.StatusCode != "" || r.StatusMsg != "" || r.Scope != "" || len(r.Attrs) > 0 || len(r.Events) > 0
}

// traceSpans decodes persisted raw OTLP into the shared engine.SpanView list
// (same code path the scoring engine uses) and wraps each span with the CSS
// geometry the waterfall view needs. Used by the lazy trace fragment, compare,
// and violation-evidence handlers. Returns nil for empty/unparseable input.

func traceSpans(raw []byte) []SpanRow {
	views := engine.SpansFromOTLP(raw)
	if len(views) == 0 {
		return nil
	}
	// views[0] is the root (pre-order). Bar widths are a percentage of the
	// root span's total wall time.
	totalMs := views[0].DurationMs
	if totalMs <= 0 {
		totalMs = 1
	}

	out := make([]SpanRow, 0, len(views))
	for _, v := range views {
		offsetPct := v.OffsetMs / totalMs * 100
		widthPct := v.DurationMs / totalMs * 100
		if widthPct < 0.5 { // keep a hairline visible even for sub-ms spans
			widthPct = 0.5
		}
		out = append(out, SpanRow{
			Depth:      v.Depth,
			SpanID:     v.SpanID,
			Name:       v.Name,
			Service:    v.Service,
			Kind:       v.Kind,
			DurationMs: v.DurationMs,
			OffsetMs:   v.OffsetMs,
			RowStyle:   template.HTMLAttr(fmt.Sprintf(` style="padding-left:%dpx"`, v.Depth*18)),
			BarStyle:   template.HTMLAttr(fmt.Sprintf(` style="margin-left:%.2f%%;width:%.2f%%"`, offsetPct, widthPct)),
			StatusCode: v.StatusCode,
			StatusMsg:  v.StatusMsg,
			Scope:      v.Scope,
			Attrs:      v.Attrs,
			Events:     v.Events,
		})
	}
	return out
}

func (u *ui) render(c *gin.Context, page, active, flash string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["Active"] = active
	data["Flash"] = flash
	// A handler may set Error itself (e.g. a partial data-load failure it wants
	// shown instead of an empty page); fall back to the ?error= redirect flash.
	if _, ok := data["Error"]; !ok {
		data["Error"] = c.Query("error")
	}
	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := u.templates[page].ExecuteTemplate(c.Writer, "base.html", data); err != nil {
		_, _ = c.Writer.WriteString("\n<!-- template error: " + err.Error() + " -->\n")
	}
}

// --- handlers --------------------------------------------------------------

// settingsIndex sends /debug/settings to the first sub-page. The settings area
// has no landing page of its own; the left rail is the navigation.
func (u *ui) settingsIndex(c *gin.Context) {
	c.Redirect(http.StatusFound, "/debug/settings/learning")
}

// settingsRiskModel renders the read-only default risk model (per-class base
// weights, categories, and enricher sub-cases) that every anomaly policy builds
// on. Tuning happens per-scope under Detection; this page is the reference.
func (u *ui) settingsRiskModel(c *gin.Context) {
	u.render(c, "settings_risk_model", "settings", c.Query("flash"), map[string]any{
		"SettingsTab": "risk-model",
		"Classes":     repo.AllDeviationClasses,
	})
}

func (u *ui) settingsSystem(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	wire, err := api.LoadSystemSettings(c.Request.Context())
	if err != nil {
		s := repo.DefaultSystemSettings()
		u.render(c, "settings_system", "settings", "", map[string]any{
			"SettingsTab":           "system",
			"Error":                 err.Error(),
			"Settings":              s,
			"MaxEnabledIdentityDims": repo.MaxTransactionIdentityDimensions,
		})
		return
	}
	s := apimap.ToSystemSettings(wire)
	u.render(c, "settings_system", "settings", c.Query("flash"), map[string]any{
		"SettingsTab":           "system",
		"Error":                   c.Query("error"),
		"Settings":                s,
		"MaxEnabledIdentityDims": repo.MaxTransactionIdentityDimensions,
	})
}

func (u *ui) settingsStorage(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	h, err := api.LoadStorageHealth(c.Request.Context())
	data := map[string]any{"SettingsTab": "storage"}
	if err != nil {
		data["Error"] = err.Error()
		data["Health"] = apitypes.StorageHealth{
			Status:    apitypes.StorageHealthStatusUnreachable,
			CheckedAt: time.Now().UTC(),
		}
		u.render(c, "settings_storage", "settings", "", data)
		return
	}
	data["Health"] = h
	u.render(c, "settings_storage", "settings", "", data)
}

func (u *ui) saveSystemSettings(c *gin.Context) {
	redirect := "/debug/settings/system"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	atoi := func(name string) int {
		n, _ := strconv.Atoi(c.PostForm(name))
		return n
	}
	body := apitypes.SystemSettings{
		Sampling: apitypes.SystemSamplingSettings{
			ExamplesPerTransaction:       atoi("examples_per_transaction"),
			ExampleSampleIntervalSeconds: atoi("example_sample_interval_seconds"),
		},
		Retention: apitypes.SystemRetentionSettings{
			ObservationRetentionDays: atoi("observation_retention_days"),
		},
		Capacity: apitypes.SystemCapacitySettings{
			MaxResidentTransactions: atoi("max_resident_transactions"),
			MaxBaselineSetMembers:   atoi("max_baseline_set_members"),
		},
		Writeback: apitypes.SystemWritebackSettings{
			FlushIntervalSeconds: atoi("flush_interval_seconds"),
		},
		Detection: apitypes.SystemDetectionSettings{
			AutoTransactionGuardrail: c.PostForm("auto_transaction_guardrail") == "1",
		},
		Identity: apitypes.SystemIdentitySettings{
			TransactionIdentityDimensions: parseIdentityDimForm(c),
		},
	}
	if c.PostForm("reset_transactions") == "1" {
		reset := true
		body.ResetTransactions = &reset
	}
	if err := api.ApplySystemSettings(c.Request.Context(), body); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape(err.Error()))
		return
	}
	flash := "system settings saved and live"
	if c.PostForm("reset_transactions") == "1" {
		flash = "system settings saved; all transactions reset"
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(flash))
}

func parseIdentityDimForm(c *gin.Context) []apitypes.SystemTransactionIdentityDimension {
	var out []apitypes.SystemTransactionIdentityDimension
	for i := 0; ; i++ {
		keyField := fmt.Sprintf("dim_key_%d", i)
		if _, ok := c.Request.PostForm[keyField]; !ok {
			break
		}
		key := strings.TrimSpace(c.PostForm(keyField))
		if key == "" {
			continue
		}
		out = append(out, apitypes.SystemTransactionIdentityDimension{
			Key:     key,
			Enabled: c.PostForm(fmt.Sprintf("dim_enabled_%d", i)) == "1",
		})
	}
	return out
}

// listTransactions renders views off /debug/transactions:
//   - (default) / ?view=services — services as expandable cards with txns inline
//   - ?view=all                  — flat "all transactions" list
//
// Legacy ?service=&namespace= (without view=services) redirects into the
// by-service view with that card opened.
func (u *ui) listTransactions(c *gin.Context) {
	ctx := c.Request.Context()
	service := c.Query("service")
	namespace := c.Query("namespace")
	view := c.Query("view")

	if view != "services" && view != "all" && service != "" && namespace != "" {
		q := url.Values{
			"view":      {"services"},
			"namespace": {namespace},
			"service":   {service},
		}
		if sortBy := c.Query("sort_by"); sortBy != "" {
			q.Set("sort_by", sortBy)
		}
		if sortOrder := c.Query("sort_order"); sortOrder != "" {
			q.Set("sort_order", sortOrder)
		}
		c.Redirect(http.StatusSeeOther, pathWithQuery("/debug/transactions", q))
		return
	}

	// Both tabs show global totals so the counts read the same in every view.
	services, err := u.repo.ListServiceStats(ctx, 1000)
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	serviceCount := len(services)
	transactionCount := 0
	for _, s := range services {
		transactionCount += int(s.TransactionCount)
	}

	params := listTransactionsParamsFromRequest(c)
	sortBy, sortOrder := resolveTransactionSort(params)

	if view != "all" {
		// Default and ?view=services: group every txn under its service card;
		// namespace/service query only mark which card is open.
		listParams := params
		listParams.Namespace = ""
		listParams.Service = ""
		txs, err := u.listTransactionStats(ctx, listParams)
		if err != nil {
			c.String(http.StatusInternalServerError, "%v", err)
			return
		}
		baseQuery := url.Values{"view": {"services"}}
		if params.SortBy != "" {
			baseQuery.Set("sort_by", string(params.SortBy))
		}
		if params.SortOrder != "" {
			baseQuery.Set("sort_order", string(params.SortOrder))
		}
		guardrails, _ := u.repo.ListServiceGuardrails(ctx)
		u.render(c, "services", "transactions", c.Query("flash"), map[string]any{
			"Cards":            buildServiceTxnCardsWithGuardrails(services, txs, guardrails, namespace, service),
			"ServiceCount":     serviceCount,
			"TransactionCount": transactionCount,
			"SortBy":           sortBy,
			"SortOrder":        sortOrder,
			"SortLinks":        transactionSortLinks("/debug/transactions", baseQuery, sortBy, sortOrder),
			"DeleteRedirect":   pathWithQuery("/debug/transactions", baseQuery),
			"Error":            c.Query("error"),
		})
		return
	}

	// Flat "all transactions" list via ProductAPI.
	txs, err := u.listTransactionStats(ctx, params)
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	baseQuery := url.Values{"view": {"all"}}
	if params.SortBy != "" {
		baseQuery.Set("sort_by", string(params.SortBy))
	}
	if params.SortOrder != "" {
		baseQuery.Set("sort_order", string(params.SortOrder))
	}
	u.render(c, "transactions", "transactions", c.Query("flash"), map[string]any{
		"Transactions":     txs,
		"Scope":            "All transactions",
		"ScopedToSvc":      false,
		"ServiceCount":     serviceCount,
		"TransactionCount": transactionCount,
		"SortBy":           sortBy,
		"SortOrder":        sortOrder,
		"SortLinks":        transactionSortLinks("/debug/transactions", baseQuery, sortBy, sortOrder),
		"DeleteRedirect":   pathWithQuery("/debug/transactions", baseQuery),
		"Error":            c.Query("error"),
	})
}

// deleteTransaction is POST /debug/transactions/:id/delete — same as
// DELETE /api/v1/transactions/{id}.
func (u *ui) deleteTransaction(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	var id int64
	if _, err := parseInt64Param(c, "id", &id); err != nil {
		return
	}
	redirect := strings.TrimSpace(c.PostForm("redirect"))
	if redirect == "" {
		redirect = "/debug/transactions"
	}
	if err := api.DeleteTransaction(c.Request.Context(), id); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("delete failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape("transaction deleted"))
}

// deleteTransactions is POST /debug/transactions/delete-bulk — same as
// POST /api/v1/transactions (bulkDeleteTransactions).
func (u *ui) deleteTransactions(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	redirect := strings.TrimSpace(c.PostForm("redirect"))
	if redirect == "" {
		redirect = "/debug/transactions"
	}
	ids, ok := parseTransactionIDForm(c, redirect)
	if !ok {
		return
	}
	deleted, err := api.DeleteTransactions(c.Request.Context(), ids)
	if err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape(err.Error()))
		return
	}
	noun := "transactions"
	if deleted == 1 {
		noun = "transaction"
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(fmt.Sprintf("deleted %d %s", deleted, noun)))
}

// promoteTransactions is POST /debug/transactions/promote-bulk — same as
// POST /api/v1/transactions/promote (bulkPromoteTransactions).
func (u *ui) promoteTransactions(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	redirect := strings.TrimSpace(c.PostForm("redirect"))
	if redirect == "" {
		redirect = "/debug/transactions"
	}
	ids, ok := parseTransactionIDForm(c, redirect)
	if !ok {
		return
	}
	promoted, err := api.PromoteTransactions(c.Request.Context(), ids)
	if err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape(err.Error()))
		return
	}
	noun := "transactions"
	if promoted == 1 {
		noun = "transaction"
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(fmt.Sprintf("promoted %d %s", promoted, noun)))
}

// parseTransactionIDForm reads checkbox-selected transaction_id values from a
// debug form POST. On invalid/empty input it redirects with ?error= and returns ok=false.
func parseTransactionIDForm(c *gin.Context, redirect string) ([]int64, bool) {
	rawIDs := c.PostFormArray("transaction_id")
	if len(rawIDs) == 0 {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("no transactions selected"))
		return nil, false
	}
	ids := make([]int64, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || id <= 0 {
			c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("invalid transaction id"))
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// promoteTransaction is POST /debug/transactions/:id/promote — same as
// POST /api/v1/transactions/{id}/baselines/promote.
func (u *ui) promoteTransaction(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	var id int64
	if _, err := parseInt64Param(c, "id", &id); err != nil {
		return
	}
	redirect := strings.TrimSpace(c.PostForm("redirect"))
	if redirect == "" {
		redirect = "/debug/transactions"
	}
	if err := api.PromoteTransactionBaselines(c.Request.Context(), id); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("promote failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape("transaction promoted"))
}

func (u *ui) transactionDetail(c *gin.Context) {
	var id int64
	if _, err := parseInt64Param(c, "id", &id); err != nil {
		return
	}
	ctx := c.Request.Context()

	tx, err := u.getTransaction(ctx, id)
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	if tx == nil {
		c.String(http.StatusNotFound, "transaction %d not found", id)
		return
	}

	// Each of the three data reads below is scoped to this transaction and cheap,
	// but a transient DB failure (e.g. ClickHouse over its query-memory cap under
	// load) must NOT render as an empty page that reads as "no baseline" — that
	// was the confusing "data vanishes, reappears on refresh" behaviour. Collect
	// any failure and surface it as a banner instead of silently dropping data.
	var loadErrs []string

	baselines, err := u.repo.GetBaselineForTransaction(ctx, id)
	if err != nil {
		loadErrs = append(loadErrs, "baseline: "+err.Error())
	}

	// Resolve each class's effective learning policy (transaction → service →
	// global) so its stability cell can track progress toward the exact target
	// that class will actually promote on.
	policyByClass := learningPolicyIndex(ctx, u.repo)
	serviceKey := repo.ServiceScopeKey(tx.Namespace, tx.Service)

	// Show a row per class the engine scores, including an empty placeholder for
	// any class with no baseline row yet (freshly reset, or not observed yet), so
	// a reset class stays in the table instead of blinking out until the next
	// trace flushes. Shared with the HTTP API (same helper) so the two views agree.
	baselines = engine.FillScoredPlaceholders(id, baselines)
	baselineRows := make([]baselineRow, 0, len(baselines))
	for _, bc := range baselines {
		var pol repo.LearningPolicy
		if idx := policyByClass[bc.Class]; idx != nil {
			pol, _ = idx.Resolve(id, serviceKey)
		}
		baselineRows = append(baselineRows, baselineRow{
			BaselineClass: bc,
			Stability:     learningStability(bc, pol),
			Histogram:     apimap.HistogramPresentation(bc.Class, bc.Data),
		})
	}

	// Normal-traffic examples (storage-capped, so the list is complete) plus this
	// transaction's deduped anomaly issues, scoped to this transaction in SQL
	// (not a cross-transaction scan filtered in memory). The issues link into the
	// Findings feed's compare view for the full investigation.
	exampleCap := engine.ExamplesPerTransaction
	if api := u.api.Load(); api != nil && *api != nil {
		if n := (*api).LiveSystemSettings().ExamplesPerTransaction; n > 0 {
			exampleCap = n
		}
	} else if s, err := u.repo.GetSystemSettings(ctx); err == nil && s.ExamplesPerTransaction > 0 {
		exampleCap = s.ExamplesPerTransaction
	}
	examples, err := u.listObservations(ctx, id, "example", exampleCap)
	if err != nil {
		loadErrs = append(loadErrs, "trace examples: "+err.Error())
	}
	var issues []repo.AnomalyIssue
	if api := u.api.Load(); api != nil && *api != nil {
		listed, err := (*api).ListAnomalies(ctx, apitypes.ListAnomaliesParams{
			WindowHours:   apitypes.WindowHours(repo.DefaultFindingsLookbackHours),
			TransactionId: id,
		})
		if err != nil {
			loadErrs = append(loadErrs, "anomaly issues: "+err.Error())
		} else {
			issues = anomalyIssuesFromWire(listed.Items)
		}
	} else {
		loadErrs = append(loadErrs, "anomaly issues: product API not ready")
	}

	data := map[string]any{
		"Transaction": tx,
		"Baselines":   baselineRows,
		"ExampleCap":  exampleCap,
		"ExSection":   obsSection{Prefix: "ex", TransactionID: id, Observations: examples},
		"Issues":      issues,
	}
	if len(loadErrs) > 0 {
		data["Error"] = "Couldn't load some data (try refresh): " + strings.Join(loadErrs, "; ")
	}
	u.render(c, "transaction_detail", "transactions", c.Query("flash"), data)
}

// transactionTraceFragment serves the waterfall HTML for one retained sample.
// Called from the txn-detail "view trace" button so page load never pulls
// raw_otlp — only the clicked trace's blob is fetched.
func (u *ui) transactionTraceFragment(c *gin.Context) {
	var id int64
	if _, err := parseInt64Param(c, "id", &id); err != nil {
		return
	}
	traceID := strings.TrimSpace(c.Param("trace_id"))
	if traceID == "" {
		c.String(http.StatusBadRequest, "missing trace_id")
		return
	}
	obs, ok, err := u.getObservation(c.Request.Context(), id, traceID)
	if err != nil {
		c.String(http.StatusInternalServerError, "load trace: %v", err)
		return
	}
	if !ok {
		c.String(http.StatusNotFound, "observation not found")
		return
	}
	rows := traceSpans(obs.RawOTLP)
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := u.templates["transaction_detail"].ExecuteTemplate(c.Writer, "traceWaterfall", rows); err != nil {
		c.String(http.StatusInternalServerError, "render: %v", err)
	}
}

func (u *ui) promoteBaselineClass(c *gin.Context) {
	var txID int64
	if _, err := parseInt64Param(c, "id", &txID); err != nil {
		return
	}
	class := repo.DeviationClass(c.PostForm("class"))
	redirect := fmt.Sprintf("/debug/transactions/%d", txID)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	if class == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("missing class"))
		return
	}

	baselines, err := u.repo.GetBaselineForTransaction(c.Request.Context(), txID)
	if err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("failed to load baselines: "+err.Error()))
		return
	}
	var row *repo.BaselineClass
	for i := range baselines {
		if baselines[i].Class == class {
			row = &baselines[i]
			break
		}
	}
	if row == nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape(fmt.Sprintf("no baseline row for %s", class)))
		return
	}
	if row.Promoted {
		c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(string(class)+" is already promoted"))
		return
	}

	if err := api.PromoteBaselineClass(c.Request.Context(), txID, class); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("promote failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(string(class)+" promoted — class will score on the next trace"))
}

// resetBaselineClass wipes one class's baseline so it re-learns from scratch.
func (u *ui) resetBaselineClass(c *gin.Context) {
	var txID int64
	if _, err := parseInt64Param(c, "id", &txID); err != nil {
		return
	}
	redirect := fmt.Sprintf("/debug/transactions/%d", txID)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	class := repo.DeviationClass(c.PostForm("class"))
	if class == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("missing class"))
		return
	}
	if err := api.ResetBaselineClass(c.Request.Context(), txID, class); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("reset failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(string(class)+" reset — it will re-learn from the next trace"))
}

// resetTransactionBaselines wipes every class of a transaction.
func (u *ui) resetTransactionBaselines(c *gin.Context) {
	var txID int64
	if _, err := parseInt64Param(c, "id", &txID); err != nil {
		return
	}
	redirect := fmt.Sprintf("/debug/transactions/%d", txID)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	if err := api.ResetTransactionBaselines(c.Request.Context(), txID); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("reset failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape("all classes reset — they will re-learn from the next trace"))
}

// sampleView is one entry in the compare view's sample-trace picker: a captured
// evidence sample returned on the investigate payload.
type sampleView struct {
	TraceID    string
	ObservedAt time.Time
}

// compareAnomalyIssue renders the issue-vs-baseline trace comparison for one
// (transaction, signature) issue. It consumes GetAnomalyIssueDetail — the same
// investigate payload the product UI uses — and only maps wire fields into the
// HTML template view (no parallel explain/risk/trace loading).
func (u *ui) compareAnomalyIssue(c *gin.Context) {
	txnID, err := strconv.ParseInt(strings.TrimSpace(c.Query("transaction_id")), 10, 64)
	signature := strings.TrimSpace(c.Query("signature"))
	if err != nil || signature == "" {
		c.String(http.StatusBadRequest, "missing transaction_id or signature")
		return
	}
	ctx := c.Request.Context()

	detail, ok, err := u.getAnomalyIssueDetail(ctx, txnID, signature)
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	if !ok {
		c.String(http.StatusNotFound, "anomaly issue not found")
		return
	}

	issue := anomalyIssueFromDetail(detail)
	findings := classFindingsFromWire(detail.ClassFindings)

	var anomalyRows []SpanRow
	var sampleViews []sampleView
	chosenTrace := ""
	compareFlaggedNames := map[string]struct{}{}
	if detail.AnomalyTrace != nil && len(detail.AnomalyTrace.RawOtlp) > 0 {
		anomalyRows = traceSpans(detail.AnomalyTrace.RawOtlp)
		chosenTrace = detail.AnomalyTrace.TraceId
		sampleViews = []sampleView{{
			TraceID:    detail.AnomalyTrace.TraceId,
			ObservedAt: detail.AnomalyTrace.ObservedAt,
		}}
		highlights := map[string]engine.SpanHighlight{}
		for _, f := range findings {
			for _, h := range f.SpanHighlights {
				if _, exists := highlights[h.SpanID]; !exists {
					highlights[h.SpanID] = h
				}
			}
		}
		for i := range anomalyRows {
			if h, ok := highlights[anomalyRows[i].SpanID]; ok {
				anomalyRows[i].Highlight = h.Severity
				anomalyRows[i].HighlightReason = h.Reason
				anomalyRows[i].Expand = true
				compareFlaggedNames[anomalyRows[i].Name] = struct{}{}
			}
		}
	}

	var baselineRows []SpanRow
	var baselineTraceID string
	if detail.BaselineTrace != nil && len(detail.BaselineTrace.RawOtlp) > 0 {
		baselineRows = traceSpans(detail.BaselineTrace.RawOtlp)
		baselineTraceID = detail.BaselineTrace.TraceId
		// Pre-open the baseline span(s) matching a flagged anomaly span (by
		// name/site) so the normal value shows next to the deviating one.
		for i := range baselineRows {
			if _, ok := compareFlaggedNames[baselineRows[i].Name]; ok {
				baselineRows[i].Expand = true
			}
		}
	}

	riskGroups, riskScore, riskFireAt, riskCorrelated, riskBonus := riskViewFromWire(detail.Risk, issue.MaxScore)

	tx, _ := u.getTransaction(ctx, txnID)

	// Blast-radius service map for this issue's service: the callers/callees up
	// to serviceMapDepth hops it can ripple to. Best-effort — a query error
	// leaves an empty map, rendered as a short note rather than failing the page.
	serviceMap := serviceMapView{Depth: serviceMapDepth}
	if sub, err := u.repo.QueryBlastRadius(ctx, issue.Namespace, issue.Service, serviceMapDepth); err == nil {
		serviceMap = buildServiceMap(sub)
		serviceMap.Depth = serviceMapDepth
	}

	u.render(c, "anomaly_compare", "anomalies", c.Query("flash"), map[string]any{
		"Issue":                issue,
		"Transaction":          tx,
		"ServiceMap":           serviceMap,
		"AnomalyRows":          anomalyRows,
		"BaselineRows":         baselineRows,
		"BaselineTraceID":      baselineTraceID,
		"BaselineErr":          "",
		"Findings":             findings,
		"Samples":              sampleViews,
		"ChosenTrace":          chosenTrace,
		"Severity":             severityForScore(issue.MaxScore),
		"RiskGroups":           riskGroups,
		"RiskScore":            riskScore,
		"RiskFireAt":           riskFireAt,
		"RiskCorrelated":       riskCorrelated,
		"RiskCorrelationBonus": riskBonus,
	})
}

// anomalyIssueFromDetail maps the wire investigate issue into the repo shape
// the compare template already expects.
func anomalyIssueFromDetail(d apitypes.AnomalyIssue) repo.AnomalyIssue {
	classes := make([]repo.DeviationClass, len(d.TriggeredClasses))
	for i, c := range d.TriggeredClasses {
		classes[i] = repo.DeviationClass(c)
	}
	return repo.AnomalyIssue{
		TransactionID:    d.TransactionId,
		Signature:        d.Signature,
		Service:          d.Service,
		Namespace:        d.Namespace,
		Operation:        d.Operation,
		Kind:             repo.TransactionKind(d.Kind),
		TriggeredClasses: classes,
		Offending:        d.Offending,
		Evidence:         d.Evidence,
		PolicyID:         d.PolicyId,
		Occurrences:      d.Occurrences,
		MaxScore:         d.MaxScore,
		FirstSeen:        d.FirstSeen,
		LastSeen:         d.LastSeen,
		LastTraceID:      d.LastTraceId,
		Status:           repo.AnomalyIssueStatus(d.Status),
	}
}

// classFindingsFromWire adapts API class_findings to engine.Finding for the
// existing compare template field names.
func classFindingsFromWire(in []apitypes.AnomalyClassFinding) []engine.Finding {
	out := make([]engine.Finding, 0, len(in))
	for _, f := range in {
		item := engine.Finding{
			Class:       repo.DeviationClass(f.Class),
			Kind:        engine.FindingKind(f.Kind),
			Title:       f.Title,
			Summary:     f.Summary,
			RawEvidence: f.RawEvidence,
		}
		if len(f.SpanHighlights) > 0 {
			item.SpanHighlights = make([]engine.SpanHighlight, len(f.SpanHighlights))
			for i, h := range f.SpanHighlights {
				item.SpanHighlights[i] = engine.SpanHighlight{
					SpanID: h.SpanId, Severity: h.Severity, Reason: h.Reason,
				}
			}
		}
		if len(f.AttrHighlights) > 0 {
			item.AttrHighlights = make([]engine.AttrHighlight, len(f.AttrHighlights))
			for i, h := range f.AttrHighlights {
				item.AttrHighlights[i] = engine.AttrHighlight{
					SpanID: h.SpanId, Key: h.Key, Value: h.Value,
				}
			}
		}
		if f.Metric != nil {
			item.Metric = &engine.MetricComparison{
				Observed: f.Metric.Observed,
				Baseline: f.Metric.Baseline,
				Unit:     f.Metric.Unit,
			}
		}
		out = append(out, item)
	}
	return out
}

// riskViewFromWire builds the compare-page risk table from the wire assessment
// already returned by GetAnomalyIssueDetail.
func riskViewFromWire(r *apitypes.RiskAssessment, fallbackScore int) (groups []riskCategoryGroup, score, fireAt int, correlated bool, bonus int) {
	score, fireAt = fallbackScore, repo.DefaultFireAtScore
	if r == nil {
		return nil, score, fireAt, false, 0
	}
	signals := make([]risk.Signal, len(r.Signals))
	for i, s := range r.Signals {
		signals[i] = risk.Signal{
			Source:       repo.SignalSource(s.Source),
			Category:     risk.Category(s.Category),
			Weight:       s.Weight,
			Confidence:   s.Confidence,
			Contribution: s.Contribution,
			Rationale:    s.Rationale,
			Mitre:        s.Mitre,
			Owasp:        s.Owasp,
		}
	}
	cats := make([]risk.Category, len(r.Categories))
	for i, c := range r.Categories {
		cats[i] = risk.Category(c)
	}
	groups = buildRiskGroups(risk.Assessment{
		Score:      r.Score,
		Severity:   risk.Severity(r.Severity),
		Signals:    signals,
		Categories: cats,
		Correlated: r.Correlated,
	})
	return groups, r.Score, r.FireAtScore, r.Correlated, r.CorrelationBonus
}

// riskCategoryGroup groups an assessment's signals by kill-chain category for the
// investigate view's Risk breakdown table, carrying the category's MITRE/OWASP tags.
type riskCategoryGroup struct {
	Label   string
	Mitre   string
	Owasp   string
	Signals []riskSignalRow
}

// riskSignalRow is one signal in the breakdown: the raw risk.Signal plus a human
// name for its source and whether it came from an enricher (vs a deviation class).
type riskSignalRow struct {
	risk.Signal
	Name       string
	IsEnricher bool
}

// buildRiskGroups folds an assessment's signals into per-category groups in the
// assessment's own (sorted) category order, labelling each signal from the class
// or enricher catalog so the source id isn't the only thing shown.
func buildRiskGroups(a risk.Assessment) []riskCategoryGroup {
	enricherLabels := map[repo.SignalSource]string{}
	for _, e := range risk.EnricherSignals {
		enricherLabels[e.Source] = e.Label
	}
	byCat := map[risk.Category]*riskCategoryGroup{}
	var order []risk.Category
	for _, s := range a.Signals {
		g := byCat[s.Category]
		if g == nil {
			info := risk.Categories[s.Category]
			g = &riskCategoryGroup{Label: risk.CategoryLabel(s.Category), Mitre: info.Mitre, Owasp: info.Owasp}
			byCat[s.Category] = g
			order = append(order, s.Category)
		}
		_, isClass := risk.Catalog[repo.DeviationClass(s.Source)]
		name := risk.ClassLabel(repo.DeviationClass(s.Source))
		if !isClass {
			if l := enricherLabels[s.Source]; l != "" {
				name = l
			} else {
				name = string(s.Source)
			}
		}
		g.Signals = append(g.Signals, riskSignalRow{Signal: s, Name: name, IsEnricher: !isClass})
	}
	groups := make([]riskCategoryGroup, 0, len(order))
	for _, c := range order {
		groups = append(groups, *byCat[c])
	}
	return groups
}

// anomalyOverrideView is one scoped alert-policy override plus a display label
// and a link target (transaction overrides deep-link to the transaction page).
type anomalyOverrideView struct {
	Policy repo.Policy
	Scope  string // "service" | "transaction"
	Label  string // e.g. "billing/checkout" or "125538551172483654"
	Link   string // transaction detail URL, or "" for a service scope
}

func (u *ui) listAnomalyPolicies(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	policies, err := api.ListAlertPolicies(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	global, overrides := groupAnomalyPolicies(policies)
	u.render(c, "anomaly_policies", "settings", c.Query("flash"), map[string]any{
		"SettingsTab": "detection",
		"Global":      global,
		"Overrides":   overrides,
		"Classes":     repo.AllDeviationClasses,
	})
}

// groupAnomalyPolicies splits the flat policy list into the single global
// default and its service/transaction overrides, sorted for a stable page. The
// global default is always present (seeded by a migration); the zero value only
// shows if the row is somehow missing, and the editor lets the operator restore
// it. Scope is normalized on read, so there is no empty-scope case to handle.
func groupAnomalyPolicies(policies []repo.Policy) (repo.Policy, []anomalyOverrideView) {
	// Fallback shown only if the seeded global row is somehow missing; use the
	// default threshold (not 0, which now means the "info" tier) so the restore
	// starts from a sane value.
	global := repo.Policy{Scope: repo.ScopeGlobal, FireAtScore: repo.DefaultFireAtScore, Enabled: true}
	var overrides []anomalyOverrideView
	for _, policy := range policies {
		switch policy.Scope {
		case repo.ScopeService:
			overrides = append(overrides, anomalyOverrideView{Policy: policy, Scope: "service", Label: policy.ScopeKey})
		case repo.ScopeTransaction:
			overrides = append(overrides, anomalyOverrideView{
				Policy: policy, Scope: "transaction", Label: policy.ScopeKey,
				Link: "/debug/transactions/" + policy.ScopeKey,
			})
		default: // global
			global = policy
		}
	}
	sort.Slice(overrides, func(i, j int) bool {
		if overrides[i].Scope != overrides[j].Scope {
			return overrides[i].Scope < overrides[j].Scope
		}
		return overrides[i].Label < overrides[j].Label
	})
	return global, overrides
}

// saveAnomalyPolicy persists an edited alert policy from the structured editor
// and reloads the in-memory scope snapshot so it takes effect immediately.
func (u *ui) saveAnomalyPolicy(c *gin.Context) {
	redirect := "/debug/settings/detection"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scope, scopeKey, ok := scopeFromForm(c)
	if !ok {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("invalid scope"))
		return
	}
	policy := parseAnomalyPolicyForm(c, scope, scopeKey)
	if err := api.UpsertAlertPolicy(c.Request.Context(), policy); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("save failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(anomalyScopeSummary(scope, scopeKey)+" saved and live"))
}

// addAnomalyOverride creates a service- or transaction-scoped override, seeded
// from the current global default so the operator starts from the known-good
// policy and narrows from there. It then reloads and returns to the editor.
func (u *ui) addAnomalyOverride(c *gin.Context) {
	redirect := "/debug/settings/detection"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scope, scopeKey, ok := scopeFromForm(c)
	if !ok || scope == repo.ScopeGlobal || scopeKey == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("an override needs a service or transaction scope and a key"))
		return
	}

	// Seed from the current global default.
	seed := repo.Policy{Scope: scope, ScopeKey: scopeKey, FireAtScore: repo.DefaultFireAtScore, Enabled: true}
	existing, err := api.ListAlertPolicies(c.Request.Context())
	if err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("load failed: "+err.Error()))
		return
	}
	for _, policy := range existing {
		if policy.Scope == repo.ScopeGlobal {
			seed.Name = policy.Name
			seed.FireAtScore = policy.FireAtScore
			seed.SignalWeights = policy.SignalWeights
			seed.EnricherLists = policy.EnricherLists
			seed.Enabled = policy.Enabled
			break
		}
	}
	if err := api.UpsertAlertPolicy(c.Request.Context(), seed); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("add override failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(anomalyScopeSummary(scope, scopeKey)+" override added — edit it below"))
}

// deleteAnomalyPolicy drops one scoped override so the scope falls back to a
// broader policy (service → global).
func (u *ui) deleteAnomalyPolicy(c *gin.Context) {
	redirect := "/debug/settings/detection"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scope, scopeKey, ok := scopeFromForm(c)
	if !ok || scope == repo.ScopeGlobal {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("only service/transaction overrides can be deleted"))
		return
	}
	if err := api.DeleteAlertPolicy(c.Request.Context(), scope, scopeKey); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("delete failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(anomalyScopeSummary(scope, scopeKey)+" override deleted"))
}

// parseAnomalyPolicyForm builds a Policy from the editor form. fire_at is the
// risk-score threshold; weight_<SOURCE> fields carry per-signal weight overrides
// for both deviation classes and enricher signals (only values differing from
// the catalog default are stored, keeping the map sparse); enabled is the on/off
// toggle. ID is left 0 so the repo assigns the scope's stable policy_id.
func parseAnomalyPolicyForm(c *gin.Context, scope repo.PolicyScope, scopeKey string) repo.Policy {
	// fire_at is taken literally so an explicit 0 (info: fire on any signal) is
	// honored; only a blank/invalid field falls back to the default.
	fireAt := repo.DefaultFireAtScore
	if raw := strings.TrimSpace(c.PostForm("fire_at")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			fireAt = parsed
		}
	}
	if fireAt < 0 {
		fireAt = 0
	}
	if fireAt > 100 {
		fireAt = 100
	}

	signalWeights := map[repo.SignalSource]int{}
	readWeight := func(source repo.SignalSource, defaultWeight int) {
		raw := c.PostForm("weight_" + string(source))
		if raw == "" {
			return
		}
		weight, err := strconv.Atoi(raw)
		if err != nil || weight < 0 || weight == defaultWeight {
			return // invalid or matches the catalog default — not an override
		}
		signalWeights[source] = weight
	}
	for _, class := range repo.AllDeviationClasses {
		defaultWeight, _ := risk.DefaultWeight(class)
		readWeight(repo.SignalSource(class), defaultWeight)
	}
	for _, enricherSignal := range risk.EnricherSignals {
		readWeight(enricherSignal.Source, enricherSignal.Weight)
	}
	if len(signalWeights) == 0 {
		signalWeights = nil
	}

	// Enricher keyword lists: for every enricher that declares an editable list,
	// read its textarea (form field "list_<key>") and store it only when it
	// differs from the catalog default, keeping the map sparse. Generic over the
	// catalog, so a new config-driven enricher needs no code here.
	var enricherLists map[repo.EnricherListKey][]string
	for _, enricherSignal := range risk.EnricherSignals {
		if enricherSignal.List == nil {
			continue
		}
		keywords := parseKeywordList(c.PostForm("list_" + string(enricherSignal.List.Key)))
		if len(keywords) == 0 || sameStringSet(keywords, enricherSignal.List.Default) {
			continue // empty or unchanged → no override, use the default
		}
		if enricherLists == nil {
			enricherLists = map[repo.EnricherListKey][]string{}
		}
		enricherLists[enricherSignal.List.Key] = keywords
	}

	name := strings.TrimSpace(c.PostForm("name"))
	if name == "" {
		name = defaultAnomalyPolicyName(scope, scopeKey)
	}
	return repo.Policy{
		Name:          name,
		FireAtScore:   fireAt,
		SignalWeights: signalWeights,
		EnricherLists: enricherLists,
		Enabled:       c.PostForm("enabled") == "on",
		Scope:         scope,
		ScopeKey:      scopeKey,
	}
}

// parseKeywordList splits an editor textarea (comma- or newline-separated) into
// a normalized keyword list: trimmed, lowercased, de-duplicated, order
// preserved. Matching is a lowercased substring test, so casing/dupes don't
// matter functionally — normalizing just keeps storage and the defaults
// comparison stable.
func parseKeywordList(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		kw := strings.ToLower(strings.TrimSpace(f))
		if kw == "" || seen[kw] {
			continue
		}
		seen[kw] = true
		out = append(out, kw)
	}
	return out
}

// sameStringSet reports whether two slices contain the same set of strings
// (order-independent), used to detect when a sensitive-table list still matches
// the defaults so it can be stored as "no override".
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	for _, s := range b {
		if !set[s] {
			return false
		}
	}
	return true
}

// defaultAnomalyPolicyName is the fallback name when the operator leaves the
// name blank, so a policy always has a readable identifier.
func defaultAnomalyPolicyName(scope repo.PolicyScope, scopeKey string) string {
	switch scope {
	case repo.ScopeService:
		return "service:" + scopeKey
	case repo.ScopeTransaction:
		return "transaction:" + scopeKey
	default:
		return "global-default"
	}
}

// scopeSuffix renders the scope portion shared by the policy flash messages:
// "(service billing/checkout)", "(transaction 123)", or "(global default)".
// Both policy editors reuse it so the phrasing stays identical.
func scopeSuffix(scope repo.PolicyScope, scopeKey string) string {
	switch scope {
	case repo.ScopeService:
		return fmt.Sprintf("(service %s)", scopeKey)
	case repo.ScopeTransaction:
		return fmt.Sprintf("(transaction %s)", scopeKey)
	default:
		return "(global default)"
	}
}

// anomalyScopeSummary is the human phrase used in flash messages, e.g.
// "anomaly policy (service billing/checkout)".
func anomalyScopeSummary(scope repo.PolicyScope, scopeKey string) string {
	return "anomaly policy " + scopeSuffix(scope, scopeKey)
}

// learningOverrideView is one scoped override plus a display label and a link
// target (transaction overrides deep-link to the transaction detail page).
type learningOverrideView struct {
	Policy repo.LearningPolicy
	Scope  string // "service" | "transaction"
	Label  string // e.g. "billing/checkout" or "125538551172483654"
	Link   string // transaction detail URL, or "" for a service scope
}

// learningClassView groups one deviation class's global default with its
// service/transaction overrides, in the shape the template renders.
type learningClassView struct {
	Class     repo.DeviationClass
	Global    repo.LearningPolicy // zero policy if the class has no global row
	Overrides []learningOverrideView
}

func (u *ui) listLearningPolicies(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	policies, err := api.ListLearningPolicies(c.Request.Context())
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	u.render(c, "learning_policies", "settings", c.Query("flash"), map[string]any{
		"SettingsTab": "learning",
		"Classes":     groupLearningPolicies(policies),
	})
}

// groupLearningPolicies folds the flat policy list into one view per class,
// separating the global default from its overrides and sorting both for a
// stable page (classes A→Z, overrides by scope then key).
func groupLearningPolicies(policies []repo.LearningPolicy) []learningClassView {
	byClass := make(map[repo.DeviationClass]*learningClassView)
	order := make([]repo.DeviationClass, 0)
	classOf := func(class repo.DeviationClass) *learningClassView {
		view, ok := byClass[class]
		if !ok {
			view = &learningClassView{Class: class, Global: repo.LearningPolicy{Class: class}}
			byClass[class] = view
			order = append(order, class)
		}
		return view
	}
	for _, policy := range policies {
		view := classOf(policy.Class)
		switch policy.Scope {
		case repo.ScopeService:
			view.Overrides = append(view.Overrides, learningOverrideView{Policy: policy, Scope: "service", Label: policy.ScopeKey})
		case repo.ScopeTransaction:
			view.Overrides = append(view.Overrides, learningOverrideView{
				Policy: policy, Scope: "transaction", Label: policy.ScopeKey,
				Link: "/debug/transactions/" + policy.ScopeKey,
			})
		default: // global / empty
			view.Global = policy
		}
	}
	out := make([]learningClassView, 0, len(order))
	for _, class := range order {
		view := byClass[class]
		sort.Slice(view.Overrides, func(i, j int) bool {
			if view.Overrides[i].Scope != view.Overrides[j].Scope {
				return view.Overrides[i].Scope < view.Overrides[j].Scope
			}
			return view.Overrides[i].Label < view.Overrides[j].Label
		})
		out = append(out, *view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Class < out[j].Class })
	return out
}

// saveLearningPolicy persists an edited policy from the structured editor and
// reloads the in-memory snapshot so it takes effect immediately. The editor
// posts three parallel arrays (cond_type / cond_value / cond_unit), one entry
// per condition row.
func (u *ui) saveLearningPolicy(c *gin.Context) {
	class := repo.DeviationClass(c.Param("class"))
	redirect := "/debug/settings/learning"
	if class == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("missing class"))
		return
	}
	api, ok := u.productAPI(c)
	if !ok {
		return
	}

	scope, scopeKey, ok := scopeFromForm(c)
	if !ok {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("invalid scope"))
		return
	}

	policy := repo.LearningPolicy{
		Class:      class,
		Mode:       repo.LearningMode(c.PostForm("mode")),
		Conditions: parseConditionsForm(c),
		Scope:      scope,
		ScopeKey:   scopeKey,
	}
	if policy.Mode == repo.LearningModeKOf {
		policy.MinMatches, _ = strconv.Atoi(c.PostForm("min_matches"))
	}

	if err := api.UpsertLearningPolicy(c.Request.Context(), policy); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("save failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(scopeSummary(class, scope, scopeKey)+" saved and live"))
}

// addLearningOverride creates a service- or transaction-scoped override for a
// class, seeded from the class's current global default so the operator starts
// from the known-good policy and narrows from there. It then reloads and sends
// the user back to edit the fresh card.
func (u *ui) addLearningOverride(c *gin.Context) {
	class := repo.DeviationClass(c.Param("class"))
	redirect := "/debug/settings/learning"
	if class == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("missing class"))
		return
	}
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scope, scopeKey, ok := scopeFromForm(c)
	if !ok || scope == repo.ScopeGlobal || scopeKey == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("an override needs a service or transaction scope and a key"))
		return
	}

	// Seed from the current global default for this class.
	seed := repo.LearningPolicy{Class: class, Scope: scope, ScopeKey: scopeKey}
	existing, err := api.ListLearningPolicies(c.Request.Context())
	if err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("load failed: "+err.Error()))
		return
	}
	for _, policy := range existing {
		if policy.Class == class && policy.Scope == repo.ScopeGlobal {
			seed.Mode = policy.Mode
			seed.Conditions = policy.Conditions
			seed.MinMatches = policy.MinMatches
			break
		}
	}

	if err := api.UpsertLearningPolicy(c.Request.Context(), seed); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("add override failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(scopeSummary(class, scope, scopeKey)+" override added — edit it below"))
}

// deleteLearningPolicy drops one scoped override so the class falls back to a
// broader policy (service → global → count-only default).
func (u *ui) deleteLearningPolicy(c *gin.Context) {
	class := repo.DeviationClass(c.Param("class"))
	redirect := "/debug/settings/learning"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scope, scopeKey, ok := scopeFromForm(c)
	if !ok || scope == repo.ScopeGlobal {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("only service/transaction overrides can be deleted"))
		return
	}
	if err := api.DeleteLearningPolicy(c.Request.Context(), class, scope, scopeKey); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("delete failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(scopeSummary(class, scope, scopeKey)+" override deleted"))
}

// scopeFromForm reads and validates the scope fields shared by the policy forms.
// A missing scope defaults to global. A service key is normalized to
// "namespace/service"; a transaction key must be a numeric transaction_id.
// Returns ok=false on a malformed key.
func scopeFromForm(c *gin.Context) (repo.PolicyScope, string, bool) {
	switch repo.PolicyScope(c.PostForm("scope")) {
	case repo.ScopeService:
		key := strings.TrimSpace(c.PostForm("scope_key"))
		if key == "" || !strings.Contains(key, "/") {
			return "", "", false
		}
		return repo.ScopeService, key, true
	case repo.ScopeTransaction:
		key := strings.TrimSpace(c.PostForm("scope_key"))
		if _, err := strconv.ParseInt(key, 10, 64); err != nil {
			return "", "", false
		}
		return repo.ScopeTransaction, key, true
	default:
		return repo.ScopeGlobal, "", true
	}
}

// scopeSummary is the human phrase used in flash messages, e.g.
// "D1_call_edges (service billing/checkout)".
func scopeSummary(class repo.DeviationClass, scope repo.PolicyScope, scopeKey string) string {
	return fmt.Sprintf("%s %s", class, scopeSuffix(scope, scopeKey))
}

// parseConditionsForm turns the editor's parallel form arrays into typed
// conditions, mapping each row's single numeric value onto the field its type
// reads. Duration types multiply by their unit (min/hour/day). Rows with a
// non-positive value or unknown type are skipped, so an empty row is harmless.
func parseConditionsForm(c *gin.Context) []repo.LearningCondition {
	types := c.PostFormArray("cond_type")
	values := c.PostFormArray("cond_value")
	units := c.PostFormArray("cond_unit")
	out := make([]repo.LearningCondition, 0, len(types))
	for i, t := range types {
		v := int64(0)
		if i < len(values) {
			v, _ = strconv.ParseInt(strings.TrimSpace(values[i]), 10, 64)
		}
		if v <= 0 {
			continue
		}
		unit := "min"
		if i < len(units) && units[i] != "" {
			unit = units[i]
		}
		switch repo.LearningConditionType(t) {
		case repo.ConditionMinObservations:
			out = append(out, repo.LearningCondition{Type: repo.ConditionMinObservations, MinObservations: v})
		case repo.ConditionMinDuration:
			out = append(out, repo.LearningCondition{Type: repo.ConditionMinDuration, MinDurationMinutes: v * unitMinutes(unit)})
		case repo.ConditionStableObservations:
			out = append(out, repo.LearningCondition{Type: repo.ConditionStableObservations, StableObservations: v})
		case repo.ConditionStableDuration:
			out = append(out, repo.LearningCondition{Type: repo.ConditionStableDuration, StableMinutes: v * unitMinutes(unit)})
		}
	}
	return out
}

// unitMinutes converts a duration unit token to minutes.
func unitMinutes(unit string) int64 {
	switch unit {
	case "hour":
		return 60
	case "day":
		return 1440
	default:
		return 1
	}
}

// parseInt64Param writes a 400 to the response and returns an error if the
// param is missing or not an int64. On success, *out is populated.
func parseInt64Param(c *gin.Context, name string, out *int64) (bool, error) {
	v, err := strconv.ParseInt(c.Param(name), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "invalid %s", name)
		return false, err
	}
	*out = v
	return true, nil
}

// --- service guardrails ----------------------------------------------------

// guardrailRuleView pairs a catalog rule type with this service's current
// allowlist (as editor text), its entry count, and mode. Count drives the
// compact summary (only configured rules are chipped); ListText/Mode drive the
// expanded editor.
type guardrailRuleView struct {
	Rule     risk.GuardrailRule
	ListText string
	Count    int
	Mode     repo.RuleMode
	Origin   string
	Auto     bool // true when origin is auto_transaction_guardrail
}

// guardrailView is one service's guardrail as the editor renders it: the scope
// key split into namespace/service for display, plus a row per catalog rule
// type (so a rule with no config still renders an empty editor).
type guardrailView struct {
	ScopeKey  string
	Namespace string
	Service   string
	Rules     []guardrailRuleView
}

// buildGuardrailViews turns stored guardrails into editor rows, one per catalog
// rule type each, sorted by scope key for a stable page.
func buildGuardrailViews(guardrails []repo.ServiceGuardrail) []guardrailView {
	out := make([]guardrailView, 0, len(guardrails))
	for _, g := range guardrails {
		ns, svc := splitScopeKey(g.ScopeKey)
		view := guardrailView{ScopeKey: g.ScopeKey, Namespace: ns, Service: svc}
		for _, rule := range risk.GuardrailRules {
			list := g.RuleLists[rule.Key]
			origin := ""
			if g.RuleOrigins != nil {
				origin = g.RuleOrigins[rule.Key]
			}
			view.Rules = append(view.Rules, guardrailRuleView{
				Rule:     rule,
				ListText: strings.Join(list, "\n"),
				Count:    len(list),
				Mode:     repo.NormalizeRuleMode(g.RuleModes[rule.Key]),
				Origin:   origin,
				Auto:     origin == repo.GuardrailOriginAutoTransactionGuardrail,
			})
		}
		out = append(out, view)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ScopeKey < out[j].ScopeKey })
	return out
}

// splitScopeKey splits a "namespace/service" scope key; a key without a slash is
// treated as a bare service name.
func splitScopeKey(key string) (namespace, service string) {
	if i := strings.Index(key, "/"); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// listGuardrails renders the service-guardrails tab: existing guardrails as
// editors, plus (when ?namespace=&service= is supplied) that service's observed
// communication profile so the operator can adopt its real callers.
func (u *ui) listGuardrails(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	guardrails, err := api.ListServiceGuardrails(ctx)
	if err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
		return
	}
	namespaces, servicesByNamespace := u.serviceDirectory(ctx)
	servicesJSON, _ := json.Marshal(servicesByNamespace)
	serviceNames, _ := u.repo.ListServiceNames(ctx)
	data := map[string]any{
		"SettingsTab":  "guardrails",
		"Guardrails":   buildGuardrailViews(guardrails),
		"Rules":        risk.GuardrailRules,
		"Namespaces":   namespaces,
		"ServicesJSON": template.JS(servicesJSON),
		"ServiceNames": serviceNames,
	}
	ns := strings.TrimSpace(c.Query("namespace"))
	svc := strings.TrimSpace(c.Query("service"))
	// Service is required to derive a profile; namespace may be empty for
	// services ingested without one (the "no namespace" group).
	if svc != "" {
		data["ProfileNamespace"] = ns
		data["ProfileService"] = svc
		data["ProfileScopeKey"] = repo.ServiceScopeKey(ns, svc)
		if profile, perr := u.repo.GetServiceProfile(ctx, ns, svc); perr == nil {
			data["Profile"] = profile
		}
		peers := make([]string, 0, len(serviceNames))
		for _, name := range serviceNames {
			if name != svc {
				peers = append(peers, name)
			}
		}
		data["PeerServiceNames"] = peers
	}
	u.render(c, "guardrails", "settings", c.Query("flash"), data)
}

// serviceDirectory returns the sorted namespace list and a namespace→services
// map (each service list sorted), for the guardrail tab's dependent dropdowns.
func (u *ui) serviceDirectory(ctx context.Context) ([]string, map[string][]string) {
	stats, err := u.repo.ListServiceStats(ctx, 10000)
	if err != nil {
		return nil, map[string][]string{}
	}
	byNamespace := map[string][]string{}
	seen := map[string]struct{}{}
	for _, s := range stats {
		key := s.Namespace + "/" + s.Service
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		byNamespace[s.Namespace] = append(byNamespace[s.Namespace], s.Service)
	}
	namespaces := make([]string, 0, len(byNamespace))
	for ns := range byNamespace {
		namespaces = append(namespaces, ns)
		sort.Strings(byNamespace[ns])
	}
	sort.Strings(namespaces)
	return namespaces, byNamespace
}

// saveGuardrail upserts one service's guardrail from the editor and reloads the
// in-memory snapshot so it takes effect immediately.
func (u *ui) saveGuardrail(c *gin.Context) {
	redirect := "/debug/settings/guardrails"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scopeKey := strings.TrimSpace(c.PostForm("scope_key"))
	if scopeKey == "" || !strings.Contains(scopeKey, "/") {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("a guardrail needs a service scope of the form namespace/service"))
		return
	}
	// Preserve rule origins (e.g. auto_transaction_guardrail) across editor saves.
	existingOrigins := map[repo.RuleKey]string{}
	if existing, err := api.ListServiceGuardrails(c.Request.Context()); err == nil {
		for _, g := range existing {
			if g.ScopeKey == scopeKey {
				for k, v := range g.RuleOrigins {
					existingOrigins[k] = v
				}
				break
			}
		}
	}
	guardrail := repo.ServiceGuardrail{
		Scope:       repo.ScopeService,
		ScopeKey:    scopeKey,
		RuleLists:   map[repo.RuleKey][]string{},
		RuleModes:   map[repo.RuleKey]repo.RuleMode{},
		RuleOrigins: existingOrigins,
	}
	for _, rule := range risk.GuardrailRules {
		if list := splitList(c.PostForm("list_" + string(rule.Key))); len(list) > 0 {
			guardrail.RuleLists[rule.Key] = list
		}
		guardrail.RuleModes[rule.Key] = repo.NormalizeRuleMode(repo.RuleMode(c.PostForm("mode_" + string(rule.Key))))
	}
	if err := api.UpsertServiceGuardrail(c.Request.Context(), guardrail); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("save failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape("guardrail for "+scopeKey+" saved and live"))
}

// deleteGuardrail removes one service's guardrail so it is no longer segmented.
func (u *ui) deleteGuardrail(c *gin.Context) {
	redirect := "/debug/settings/guardrails"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scopeKey := strings.TrimSpace(c.PostForm("scope_key"))
	if scopeKey == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("missing scope key"))
		return
	}
	if err := api.DeleteServiceGuardrail(c.Request.Context(), scopeKey); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("delete failed: "+err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape("guardrail for "+scopeKey+" deleted"))
}

// seedGuardrail adds the operator's checkbox-selected callers/callees/egress
// (plus any free-form egress hosts) to that service's guardrail, creating it if
// needed, in the chosen mode. Callers/callees may be any seen service name
// (from the cluster-wide service-names list), not only observed edges; egress
// is open-ended.
func (u *ui) seedGuardrail(c *gin.Context) {
	redirect := "/debug/settings/guardrails"
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	scopeKey := strings.TrimSpace(c.PostForm("scope_key"))
	if scopeKey == "" || !strings.Contains(scopeKey, "/") {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("a guardrail needs a service scope of the form namespace/service"))
		return
	}
	items := map[string][]string{}
	for _, rule := range risk.GuardrailRules {
		if arr := c.PostFormArray(string(rule.Key)); len(arr) > 0 {
			items[string(rule.Key)] = arr
		}
	}
	// Free-form egress hosts (host or host:port) merge into allowed_egress.
	if extra := splitList(c.PostForm("allowed_egress_extra")); len(extra) > 0 {
		egressKey := string(risk.RuleAllowedEgress)
		items[egressKey] = uniqueStrings(append(items[egressKey], extra...))
	}
	req := apitypes.GuardrailSeedRequest{
		ScopeKey: scopeKey,
		Mode:     apitypes.RuleMode(c.PostForm("mode")),
		Items:    items,
	}
	if err := api.SeedServiceGuardrail(c.Request.Context(), req); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape("adopted allowlist into "+scopeKey))
}

func (u *ui) enableTransactionGuardrail(c *gin.Context) {
	u.runTransactionGuardrailAction(c, func(api ProductAPI, ns, svc string) error {
		return api.EnableTransactionGuardrail(c.Request.Context(), ns, svc)
	}, "transaction guardrail enabled")
}

func (u *ui) disableTransactionGuardrail(c *gin.Context) {
	u.runTransactionGuardrailAction(c, func(api ProductAPI, ns, svc string) error {
		return api.DisableTransactionGuardrail(c.Request.Context(), ns, svc)
	}, "transaction guardrail disabled")
}

func (u *ui) forcePromoteServiceTransactions(c *gin.Context) {
	u.runTransactionGuardrailAction(c, func(api ProductAPI, ns, svc string) error {
		return api.ForcePromoteService(c.Request.Context(), ns, svc)
	}, "service force-promoted")
}

func (u *ui) runTransactionGuardrailAction(c *gin.Context, fn func(ProductAPI, string, string) error, flash string) {
	redirect := c.PostForm("redirect")
	if redirect == "" {
		redirect = "/debug/transactions?view=services"
	}
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ns := strings.TrimSpace(c.PostForm("namespace"))
	svc := strings.TrimSpace(c.PostForm("service"))
	if ns == "" || svc == "" {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape("namespace and service are required"))
		return
	}
	if err := fn(api, ns, svc); err != nil {
		c.Redirect(http.StatusSeeOther, redirect+"?error="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusSeeOther, redirect+"?flash="+url.QueryEscape(flash))
}

// splitList parses a comma- or newline-separated editor field into a trimmed,
// de-duplicated, order-preserving list.
func splitList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' })
	return uniqueStrings(fields)
}

// uniqueStrings returns a trimmed, de-duplicated, order-preserving copy of in.
func uniqueStrings(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, f := range in {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}
