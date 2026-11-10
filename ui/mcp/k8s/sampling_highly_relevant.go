package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

// Highly-relevant rules always keep traces matching — errors, slow requests,
// specific operations on flagship services.

func createHROHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("sampling_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		var hro odigosv1.HighlyRelevantOperation
		hro.Name = name
		hro.Notes = stringArg(req, "notes", "")
		hro.Disabled = boolArg(req, "disabled", false)
		hro.Error = boolArg(req, "error", false)
		if dur := intArg(req, "duration_at_least_ms", -1); dur >= 0 {
			hro.DurationAtLeastMs = &dur
		}
		if pct := floatArg(req, "percentage_at_least", -1); pct >= 0 {
			hro.PercentageAtLeast = &pct
		}

		op, _, err := decodeTailSamplingOp(req, "operation")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		hro.Operation = op

		if scope, err := decodeSamplingSourcesScope(req, "source_scopes"); err == nil {
			hro.SourceScopes = scope
		}

		return mutateSamplingGroup(ctx, id, boolArg(req, "dry_run", true), func(s *odigosv1.Sampling) (string, error) {
			s.Spec.HighlyRelevantOperations = append(s.Spec.HighlyRelevantOperations, hro)
			return fmt.Sprintf("added highly-relevant rule %q", name), nil
		})
	}
}

func updateHROHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("sampling_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ruleName, err := req.RequireString("rule_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mutateSamplingGroup(ctx, id, boolArg(req, "dry_run", true), func(s *odigosv1.Sampling) (string, error) {
			for i := range s.Spec.HighlyRelevantOperations {
				if s.Spec.HighlyRelevantOperations[i].Name != ruleName {
					continue
				}
				r := &s.Spec.HighlyRelevantOperations[i]
				if dp := boolArgPtr(req, "error"); dp != nil {
					r.Error = *dp
				}
				if dur := intArg(req, "duration_at_least_ms", -1); dur >= 0 {
					r.DurationAtLeastMs = &dur
				}
				if pct := floatArg(req, "percentage_at_least", -1); pct >= 0 {
					r.PercentageAtLeast = &pct
				}
				if v := stringArg(req, "notes", ""); v != "" {
					r.Notes = v
				}
				if dp := boolArgPtr(req, "disabled"); dp != nil {
					r.Disabled = *dp
				}
				if op, _, err := decodeTailSamplingOp(req, "operation"); err == nil && op != nil {
					r.Operation = op
				}
				if scope, err := decodeSamplingSourcesScope(req, "source_scopes"); err == nil && scope != nil {
					r.SourceScopes = scope
				}
				return fmt.Sprintf("updated highly-relevant rule %q", ruleName), nil
			}
			return "", fmt.Errorf("rule %q not found", ruleName)
		})
	}
}

func deleteHROHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("sampling_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		ruleName, err := req.RequireString("rule_name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mutateSamplingGroup(ctx, id, boolArg(req, "dry_run", true), func(s *odigosv1.Sampling) (string, error) {
			for i := range s.Spec.HighlyRelevantOperations {
				if s.Spec.HighlyRelevantOperations[i].Name == ruleName {
					s.Spec.HighlyRelevantOperations = append(s.Spec.HighlyRelevantOperations[:i], s.Spec.HighlyRelevantOperations[i+1:]...)
					return fmt.Sprintf("removed highly-relevant rule %q", ruleName), nil
				}
			}
			return "", fmt.Errorf("rule %q not found", ruleName)
		})
	}
}
