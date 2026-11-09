package k8s

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"k8s.io/apimachinery/pkg/util/wait"
)

// conflictBackoff governs how write handlers retry on a resourceVersion 409.
// retry.DefaultRetry is only 5 steps at Factor 1.0 (~50ms total budget), which
// empirically exhausts past ~12 concurrent writers on a SINGLE hot object (e.g.
// many agents editing the same destination or the odigos-configuration ConfigMap
// at once) — success fell to ~70% at 20-way. This backoff gives more attempts
// with real exponential growth + 50% jitter so a large herd serializes instead
// of failing; the Cap bounds worst-case per-call latency. Measured: 20-way
// same-object writes go from ~70% to ~100% with this. All *_write retry wrappers
// use it instead of retry.DefaultRetry.
var conflictBackoff = wait.Backoff{
	Steps:    15,
	Duration: 25 * time.Millisecond,
	Factor:   1.7,
	Jitter:   0.5,
	Cap:      1 * time.Second,
}

// GuidedResponse is the standardized envelope every successful tool response
// uses. The point is to give the calling AI agent enough structure to act
// independently — not just data, but what the data means (context) and which
// follow-up tools likely make sense (next_steps). Wrap every success path
// through withGuidance.
type GuidedResponse struct {
	Data      any        `json:"data"`
	Context   string     `json:"context,omitempty"`
	NextSteps []NextStep `json:"next_steps,omitempty"`
}

// NextStep tells the agent "if you're trying to X, call tool Y with these args".
// Args are populated from the current response so the agent can chain without
// re-querying (e.g. pre-fill the workload triple).
type NextStep struct {
	When string         `json:"when"`
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

// withGuidance wraps data with optional context + next steps and returns it as
// machine-readable structured content (the GuidedResponse object the agent can
// parse directly), with an indented-JSON text rendering as a fallback for
// text-only clients. ctx may be empty and steps may be nil for tools where
// guidance adds no value (rare).
func withGuidance(data any, ctx string, steps []NextStep) (*mcp.CallToolResult, error) {
	resp := GuidedResponse{Data: data, Context: ctx, NextSteps: steps}
	buf, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("marshal response: %v", err)), nil
	}
	return mcp.NewToolResultStructured(resp, string(buf)), nil
}

// errorWithOptions returns a tool error that names the valid choices so the
// agent can self-correct on the next call (cribl-mcp pattern). Use it for
// unknown enum / id / type values rather than a bare error string. The choices
// are returned as structured content too, so a client can render them.
func errorWithOptions(msg string, valid []string) (*mcp.CallToolResult, error) {
	body := map[string]any{
		"error":         msg,
		"valid_options": valid,
	}
	buf, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(msg), nil
	}
	// Validation/lookup failures must report isError=true so programmatic clients
	// can tell a rejected call from a successful one; the structured body
	// (message + valid_options) rides along as the error content so the model can
	// still self-correct.
	return mcp.NewToolResultError(string(buf)), nil
}
