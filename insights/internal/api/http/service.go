package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apimap"
	"github.com/odigos-io/odigos-enterprise/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

var (
	errAdminUnavailable    = errors.New("engine not ready; try again shortly")
	errTransactionNotFound = errors.New("transaction not found")
)

func (a *API) adminOrErr() (Admin, error) {
	if p := a.admin.Load(); p != nil && *p != nil {
		return *p, nil
	}
	return nil, errAdminUnavailable
}

// ---------- learning policies ----------

func (a *API) ListLearningPolicies(ctx context.Context) ([]repo.LearningPolicy, error) {
	return a.Repo.ListLearningPolicies(ctx)
}

func (a *API) UpsertLearningPolicy(ctx context.Context, p repo.LearningPolicy) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	if err := a.Repo.UpsertLearningPolicy(ctx, p); err != nil {
		return err
	}
	return admin.ReloadLearningConfig(ctx)
}

func (a *API) DeleteLearningPolicy(ctx context.Context, class repo.DeviationClass, scope repo.PolicyScope, scopeKey string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	scope = repo.NormalizePolicyScope(scope)
	if scope == repo.ScopeGlobal {
		return fmt.Errorf("only service/transaction overrides can be deleted")
	}
	if err := a.Repo.DeleteLearningPolicy(ctx, class, scope, scopeKey); err != nil {
		return err
	}
	return admin.ReloadLearningConfig(ctx)
}

// ---------- alert / detection policies ----------

func (a *API) ListAlertPolicies(ctx context.Context) ([]repo.Policy, error) {
	return a.Repo.ListAlertPolicies(ctx)
}

func (a *API) ListEnabledPolicies(ctx context.Context) ([]repo.Policy, error) {
	return a.Repo.ListEnabledPolicies(ctx)
}

func (a *API) UpsertAlertPolicy(ctx context.Context, p repo.Policy) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	if err := a.Repo.UpsertAlertPolicy(ctx, p); err != nil {
		return err
	}
	return admin.ReloadAlertConfig(ctx)
}

func (a *API) DeleteAlertPolicy(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	scope = repo.NormalizePolicyScope(scope)
	if scope == repo.ScopeGlobal {
		return fmt.Errorf("only service/transaction overrides can be deleted")
	}
	if err := a.Repo.DeleteAlertPolicy(ctx, scope, scopeKey); err != nil {
		return err
	}
	return admin.ReloadAlertConfig(ctx)
}

// ---------- guardrails ----------

func (a *API) ListServiceGuardrails(ctx context.Context) ([]repo.ServiceGuardrail, error) {
	return a.Repo.ListServiceGuardrails(ctx)
}

func (a *API) UpsertServiceGuardrail(ctx context.Context, g repo.ServiceGuardrail) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	if repo.NormalizePolicyScope(g.Scope) == repo.ScopeTransaction {
		if strings.TrimSpace(g.ScopeKey) == "" {
			return fmt.Errorf("a transaction guardrail needs a transaction id scope key")
		}
	} else if !isServiceScopeKey(g.ScopeKey) {
		return fmt.Errorf("a guardrail needs a service scope of the form namespace/service")
	}
	if err := a.stampTransactionIdentityScheme(ctx, &g); err != nil {
		return err
	}
	if err := a.Repo.UpsertServiceGuardrail(ctx, g); err != nil {
		return err
	}
	return admin.ReloadGuardrailConfig(ctx)
}

