package graph

import (
	"context"
	"log/slog"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/odigos-io/central-backend/internal/logger"
	"github.com/odigos-io/odigos-enterprise/connectors/testconnection"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func connectorLogger(ctx context.Context, operation, provider string) *slog.Logger {
	log := logger.Log
	if log == nil {
		log = slog.Default()
	}
	log = log.With("operation", operation, "provider", provider)
	if graphql.HasOperationContext(ctx) {
		log = log.With("requestId", graphql.GetOperationContext(ctx).Headers.Get("X-Request-ID"))
	}
	return log
}

// Record stages and safe diagnostics, never GraphQL input/credential payloads.
// Expected provider failures are returned as checks, so gqlgen's error handler
// does not log them automatically.
func verifyCloudConnector(ctx context.Context, operation string, input testconnection.Input) (*testconnection.Result, error) {
	log := connectorLogger(ctx, operation, input.Provider)
	start := time.Now()
	log.Info("Cloud connector verification started")
	res, err := testconnection.Run(ctx, input)
	if err != nil {
		return nil, err
	}
	for _, check := range res.Checks {
		if !check.Passed {
			log.Warn("Cloud connector verification check failed", "check", check.Name, "error", check.Message, "remediation", check.Remediation)
		}
	}
	log.Info("Cloud connector verification completed", "success", res.Success, "duration", time.Since(start).String())
	return res, nil
}

func cloudConnectorOperationError(ctx context.Context, operation, provider string, credentials map[string]string, err error) error {
	if err == nil {
		return nil
	}
	message := testconnection.SafeMessage(provider, credentials, err.Error())
	connectorLogger(ctx, operation, provider).Error("Cloud connector operation failed", "error", message)
	extensions := map[string]any{"provider": provider, "operation": operation}
	if graphql.HasOperationContext(ctx) {
		if id := graphql.GetOperationContext(ctx).Headers.Get("X-Request-ID"); id != "" {
			extensions["requestId"] = id
		}
	}
	return &gqlerror.Error{Message: message, Extensions: extensions}
}
