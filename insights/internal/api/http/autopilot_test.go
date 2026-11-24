package http

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// stubCallTreeRepo implements only ListCallTreeTransactions; the embedded
// interface panics on anything else.
type stubCallTreeRepo struct {
	repo.Repository
	params *repo.CallTreeTransactionsParams
	out    []repo.CallTreeTransaction
}

func (r *stubCallTreeRepo) ListCallTreeTransactions(_ context.Context, params repo.CallTreeTransactionsParams) ([]repo.CallTreeTransaction, error) {
	r.params = &params
	return r.out, nil
}

func TestListAutopilotCallTrees(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	stub := &stubCallTreeRepo{out: []repo.CallTreeTransaction{{
		TransactionID: 7,
		Operation:     "POST /claims",
		EntryKind:     "HTTP",
		Nodes:         []repo.CallTreeNode{{SampleType: "samples", NodeID: "a", Parent: "root", FunctionName: "submit", FrameType: "jvm"}},
	}}}
	h, _ := NewRouter(stub, commonlogger.LoggerCompat())

	w := do(t, h, http.MethodGet, "/api/v1/autopilot/call-trees?namespace=claims-demo&workload_kind=Deployment&workload_name=claims&container_name=app&transaction_id=7&transaction_id=-2&search=7&limit=5&offset=10", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body)
	}
	got := stub.params
	if got == nil || got.Namespace != "claims-demo" || got.WorkloadKind != "Deployment" || got.WorkloadName != "claims" || got.ContainerName != "app" ||
		len(got.TransactionIDs) != 2 || got.TransactionIDs[1] != -2 || got.Search != "7" || got.Limit != 5 || got.Offset != 10 {
		t.Fatalf("repo params = %+v", got)
	}
	var body apitypes.AutopilotCallTreeList
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 1 || body.Items[0].Nodes[0].Id != "samples:a" || body.Items[0].Nodes[0].ParentId != "" {
		t.Fatalf("body = %+v", body)
	}

	for _, query := range []string{
		"namespace=ns&workload_kind=Deployment&workload_name=w",                                   // no container
		"namespace=ns&workload_kind=Deployment&workload_name=w&container_name=c&limit=2000",       // over the cap
		"namespace=ns&workload_kind=Deployment&workload_name=w&container_name=c&offset=-1",        // negative offset
		"namespace=ns&workload_kind=Deployment&workload_name=w&container_name=c&transaction_id=x", // not an id
	} {
		if w := do(t, h, http.MethodGet, "/api/v1/autopilot/call-trees?"+query, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", query, w.Code)
		}
	}
}
