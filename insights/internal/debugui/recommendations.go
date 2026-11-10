package debugui

import (
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// The Recommendations page is the reference implementation for the product UI:
// it consumes GET /recommendations and the POST …/recommendations/{action}
// endpoints through the same ProductAPI functions the HTTP handlers call, and
// renders the wire types as they are. One list carries every kind; the page
// switches on `kind` to lay a service's own guardrail recommendation out as a
// card above its transactions' attribute correlation rows. Tabs are the
// recommendation lifecycle (open / applied / dismissed).

// recommendationKinds is the kind filter's option list. Add a kind here when
// the miner learns a new one; the API's enum is the source of truth.
var recommendationKinds = []struct{ Value, Label, Desc string }{
	{string(apitypes.RecommendationKindAttributeCorrelation), "Attribute correlation", "two span attributes that always agree within a trace"},
	{string(apitypes.RecommendationKindServiceGuardrail), "Service guardrail", "enforce a fully-learned service's allowlists from what was observed"},
}

// recRowsShown is how many of a transaction's recommendations render before
// the rest fold behind a "show more" row, so one busy transaction does not
// push the others off screen.
const recRowsShown = 5

// recTxn is one transaction's recommendations in the active tab, nested under
// its service card like transactions-with-anomalies on the Findings page.
type recTxn struct {
	TransactionID int64
	Operation     string
	Kind          apitypes.TransactionKind
	Items         []apitypes.Recommendation
	HighCount     int
	// Shown is how many Items render expanded; the remainder start folded.
	Shown int
}

// recCard is one service's recommendations, worst-first: the card header shows
// how many are waiting and how many the engine is confident about. Guardrail
// is the service's own service_guardrail recommendation in the active tab, if
// any; Transactions hold its attribute_correlation rows.
type recCard struct {
	Namespace    string
	Service      string
	ScopeKey     string
	Guardrail    *apitypes.Recommendation
	Transactions []recTxn
	Count        int
	HighCount    int
}

// recCounts is the per-tab tally shown on the tab labels.
type recCounts struct {
	Open, Applied, Dismissed int
}

// listRecommendations is GET /debug/recommendations.
func (u *ui) listRecommendations(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	tab := normalizeRecommendationTab(c.Query("tab"))
	kind := strings.TrimSpace(c.Query("kind"))
	namespace := strings.TrimSpace(c.Query("namespace"))
	service := strings.TrimSpace(c.Query("service"))
	txnID, _ := strconv.ParseInt(strings.TrimSpace(c.Query("transaction_id")), 10, 64)
	includeStale := c.Query("include_stale") == "1"

	// One list call with the shared filters; tab counts and the active tab's
	// cards are both derived from it, so the numbers always agree.
	listed, err := api.ListRecommendations(ctx, apitypes.ListRecommendationsParams{
		Kind:          apitypes.RecommendationKind(kind),
		TransactionId: txnID,
		Namespace:     apitypes.NamespaceFilter(namespace),
		Service:       apitypes.ServiceFilter(service),
		IncludeStale:  includeStale,
	})
	data := map[string]any{
		"Tab":           tab,
		"Kind":          kind,
		"Kinds":         recommendationKinds,
		"Namespace":     namespace,
		"Service":       service,
		"TransactionID": txnID,
		"IncludeStale":  includeStale,
	}
	if err != nil {
		data["Error"] = "Couldn't load recommendations (try refresh): " + err.Error()
		u.render(c, "recommendations", "recommendations", "", data)
		return
	}
	var counts recCounts
	var inTab []apitypes.Recommendation
	for _, rec := range listed.Items {
		switch rec.State {
		case apitypes.RecommendationStateApplied:
			counts.Applied++
		case apitypes.RecommendationStateDismissed:
			counts.Dismissed++
		default:
			counts.Open++
		}
		if string(rec.State) == tab {
			inTab = append(inTab, rec)
		}
	}
	if txnID != 0 {
		if tx, found, err := api.GetTransaction(ctx, txnID); err == nil && found {
			data["Transaction"] = tx
		}
	}

	base := url.Values{}
	for k, v := range map[string]string{"kind": kind, "namespace": namespace, "service": service} {
		if v != "" {
			base.Set(k, v)
		}
	}
	if txnID != 0 {
		base.Set("transaction_id", strconv.FormatInt(txnID, 10))
	}
	if includeStale {
		base.Set("include_stale", "1")
	}
	tabURL := func(t string) string {
		q := cloneQuery(base)
		q.Set("tab", t)
		return pathWithQuery("/debug/recommendations", q)
	}
	self := cloneQuery(base)
	self.Set("tab", tab)

	data["Cards"] = buildRecommendationCards(inTab)
	data["Counts"] = counts
	data["TabOpen"] = tabURL(string(apitypes.RecommendationStateOpen))
	data["TabApplied"] = tabURL(string(apitypes.RecommendationStateApplied))
	data["TabDismissed"] = tabURL(string(apitypes.RecommendationStateDismissed))
	data["SelfURL"] = pathWithQuery("/debug/recommendations", self)
	data["Namespaces"], data["Services"] = u.serviceDirectory(ctx)
	u.render(c, "recommendations", "recommendations", c.Query("flash"), data)
}

// normalizeRecommendationTab clamps ?tab= to a lifecycle state, defaulting to
// the actionable "open".
func normalizeRecommendationTab(s string) string {
	switch apitypes.RecommendationState(s) {
	case apitypes.RecommendationStateApplied, apitypes.RecommendationStateDismissed:
		return s
	default:
		return string(apitypes.RecommendationStateOpen)
	}
}

// buildRecommendationCards nests the wire list as service → (its own guardrail
// recommendation, transaction → attribute correlation rows), services with the
// most high-confidence items first and rows in the engine's rank order within
// a transaction.
func buildRecommendationCards(items []apitypes.Recommendation) []recCard {
	byService := map[string]*recCard{}
	byTxn := map[int64]*recTxn{}
	var serviceOrder []string
	for i := range items {
		rec := items[i]
		key := repo.ServiceScopeKey(rec.Namespace, rec.Service)
		card := byService[key]
		if card == nil {
			card = &recCard{Namespace: rec.Namespace, Service: rec.Service, ScopeKey: key}
			byService[key] = card
			serviceOrder = append(serviceOrder, key)
		}
		card.Count++
		if rec.Confidence.Level == apitypes.RecommendationConfidenceLevelHigh {
			card.HighCount++
		}
		if rec.Kind == apitypes.RecommendationKindServiceGuardrail {
			card.Guardrail = &items[i]
			continue
		}
		txn := byTxn[rec.TransactionId]
		if txn == nil {
			txn = &recTxn{TransactionID: rec.TransactionId, Operation: rec.Operation, Kind: rec.TransactionKind}
			byTxn[rec.TransactionId] = txn
			card.Transactions = append(card.Transactions, recTxn{TransactionID: rec.TransactionId})
		}
		txn.Items = append(txn.Items, rec)
		if rec.Confidence.Level == apitypes.RecommendationConfidenceLevelHigh {
			txn.HighCount++
		}
	}
	out := make([]recCard, 0, len(serviceOrder))
	for _, key := range serviceOrder {
		card := byService[key]
		for i := range card.Transactions {
			card.Transactions[i] = *byTxn[card.Transactions[i].TransactionID]
			items := card.Transactions[i].Items
			sort.SliceStable(items, func(a, b int) bool { return items[a].Rank < items[b].Rank })
			card.Transactions[i].Shown = min(recRowsShown, len(items))
		}
		out = append(out, *card)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].HighCount != out[j].HighCount {
			return out[i].HighCount > out[j].HighCount
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].ScopeKey < out[j].ScopeKey
	})
	return out
}

