package interrogation

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

//go:generate go run ../autopilot/schema/genjsonschema -type interrogation.Reply -json reply.schema.json -go ""

//go:embed reply.schema.json
var replySchemaJSON []byte

// Reply is the model's reply: the loop's AnalysisResponse and the answer to
// the question. No field is a pointer or omitted when empty, so the strict
// JSON schema requires every one.
type Reply struct {
	apschema.AnalysisResponse
	Answer string `json:"answer" jsonschema:"required"`
	// Certainty is the model's own number; it is shown and nothing stops on
	// it.
	Certainty int  `json:"certainty" jsonschema:"required,minimum=0,maximum=100"`
	Done      bool `json:"done" jsonschema:"required"`
}

// Validate checks the reply as the loop checks its own, then the answer.
// Certainty bounds are checked here because a provider may drop them from the
// schema.
func (r *Reply) Validate() error {
	if err := r.AnalysisResponse.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Answer) == "" {
		return errors.New("answer is empty")
	}
	if r.Certainty < 0 || r.Certainty > 100 {
		return fmt.Errorf("certainty %d out of range 0..100", r.Certainty)
	}
	return nil
}

// parseReply decodes and validates a model reply as schema.Parse does for the
// loop: surrounding space is trimmed, unknown fields are ignored and nothing
// else is stripped.
func parseReply(raw string) (*Reply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty reply")
	}
	var r Reply
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return nil, fmt.Errorf("decode reply: %w", err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// replySchema is the JSON schema the model's reply must follow.
func replySchema() map[string]any {
	var out map[string]any
	if err := json.Unmarshal(replySchemaJSON, &out); err != nil {
		panic("interrogation: invalid embedded reply.schema.json: " + err.Error())
	}
	return out
}
