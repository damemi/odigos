package engine

import (
	"context"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// ProcessServiceGraphMetrics folds a batch of decoded servicegraph edges into
// the repository. Like Process (the trace path) it never returns an error to
// the caller: the OTLP metrics receiver must not NACK the whole export because
// one write failed, so failures are logged (throttled) and dropped. Called off
// the OTLP metrics receiver, not per trace.
func (e *Engine) ProcessServiceGraphMetrics(ctx context.Context, edges []repo.ServiceGraphEdge) {
	if len(edges) == 0 {
		return
	}
	if err := e.repo.UpsertServiceGraphEdges(ctx, edges); err != nil {
		e.warn("upsert service graph edges", err)
	}
}