// stampTransactionIdentityScheme records which identity scheme an
// allowed_transactions allowlist was written under. Every path that stores such
// a list goes through here, because the engine refuses to enforce a list whose
// stamp does not match the scheme it is currently building identities with -
// that is what stops a stale allowlist from reporting every transaction as
// forbidden after an operator edits the identity dimensions. Clears the stamp
// when the rule carries no entries.
func (a *API) stampTransactionIdentityScheme(ctx context.Context, guardrail *repo.ServiceGuardrail) error {
	if len(guardrail.RuleLists[risk.RuleAllowedTransactions]) == 0 {
		repo.SetRuleFingerprint(guardrail, risk.RuleAllowedTransactions, "")
		return nil
	}
	settings, err := a.Repo.GetSystemSettings(ctx)
	if err != nil {
		return fmt.Errorf("read identity dimensions: %w", err)
	}
	fingerprint := repo.IdentityDimensionsFingerprint(settings.WithDefaults().TransactionIdentityDimensions)
	repo.SetRuleFingerprint(guardrail, risk.RuleAllowedTransactions, fingerprint)
	return nil
}

func (a *API) DeleteServiceGuardrail(ctx context.Context, scope repo.PolicyScope, scopeKey string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	scopeKey = strings.TrimSpace(scopeKey)
	if scopeKey == "" {
		return fmt.Errorf("missing scope_key")
	}
	if err := a.Repo.DeleteServiceGuardrail(ctx, repo.NormalizePolicyScope(scope), scopeKey); err != nil {
		return err
	}
	return admin.ReloadGuardrailConfig(ctx)
}

func (a *API) SeedServiceGuardrail(ctx context.Context, req apitypes.GuardrailSeedRequest) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	if !isServiceScopeKey(req.ScopeKey) {
		return fmt.Errorf("a guardrail needs a service scope of the form namespace/service")
	}
	mode := repo.NormalizeRuleMode(repo.RuleMode(req.Mode))
	guardrail, err := a.loadOrInitGuardrail(ctx, req.ScopeKey)
	if err != nil {
		return err
	}
	if guardrail.RuleLists == nil {
		guardrail.RuleLists = map[repo.RuleKey][]string{}
	}
	if guardrail.RuleModes == nil {
		guardrail.RuleModes = map[repo.RuleKey]repo.RuleMode{}
	}
	added := 0
	for _, rule := range risk.GuardrailRules {
		items := req.Items[string(rule.Key)]
		if len(items) == 0 {
			continue
		}
		for _, item := range items {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			before := len(guardrail.RuleLists[rule.Key])
			guardrail.RuleLists[rule.Key] = appendUnique(guardrail.RuleLists[rule.Key], item)
			if len(guardrail.RuleLists[rule.Key]) > before {
				added++
			}
		}
		guardrail.RuleModes[rule.Key] = mode
	}
	if added == 0 {
		return fmt.Errorf("select at least one caller, callee, egress destination, or transaction to adopt")
	}
	if err := a.stampTransactionIdentityScheme(ctx, &guardrail); err != nil {
		return err
	}
	if err := a.Repo.UpsertServiceGuardrail(ctx, guardrail); err != nil {
		return err
	}
	return admin.ReloadGuardrailConfig(ctx)
}

func (a *API) EnableTransactionGuardrail(ctx context.Context, namespace, service string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.EnableTransactionGuardrail(ctx, namespace, service)
}

func (a *API) DisableTransactionGuardrail(ctx context.Context, namespace, service string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.DisableTransactionGuardrail(ctx, namespace, service)
}

func (a *API) ForcePromoteService(ctx context.Context, namespace, service string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.ForcePromoteService(ctx, namespace, service)
}

func (a *API) AcceptGuardrailViolation(ctx context.Context, scopeKey string, ruleKey repo.RuleKey, offending string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	guardrail, err := a.loadOrInitGuardrail(ctx, scopeKey)
	if err != nil {
		return err
	}
	if guardrail.RuleLists == nil {
		guardrail.RuleLists = map[repo.RuleKey][]string{}
	}
	if guardrail.RuleModes == nil {
		guardrail.RuleModes = map[repo.RuleKey]repo.RuleMode{}
	}
	guardrail.RuleLists[ruleKey] = appendUnique(guardrail.RuleLists[ruleKey], offending)
	guardrail.RuleModes[ruleKey] = repo.RuleModeEnforce
	if err := a.stampTransactionIdentityScheme(ctx, &guardrail); err != nil {
		return err
	}
	if err := a.Repo.UpsertServiceGuardrail(ctx, guardrail); err != nil {
		return err
	}
	if err := admin.ReloadGuardrailConfig(ctx); err != nil {
		return err
	}
	return a.Repo.ResolveGuardrailViolation(ctx, repo.ScopeService, scopeKey, ruleKey, offending, repo.GuardrailResolveAccept)
}

