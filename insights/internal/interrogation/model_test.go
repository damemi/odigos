package interrogation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/workflowagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	apmodel "github.com/odigos-io/odigos/insights/internal/autopilot/model"
)

const testAPIKey = "test-master-key"

// gateway is a fake LiteLLM gateway that records every request it gets.
type gateway struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []gatewayRequest
}

type gatewayRequest struct {
	method, path string
	header       http.Header
	body         map[string]any
}

func newGateway(t *testing.T, h http.HandlerFunc) *gateway {
	t.Helper()
	g := &gateway{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		g.mu.Lock()
		g.reqs = append(g.reqs, gatewayRequest{method: r.Method, path: r.URL.Path, header: r.Header.Clone(), body: body})
		g.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *gateway) url() string { return g.srv.URL + "/v1" }

func (g *gateway) requests() []gatewayRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.reqs)
}

func writeCompletion(w http.ResponseWriter, content, finish string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      "chatcmpl-1",
		"object":  "chat.completion",
		"created": 1,
		"model":   "upstream-model",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": content},
			"finish_reason": finish,
		}},
		"usage": map[string]any{"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150},
	})
}

func writeError(w http.ResponseWriter, status int, header map[string]string, body string) {
	for k, v := range header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

// recordingPause is the loop's real pause with the errors it was told about.
type recordingPause struct {
	*loop.Manager
	mu    sync.Mutex
	noted []error
}

func newRecordingPause() *recordingPause {
	return &recordingPause{Manager: &loop.Manager{}}
}

func (p *recordingPause) NoteModelError(err error, now time.Time) time.Time {
	p.mu.Lock()
	p.noted = append(p.noted, err)
	p.mu.Unlock()
	return p.Manager.NoteModelError(err, now)
}

func (p *recordingPause) notedErrors() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.noted)
}

func testModel(t *testing.T, g *gateway, pause Pause, now time.Time) model.LLM {
	t.Helper()
	llm, err := newModel(context.Background(), g.url(), testAPIKey, pause)
	if err != nil {
		t.Fatal(err)
	}
	llm.(*gatewayModel).now = func() time.Time { return now }
	return llm
}

// configSpy records the config of every call the agent makes.
type configSpy struct {
	model.LLM
	mu   sync.Mutex
	seen []*genai.GenerateContentConfig
}

func (s *configSpy) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	s.mu.Lock()
	s.seen = append(s.seen, req.Config)
	s.mu.Unlock()
	return s.LLM.GenerateContent(ctx, req, stream)
}

func (s *configSpy) configs() []*genai.GenerateContentConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.seen)
}

// call makes one model call outside an agent and returns its last response.
func call(ctx context.Context, llm model.LLM, timeout time.Duration) (*model.LLMResponse, error) {
	req := &model.LLMRequest{
		Contents: []*genai.Content{genai.NewContentFromText("{}", genai.RoleUser)},
		Config:   &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{Timeout: genai.Ptr(timeout)}},
	}
	var last *model.LLMResponse
	for resp, err := range llm.GenerateContent(ctx, req, false) {
		if err != nil {
			return nil, err
		}
		last = resp
	}
	return last, nil
}

