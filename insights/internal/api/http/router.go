// Package http exposes the Gin-based HTTP API consumed by the insights UI.
// Handlers are driven entirely by the Repository and know nothing about the
// backing database.
package http

import (
	"io"
	"time"

	"github.com/gin-gonic/gin"
	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/metrics"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// NewRouter builds the API's *gin.Engine wired to the repository, using the
// shared odigos logger for request and recovery logs. It returns the engine and
// the *API; call API.SetAdmin once the engine exists to enable write endpoints
// (they return 503 until then). Called by main at startup.
func NewRouter(r repo.Repository, log *commonlogger.OdigosLogger) (*gin.Engine, *API) {
	// ReleaseMode suppresses Gin's debug banner and per-route prints; the
	// writers are discarded so nothing leaks to stdout.
	gin.SetMode(gin.ReleaseMode)
	gin.DefaultWriter = io.Discard
	gin.DefaultErrorWriter = io.Discard

	engine := gin.New()
	engine.Use(zapAccessLog(log, "/healthz", "/readyz", "/metrics"))
	engine.Use(zapRecovery(log))

	api := &API{Repo: r}

	engine.GET("/healthz", healthzHandler)
	engine.GET("/readyz", readyzHandler(r))
	engine.GET("/metrics", gin.WrapH(metrics.Handler()))

	v1 := engine.Group("/api/v1")
	{
		// Contract docs: the raw OpenAPI spec and a Swagger UI over it.
		v1.GET("/openapi.yaml", serveOpenAPISpec)
		v1.GET("/docs", serveDocs)

		// Per-service rollups for the group-by-service overview.
		v1.GET("/services", api.listServices)
		v1.GET("/service-names", api.listServiceNames)
		v1.GET("/services/profile", api.getServiceProfile)
		// Service-graph neighborhood (callers/callees + virtual deps) around one
		// service - backs the per-anomaly blast-radius map.
		v1.GET("/services/blast-radius", api.getServiceBlastRadius)

		// A workload container's transactions with their autopilot call trees -
		// backs the Profiling page's "By transaction" view.
		v1.GET("/autopilot/call-trees", api.listAutopilotCallTrees)

		// Transactions the UI browses, most frequent first. Filterable by
		// ?namespace=&service= (drill into one service) and ?kind=HTTP|GRPC|...
		v1.GET("/transactions", api.listTransactions)
		v1.POST("/transactions", api.bulkDeleteTransactions)
		v1.POST("/transactions/promote", api.bulkPromoteTransactions)
		v1.GET("/transactions/:transaction_id", api.getTransaction)
		v1.DELETE("/transactions/:transaction_id", api.deleteTransaction)

		// Baselines for a transaction (returns all per-class rows). Promote/reset
		// a single class, or promote/reset every class of the transaction.
		v1.GET("/transactions/:transaction_id/baseline", api.getBaselineForTransaction)
		v1.POST("/transactions/:transaction_id/baseline/:class/promote", api.promoteBaselineClass)
		v1.POST("/transactions/:transaction_id/baseline/:class/reset", api.resetBaselineClass)
		v1.POST("/transactions/:transaction_id/baselines/reset", api.resetTransactionBaselines)
		v1.POST("/transactions/:transaction_id/baselines/promote", api.promoteTransactionBaselines)

		// Trace examples: list = pick a sample (meta only); get = load OTLP for waterfall/compare.
		v1.GET("/transactions/:transaction_id/observations", api.listObservations)
		v1.GET("/transactions/:transaction_id/observations/:trace_id", api.getObservation)

		// Alert policies: list, upsert one scope, delete one override.
		v1.GET("/policies", api.listPolicies)
		v1.PUT("/policies", api.upsertPolicy)
		v1.DELETE("/policies", api.deletePolicy)

		// Learning policies: list, upsert one scope, delete one override.
		v1.GET("/learning-policies", api.listLearningPolicies)
		v1.PUT("/learning-policies", api.upsertLearningPolicy)
		v1.DELETE("/learning-policies", api.deleteLearningPolicy)

		// Unified findings feed (anomalies + policy violations), ranked by score.
		v1.GET("/findings", api.listFindings)

		// Anomaly issues: list, drill into one, bulk-resolve (POST collection),
		// or resolve one (POST detail with {resolution}).
		v1.GET("/anomalies", api.listAnomalies)
		v1.POST("/anomalies", api.bulkResolveAnomalies)
		v1.GET("/anomalies/:transaction_id/:signature", api.getAnomalyIssue)
		v1.POST("/anomalies/:transaction_id/:signature", api.resolveAnomalyIssue)

		// Service guardrails: list, upsert, delete, seed from observed profile.
		v1.GET("/guardrails", api.listGuardrails)
		v1.PUT("/guardrails", api.upsertGuardrail)
		v1.DELETE("/guardrails", api.deleteGuardrail)
		v1.POST("/guardrails/seed", api.seedGuardrail)
		v1.POST("/services/transaction-guardrail/enable", api.enableTransactionGuardrail)
		v1.POST("/services/transaction-guardrail/disable", api.disableTransactionGuardrail)
		v1.POST("/services/transaction-guardrail/force-promote", api.forcePromoteServiceTransactions)

		// Guardrail violations: list and resolve (allow / dismiss / reopen).
		v1.GET("/guardrails/violations", api.listGuardrailViolations)
		v1.GET("/guardrails/violations/evidence", api.getGuardrailViolation)
		v1.POST("/guardrails/violations/accept", api.acceptViolation)
		v1.POST("/guardrails/violations/dismiss", api.dismissViolation)
		v1.POST("/guardrails/violations/reopen", api.reopenViolation)

		// Guardrail recommendations: rules Insights mined and suggests; the
		// operator applies / dismisses them, previews edits, or re-mines.
		v1.GET("/recommendations", api.listRecommendations)
		v1.GET("/recommendations/:recommendation_id", api.getRecommendation)
		v1.POST("/recommendations/apply", api.applyRecommendations)
		v1.POST("/recommendations/dismiss", api.dismissRecommendations)
		v1.POST("/recommendations/restore", api.restoreRecommendations)
		v1.POST("/recommendations/revert", api.revertRecommendations)
		v1.POST("/recommendations/preview", api.previewRecommendation)
		v1.POST("/recommendations/recompute", api.recomputeRecommendations)

		// Risk-model reference data (classes, enrichers, categories, rules, tags).
		v1.GET("/catalog", api.getCatalog)

		v1.GET("/system-settings", api.getSystemSettings)
		v1.PUT("/system-settings", api.putSystemSettings)

		v1.GET("/storage-health", api.getStorageHealth)

		// Findings integrations: backend catalog, CRUD, connectivity test,
		// per-finding delivery ledger, attempt audit, manual retry.
		v1.GET("/integrations/catalog", api.getIntegrationCatalog)
		v1.GET("/integrations", api.listIntegrations)
		v1.POST("/integrations", api.createIntegration)
		v1.GET("/integrations/:integration_id", api.getIntegration)
		v1.PUT("/integrations/:integration_id", api.updateIntegration)
		v1.DELETE("/integrations/:integration_id", api.deleteIntegration)
		v1.POST("/integrations/:integration_id/test", api.testIntegration)
		v1.GET("/integrations/:integration_id/deliveries", api.listIntegrationDeliveries)
		v1.POST("/integrations/:integration_id/deliveries/retry", api.retryIntegrationDeliveries)
		v1.GET("/integrations/:integration_id/attempts", api.listIntegrationAttempts)
		v1.GET("/findings/deliveries", api.getFindingDeliveries)

		// On-demand interrogations: start a run on a source, follow it, stop
		// or extend it; the changes runs made to the cluster, across runs.
		v1.GET("/interrogations", api.listInterrogations)
		v1.POST("/interrogations", limitInterrogationBody, api.startInterrogation)
		v1.GET("/interrogations/summary", api.getInterrogationSummary)
		v1.GET("/interrogations/options", api.getInterrogationOptions)
		v1.GET("/interrogations/sources", api.listInterrogationSources)
		v1.GET("/interrogations/sources/transactions", api.listInterrogationSourceTransactions)
		v1.GET("/interrogations/:interrogation_id", api.getInterrogation)
		v1.GET("/interrogations/:interrogation_id/events", api.listInterrogationEvents)
		v1.POST("/interrogations/:interrogation_id/stop", limitInterrogationBody, api.stopInterrogation)
		v1.POST("/interrogations/:interrogation_id/extend", limitInterrogationBody, api.extendInterrogation)
		v1.GET("/interrogations/:interrogation_id/changes", api.listInterrogationRunChanges)
		v1.GET("/interrogation-changes", api.listInterrogationChanges)
		v1.GET("/interrogation-changes/summary", api.getInterrogationChangesSummary)
		v1.POST("/interrogation-changes/revert-all", limitInterrogationBody, api.revertAllInterrogationChanges)
		v1.GET("/interrogation-changes/:change_id", api.getInterrogationChange)
		v1.POST("/interrogation-changes/:change_id/keep", limitInterrogationBody, api.interrogationChangeAction(repo.ChangeKeep))
		v1.POST("/interrogation-changes/:change_id/revert", limitInterrogationBody, api.interrogationChangeAction(repo.ChangeRevert))
		v1.POST("/interrogation-changes/:change_id/remove", limitInterrogationBody, api.interrogationChangeAction(repo.ChangeRemove))
		v1.POST("/interrogation-changes/:change_id/undo", limitInterrogationBody, api.undoInterrogationChange)
	}

	return engine, api
}

// zapAccessLog logs one line per request through the odigos logger, bucketing
// severity by HTTP status. Paths in skipPaths (e.g. health probes) are dropped
// to keep probe noise out of the log.
func zapAccessLog(log *commonlogger.OdigosLogger, skipPaths ...string) gin.HandlerFunc {
	skip := make(map[string]struct{}, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = struct{}{}
	}
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		c.Next()

		if _, drop := skip[path]; drop {
			return
		}
		if raw != "" {
			path = path + "?" + raw
		}

		status := c.Writer.Status()
		fields := []interface{}{
			"status", status,
			"method", c.Request.Method,
			"path", path,
			"latency_ms", time.Since(start).Milliseconds(),
			"client_ip", c.ClientIP(),
		}
		if errs := c.Errors.ByType(gin.ErrorTypePrivate).String(); errs != "" {
			fields = append(fields, "errors", errs)
		}

		switch {
		case status >= 500:
			log.Error("http request", fields...)
		case status >= 400:
			log.Warn("http request", fields...)
		default:
			log.Info("http request", fields...)
		}
	}
}

// zapRecovery recovers from handler panics, logging them through the odigos
// logger (instead of stderr) before returning 500.
func zapRecovery(log *commonlogger.OdigosLogger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		log.Error("panic recovered",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"panic", recovered,
		)
		c.AbortWithStatus(500)
	})
}