func (a *API) ResolveGuardrailViolation(ctx context.Context, scopeKey string, ruleKey repo.RuleKey, offending string, resolution repo.GuardrailResolution) error {
	return a.Repo.ResolveGuardrailViolation(ctx, repo.ScopeService, scopeKey, ruleKey, offending, resolution)
}

// ---------- guardrail recommendations ----------

// ListRecommendations is the product API for GET /api/v1/recommendations -
// every kind in one list, same params and wire shape the real UI and debug UI
// consume.
func (a *API) ListRecommendations(ctx context.Context, params apitypes.ListRecommendationsParams) (apitypes.RecommendationList, error) {
	admin, err := a.adminOrErr()
	if err != nil {
		return apitypes.RecommendationList{}, err
	}
	if params.Kind != "" && !params.Kind.Valid() {
		return apitypes.RecommendationList{}, fmt.Errorf("%w: unknown recommendation kind %q", errBadRequest, params.Kind)
	}
	if params.State != "" && !params.State.Valid() {
		return apitypes.RecommendationList{}, fmt.Errorf("%w: unknown recommendation state %q", errBadRequest, params.State)
	}
	views, err := admin.ListRecommendations(ctx, repo.RecommendationFilter{
		Kind:          repo.RecommendationKind(params.Kind),
		State:         repo.RecommendationState(params.State),
		TransactionID: params.TransactionId,
		Namespace:     string(params.Namespace),
		Service:       string(params.Service),
		IncludeStale:  params.IncludeStale,
	})
	if err != nil {
		return apitypes.RecommendationList{}, err
	}
	return apimap.RecommendationList(views), nil
}

// GetRecommendation is GET /api/v1/recommendations/{id}, any kind.
func (a *API) GetRecommendation(ctx context.Context, id string) (apitypes.Recommendation, bool, error) {
	admin, err := a.adminOrErr()
	if err != nil {
		return apitypes.Recommendation{}, false, err
	}
	view, ok, err := admin.GetRecommendation(ctx, strings.TrimSpace(id))
	if err != nil || !ok {
		return apitypes.Recommendation{}, false, err
	}
	return apimap.FromRecommendation(view), true, nil
}

// ApplyRecommendations is POST /api/v1/recommendations/apply: each item becomes
// a rule on its transaction guardrail (optionally with the operator's edits),
// or the selected rules on its service guardrail.
func (a *API) ApplyRecommendations(ctx context.Context, req apitypes.RecommendationApplyRequest) (apitypes.RecommendationBulkResult, error) {
	admin, err := a.adminOrErr()
	if err != nil {
		return apitypes.RecommendationBulkResult{}, err
	}
	if len(req.Items) == 0 {
		return apitypes.RecommendationBulkResult{}, fmt.Errorf("%w: select at least one recommendation", errBadRequest)
	}
	for _, it := range req.Items {
		if it.Spec != nil {
			if err := apimap.ValidateCorrelationSpec(*it.Spec); err != nil {
				return apitypes.RecommendationBulkResult{}, fmt.Errorf("%w: recommendation %s: %v", errBadRequest, it.Id, err)
			}
		}
	}
	return apimap.FromRecommendationBulkResult(admin.ApplyRecommendations(ctx, apimap.ToRecommendationApplyItems(req))), nil
}

