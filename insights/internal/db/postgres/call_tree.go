package postgres

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

// callTreeInsertChunk bounds the nodes sent in one INSERT.
const callTreeInsertChunk = 5000

// InsertCallTreeNodes stores autopilot call tree nodes. Nodes already stored
// are skipped, so re-sending a whole call tree only adds its new nodes. Each
// chunk is one statement: the columns go in as arrays and unnest turns them
// back into rows.
func (r *Repo) InsertCallTreeNodes(ctx context.Context, nodes []repo.CallTreeNode) error {
	for start := 0; start < len(nodes); start += callTreeInsertChunk {
		chunk := nodes[start:min(start+callTreeInsertChunk, len(nodes))]
		var cols [7][]any
		for _, n := range chunk {
			for i, v := range []any{
				n.SourceID, n.TransactionID, n.SampleType, n.NodeID, n.Parent, n.FunctionName, n.FrameType,
			} {
				cols[i] = append(cols[i], v)
			}
		}
		if _, err := r.pool.Exec(ctx, `
INSERT INTO autopilot_call_tree_nodes (
	source_id, transaction_id, sample_type, node_id, parent, function_name, frame_type
)
SELECT * FROM unnest(
	$1::bigint[], $2::bigint[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[]
)
ON CONFLICT DO NOTHING`, cols[0], cols[1], cols[2], cols[3], cols[4], cols[5], cols[6]); err != nil {
			return fmt.Errorf("insert call tree nodes: %w", err)
		}
	}
	return nil
}