// analyzeRunner runs the analyze agent as the workflow does: a node hands it
// the request and the next node receives what it returns.
func analyzeRunner(t *testing.T, llm model.LLM, request string, got *[]any) (*runner.Runner, string) {
	t.Helper()
	analyze, err := newAnalyzeAgent(llm)
	if err != nil {
		t.Fatal(err)
	}
	node, err := workflow.NewAgentNode(analyze, workflow.NodeConfig{})
	if err != nil {
		t.Fatal(err)
	}
	gather := workflow.NewFunctionNode("gather", func(agent.Context, any) (string, error) {
		return request, nil
	}, workflow.NodeConfig{})
	// record sends every other reply round again, as a run's next round does.
	record := workflow.NewEmittingFunctionNode("record", func(ctx agent.Context, in any, emit func(*session.Event) error) (any, error) {
		*got = append(*got, in)
		if len(*got)%2 == 0 {
			return nil, nil
		}
		ev := session.NewEvent(ctx, ctx.InvocationID())
		ev.Routes = []string{"again"}
		return nil, emit(ev)
	}, workflow.NodeConfig{})
	root, err := workflowagent.New(workflowagent.Config{
		Name: "interrogation",
		Edges: workflow.NewEdgeBuilder().
			Add(workflow.Start, gather).
			Add(gather, node).
			Add(node, record).
			AddRoute(record, gather, workflow.StringRoute("again")).
			Build(),
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := session.InMemoryService()
	const id = "run-1"
	if _, err := svc.Create(context.Background(), &session.CreateRequest{AppName: AppName, UserID: id, SessionID: id}); err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: AppName, Agent: root, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	return r, id
}

// The request the gateway gets from the analyze agent, the reply the next
// node gets back, and no history on a later call in the same invocation or
// session.
func TestAnalyzeAgent_Request(t *testing.T) {
	const request = `{"question":"why is checkout slow?","transactions":[]}`
	// Spacing kept to show the text is handed on untouched.
	const reply = `{"answer": "the lookup is slow",  "certainty": 70}`
	g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, reply, "stop") })
	llm := &configSpy{LLM: testModel(t, g, newRecordingPause(), time.Now())}
	var got []any
	r, id := analyzeRunner(t, llm, request, &got)

	ctx := context.Background()
	for range 2 {
		if err := invoke(ctx, r, id, "go"); err != nil {
			t.Fatal(err)
		}
	}

	if want := []any{reply, reply, reply, reply}; !reflect.DeepEqual(got, want) {
		t.Fatalf("record got %#v, want %#v", got, want)
	}
	reqs := g.requests()
	if len(reqs) != 4 {
		t.Fatalf("gateway got %d requests for 4 calls, want 4", len(reqs))
	}
	for i, req := range reqs {
		if req.method != http.MethodPost || req.path != "/v1/chat/completions" {
			t.Errorf("request %d: %s %s, want POST /v1/chat/completions", i, req.method, req.path)
		}
		if auth := req.header.Get("Authorization"); auth != "Bearer "+testAPIKey {
			t.Errorf("request %d: Authorization %q, want the master key", i, auth)
		}
		b := req.body
		if b["model"] != apmodel.LiteLLMModelName {
			t.Errorf("request %d: model %v, want %q", i, b["model"], apmodel.LiteLLMModelName)
		}
		if b["temperature"] != 0.0 {
			t.Errorf("request %d: temperature %v, want 0", i, b["temperature"])
		}
		if _, ok := b["tools"]; ok {
			t.Errorf("request %d: tools sent: %v", i, b["tools"])
		}
		wantMessages := []any{
			map[string]any{"role": "system", "content": Prompt},
			map[string]any{"role": "user", "content": request},
		}
		if !reflect.DeepEqual(b["messages"], wantMessages) {
			t.Errorf("request %d: messages\n%s\nwant the prompt and the request only", i, mustJSON(t, b["messages"]))
		}
		checkResponseFormat(t, b["response_format"])
	}
	for i, cfg := range llm.configs() {
		if cfg == nil || cfg.HTTPOptions == nil || cfg.HTTPOptions.Timeout == nil || *cfg.HTTPOptions.Timeout != modelTimeout {
			t.Errorf("call %d: config %+v, want an HTTP timeout of %v", i, cfg, modelTimeout)
		}
	}
}

// A workflow node hides history by itself; the agent's own setting keeps it
// hidden when the agent is run any other way.
func TestAnalyzeAgent_NoHistoryOutsideANode(t *testing.T) {
	g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, `{"answer":"x"}`, "stop") })
	analyze, err := newAnalyzeAgent(testModel(t, g, newRecordingPause(), time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	svc := session.InMemoryService()
	const id = "run-1"
	ctx := context.Background()
	if _, err := svc.Create(ctx, &session.CreateRequest{AppName: AppName, UserID: id, SessionID: id}); err != nil {
		t.Fatal(err)
	}
	r, err := runner.New(runner.Config{AppName: AppName, Agent: analyze, SessionService: svc})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"first", "second"} {
		if err := invoke(ctx, r, id, text); err != nil {
			t.Fatal(err)
		}
	}

	reqs := g.requests()
	if len(reqs) != 2 {
		t.Fatalf("gateway got %d requests, want 2", len(reqs))
	}
	// Run as the root agent, ADK adds the agent's name to the system message,
	// so only the turns are compared.
	msgs, _ := reqs[1].body["messages"].([]any)
	if len(msgs) != 2 || !reflect.DeepEqual(msgs[1], map[string]any{"role": "user", "content": "second"}) {
		t.Errorf("second call's messages\n%s\nwant the system message and the second request only", mustJSON(t, msgs))
	}
}