// DismissRecommendations is POST /api/v1/recommendations/dismiss.
func (a *API) DismissRecommendations(ctx context.Context, ids []string) (apitypes.RecommendationBulkResult, error) {
	return a.recommendationBulk(ctx, ids, func(admin Admin, ids []string) engine.RecommendationBulkResult {
		return admin.DismissRecommendations(ctx, ids)
	})
}

// RestoreRecommendations is POST /api/v1/recommendations/restore.
func (a *API) RestoreRecommendations(ctx context.Context, ids []string) (apitypes.RecommendationBulkResult, error) {
	return a.recommendationBulk(ctx, ids, func(admin Admin, ids []string) engine.RecommendationBulkResult {
		return admin.RestoreRecommendations(ctx, ids)
	})
}

// RevertRecommendations is POST /api/v1/recommendations/revert.
func (a *API) RevertRecommendations(ctx context.Context, ids []string) (apitypes.RecommendationBulkResult, error) {
	return a.recommendationBulk(ctx, ids, func(admin Admin, ids []string) engine.RecommendationBulkResult {
		return admin.RevertRecommendations(ctx, ids)
	})
}

func (a *API) recommendationBulk(ctx context.Context, ids []string, do func(Admin, []string) engine.RecommendationBulkResult) (apitypes.RecommendationBulkResult, error) {
	admin, err := a.adminOrErr()
	if err != nil {
		return apitypes.RecommendationBulkResult{}, err
	}
	clean := make([]string, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			clean = append(clean, id)
		}
	}
	if len(clean) == 0 {
		return apitypes.RecommendationBulkResult{}, fmt.Errorf("%w: select at least one recommendation", errBadRequest)
	}
	return apimap.FromRecommendationBulkResult(do(admin, clean)), nil
}

// PreviewRecommendation is POST /api/v1/recommendations/preview: how an
// (edited) spec fares on the transaction's stored relation samples.
func (a *API) PreviewRecommendation(ctx context.Context, req apitypes.RecommendationPreviewRequest) (apitypes.RecommendationPreview, error) {
	admin, err := a.adminOrErr()
	if err != nil {
		return apitypes.RecommendationPreview{}, err
	}
	if req.TransactionId <= 0 {
		return apitypes.RecommendationPreview{}, fmt.Errorf("%w: transaction_id is required", errBadRequest)
	}
	if err := apimap.ValidateCorrelationSelectors(req.Spec); err != nil {
		return apitypes.RecommendationPreview{}, fmt.Errorf("%w: %v", errBadRequest, err)
	}
	preview, err := admin.PreviewRecommendation(ctx, req.TransactionId, apimap.ToCorrelationSpec(req.Spec))
	if err != nil {
		return apitypes.RecommendationPreview{}, err
	}
	return apimap.FromRecommendationPreview(preview), nil
}

// RecomputeRecommendations is POST /api/v1/recommendations/recompute: one
// transaction, one service's card, or (empty request) everything.
func (a *API) RecomputeRecommendations(ctx context.Context, req apitypes.RecommendationRecomputeRequest) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	namespace, service := strings.TrimSpace(req.Namespace), strings.TrimSpace(req.Service)
	switch {
	case req.TransactionId != 0 && service != "":
		return fmt.Errorf("%w: pass either transaction_id or namespace/service, not both", errBadRequest)
	case service != "":
		return admin.RecomputeServiceGuardrailRecommendation(ctx, namespace, service)
	case namespace != "":
		return fmt.Errorf("%w: service is required with namespace", errBadRequest)
	case req.TransactionId != 0:
		if _, found, err := a.Repo.GetTransactionByID(ctx, req.TransactionId); err != nil {
			return err
		} else if !found {
			return errTransactionNotFound
		}
	}
	return admin.RecomputeRecommendations(ctx, req.TransactionId)
}

// ---------- baselines ----------

