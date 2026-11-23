package interrogation

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

func validReply() Reply {
	return Reply{
		AnalysisResponse: apschema.AnalysisResponse{
			SchemaVersion: apschema.Version,
			TransactionID: "8841",
			Recommendations: []apschema.Recommendation{{
				Category: apschema.CategoryObservability,
				Function: "com.claims.IntakeService.validate",
				Language: apschema.LangJava,
				Probe:    apschema.Probe{ClassName: "com.claims.IntakeService", MethodName: "validate"},
				Why:      "IntakeService.validate is called from ClaimController.submit.",
			}},
		},
		Answer:    "Time goes to IntakeService.validate, called from ClaimController.submit.",
		Certainty: 45,
	}
}

func TestParseReply(t *testing.T) {
	b, err := json.Marshal(validReply())
	if err != nil {
		t.Fatal(err)
	}
	valid := string(b)
	withUnknown := strings.Replace(valid, `"done":false`, `"done":false,"confidenceNote":"x"`, 1)
	if withUnknown == valid {
		t.Fatal("test reply has no done field")
	}

	for name, raw := range map[string]string{
		"plain":             valid,
		"surrounding space": "\n  " + valid + "\n",
		"unknown field":     withUnknown,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseReply(raw)
			if err != nil {
				t.Fatalf("parseReply: %v", err)
			}
			if want := validReply(); !reflect.DeepEqual(*got, want) {
				t.Fatalf("parseReply = %+v, want %+v", *got, want)
			}
		})
	}

	// The loop's decoder accepts the same unknown field.
	if _, err := apschema.Parse([]byte(withUnknown)); err != nil {
		t.Fatalf("schema.Parse refuses the unknown field: %v", err)
	}

	for name, raw := range map[string]string{
		"empty":   "  ",
		"prose":   "The answer is that validate is slow.",
		"fenced":  "```json\n" + valid + "\n```",
		"invalid": strings.Replace(valid, `"certainty":45`, `"certainty":150`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := parseReply(raw); err == nil {
				t.Fatalf("parseReply = %+v, want an error", got)
			}
		})
	}
}

func TestReplySchema(t *testing.T) {
	s := replySchema()
	if s["type"] != "object" || s["additionalProperties"] != false {
		t.Fatalf("root is not a closed object: type %v, additionalProperties %v", s["type"], s["additionalProperties"])
	}
	var required []string
	for _, r := range s["required"].([]any) {
		required = append(required, r.(string))
	}
	if want := []string{"schemaVersion", "transactionId", "recommendations", "summary", "reasons", "answer", "certainty", "done"}; !reflect.DeepEqual(required, want) {
		t.Fatalf("required = %v, want %v", required, want)
	}
	certainty := s["properties"].(map[string]any)["certainty"].(map[string]any)
	if certainty["type"] != "integer" || certainty["minimum"] != 0.0 || certainty["maximum"] != 100.0 {
		t.Fatalf("certainty = %v, want an integer 0..100", certainty)
	}

	// The probes the model may recommend follow the loop's schema.
	raw, err := os.ReadFile("../autopilot/schema/analysis_response.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var loopSchema map[string]any
	if err := json.Unmarshal(raw, &loopSchema); err != nil {
		t.Fatal(err)
	}
	for _, def := range []string{"Recommendation", "Probe"} {
		got := s["$defs"].(map[string]any)[def]
		want := loopSchema["$defs"].(map[string]any)[def]
		if !reflect.DeepEqual(got, want) {
			t.Errorf("$defs.%s = %v, want the loop's %v", def, got, want)
		}
	}
}

// A reply from before summary and reasons, or from a model that leaves them
// out, is valid, and the answer then has neither.
func TestParseReply_WithoutSummaryAndReasons(t *testing.T) {
	b, err := json.Marshal(validReply())
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "summary")
	delete(fields, "reasons")
	b, _ = json.Marshal(fields)
	reply, err := parseReply(string(b))
	if err != nil {
		t.Fatalf("parseReply: %v", err)
	}

	st := &State{Rounds: []Round{{Round: 1}}, Round: 1}
	st.answer(reply, time.Date(2026, 11, 17, 9, 40, 0, 0, time.UTC))
	for what, v := range map[string]any{"answer": st.Answer, "round reply": st.Rounds[0].Reply} {
		b, _ := json.Marshal(v)
		var got map[string]any
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if _, ok := got["summary"]; ok {
			t.Errorf("%s has a summary: %s", what, b)
		}
		if _, ok := got["reasons"]; ok {
			t.Errorf("%s has reasons: %s", what, b)
		}
	}
	if p := answerUpdated(st, st.Answer.UpdatedAt).Payload; p["summary"] != nil || p["reasons"] != nil {
		t.Errorf("answer.updated payload %v, want no summary or reasons", p)
	}
}

