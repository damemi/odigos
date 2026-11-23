package interrogation

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/odigos-io/odigos/common/autopilot"
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

//go:generate go run ../autopilot/schema/genjsonschema -type interrogation.Reply -json reply.schema.json -go ""

//go:embed reply.schema.json
var replySchemaJSON []byte

// Reply is the model's reply: the loop's AnalysisResponse and the answer to
// the question. No field is a pointer or omitted when empty, so the strict
// JSON schema requires every one.
type Reply struct {
	apschema.AnalysisResponse
	// Summary and Reasons come before Answer so that the model writes them
	// first and leaves Answer what they do not say. A reply without them is
	// valid.
	Summary string        `json:"summary" jsonschema:"required"`
	Reasons []ReplyReason `json:"reasons" jsonschema:"required,maxItems=5"`
	Answer  string        `json:"answer" jsonschema:"required"`
	// Certainty is the model's own number; it is shown and nothing stops on
	// it.
	Certainty int  `json:"certainty" jsonschema:"required,minimum=0,maximum=100"`
	Done      bool `json:"done" jsonschema:"required"`
}

// ReplyReason is a reason of the answer as the model gives it: the functions
// it rests on by their probe target and the transactions by their id.
type ReplyReason struct {
	Text           string   `json:"text" jsonschema:"required"`
	Basis          string   `json:"basis" jsonschema:"required,enum=measured,enum=inferred"`
	Targets        []string `json:"targets" jsonschema:"required,maxItems=8"`
	TransactionIDs []string `json:"transactionIds" jsonschema:"required"`
}

// Limits of the structured answer. A reply over them is cut, not refused.
const (
	maxSummary       = 300
	maxReasons       = 5
	maxReasonText    = 300
	maxReasonTargets = 8
	// minMeasuredSpans is the fewest spans a probe needs to back a reason, as
	// the prompt tells the model. No span at all is a measurement too: the
	// function was not called.
	minMeasuredSpans = 5
)

// Validate checks the reply as the loop checks its own, then the answer.
// Certainty bounds are checked here because a provider may drop them from the
// schema.
func (r *Reply) Validate() error {
	if err := r.AnalysisResponse.Validate(); err != nil {
		return err
	}
	// The summary carries the conclusion; the answer may have nothing to add.
	if strings.TrimSpace(r.Answer) == "" && strings.TrimSpace(r.Summary) == "" {
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
		// A badly typed summary or reason must not cost the answer.
		var fields map[string]json.RawMessage
		if json.Unmarshal([]byte(raw), &fields) != nil {
			return nil, fmt.Errorf("decode reply: %w", err)
		}
		delete(fields, "summary")
		delete(fields, "reasons")
		rest, merr := json.Marshal(fields)
		if merr != nil {
			return nil, fmt.Errorf("decode reply: %w", merr)
		}
		r = Reply{}
		if err := json.Unmarshal(rest, &r); err != nil {
			return nil, fmt.Errorf("decode reply: %w", err)
		}
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

// answerReasons links the reply's reasons to the run: a cited target to the
// run's changes on that function, a transaction id to the scope. What links
// to neither is dropped. A reason is measured only when a function it cites
// was measured; one that cites no function, when a transaction it cites has
// timings in this round.
func (st *State) answerReasons(rs []ReplyReason) []AnswerReason {
	var timed []TxTiming
	if r := st.currentRound(); r != nil {
		timed = r.Timings
	}
	var out []AnswerReason
	for _, rr := range rs[:min(len(rs), maxReasons)] {
		text := cut(rr.Text, maxReasonText)
		if text == "" {
			continue
		}
		reason := AnswerReason{Text: text, Basis: apitypes.Inferred, ChangeIds: []string{}, TransactionIds: []string{}}
		probed, timedTx := false, false
		for _, cited := range rr.Targets[:min(len(rr.Targets), maxReasonTargets)] {
			cited = strings.TrimSpace(cited)
			// The call graph names a Java method with its return type and
			// parameters; its probe target has neither.
			method := autopilot.ClassFromFrameName(cited)
			for _, p := range st.Probes {
				if (p.Target == cited || p.Target == method) && !slices.Contains(reason.ChangeIds, p.ChangeID) {
					reason.ChangeIds = append(reason.ChangeIds, p.ChangeID)
					probed = probed || p.Measured != nil && (p.Measured.Spans == 0 || p.Measured.Spans >= minMeasuredSpans)
				}
			}
		}
		for _, raw := range rr.TransactionIDs {
			id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			tx := repo.TransactionID(id)
			if err != nil || !slices.ContainsFunc(st.Scope, func(s ScopeTransaction) bool { return s.TransactionID == tx }) {
				continue
			}
			if v := strconv.FormatInt(id, 10); !slices.Contains(reason.TransactionIds, v) {
				reason.TransactionIds = append(reason.TransactionIds, v)
			}
			timedTx = timedTx || slices.ContainsFunc(timed, func(t TxTiming) bool { return t.TransactionID == tx })
		}
		measured := timedTx
		if len(rr.Targets) > 0 {
			measured = probed
		}
		if rr.Basis == string(apitypes.Measured) && measured {
			reason.Basis = apitypes.Measured
		}
		out = append(out, reason)
	}
	return out
}

// cut trims s and cuts it to n characters, the last an ellipsis.
func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