// GetTransactionBaseline returns every scored class for one transaction, with
// a computed learning/stability snapshot on each row.
func (a *API) GetTransactionBaseline(ctx context.Context, txID int64) (apitypes.BaselineClassList, error) {
	rows, err := a.Repo.GetBaselineForTransaction(ctx, txID)
	if err != nil {
		return apitypes.BaselineClassList{}, err
	}
	rows = engine.FillScoredPlaceholders(txID, rows)

	policies, err := a.Repo.ListLearningPolicies(ctx)
	if err != nil {
		return apitypes.BaselineClassList{}, err
	}
	cfg := engine.LearningConfigFromRows(policies)

	serviceKey := ""
	if tx, found, err := a.Repo.GetTransactionByID(ctx, txID); err != nil {
		return apitypes.BaselineClassList{}, err
	} else if found {
		serviceKey = repo.ServiceScopeKey(tx.Namespace, tx.Service)
	}

	now := time.Now()
	learning := make([]engine.BaselineLearning, len(rows))
	for i, bc := range rows {
		pol := cfg.Resolve(bc.Class, txID, serviceKey)
		learning[i] = engine.LearningOf(bc, pol, now)
	}
	return apimap.BaselineClassList(rows, learning), nil
}

func (a *API) PromoteBaselineClass(ctx context.Context, txID int64, class repo.DeviationClass) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.PromoteBaselineClass(ctx, txID, class)
}

func (a *API) ResetBaselineClass(ctx context.Context, txID int64, class repo.DeviationClass) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.ResetBaselineClass(ctx, txID, class)
}

func (a *API) ResetTransactionBaselines(ctx context.Context, txID int64) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.ResetTransactionBaselines(ctx, txID)
}

// ---------- transactions ----------

// ListTransactions is the product API for GET /api/v1/transactions - same
// OpenAPI params and wire shape the real UI and debug UI consume.
func (a *API) ListTransactions(ctx context.Context, params apitypes.ListTransactionsParams) (apitypes.TransactionStatList, error) {
	sortBy, err := repo.NormalizeTransactionSortBy(string(params.SortBy))
	if err != nil {
		return apitypes.TransactionStatList{}, err
	}
	sortOrder, err := repo.NormalizeSortOrder(string(params.SortOrder))
	if err != nil {
		return apitypes.TransactionStatList{}, err
	}
	listParams := repo.TransactionListParams{
		Namespace: params.Namespace,
		Service:   params.Service,
		Kind:      string(params.Kind),
		SortBy:    sortBy,
		SortOrder: sortOrder,
		Limit:     1000,
	}.WithDefaults()
	since, until, err := transactionActiveWindowFromParams(params.WindowHours, params.From, params.To)
	if err != nil {
		return apitypes.TransactionStatList{}, err
	}
	listParams.ActiveSince = since
	listParams.ActiveUntil = until
	stats, err := a.Repo.ListTransactionStats(ctx, listParams)
	if err != nil {
		return apitypes.TransactionStatList{}, err
	}
	return apimap.TransactionStatList(stats), nil
}

// GetTransaction returns one transaction's identity (service, namespace,
// operation, kind). Shared by the HTTP detail handler and the debug UI.
func (a *API) GetTransaction(ctx context.Context, transactionID int64) (repo.Transaction, bool, error) {
	return a.Repo.GetTransactionByID(ctx, transactionID)
}

// DeleteTransaction permanently removes a transaction via the engine so the
// in-memory cache and policy snapshots stay consistent.
func (a *API) DeleteTransaction(ctx context.Context, transactionID int64) error {
	_, found, err := a.Repo.GetTransactionByID(ctx, transactionID)
	if err != nil {
		return err
	}
	if !found {
		return errTransactionNotFound
	}
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.DeleteTransaction(ctx, transactionID)
}

// bulkPartialError is returned when a bulk mutate partially succeeds. Mapped to
// HTTP 422 by writeAPIError (same contract as POST /anomalies bulk-resolve).
type bulkPartialError struct {
	verb         string
	done, failed int
	last         error
}

