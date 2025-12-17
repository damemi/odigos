package websocketserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/odigos-io/central-backend/internal/types"
)

func TestCalculateBatchSize(t *testing.T) {
	ws := &WebSocketServer{}

	// Create test sources
	sources := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		sources[i] = map[string]interface{}{
			"namespace":         "test-namespace",
			"name":              "test-source-name",
			"kind":              "Deployment",
			"selected":          true,
			"currentStreamName": "default",
		}
	}

	request := types.GqlRequestPayload{
		Query: `mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
			persistK8sSources(sources: $sources)
		}`,
		Variables: map[string]interface{}{
			"sources": sources,
		},
		RequestId: "test-request-id",
		IsAdmin:   false,
	}

	batchSize := ws.calculateBatchSize(request, sources)

	// Batch size should be positive
	if batchSize <= 0 {
		t.Errorf("expected positive batch size, got %d", batchSize)
	}

	// With default 10MB max and small sources, batch size should be large
	if batchSize < 10 {
		t.Errorf("expected batch size >= 10 for small sources, got %d", batchSize)
	}
}

func TestCalculateBatchSizeWithSmallMaxSize(t *testing.T) {
	// Temporarily set a small max message size
	originalMaxSize := MaxMessageSize
	MaxMessageSize = 1024 // 1KB
	defer func() { MaxMessageSize = originalMaxSize }()

	ws := &WebSocketServer{}

	// Create test sources
	sources := make([]interface{}, 100)
	for i := 0; i < 100; i++ {
		sources[i] = map[string]interface{}{
			"namespace":         "test-namespace",
			"name":              "test-source-name",
			"kind":              "Deployment",
			"selected":          true,
			"currentStreamName": "default",
		}
	}

	request := types.GqlRequestPayload{
		Query: `mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
			persistK8sSources(sources: $sources)
		}`,
		Variables: map[string]interface{}{
			"sources": sources,
		},
		RequestId: "test-request-id",
		IsAdmin:   false,
	}

	batchSize := ws.calculateBatchSize(request, sources)

	// Batch size should be positive but small due to size limit
	if batchSize <= 0 {
		t.Errorf("expected positive batch size, got %d", batchSize)
	}

	// With 1KB max, batch size should be small (but at least 1)
	if batchSize > 10 {
		t.Errorf("expected smaller batch size with 1KB limit, got %d", batchSize)
	}
}

func TestMessageSizeCheck(t *testing.T) {
	// Create a message and verify its size
	sources := make([]interface{}, 10)
	for i := 0; i < 10; i++ {
		sources[i] = map[string]interface{}{
			"namespace":         "test-namespace",
			"name":              "test-source",
			"kind":              "Deployment",
			"selected":          true,
			"currentStreamName": "default",
		}
	}

	msg := WebSocketMessageToProxy{
		Type:  MessageTypeGraphQLQuery,
		Query: `mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) { persistK8sSources(sources: $sources) }`,
		Variables: map[string]interface{}{
			"sources": sources,
		},
		RequestId: "test-id",
		IsAdmin:   false,
	}

	jsonMsg, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("failed to marshal message: %v", err)
	}

	// Small message should be under the default limit
	if len(jsonMsg) > DefaultMaxMessageSize {
		t.Errorf("small test message unexpectedly large: %d bytes", len(jsonMsg))
	}
}

func TestBatchSplitting(t *testing.T) {
	// Test that sources can be split into batches correctly
	sources := make([]interface{}, 25)
	for i := 0; i < 25; i++ {
		sources[i] = map[string]interface{}{
			"index": i,
			"name":  "source",
		}
	}

	batchSize := 10

	// Simulate batch splitting
	var batches [][]interface{}
	for i := 0; i < len(sources); i += batchSize {
		end := i + batchSize
		if end > len(sources) {
			end = len(sources)
		}
		batches = append(batches, sources[i:end])
	}

	// Should have 3 batches: 10, 10, 5
	if len(batches) != 3 {
		t.Errorf("expected 3 batches, got %d", len(batches))
	}

	if len(batches[0]) != 10 {
		t.Errorf("expected first batch to have 10 items, got %d", len(batches[0]))
	}

	if len(batches[1]) != 10 {
		t.Errorf("expected second batch to have 10 items, got %d", len(batches[1]))
	}

	if len(batches[2]) != 5 {
		t.Errorf("expected third batch to have 5 items, got %d", len(batches[2]))
	}

	// Verify all items are preserved
	totalItems := 0
	for _, batch := range batches {
		totalItems += len(batch)
	}
	if totalItems != 25 {
		t.Errorf("expected 25 total items across batches, got %d", totalItems)
	}
}

func TestQueryContainsPersistSources(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		expected bool
	}{
		{
			name: "persistK8sSources mutation",
			query: `mutation PersistSources($sources: [PersistNamespaceSourceInput!]!) {
				persistK8sSources(sources: $sources)
			}`,
			expected: true,
		},
		{
			name: "other mutation",
			query: `mutation CreateDestination($destination: DestinationInput!) {
				createNewDestination(destination: $destination) { id }
			}`,
			expected: false,
		},
		{
			name: "query",
			query: `query GetSources {
				computePlatform { sources { name } }
			}`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use strings.Contains as the implementation does
			actualContains := strings.Contains(tt.query, "persistK8sSources")

			if actualContains != tt.expected {
				t.Errorf("expected contains to return %v for query, got %v", tt.expected, actualContains)
			}
		})
	}
}
