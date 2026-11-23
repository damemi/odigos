package interrogation

import (
	"strconv"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
)

const (
	// RequestVersion is the version of Request the prompt describes.
	RequestVersion = "1"
	// PromptRevision changes with the prompt's addendum.
	PromptRevision = "1"
)

// Request is the model's user message for one round. Its evidence is the
// loop's request for each transaction, so the loop's prompt keeps describing
// it.
type Request struct {
	SchemaVersion     string                   `json:"schemaVersion"`
	Question          string                   `json:"question"`
	Goal              string                   `json:"goal"`
	Focus             []string                 `json:"focus"`
	Round             int                      `json:"round"`
	MaxRounds         int                      `json:"maxRounds"`
	Final             bool                     `json:"final"`
	ProbeBudget       int                      `json:"probeBudget"`
	AddedProbes       []AddedProbe             `json:"addedProbes"`
	PreviousAnswer    string                   `json:"previousAnswer"`
	PreviousCertainty int                      `json:"previousCertainty"`
	Service           apschema.Service         `json:"service"`
	ExistingProbes    []apschema.ExistingProbe `json:"existingProbes"`
	Transactions      []RequestTransaction     `json:"transactions"`
}

// AddedProbe is a probe the run asked for in an earlier round.
type AddedProbe struct {
	Language string `json:"language"`
	Target   string `json:"target"`
	Round    int    `json:"round"`
	// State is requested, applied, denied or reverted.
	State string `json:"state"`
}

// RequestTransaction is the evidence of one transaction in scope.
type RequestTransaction struct {
	Transaction     apschema.RequestTxn        `json:"transaction"`
	Container       string                     `json:"container"`
	CallGraph       []apschema.RequestCallEdge `json:"callGraph"`
	EvidenceChanged bool                       `json:"evidenceChanged"`
}

// transactionEvidence is one transaction a round read: the loop's request
// for it (loop.BuildFullAnalysisRequest) and whether its call graph changed
// since the previous round.
type transactionEvidence struct {
	Request apschema.AnalysisRequest
	Changed bool
}

// Revision names the prompt, the request and the model a run uses. It is
// stored on the run and on its probe rows.
func Revision(modelAlias string) string {
	return "interrogation=" + PromptRevision + "/" + loop.AnalysisRevision(modelAlias)
}

// buildRequest is the round's user message. Service, existing probes and
// each transaction's call graph are copied from the loop's requests; the
// service spans every container, so it names none.
func buildRequest(st *State, txs []transactionEvidence) Request {
	req := Request{
		SchemaVersion:  RequestVersion,
		Question:       st.Request.Question,
		Goal:           st.Request.Goal,
		Focus:          make([]string, 0, len(st.Request.TransactionIDs)),
		Round:          st.Round,
		MaxRounds:      st.Request.Budget.MaxRounds,
		Final:          st.Final,
		ProbeBudget:    st.Request.Budget.MaxProbes,
		AddedProbes:    []AddedProbe{},
		Service:        apschema.Service{Namespace: st.Request.Source.Namespace, Kind: st.Request.Source.Kind, Name: st.Request.Source.Name},
		ExistingProbes: []apschema.ExistingProbe{},
		Transactions:   make([]RequestTransaction, 0, len(txs)),
	}
	for _, id := range st.Request.TransactionIDs {
		req.Focus = append(req.Focus, strconv.FormatInt(id, 10))
	}
	if st.Answer != nil {
		req.PreviousAnswer, req.PreviousCertainty = st.Answer.Text, st.Answer.Certainty
	}
	for _, p := range st.Probes {
		var state string
		switch p.Status {
		case ProbeRequested, ProbeApplied, ProbeReverted:
			state = string(p.Status)
		case ProbeDenied, ProbeFailed:
			state = string(ProbeDenied)
		default:
			continue
		}
		req.AddedProbes = append(req.AddedProbes, AddedProbe{Language: p.Language, Target: p.Target, Round: p.Round, State: state})
		// A denied proposal never had a row, so it used none of the budget.
		if p.Status != ProbeDenied {
			req.ProbeBudget--
		}
	}
	req.ProbeBudget = max(req.ProbeBudget, 0)

	for i, tx := range txs {
		if i == 0 {
			req.Service = tx.Request.Service
			req.Service.Container = ""
			req.ExistingProbes = nonNil(tx.Request.ExistingProbes)
		}
		req.Transactions = append(req.Transactions, RequestTransaction{
			Transaction:     tx.Request.Transaction,
			Container:       tx.Request.Service.Container,
			CallGraph:       nonNil(tx.Request.CallGraph),
			EvidenceChanged: tx.Changed,
		})
	}
	return req
}

// Prompt is the system instruction of the model call: the loop's, which
// describes the evidence and the probes, and what an interrogation adds.
const Prompt = apschema.SystemPrompt + "\n\n" + promptAddendum

const promptAddendum = `Interrogation mode. Everything above applies to the probes you recommend. What differs:

The user message is a JSON InterrogationRequest (schemaVersion 1), not an AnalysisRequest:
- question: what the user wants to know about this service. Empty means: find what is most worth knowing for the goal.
- goal: "observability" or "security". It decides which probes are useful and what the answer is about.
- focus: transaction ids the user selected. When not empty, answer about these first.
- round, maxRounds: this is round ` + "`round`" + ` of at most ` + "`maxRounds`" + `.
- final: when true, no probe can be added after this reply. Return recommendations: [] and give your best answer.
- probeBudget: how many more probes this interrogation may add. Never recommend more than this.
- addedProbes: probes this interrogation added in earlier rounds and their state (requested, applied, denied, reverted).
- previousAnswer, previousCertainty: your answer after the previous round. Empty and 0 in round 1.
- service, existingProbes: as in an AnalysisRequest.
- transactions: one entry per observed entry operation: transaction (id, rootSpanName, rootSpanKind), container, callGraph (the full caller→callee edges, as in full mode) and evidenceChanged (true when the edges differ from the previous round).

The evidence is call-graph structure only. You have no durations, error rates, payloads or source. Do not state them. Say plainly what the evidence cannot show.

Reply with the AnalysisResponse fields plus:
- answer: 1–4 sentences that answer the question from the evidence, naming functions and at least one caller→callee edge.
- certainty: integer 0–100. How sure you are that the answer is correct and complete given only this evidence.
- done: true when more probes would not change the answer. False only when the probes you recommend in this reply would.
- transactionId: the id of the transaction the answer mainly concerns.

Recommend a probe only when it helps answer the question. Every recommended function must appear in a callGraph of this request.`