func TestAnswerReasons(t *testing.T) {
	measured := &Measured{Spans: 41, AvgMs: 1005}
	st := &State{
		Round: 2,
		Probes: []Probe{
			{ChangeID: "chg_buy", Target: "com.shop.InventoryService.buy", Status: ProbeApplied, Measured: measured},
			{ChangeID: "chg_check", Target: "com.shop.StockClient.check", Status: ProbeApplied},
			{ChangeID: "chg_audit", Target: "com.shop.Audit.write", Status: ProbeDenied},
			{ChangeID: "chg_dead", Target: "com.shop.Legacy.run", Status: ProbeApplied, Measured: &Measured{}},
			{ChangeID: "chg_rare", Target: "com.shop.Rare.once", Status: ProbeApplied, Measured: &Measured{Spans: 1}},
		},
		Scope:  []ScopeTransaction{{TransactionID: 7636099928166363546}, {TransactionID: 42}},
		Rounds: []Round{{Round: 1}, {Round: 2, Timings: []TxTiming{{TransactionID: 7636099928166363546}}}},
	}
	long := strings.Repeat("é", 400)
	tooMany := make([]string, 0, 10)
	for i := range 8 {
		tooMany = append(tooMany, "com.shop.Unknown.f"+string(rune('a'+i)))
	}
	tooMany = append(tooMany, "com.shop.InventoryService.buy")

	for _, tc := range []struct {
		name string
		in   ReplyReason
		want AnswerReason
	}{{
		name: "measured probe",
		in:   ReplyReason{Text: "buy takes 1005 ms", Basis: "measured", Targets: []string{" com.shop.InventoryService.buy ", "com.shop.InventoryService.buy"}, TransactionIDs: []string{}},
		want: AnswerReason{Text: "buy takes 1005 ms", Basis: apitypes.Measured, ChangeIds: []string{"chg_buy"}, TransactionIds: []string{}},
	}, {
		name: "call-graph name",
		in:   ReplyReason{Text: "buy takes 1005 ms", Basis: "measured", Targets: []string{"void com.shop.InventoryService.buy(int)"}},
		want: AnswerReason{Text: "buy takes 1005 ms", Basis: apitypes.Measured, ChangeIds: []string{"chg_buy"}, TransactionIds: []string{}},
	}, {
		name: "unknown target dropped, unlinked measured is inferred",
		in:   ReplyReason{Text: "the controller is slow", Basis: "measured", Targets: []string{"com.shop.Controller.post"}},
		want: AnswerReason{Text: "the controller is slow", Basis: apitypes.Inferred, ChangeIds: []string{}, TransactionIds: []string{}},
	}, {
		name: "timed transaction",
		in:   ReplyReason{Text: "POST /buy averages 1060 ms", Basis: "measured", TransactionIDs: []string{"7636099928166363546", "7636099928166363546", "99", "x"}},
		want: AnswerReason{Text: "POST /buy averages 1060 ms", Basis: apitypes.Measured, ChangeIds: []string{}, TransactionIds: []string{"7636099928166363546"}},
	}, {
		name: "unmeasured probe and untimed transaction",
		in:   ReplyReason{Text: "check is fast", Basis: "measured", Targets: []string{"com.shop.StockClient.check", "com.shop.Audit.write"}, TransactionIDs: []string{"42"}},
		want: AnswerReason{Text: "check is fast", Basis: apitypes.Inferred, ChangeIds: []string{"chg_check", "chg_audit"}, TransactionIds: []string{"42"}},
	}, {
		name: "timed transaction does not back an unmeasured function",
		in:   ReplyReason{Text: "write takes 900 ms", Basis: "measured", Targets: []string{"com.shop.Audit.write"}, TransactionIDs: []string{"7636099928166363546"}},
		want: AnswerReason{Text: "write takes 900 ms", Basis: apitypes.Inferred, ChangeIds: []string{"chg_audit"}, TransactionIds: []string{"7636099928166363546"}},
	}, {
		name: "no span is a measurement",
		in:   ReplyReason{Text: "run is never called", Basis: "measured", Targets: []string{"com.shop.Legacy.run"}},
		want: AnswerReason{Text: "run is never called", Basis: apitypes.Measured, ChangeIds: []string{"chg_dead"}, TransactionIds: []string{}},
	}, {
		name: "too few spans",
		in:   ReplyReason{Text: "once takes 3 ms", Basis: "measured", Targets: []string{"com.shop.Rare.once"}},
		want: AnswerReason{Text: "once takes 3 ms", Basis: apitypes.Inferred, ChangeIds: []string{"chg_rare"}, TransactionIds: []string{}},
	}, {
		name: "inferred stays inferred",
		in:   ReplyReason{Text: "buy is the only callee", Basis: "inferred", Targets: []string{"com.shop.InventoryService.buy"}},
		want: AnswerReason{Text: "buy is the only callee", Basis: apitypes.Inferred, ChangeIds: []string{"chg_buy"}, TransactionIds: []string{}},
	}, {
		name: "text and targets cut",
		in:   ReplyReason{Text: long, Basis: "measured", Targets: tooMany},
		want: AnswerReason{Text: strings.Repeat("é", maxReasonText-1) + "…", Basis: apitypes.Inferred, ChangeIds: []string{}, TransactionIds: []string{}},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := st.answerReasons([]ReplyReason{tc.in})
			if len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
				t.Fatalf("answerReasons = %+v, want %+v", got, tc.want)
			}
		})
	}

	var many []ReplyReason
	for i := range maxReasons + 2 {
		many = append(many, ReplyReason{Text: strings.Repeat("r", i), Basis: "inferred"})
	}
	if got := st.answerReasons(many); len(got) != maxReasons-1 || got[0].Text != "r" {
		t.Errorf("answerReasons of %d = %+v, want the first %d with text", len(many), got, maxReasons-1)
	}

	reply := validReply()
	reply.Summary = strings.Repeat("s", 400)
	st.answer(&reply, time.Date(2026, 11, 17, 9, 40, 0, 0, time.UTC))
	if n := len([]rune(st.Answer.Summary)); n != maxSummary || st.Rounds[1].Reply.Summary != st.Answer.Summary {
		t.Errorf("summary of %d characters, round reply %q; want %d, the same", n, st.Rounds[1].Reply.Summary, maxSummary)
	}
}

