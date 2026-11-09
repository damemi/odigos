package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

// Noisy-operation rules drop traces matching an operation, keeping at most
// percentageAtMost%. Typical targets: health probes, metrics scrapes,
// internal cron pings — anything with no diagnostic value.

func createNoisyRuleHandler() server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("sampling_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		pct := floatArg(req, "percentage_at_most", -1)
		if pct < 0 {
			return mcp.NewToolResultError("percentage_at_most is required (0-100)"), nil
		}

		var op odigosv1.NoisyOperation
		op.Name = name
		op.Disabled = boolArg(req, "disabled", false)
		op.Notes = stringArg(req, "notes", "")
		op.PercentageAtMost = &pct

		hsom, _, err := decodeHeadSamplingOp(req, "operation")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		op.Operation = hsom

		scope, err := decodeSamplingSourcesScope(req, "source_scopes")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		op.SourceScopes = scope

		return mutateSamplingGroup(ctx, id, boolArg(req, "dry_run", true), func(s *odigosv1.Sampling) (string, error) {
			s.Spec.NoisyOperations = append(s.Spec.NoisyOperations, op)
			return fmt.Sprintf("added noisy rule %q (percentageAtMost=%.2f)", name, pct), nil
		})
	}
}

func updateNoisyRuleHandler() server.ToolHandlerFunc {
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
			for i := range s.Spec.NoisyOperations {
				if s.Spec.NoisyOperations[i].Name != ruleName {
					continue
				}
				r := &s.Spec.NoisyOperations[i]
				if pct := floatArg(req, "percentage_at_most", -1); pct >= 0 {
					r.PercentageAtMost = &pct
				}
				if v := stringArg(req, "notes", ""); v != "" {
					r.Notes = v
				}
				if dp := boolArgPtr(req, "disabled"); dp != nil {
					r.Disabled = *dp
				}
				if op, _, err := decodeHeadSamplingOp(req, "operation"); err == nil && op != nil {
					r.Operation = op
				}
				if scope, err := decodeSamplingSourcesScope(req, "source_scopes"); err == nil && scope != nil {
					r.SourceScopes = scope
				}
				return fmt.Sprintf("updated noisy rule %q", ruleName), nil
			}
			return "", fmt.Errorf("rule %q not found in group %q", ruleName, id)
		})
	}
}

func deleteNoisyRuleHandler() server.ToolHandlerFunc {
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
			for i := range s.Spec.NoisyOperations {
				if s.Spec.NoisyOperations[i].Name == ruleName {
					s.Spec.NoisyOperations = append(s.Spec.NoisyOperations[:i], s.Spec.NoisyOperations[i+1:]...)
					return fmt.Sprintf("removed noisy rule %q", ruleName), nil
				}
			}
			return "", fmt.Errorf("rule %q not found in group %q", ruleName, id)
		})
	}
}