func (e *bulkPartialError) Error() string {
	verb := e.verb
	if verb == "" {
		verb = "processed"
	}
	return fmt.Sprintf("%s %d, %d failed: %v", verb, e.done, e.failed, e.last)
}

// DeleteTransactions permanently removes many transactions. Returns how many
// were deleted. On partial failure returns that count plus a bulkPartialError.
func (a *API) DeleteTransactions(ctx context.Context, transactionIDs []int64) (int, error) {
	var done, failed int
	var lastErr error
	for _, id := range transactionIDs {
		if id <= 0 {
			failed++
			continue
		}
		if err := a.DeleteTransaction(ctx, id); err != nil {
			failed++
			lastErr = err
			continue
		}
		done++
	}
	if failed > 0 {
		return done, &bulkPartialError{verb: "deleted", done: done, failed: failed, last: lastErr}
	}
	return done, nil
}

// PromoteTransactionBaselines force-promotes every scored class of one
// transaction (skips learning gates). Same cleanup path ForcePromoteService
// uses per transaction.
func (a *API) PromoteTransactionBaselines(ctx context.Context, transactionID int64) error {
	_, found, err := a.Repo.GetTransactionByID(ctx, transactionID)
	if err != nil {
		return err
	}
	if !found {
		return errTransactionNotFound
	}
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return admin.PromoteTransactionBaselines(ctx, transactionID)
}

// PromoteTransactions force-promotes every scored class on many transactions.
// Returns how many transactions were fully promoted. On partial failure returns
// that count plus a bulkPartialError.
func (a *API) PromoteTransactions(ctx context.Context, transactionIDs []int64) (int, error) {
	admin, err := a.adminOrErr()
	if err != nil {
		return 0, err
	}
	done, failed, lastErr := admin.PromoteTransactionsBaselines(ctx, transactionIDs)
	if failed > 0 {
		return done, &bulkPartialError{verb: "promoted", done: done, failed: failed, last: lastErr}
	}
	return done, nil
}

// ---------- observations ----------

// ListObservations returns retained sample metadata for one transaction
// (no raw_otlp). Shared by the HTTP list handler and the debug UI.
func (a *API) ListObservations(ctx context.Context, transactionID int64, sampleReason string, limit int) ([]repo.Observation, error) {
	return a.Repo.ListObservationsByTransaction(ctx, transactionID, sampleReason, limit)
}

// GetObservation returns one retained sample including raw_otlp.
// Shared by the HTTP detail handler and the debug UI.
func (a *API) GetObservation(ctx context.Context, transactionID int64, traceID string) (repo.Observation, bool, error) {
	return a.Repo.GetObservationByTrace(ctx, transactionID, traceID)
}

// ---------- anomalies / findings ----------

// ListFindings is the product API for GET /api/v1/findings - same params and
// wire shape the real UI and debug UI consume.
func (a *API) ListFindings(ctx context.Context, params apitypes.ListFindingsParams) (apitypes.FindingList, error) {
	filter, err := findingListFilterFromTimeParams(
		params.WindowHours, params.From, params.To,
		params.Service, params.Namespace, params.Status,
		findingsListLimit,
	)
	if err != nil {
		return apitypes.FindingList{}, err
	}

	var findings []apitypes.Finding
	kind := params.Kind

	if kind == "" || kind == apitypes.Violation {
		groups, err := a.Repo.ListGuardrailViolations(ctx, filter)
		if err != nil {
			return apitypes.FindingList{}, err
		}
		for _, group := range groups {
			findings = append(findings, apimap.FindingFromViolation(group))
		}
	}
	if kind == "" || kind == apitypes.Anomaly {
		issues, err := a.Repo.ListAnomalyIssues(ctx, filter)
		if err != nil {
			return apitypes.FindingList{}, err
		}
		for _, issue := range issues {
			findings = append(findings, apimap.FindingFromAnomaly(issue))
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Score != findings[j].Score {
			return findings[i].Score > findings[j].Score
		}
		return findings[i].LastSeen.After(findings[j].LastSeen)
	})
	return apimap.FindingList(findings), nil
}

