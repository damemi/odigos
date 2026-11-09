package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
)

// Cost-reduction rules sample matching traces at percentageAtMost%. Same
// shape as noisy but the intent (and the metric attribution) is explicit cost
// optimization — useful when the SRE team wants to attribute savings.

func createCRRHandler() server.ToolHandlerFunc {
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

		var crr odigosv1.CostReductionRule
		crr.Name = name
		crr.Disabled = boolArg(req, "disabled", false)
		crr.Notes = stringArg(req, "notes", "")
		crr.PercentageAtMost = pct

		op, _, err := decodeTailSamplingOp(req, "operation")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		crr.Operation = op

		if scope, err := decodeSamplingSourcesScope(req, "source_scopes"); err == nil {
			crr.SourceScopes = scope
		}

		return mutateSamplingGroup(ctx, id, boolArg(req, "dry_run", true), func(s *odigosv1.Sampling) (string, error) {
			s.Spec.CostReductionRules = append(s.Spec.CostReductionRules, crr)
			return fmt.Sprintf("added cost-reduction rule %q (percentageAtMost=%.2f)", name, pct), nil
		})
	}
}

func updateCRRHandler() server.ToolHandlerFunc {
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
			for i := range s.Spec.CostReductionRules {
				if s.Spec.CostReductionRules[i].Name != ruleName {
					continue
				}
				r := &s.Spec.CostReductionRules[i]
				if pct := floatArg(req, "percentage_at_most", -1); pct >= 0 {
					r.PercentageAtMost = pct
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
				return fmt.Sprintf("updated cost-reduction rule %q", ruleName), nil
			}
			return "", fmt.Errorf("rule %q not found", ruleName)
		})
	}
}

func deleteCRRHandler() server.ToolHandlerFunc {
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
			for i := range s.Spec.CostReductionRules {
				if s.Spec.CostReductionRules[i].Name == ruleName {
					s.Spec.CostReductionRules = append(s.Spec.CostReductionRules[:i], s.Spec.CostReductionRules[i+1:]...)
					return fmt.Sprintf("removed cost-reduction rule %q", ruleName), nil
				}
			}
			return "", fmt.Errorf("rule %q not found", ruleName)
		})
	}
}
