package k8s

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"

	"github.com/odigos-io/odigos/api/k8sconsts"
	odigosv1 "github.com/odigos-io/odigos/api/odigos/v1alpha1"
	commonapisampling "github.com/odigos-io/odigos/common/api/sampling"
	"github.com/odigos-io/odigos/frontend/kube"
	"github.com/odigos-io/odigos/k8sutils/pkg/env"
)

// sampling_matchers.go holds the parsing + read-modify-write helpers shared
// by the per-rule-family handlers (sampling_noisy.go, sampling_highly_relevant.go,
// sampling_cost_reduction.go). Keeping them in one file mirrors mcp-grafana's
// alerting_manage_rules_handlers.go vs alerting_manage_rules_datasource.go
// split: one helper file per axis, then a file per rule family that consumes it.

// decodeHeadSamplingOp parses an "operation" arg as a HeadSamplingOperationMatcher.
// Returns (matcher, true, nil) on success, (nil, false, nil) if absent.
func decodeHeadSamplingOp(req mcp.CallToolRequest, key string) (*commonapisampling.HeadSamplingOperationMatcher, bool, error) {
	out := &commonapisampling.HeadSamplingOperationMatcher{}
	ok, err := decodeArg(req, key, out)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return out, true, nil
}

// decodeTailSamplingOp parses an "operation" arg as a TailSamplingOperationMatcher.
func decodeTailSamplingOp(req mcp.CallToolRequest, key string) (*commonapisampling.TailSamplingOperationMatcher, bool, error) {
	out := &commonapisampling.TailSamplingOperationMatcher{}
	ok, err := decodeArg(req, key, out)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return out, true, nil
}

// decodeSamplingSourcesScope returns the k8sconsts SourcesScopes alias used
// by Sampling rule structs.
func decodeSamplingSourcesScope(req mcp.CallToolRequest, key string) (*k8sconsts.SourcesScopes, error) {
	out := &k8sconsts.SourcesScopes{}
	ok, err := decodeArg(req, key, out)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return out, nil
}

// mutateSamplingGroup is the read-modify-write template used by every per-rule
// handler. The mutate closure returns a short human-readable description of
// the change that gets surfaced in the response (both dry-run and apply).
func mutateSamplingGroup(ctx context.Context, id string, dryRun bool, mutate func(s *odigosv1.Sampling) (string, error)) (*mcp.CallToolResult, error) {
	ns := env.GetCurrentNamespace()

	// Re-fetch + re-run the caller's mutate on a resourceVersion conflict so
	// concurrent edits to the same sampling group don't fail under load. The
	// mutate closure is pure w.r.t. the request args, so replaying it against a
	// fresh object is safe.
	var done *mcp.CallToolResult
	applyErr := retry.RetryOnConflict(conflictBackoff, func() error {
		s, err := kube.DefaultClient.OdigosClient.Samplings(ns).Get(ctx, id, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				done = mcp.NewToolResultError(fmt.Sprintf("sampling group %q not found", id))
				return nil
			}
			return err
		}
		change, err := mutate(s)
		if err != nil {
			done = mcp.NewToolResultError(err.Error())
			return nil
		}
		if dryRun {
			done, _ = withGuidance(map[string]any{"action": "would_change", "sampling_id": id, "diff": change}, "Dry-run.", nil)
			return nil
		}
		updated, err := kube.DefaultClient.OdigosClient.Samplings(ns).Update(ctx, s, metav1.UpdateOptions{})
		if err != nil {
			return err
		}
		done, _ = withGuidance(map[string]any{"action": "updated", "sampling_id": updated.Name, "diff": change}, "Sampling group updated; collectors reload tail-sampling config.", nil)
		return nil
	})
	if applyErr != nil {
		return mcp.NewToolResultError(fmt.Sprintf("update sampling: %v", applyErr)), nil
	}
	return done, nil
}