// --- actions ---------------------------------------------------------------

// recommendationsRedirect is the list URL to return to after a no-JS action.
func recommendationsRedirect(c *gin.Context) string {
	if r := strings.TrimSpace(c.PostForm("redirect")); strings.HasPrefix(r, "/debug/recommendations") {
		return r
	}
	return "/debug/recommendations"
}

// recommendationIDs reads the selected ids from a form: one "id" for a row
// action, many for a bulk one.
func recommendationIDs(c *gin.Context) []string {
	var ids []string
	for _, id := range c.PostFormArray("id") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// applyRecommendations is POST /debug/recommendations/apply. Without an edited
// spec it applies the recommendations as mined; with one (the edit dialog) it
// applies the single edited rule. A service guardrail card posts its id plus
// the checked "rule" boxes, which become the apply item's rule subset.
func (u *ui) applyRecommendations(c *gin.Context) {
	redirect := recommendationsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ids := recommendationIDs(c)
	if len(ids) == 0 {
		actionFail(c, redirect, "no recommendations selected")
		return
	}
	req := apitypes.RecommendationApplyRequest{}
	for _, id := range ids {
		req.Items = append(req.Items, apitypes.RecommendationApplyItem{Id: id})
	}
	// Plain apply enforces the recommendation as mined; the edit dialogs post
	// the operator's changes on top - an edited spec for a correlation, the
	// subset of rules to keep for a service guardrail.
	if c.PostForm("edited") == "1" && len(ids) == 1 {
		spec := correlationSpecFromForm(c)
		req.Items[0].Spec = &spec
	}
	if c.PostForm("edited_rules") == "1" && len(ids) == 1 {
		rules := c.PostFormArray("rule")
		if len(rules) == 0 {
			actionFail(c, redirect, "keep at least one thing for the guardrail to cover, or dismiss the recommendation")
			return
		}
		req.Items[0].Rules = rules
	}
	res, err := api.ApplyRecommendations(c.Request.Context(), req)
	finishRecommendationBulk(c, redirect, "applied", "now enforced on the guardrail; a trace that breaks it raises a finding", res, err)
}

// correlationSpecFromForm reads the edit dialog's fields into a wire spec.
func correlationSpecFromForm(c *gin.Context) apitypes.CorrelationSpec {
	return apitypes.CorrelationSpec{
		Name:     strings.TrimSpace(c.PostForm("name")),
		Relation: apitypes.CorrelationSpecRelation(strings.TrimSpace(c.PostForm("relation"))),
		Severity: apitypes.Severity(strings.TrimSpace(c.PostForm("severity"))),
		Why:      strings.TrimSpace(c.PostForm("why")),
		Left: apitypes.CorrelationSelector{
			Service: strings.TrimSpace(c.PostForm("left_service")),
			Span:    strings.TrimSpace(c.PostForm("left_span")),
			Attr:    strings.TrimSpace(c.PostForm("left_attr")),
			Extract: strings.TrimSpace(c.PostForm("left_extract")),
		},
		Right: apitypes.CorrelationSelector{
			Service: strings.TrimSpace(c.PostForm("right_service")),
			Span:    strings.TrimSpace(c.PostForm("right_span")),
			Attr:    strings.TrimSpace(c.PostForm("right_attr")),
			Extract: strings.TrimSpace(c.PostForm("right_extract")),
		},
	}
}

// dismissRecommendations is POST /debug/recommendations/dismiss.
func (u *ui) dismissRecommendations(c *gin.Context) {
	u.recommendationIDsAction(c, "dismissed", "hidden from Open; the miner remembers the decision", func(api ProductAPI, ids []string) (apitypes.RecommendationBulkResult, error) {
		return api.DismissRecommendations(c.Request.Context(), ids)
	})
}

// restoreRecommendations is POST /debug/recommendations/restore.
func (u *ui) restoreRecommendations(c *gin.Context) {
	u.recommendationIDsAction(c, "restored", "back in the Open tab", func(api ProductAPI, ids []string) (apitypes.RecommendationBulkResult, error) {
		return api.RestoreRecommendations(c.Request.Context(), ids)
	})
}

// revertRecommendations is POST /debug/recommendations/revert.
func (u *ui) revertRecommendations(c *gin.Context) {
	u.recommendationIDsAction(c, "removed from guardrail", "the rule is gone and the recommendation is back in Open", func(api ProductAPI, ids []string) (apitypes.RecommendationBulkResult, error) {
		return api.RevertRecommendations(c.Request.Context(), ids)
	})
}

func (u *ui) recommendationIDsAction(c *gin.Context, pastVerb, effect string, do func(ProductAPI, []string) (apitypes.RecommendationBulkResult, error)) {
	redirect := recommendationsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	ids := recommendationIDs(c)
	if len(ids) == 0 {
		actionFail(c, redirect, "no recommendations selected")
		return
	}
	res, err := do(api, ids)
	finishRecommendationBulk(c, redirect, pastVerb, effect, res, err)
}

// finishRecommendationBulk turns a bulk outcome into the fetch/redirect reply:
// success when every item went through, otherwise the first item error.
func finishRecommendationBulk(c *gin.Context, redirect, pastVerb, effect string, res apitypes.RecommendationBulkResult, err error) {
	if err != nil {
		actionFail(c, redirect, pastVerb+" failed: "+err.Error())
		return
	}
	if res.Failed > 0 {
		msg := fmt.Sprintf("%s %d, %d failed", pastVerb, res.Done, res.Failed)
		if len(res.Errors) > 0 {
			msg += ": " + res.Errors[0].Error
		}
		actionFail(c, redirect, msg)
		return
	}
	actionOK(c, redirect, fmt.Sprintf("%s %d recommendation%s - %s", pastVerb, res.Done, plural(res.Done), effect))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// previewRecommendation is POST /debug/recommendations/preview: the edit
// dialog posts the current spec and gets back an HTML fragment with how it
// fares on the transaction's stored samples.
func (u *ui) previewRecommendation(c *gin.Context) {
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	txnID, err := strconv.ParseInt(strings.TrimSpace(c.PostForm("transaction_id")), 10, 64)
	if err != nil {
		c.String(http.StatusBadRequest, "missing transaction")
		return
	}
	preview, err := api.PreviewRecommendation(c.Request.Context(), apitypes.RecommendationPreviewRequest{
		TransactionId: txnID,
		Spec:          correlationSpecFromForm(c),
	})
	if err != nil {
		c.String(http.StatusUnprocessableEntity, "%s", err.Error())
		return
	}
	c.Header("Content-Type", "text/html; charset=utf-8")
	if err := u.templates["recommendations"].ExecuteTemplate(c.Writer, "recPreview", preview); err != nil {
		c.String(http.StatusInternalServerError, "%v", err)
	}
}

// recomputeRecommendations is POST /debug/recommendations/recompute: re-mine
// one transaction or one service's guardrail card now, or everything.
func (u *ui) recomputeRecommendations(c *gin.Context) {
	redirect := recommendationsRedirect(c)
	api, ok := u.productAPI(c)
	if !ok {
		return
	}
	req := apitypes.RecommendationRecomputeRequest{
		Namespace: strings.TrimSpace(c.PostForm("namespace")),
		Service:   strings.TrimSpace(c.PostForm("service")),
	}
	req.TransactionId, _ = strconv.ParseInt(strings.TrimSpace(c.PostForm("transaction_id")), 10, 64)
	if err := api.RecomputeRecommendations(c.Request.Context(), req); err != nil {
		actionFail(c, redirect, "recompute failed: "+err.Error())
		return
	}
	switch {
	case req.Service != "":
		actionOK(c, redirect, "rebuilding this service's guardrail card in the background - refresh in a few seconds")
	case req.TransactionId != 0:
		actionOK(c, redirect, "re-mining this transaction in the background - refresh in a few seconds")
	default:
		actionOK(c, redirect, "re-mining every promoted transaction and service in the background - refresh in a minute")
	}
}
