package metrics

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandler_ExposesInstrumentation drives a few observations through the
// collectors and asserts they surface in the /metrics scrape output with the
// right names, labels, and values.
func TestHandler_ExposesInstrumentation(t *testing.T) {
	_ = observe("GetBaselineForTransaction", func() error { return nil })
	_ = observe("GetBaselineForTransaction", func() error { return nil })
	_ = observe("UpsertBaselineClass", func() error { return errors.New("boom") })
	IncTracesProcessed()
	AddSubtransactionsProcessed(3)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	Handler().ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Fatalf("metrics handler status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()

	wants := []string{
		`insights_repo_calls_total{method="GetBaselineForTransaction",status="ok"} 2`,
		`insights_repo_calls_total{method="UpsertBaselineClass",status="error"} 1`,
		`insights_repo_call_duration_seconds_count{method="GetBaselineForTransaction"} 2`,
		`insights_engine_traces_processed_total 1`,
		`insights_engine_subtransactions_processed_total 3`,
	}
	for _, w := range wants {
		if !strings.Contains(body, w) {
			t.Errorf("metrics output missing %q\n---\n%s", w, body)
		}
	}
}
