// Package model is a thin OpenAI-compatible chat-completions client.
// Interrogation always talks to in-cluster odigos-litellm; provider routing
// is configured in LiteLLM, not here.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/odigos-io/odigos/insights/internal/interrogation/schema"
)

const (
	defaultTimeout = 2 * time.Minute

	// LiteLLMServiceURL is the cluster-local OpenAI-compatible endpoint.
	LiteLLMServiceURL = "http://odigos-litellm:4000/v1"
	// LiteLLMModelName is the fixed LiteLLM model_list alias (not the upstream model id).
	LiteLLMModelName = "interrogation"
)

// Config holds credentials for odigos-litellm (master key). Base URL and model
// alias are fixed constants above.
type Config struct {
	APIKey  string
	Timeout time.Duration
}

// Enabled reports whether the worker can authenticate to odigos-litellm.
func (c Config) Enabled() bool {
	return strings.TrimSpace(c.APIKey) != ""
}

// Client posts to odigos-litellm /v1/chat/completions. System instructions and
// the strict response schema are fixed at construction; each request carries
// only the user-role analysis payload (JSON evidence).
type Client struct {
	cfg            Config
	httpClient     *http.Client
	endpoint       string
	systemPrompt   string
	responseFormat any
}

// Enabled reports whether this client can make a completion request.
func (c *Client) Enabled() bool {
	return c != nil && c.cfg.Enabled()
}

// New builds a client wired for the interrogation analysis contract.
func New(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		cfg: cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		endpoint:       completionsURL(LiteLLMServiceURL),
		systemPrompt:   schema.SystemPrompt,
		responseFormat: schema.StrictResponseFormat(),
	}
}

func completionsURL(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return ""
	}
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/chat/completions"
	}
	return base + "/v1/chat/completions"
}

// CompleteAnalysis runs one non-streaming completion. userJSON is the
// AnalysisRequest payload only (role=user).
func (c *Client) CompleteAnalysis(ctx context.Context, userJSON string) (string, error) {
	if c == nil || !c.cfg.Enabled() {
		return "", fmt.Errorf("litellm is not configured (missing master key)")
	}

	messages := []map[string]string{
		{"role": "system", "content": c.systemPrompt},
		{"role": "user", "content": userJSON},
	}

	body := map[string]any{
		"model":           LiteLLMModelName,
		"messages":        messages,
		"temperature":     0,
		"response_format": c.responseFormat,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.cfg.APIKey))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("chat completions: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", fmt.Errorf("read completions response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &HTTPError{
			StatusCode: resp.StatusCode,
			Body:       truncate(string(payload), 512),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return "", fmt.Errorf("%w: decode: %w", ErrInvalidResponse, err)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return "", fmt.Errorf("chat completions: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("%w: empty content", ErrInvalidResponse)
	}
	return parsed.Choices[0].Message.Content, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