func checkResponseFormat(t *testing.T, got any) {
	t.Helper()
	rf, _ := got.(map[string]any)
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format %s, want type json_schema", mustJSON(t, got))
	}
	js, _ := rf["json_schema"].(map[string]any)
	if js["strict"] != true {
		t.Errorf("json_schema.strict = %v, want true", js["strict"])
	}
	sent, _ := js["schema"].(map[string]any)
	if sent == nil {
		t.Fatalf("json_schema carries no schema: %s", mustJSON(t, js))
	}

	wantRefs, gotRefs := map[string]string{}, map[string]string{}
	collectRefs(replySchema(), "", wantRefs)
	collectRefs(sent, "", gotRefs)
	if len(wantRefs) == 0 {
		t.Fatal("reply schema has no $ref; the check proves nothing")
	}
	if !reflect.DeepEqual(gotRefs, wantRefs) {
		t.Errorf("$ref after the strict rewrite: %v, want %v", gotRefs, wantRefs)
	}
	// The reply schema is strict already, so the rewrite may only reorder
	// required lists.
	want := replySchema()
	sortRequired(want)
	sortRequired(sent)
	if mustJSON(t, sent) != mustJSON(t, want) {
		t.Errorf("schema sent:\n%s\nwant the reply schema:\n%s", mustJSON(t, sent), mustJSON(t, want))
	}
}

func collectRefs(v any, path string, out map[string]string) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if k == "$ref" {
				out[path] = fmt.Sprint(child)
				continue
			}
			collectRefs(child, path+"/"+k, out)
		}
	case []any:
		for i, child := range v {
			collectRefs(child, fmt.Sprintf("%s/%d", path, i), out)
		}
	}
}

func sortRequired(v any) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if req, ok := child.([]any); ok && k == "required" {
				slices.SortFunc(req, func(a, b any) int { return strings.Compare(fmt.Sprint(a), fmt.Sprint(b)) })
				continue
			}
			sortRequired(child)
		}
	case []any:
		for _, child := range v {
			sortRequired(child)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGateway_RecordsGlobalPause(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	budget := `{"error":{"message":"Budget has been exceeded! Current cost: 10.2, Max budget: 10.0","type":"budget_exceeded","param":null,"code":"400"}}`
	for _, tc := range []struct {
		name       string
		status     int
		header     map[string]string
		body       string
		wantUntil  time.Time // zero: any time after now
		retryAfter time.Duration
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, header: map[string]string{"Retry-After": "3600"}, body: `{"error":{"message":"rate limited"}}`, wantUntil: now.Add(time.Hour), retryAfter: time.Hour},
		{name: "rate limit, plain body", status: http.StatusTooManyRequests, body: "slow down"},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"error":{"message":"invalid key"}}`},
		{name: "forbidden", status: http.StatusForbidden, body: `{"error":{"message":"forbidden"}}`},
		{name: "budget", status: http.StatusBadRequest, body: budget},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) { writeError(w, tc.status, tc.header, tc.body) })
			pause := newRecordingPause()
			_, err := call(context.Background(), testModel(t, g, pause, now), time.Minute)

			var callErr *ModelCallError
			if !errors.As(err, &callErr) {
				t.Fatalf("error %v, want a *ModelCallError", err)
			}
			loopUntil := pause.ModelPausedUntil(now)
			if loopUntil.IsZero() {
				t.Fatal("the loop is not paused")
			}
			if !callErr.Paused || !callErr.Until.Equal(loopUntil) {
				t.Errorf("ModelCallError paused %v until %v, want paused until the loop's %v", callErr.Paused, callErr.Until, loopUntil)
			}
			if !tc.wantUntil.IsZero() && !loopUntil.Equal(tc.wantUntil) {
				t.Errorf("paused until %v, want %v (Retry-After)", loopUntil, tc.wantUntil)
			}
			var httpErr *apmodel.HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("error %v does not carry an *apmodel.HTTPError", err)
			}
			if httpErr.StatusCode != tc.status || httpErr.Body != tc.body || httpErr.RetryAfter != tc.retryAfter {
				t.Errorf("HTTPError = {%d %q %v}, want {%d %q %v}", httpErr.StatusCode, httpErr.Body, httpErr.RetryAfter, tc.status, tc.body, tc.retryAfter)
			}
			if n := len(pause.notedErrors()); n != 1 {
				t.Errorf("NoteModelError called %d times, want 1", n)
			}
			if n := len(g.requests()); n != 1 {
				t.Errorf("gateway got %d requests, want 1", n)
			}
		})
	}
}

func TestGateway_PausedMakesNoRequest(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, `{}`, "stop") })
	pause := newRecordingPause()
	until := pause.Manager.NoteModelError(&apmodel.HTTPError{StatusCode: http.StatusTooManyRequests, RetryAfter: time.Hour}, now)

	_, err := call(context.Background(), testModel(t, g, pause, now.Add(time.Minute)), time.Minute)
	var callErr *ModelCallError
	if !errors.As(err, &callErr) || !callErr.Paused || !callErr.Until.Equal(until) {
		t.Fatalf("error %v, want a ModelCallError paused until %v", err, until)
	}
	if n := len(g.requests()); n != 0 {
		t.Errorf("gateway got %d requests during a pause", n)
	}
	if n := len(pause.notedErrors()); n != 0 {
		t.Errorf("a refused call was noted %d times", n)
	}
}

func TestGateway_InvalidResponse(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "no choices", handler: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m","choices":[]}`)
		}},
		{name: "empty text", handler: func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, "", "stop") }},
		{name: "blank text", handler: func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, " \n ", "stop") }},
		{name: "truncated", handler: func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, `{"answer":"the look`, "length") }},
		{name: "no finish reason", handler: func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, `{"answer":"x"}`, "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGateway(t, tc.handler)
			pause := newRecordingPause()
			resp, err := call(context.Background(), testModel(t, g, pause, now), time.Minute)

			var callErr *ModelCallError
			if !errors.As(err, &callErr) || !errors.Is(err, apmodel.ErrInvalidResponse) {
				t.Fatalf("got %v, %v; want a ModelCallError with ErrInvalidResponse", resp, err)
			}
			if callErr.Paused {
				t.Errorf("an invalid reply paused calls until %v", callErr.Until)
			}
			if until := pause.ModelPausedUntil(now); !until.IsZero() {
				t.Errorf("the loop is paused until %v", until)
			}
		})
	}
}