// ListAnomalies is the product API for GET /api/v1/anomalies.
func (a *API) ListAnomalies(ctx context.Context, params apitypes.ListAnomaliesParams) (apitypes.AnomalyIssueList, error) {
	filter, err := findingListFilterFromTimeParams(
		params.WindowHours, params.From, params.To,
		params.Service, params.Namespace, params.Status,
		findingsListLimit,
	)
	if err != nil {
		return apitypes.AnomalyIssueList{}, err
	}
	var issues []repo.AnomalyIssue
	if params.TransactionId != 0 {
		issues, err = a.Repo.ListAnomalyIssuesByTransaction(ctx, params.TransactionId, filter)
	} else {
		issues, err = a.Repo.ListAnomalyIssues(ctx, filter)
	}
	if err != nil {
		return apitypes.AnomalyIssueList{}, err
	}
	return apimap.AnomalyIssueList(issues), nil
}

// GetAnomalyIssue returns one issue including evidence (repo shape). Prefer
// GetAnomalyIssueDetail for the investigate HTTP/UI payload.
func (a *API) GetAnomalyIssue(ctx context.Context, transactionID int64, signature string) (repo.AnomalyIssue, bool, error) {
	return a.Repo.GetAnomalyIssue(ctx, transactionID, signature)
}

// GetAnomalyIssueDetail is the product investigate payload for
// GET /api/v1/anomalies/{transaction_id}/{signature}: issue + risk +
// class_findings + anomaly/baseline compare traces. Shared by the HTTP handler
// and the debug UI so both consume the same contract as the real UI.
func (a *API) GetAnomalyIssueDetail(ctx context.Context, transactionID int64, signature string) (apitypes.AnomalyIssue, bool, error) {
	issue, ok, err := a.Repo.GetAnomalyIssue(ctx, transactionID, signature)
	if err != nil || !ok {
		return apitypes.AnomalyIssue{}, ok, err
	}
	out := apimap.FromAnomalyIssue(issue)

	if assessment, fireAt, aErr := a.AssessAnomalyIssue(ctx, issue); aErr == nil {
		riskWire := apimap.FromAssessment(assessment, fireAt)
		out.Risk = &riskWire
	}

	var anomalySpans []engine.SpanView
	if obs := a.loadAnomalyEvidenceTrace(ctx, transactionID, signature, issue.LastTraceID); obs != nil {
		wire := apimap.FromObservation(*obs)
		out.AnomalyTrace = &wire
		if len(obs.RawOTLP) > 0 {
			anomalySpans = engine.SpansFromOTLP(obs.RawOTLP)
		}
	}
	if obs := a.loadBaselineExampleTrace(ctx, transactionID); obs != nil {
		wire := apimap.FromObservation(*obs)
		out.BaselineTrace = &wire
	}

	evidenceByClass := map[string]json.RawMessage{}
	_ = json.Unmarshal(issue.Evidence, &evidenceByClass)
	out.ClassFindings = make([]apitypes.AnomalyClassFinding, 0, len(issue.TriggeredClasses))
	for _, class := range issue.TriggeredClasses {
		f := engine.Explain(class, evidenceByClass[string(class)], anomalySpans)
		out.ClassFindings = append(out.ClassFindings, apimap.FromClassFinding(f))
	}
	return out, true, nil
}

// loadAnomalyEvidenceTrace returns the retained evidence sample for an issue
// (best-effort). Prefers sample_reason=anomaly:<signature>; falls back to
// last_trace_id when the tagged sample is missing.
func (a *API) loadAnomalyEvidenceTrace(ctx context.Context, transactionID int64, signature, lastTraceID string) *repo.Observation {
	samples, err := a.ListObservations(ctx, transactionID, repo.AnomalySampleReason(signature), 1)
	if err == nil && len(samples) > 0 {
		if full, ok, gerr := a.GetObservation(ctx, transactionID, samples[0].TraceID); gerr == nil && ok {
			return &full
		}
	}
	if lastTraceID == "" {
		return nil
	}
	full, ok, err := a.GetObservation(ctx, transactionID, lastTraceID)
	if err != nil || !ok {
		return nil
	}
	return &full
}

