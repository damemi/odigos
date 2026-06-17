package mcp

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestToolsListReadOnly drives the real /mcp handler with a tools/list
// JSON-RPC call and asserts the catalog is exactly the two showcase tools and
// that both advertise read-only annotations (so a read-only-mode agent and the
// MCP client both see them as non-mutating).
func TestToolsListReadOnly(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post /mcp: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read body: %v", err)
	}
	payload := extractJSON(buf.String())

	var out struct {
		Result struct {
			Tools []struct {
				Name        string `json:"name"`
				Annotations struct {
					ReadOnlyHint *bool `json:"readOnlyHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(payload), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", payload, err)
	}

	want := map[string]bool{"list_enabled_sources": false, "list_destinations": false}
	if len(out.Result.Tools) != len(want) {
		t.Fatalf("got %d tools, want %d", len(out.Result.Tools), len(want))
	}
	for _, tool := range out.Result.Tools {
		seen, known := want[tool.Name]
		if !known {
			t.Errorf("unexpected tool %q", tool.Name)
			continue
		}
		if seen {
			t.Errorf("duplicate tool %q", tool.Name)
		}
		want[tool.Name] = true
		if tool.Annotations.ReadOnlyHint == nil || !*tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q: readOnlyHint = %v, want true", tool.Name, tool.Annotations.ReadOnlyHint)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing tool %q", name)
		}
	}
}

// extractJSON returns the JSON-RPC payload from a response body that may be
// either a plain JSON object or an SSE frame (one or more "data:" lines).
func extractJSON(body string) string {
	body = strings.TrimSpace(body)
	if !strings.HasPrefix(body, "data:") {
		return body
	}
	var b strings.Builder
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "data:") {
			b.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return b.String()
}
