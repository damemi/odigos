package engine

import (
	"encoding/json"
	"testing"

	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

func TestScoredClassesOmitsUnimplementedD6(t *testing.T) {
	got := ScoredClasses()
	want := []repo.DeviationClass{
		repo.D1CallEdges, repo.D2Egress, repo.D3Latency, repo.D4ArgsReturns,
		repo.D5Libraries, repo.D7DBAccess, repo.D8PayloadSize,
	}
	if len(got) != len(want) {
		t.Fatalf("ScoredClasses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ScoredClasses[%d] = %s, want %s", i, got[i], want[i])
		}
	}
	for _, class := range got {
		if class == repo.D6AttrRelations {
			t.Fatal("D6 has no scorer and must not get a learning placeholder")
		}
	}
}

func TestFillScoredPlaceholders(t *testing.T) {
	const txn int64 = 42
	d2 := repo.BaselineClass{
		TransactionID:     txn,
		Class:             repo.D2Egress,
		Data:              json.RawMessage(`{"peers":[]}`),
		DataSchemaVersion: 1,
		ObservationCount:  9,
		Promoted:          true,
	}
	legacy := repo.BaselineClass{
		TransactionID: txn,
		Class:         repo.DeviationClass("D9_retired"),
		Data:          json.RawMessage(`{"kept":true}`),
	}

	got := FillScoredPlaceholders(txn, []repo.BaselineClass{d2, legacy})

	byClass := map[repo.DeviationClass]repo.BaselineClass{}
	order := make([]repo.DeviationClass, 0, len(got))
	for _, row := range got {
		byClass[row.Class] = row
		order = append(order, row.Class)
	}

	scored := ScoredClasses()
	if len(order) < len(scored) {
		t.Fatalf("rows = %v, want at least every scored class", order)
	}
	for i, class := range scored {
		if order[i] != class {
			t.Fatalf("order[%d] = %s, want scored class %s so the table does not blink", i, order[i], class)
		}
	}
	if order[len(scored)] != legacy.Class {
		t.Fatalf("legacy stored class must be appended, not dropped; got %v", order)
	}

	kept := byClass[repo.D2Egress]
	if !kept.Promoted || kept.ObservationCount != 9 || string(kept.Data) != `{"peers":[]}` {
		t.Fatalf("existing D2 row was rewritten: %+v", kept)
	}

	placeholder := byClass[repo.D1CallEdges]
	if placeholder.Promoted || placeholder.ObservationCount != 0 {
		t.Fatalf("missing class must look like learning-nothing-yet: %+v", placeholder)
	}
	if string(placeholder.Data) != "{}" || placeholder.TransactionID != txn {
		t.Fatalf("placeholder = %+v", placeholder)
	}
}

func TestFillScoredPlaceholders_EmptyRows(t *testing.T) {
	got := FillScoredPlaceholders(7, nil)
	if len(got) != len(ScoredClasses()) {
		t.Fatalf("empty store must still list every scored class, got %d", len(got))
	}
}