// loadBaselineExampleTrace returns one learning-phase example for compare
// (best-effort).
func (a *API) loadBaselineExampleTrace(ctx context.Context, transactionID int64) *repo.Observation {
	samples, err := a.ListObservations(ctx, transactionID, repo.ExampleSampleReason, 1)
	if err != nil || len(samples) == 0 {
		return nil
	}
	full, ok, err := a.GetObservation(ctx, transactionID, samples[0].TraceID)
	if err != nil || !ok {
		return nil
	}
	return &full
}

func (a *API) ResolveAnomalyIssue(ctx context.Context, txID int64, signature, resolution string) error {
	admin, err := a.adminOrErr()
	if err != nil {
		return err
	}
	return applyAnomalyResolution(ctx, admin, resolution, txID, signature)
}

func (a *API) AssessAnomalyIssue(ctx context.Context, issue repo.AnomalyIssue) (risk.Assessment, int, error) {
	admin, err := a.adminOrErr()
	if err != nil {
		return risk.Assessment{}, 0, err
	}
	return admin.AssessAnomalyIssue(ctx, issue)
}

func (a *API) LiveSystemSettings() repo.SystemSettings {
	if admin, err := a.adminOrErr(); err == nil {
		return admin.SystemSettings()
	}
	s, err := a.Repo.GetSystemSettings(context.Background())
	if err != nil {
		return repo.DefaultSystemSettings()
	}
	return s.WithDefaults()
}

// ListGuardrailViolations is the product API for GET /api/v1/guardrails/violations.
func (a *API) ListGuardrailViolations(ctx context.Context, params apitypes.ListGuardrailViolationsParams) (apitypes.GuardrailViolationList, error) {
	filter, err := findingListFilterFromTimeParams(
		params.WindowHours, params.From, params.To,
		params.Service, params.Namespace, params.Status,
		findingsListLimit,
	)
	if err != nil {
		return apitypes.GuardrailViolationList{}, err
	}
	groups, err := a.Repo.ListGuardrailViolations(ctx, filter)
	if err != nil {
		return apitypes.GuardrailViolationList{}, err
	}
	return apimap.GuardrailViolationList(groups), nil
}

// GetGuardrailViolationDetail is the product investigate payload for
// GET /api/v1/guardrails/violations/evidence: violation + summary + span
// highlights + evidence trace. Shared by the HTTP handler and the debug UI.
func (a *API) GetGuardrailViolationDetail(ctx context.Context, scopeKey string, ruleKey repo.RuleKey, offending string) (apitypes.GuardrailViolationDetail, bool, error) {
	group, ok, err := a.Repo.GetGuardrailViolation(ctx, scopeKey, ruleKey, offending)
	if err != nil || !ok {
		return apitypes.GuardrailViolationDetail{}, ok, err
	}
	out := apimap.FromGuardrailViolationDetail(group)

	obs, found, oerr := a.Repo.GetLatestObservationByReason(ctx, repo.GuardrailEvidenceReason(ruleKey, offending))
	if oerr != nil {
		return apitypes.GuardrailViolationDetail{}, false, oerr
	}
	if found {
		wire := apimap.FromObservation(obs)
		out.EvidenceTrace = &wire
		if len(obs.RawOTLP) > 0 {
			spans := engine.SpansFromOTLP(obs.RawOTLP)
			out.SpanHighlights = apimap.FromSpanHighlights(engine.GuardrailSpanHighlights(ruleKey, offending, spans))
		}
	}
	return out, true, nil
}