// cannedModel streams its responses as given.
type cannedModel []*model.LLMResponse

func (cannedModel) Name() string { return "canned" }

func (m cannedModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for _, resp := range m {
			if !yield(resp, nil) {
				return
			}
		}
	}
}

// A streamed reply's partial chunks carry no finish reason and may carry no
// text; only the final response is checked.
func TestGateway_StreamPartials(t *testing.T) {
	final := &model.LLMResponse{Content: genai.NewContentFromText(`{"answer":"x"}`, genai.RoleModel), FinishReason: genai.FinishReasonStop}
	canned := cannedModel{
		{Partial: true},
		{Partial: true, Content: genai.NewContentFromText(`{"answer":`, genai.RoleModel)},
		final,
	}
	llm := &gatewayModel{LLM: canned, pause: newRecordingPause(), now: time.Now}

	var got []*model.LLMResponse
	for resp, err := range llm.GenerateContent(context.Background(), &model.LLMRequest{}, true) {
		if err != nil {
			t.Fatalf("response %d: %v", len(got), err)
		}
		got = append(got, resp)
	}
	if !slices.Equal(got, canned) {
		t.Fatalf("got %d responses, want the %d streamed", len(got), len(canned))
	}
}

// openai-go falls back to OPENAI_API_KEY when the key it is given is empty.
func TestNewModel_EmptyKey(t *testing.T) {
	const envKey = "key-from-the-environment"
	t.Setenv("OPENAI_API_KEY", envKey)
	g := newGateway(t, func(w http.ResponseWriter, _ *http.Request) { writeCompletion(w, `{}`, "stop") })
	pause := newRecordingPause()
	ctx := context.Background()

	if _, err := NewModel(ctx, "", pause); err == nil {
		t.Error("NewModel accepted an empty key")
	}
	for _, key := range []string{"", " \n"} {
		if _, err := newModel(ctx, g.url(), key, pause); err == nil {
			t.Errorf("key %q accepted", key)
		}
	}

	llm, err := newModel(ctx, g.url(), testAPIKey, pause)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, llm, time.Minute); err != nil {
		t.Fatal(err)
	}
	for _, req := range g.requests() {
		if auth := req.header.Get("Authorization"); auth != "Bearer "+testAPIKey {
			t.Errorf("Authorization %q, want the master key", auth)
		}
		for name, values := range req.header {
			for _, v := range values {
				if strings.Contains(v, envKey) {
					t.Errorf("header %s carries OPENAI_API_KEY", name)
				}
			}
		}
	}
	if n := len(g.requests()); n != 1 {
		t.Errorf("gateway got %d requests, want 1", n)
	}
}
