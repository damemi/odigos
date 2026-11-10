package gql

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/odigos-io/odigos/connectors/base/api"
	"github.com/odigos-io/odigos/connectors/base/domain/collectormetrics"
)

type metricsCentral struct{ api.CentralInterface }

func (metricsCentral) ManagedCollectorMetrics(_ context.Context, region string, refresh bool) (collectormetrics.Record, bool, error) {
	if region != "us-east-1" || refresh {
		return collectormetrics.Record{}, false, nil
	}
	spans := 42
	return collectormetrics.Record{
		Snapshot:   collectormetrics.Snapshot{AcceptedSpans: &spans},
		ObservedAt: time.Date(2026, 10, 4, 6, 0, 0, 0, time.UTC),
	}, true, nil
}

func TestManagedCollectorMetricsGraphQLQuery(t *testing.T) {
	query := []byte(`{"query":"query { managedCollectorMetrics(region: \"us-east-1\") { region observedAt live acceptedSpans refusedSpans } }"}`)
	req := httptest.NewRequest(http.MethodPost, "/graphql", bytes.NewReader(query))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewHandler(metricsCentral{}).ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("GraphQL status = %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Metrics struct {
				Region        string `json:"region"`
				ObservedAt    string `json:"observedAt"`
				Live          bool   `json:"live"`
				AcceptedSpans *int   `json:"acceptedSpans"`
				RefusedSpans  *int   `json:"refusedSpans"`
			} `json:"managedCollectorMetrics"`
		} `json:"data"`
		Errors []any `json:"errors"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Errors) != 0 || payload.Data.Metrics.Region != "us-east-1" ||
		payload.Data.Metrics.AcceptedSpans == nil || *payload.Data.Metrics.AcceptedSpans != 42 ||
		payload.Data.Metrics.RefusedSpans != nil || payload.Data.Metrics.Live {
		t.Fatalf("GraphQL metrics = %+v, errors = %+v", payload.Data.Metrics, payload.Errors)
	}
}
