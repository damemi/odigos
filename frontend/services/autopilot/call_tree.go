package autopilot

import (
	"cmp"
	"slices"
	"strings"
)

// assembleTransactions groups flat call-tree edges into transactions with
// aggregated functions. SeenCount is the sum of sample-type root edge row counts.
func assembleTransactions(edges []callTreeEdgeRow) []Transaction {
	type txAcc struct {
		seen int64
		fns  map[string]*Function
	}
	byTx := map[string]*txAcc{}
	order := make([]string, 0)

	for _, e := range edges {
		acc, ok := byTx[e.TransactionID]
		if !ok {
			acc = &txAcc{fns: map[string]*Function{}}
			byTx[e.TransactionID] = acc
			order = append(order, e.TransactionID)
		}
		if e.Parent == callTreeRootParent && e.SampleType == "samples" {
			acc.seen += int64(e.Count)
		}
		key := e.FunctionName + "\x00" + e.FrameType + "\x00" + e.SampleType
		fn, ok := acc.fns[key]
		if !ok {
			fn = &Function{
				Name:       e.FunctionName,
				FrameType:  e.FrameType,
				SampleType: e.SampleType,
			}
			acc.fns[key] = fn
		}
		fn.SeenCount += int64(e.Count)
	}

	out := make([]Transaction, 0, len(order))
	for _, id := range order {
		acc := byTx[id]
		fns := make([]Function, 0, len(acc.fns))
		for _, fn := range acc.fns {
			fns = append(fns, *fn)
		}
		slices.SortFunc(fns, func(a, b Function) int {
			if c := cmp.Compare(b.SeenCount, a.SeenCount); c != 0 {
				return c
			}
			if c := strings.Compare(a.Name, b.Name); c != 0 {
				return c
			}
			if c := strings.Compare(a.FrameType, b.FrameType); c != 0 {
				return c
			}
			return strings.Compare(a.SampleType, b.SampleType)
		})
		out = append(out, Transaction{
			ID:        id,
			SeenCount: acc.seen,
			Functions: fns,
		})
	}
	slices.SortFunc(out, func(a, b Transaction) int {
		if c := cmp.Compare(b.SeenCount, a.SeenCount); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// callTreeFromEdges maps stored call-tree edges to flat GraphQL call tree nodes.
// Roots have ParentID == "" (stored parent "root"). Sorted by seenCount desc, then name.
func callTreeFromEdges(edges []callTreeEdgeRow) []CallTreeNode {
	out := make([]CallTreeNode, 0, len(edges))
	for _, e := range edges {
		parentID := e.Parent
		if parentID == callTreeRootParent {
			parentID = ""
		}
		out = append(out, CallTreeNode{
			ID:         e.NodeID,
			ParentID:   parentID,
			Name:       e.FunctionName,
			FrameType:  e.FrameType,
			SampleType: e.SampleType,
			SeenCount:  int64(e.Count),
		})
	}
	sortCallTreeNodes(out)
	return out
}

func sortCallTreeNodes(nodes []CallTreeNode) {
	slices.SortFunc(nodes, func(a, b CallTreeNode) int {
		if c := cmp.Compare(b.SeenCount, a.SeenCount); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}
