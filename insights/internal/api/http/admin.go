package http

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/odigos-io/odigos-enterprise/insights/internal/engine"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
	"github.com/odigos-io/odigos-enterprise/insights/internal/risk"
)

// Admin is the engine-backed side of the write endpoints: the actions that must
// also touch the engine's in-memory state, not just the DB - accepting an
// anomaly folds its trace into the baseline; resetting a baseline drops it from
// the cache; and every policy/guardrail write must reload the engine's snapshot
// to apply live. It mirrors the debug UI's Admin and is satisfied by
// *engine.Engine. Wired via SetAdmin after the engine is constructed, so read
// endpoints serve from startup while writes return 503 until then.
type Admin interface {
	AcceptAnomalyIssue(ctx context.Context, transactionID int64, signature string) error
	DismissAnomalyIssue(ctx context.Context, transactionID int64, signature string) error
	ReopenAnomalyIssue(ctx context.Context, transactionID int64, signature string) error
	AssessAnomalyIssue(ctx context.Context, issue repo.AnomalyIssue) (risk.Assessment, int, error)
	ResetBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error
	ResetTransactionBaselines(ctx context.Context, transactionID int64) error
	DeleteTransaction(ctx context.Context, transactionID int64) error
	DeleteAllTransactions(ctx context.Context) error
	ReloadAlertConfig(ctx context.Context) error
	ReloadLearningConfig(ctx context.Context) error
	ReloadGuardrailConfig(ctx context.Context) error
	ReloadSystemConfig(ctx context.Context) error
	SystemSettings() repo.SystemSettings
	PromoteBaselineClass(ctx context.Context, transactionID int64, class repo.DeviationClass) error
	PromoteTransactionBaselines(ctx context.Context, transactionID int64) error
	EnableTransactionGuardrail(ctx context.Context, namespace, service string) error
	DisableTransactionGuardrail(ctx context.Context, namespace, service string) error
	ForcePromoteService(ctx context.Context, namespace, service string) error

	// Guardrail recommendations (see engine/recommendations.go).
	ListRecommendations(ctx context.Context, filter repo.RecommendationFilter) ([]engine.RecommendationView, error)
	GetRecommendation(ctx context.Context, id string) (engine.RecommendationView, bool, error)
	ApplyRecommendations(ctx context.Context, items []engine.RecommendationApplyItem) engine.RecommendationBulkResult
	DismissRecommendations(ctx context.Context, ids []string) engine.RecommendationBulkResult
	RestoreRecommendations(ctx context.Context, ids []string) engine.RecommendationBulkResult
	RevertRecommendations(ctx context.Context, ids []string) engine.RecommendationBulkResult
	PreviewRecommendation(ctx context.Context, transactionID int64, spec repo.CorrelationSpec) (engine.RecommendationPreview, error)
	RecomputeRecommendations(ctx context.Context, transactionID int64) error
	RecomputeServiceGuardrailRecommendation(ctx context.Context, namespace, service string) error
}

// SetAdmin wires the engine-backed write actions. Called once at startup after
// the engine is built; until then, write endpoints return 503.
func (a *API) SetAdmin(admin Admin) { a.admin.Store(&admin) }

// requireAdmin returns the wired Admin, or writes 503 and returns ok=false when
// the engine is not yet ready.
func (a *API) requireAdmin(c *gin.Context) (Admin, bool) {
	admin, err := a.adminOrErr()
	if err != nil {
		writeError(c, http.StatusServiceUnavailable, err)
		return nil, false
	}
	return admin, true
}
