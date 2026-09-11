package interrogation

import "github.com/odigos-io/odigos/frontend/graph/model"

// TransactionsToModel converts Redis-backed transactions to GraphQL models.
func TransactionsToModel(txs []Transaction) []*model.InterrogationTransaction {
	out := make([]*model.InterrogationTransaction, 0, len(txs))
	for _, tx := range txs {
		fns := make([]*model.InterrogationFunction, 0, len(tx.Functions))
		for _, fn := range tx.Functions {
			fns = append(fns, &model.InterrogationFunction{
				Name:       fn.Name,
				FrameType:  fn.FrameType,
				SampleType: fn.SampleType,
			})
		}
		out = append(out, &model.InterrogationTransaction{
			ID:        tx.ID,
			Functions: fns,
		})
	}
	return out
}