// A summary alone is an answer, and a badly typed summary or reason does not
// cost the reply.
func TestParseReply_SummaryOnlyAndBadReasons(t *testing.T) {
	set := func(mutate func(map[string]json.RawMessage)) string {
		b, err := json.Marshal(validReply())
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			t.Fatal(err)
		}
		mutate(fields)
		b, _ = json.Marshal(fields)
		return string(b)
	}

	got, err := parseReply(set(func(f map[string]json.RawMessage) {
		f["summary"] = json.RawMessage(`"X is slow."`)
		f["answer"] = json.RawMessage(`""`)
	}))
	if err != nil || got.Summary != "X is slow." {
		t.Fatalf("summary only: %+v, %v", got, err)
	}

	if _, err := parseReply(set(func(f map[string]json.RawMessage) {
		f["summary"] = json.RawMessage(`""`)
		f["answer"] = json.RawMessage(`""`)
	})); err == nil {
		t.Fatal("a reply with no summary and no answer was accepted")
	}

	got, err = parseReply(set(func(f map[string]json.RawMessage) {
		f["reasons"] = json.RawMessage(`[{"text":"t","basis":"measured","targets":"com.X.y","transactionIds":[8841]}]`)
	}))
	if err != nil || len(got.Reasons) != 0 || got.Answer != validReply().Answer {
		t.Fatalf("badly typed reasons: %+v, %v", got, err)
	}
}
