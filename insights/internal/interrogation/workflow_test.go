package interrogation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	"github.com/odigos-io/odigos/insights/internal/autopilot/measure"
	apmodel "github.com/odigos-io/odigos/insights/internal/autopilot/model"
	apschema "github.com/odigos-io/odigos/insights/internal/autopilot/schema"
	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// What the leader wires into Deps.
var (
	_ Loop     = (*loop.Manager)(nil)
	_ Evidence = (*apstore.Client)(nil)
	_ Changes  = (repo.Repository)(nil)
)

// testClock is the time every node and fake reads.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t.After(c.t) {
		c.t = t
	}
}

func (c *testClock) Advance(d time.Duration) { c.Set(c.Now().Add(d)) }

// fakeLoop stands in for *loop.Manager. Its model-call pause is the real
// one.
type fakeLoop struct {
	*loop.Manager
	changes *fakeChanges

	mu sync.Mutex
	// gone makes the workload uninstrumented from the given Instrumented
	// call on (1-based); 0 never.
	goneFrom     int
	instrumented int
	// deny refuses a probe by target.
	deny       map[string]*loop.ProbeDeniedError
	requestErr error
	requests   []loop.ProbeRequest
	// timings is what ProbeTimings reads; nil reads none.
	timings measure.Timings
}

func (l *fakeLoop) ProbeTimings(context.Context, apstore.SourceKey, time.Time) (measure.Timings, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.timings, nil
}

func (l *fakeLoop) Instrumented(context.Context, apstore.SourceKey) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.instrumented++
	return l.goneFrom == 0 || l.instrumented < l.goneFrom, nil
}

func (l *fakeLoop) ProbesInEffect(context.Context, apstore.SourceKey) ([]loop.CustomProbe, error) {
	return nil, nil
}

// RequestProbe stores the probe as the real one does, as an approved row
// that expires at the run's deadline.
func (l *fakeLoop) RequestProbe(_ context.Context, req loop.ProbeRequest) (apstore.RequestedProbe, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests = append(l.requests, req)
	if l.requestErr != nil {
		return apstore.RequestedProbe{}, l.requestErr
	}
	target := loop.ProbeTarget(req.Recommendation)
	if d := l.deny[target]; d != nil {
		return apstore.RequestedProbe{}, d
	}
	id := loop.ChangeID(req.InterrogationID, req.Recommendation.Language, target)
	ch := l.changes.insert(req.InterrogationID, repo.InterrogationChange{
		ChangeID: id,
		Round:    req.Round,
		Language: req.Recommendation.Language,
		Desired:  repo.ChangeApproved,
	})
	return apstore.RequestedProbe{ChangeID: ch.ChangeID, InterrogationID: req.InterrogationID, State: ch.Desired, ExpiresAt: ch.ExpiresAt}, nil
}

func (l *fakeLoop) probeRequests() []loop.ProbeRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.requests)
}

// fakeChanges is the repo's probe rows, with a stand-in reconciler that puts
// a row in the rule on the placeAfter-th read of it.
type fakeChanges struct {
	clock *testClock
	// expiresAt gives an inserted row its expiry.
	expiresAt func() time.Time

	mu         sync.Mutex
	rows       map[string]*repo.InterrogationChange
	runs       map[string]string // change id to run id
	reads      map[string]int
	placeAfter int // 0: never placed
	block      string
	ended      []string
	reverts    int
	revertErr  error
}

func (c *fakeChanges) insert(run string, ch repo.InterrogationChange) repo.InterrogationChange {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.rows[ch.ChangeID]; ok {
		return *prev
	}
	ch.ExpiresAt = c.expiresAt()
	c.rows[ch.ChangeID] = &ch
	c.runs[ch.ChangeID] = run
	return ch
}

func (c *fakeChanges) GetInterrogationChange(_ context.Context, id string) (repo.InterrogationChange, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.rows[id]
	if !ok {
		return repo.InterrogationChange{}, false, nil
	}
	c.reads[id]++
	if ch.Live(c.clock.Now()) {
		if c.placeAfter > 0 && c.reads[id] >= c.placeAfter {
			ch.InRule, ch.AppliedAt = true, c.clock.Now()
		}
		if c.block != "" {
			ch.BlockReason = c.block
		}
	}
	return *ch, true, nil
}

func (c *fakeChanges) ListInterrogationChanges(_ context.Context, id string) ([]repo.InterrogationChange, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []repo.InterrogationChange
	for changeID, ch := range c.rows {
		if c.runs[changeID] == id {
			out = append(out, *ch)
		}
	}
	return out, nil
}

func (c *fakeChanges) EndInterrogationChange(_ context.Context, id, reason string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.rows[id]
	if !ok || ch.Desired != repo.ChangeApproved {
		return nil
	}
	ch.Desired, ch.EndReason, ch.InRule = repo.ChangeReverted, reason, false
	c.ended = append(c.ended, id+":"+reason)
	return nil
}

func (c *fakeChanges) RevertInterrogationProbes(_ context.Context, id string, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reverts++
	for changeID, ch := range c.rows {
		if c.runs[changeID] == id && ch.Desired == repo.ChangeApproved && ch.ExpiresAt.After(now) {
			ch.Desired, ch.EndReason = repo.ChangeReverted, endReasonStop
		}
	}
	if err := c.revertErr; err != nil {
		c.revertErr = nil
		return err
	}
	return nil
}

// sourceGone ends a row as the reconciler does when its workload is no
// longer instrumented.
func (c *fakeChanges) sourceGone(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.rows[id]
	ch.Desired, ch.EndReason = repo.ChangeReverted, "source_gone"
}

func (c *fakeChanges) row(id string) (repo.InterrogationChange, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.rows[id]
	if !ok {
		return repo.InterrogationChange{}, false
	}
	return *ch, true
}

// fakeStore is the autopilot store's call trees.
type fakeStore struct {
	mu      sync.Mutex
	sources []apstore.SourceKey
	txs     map[string][]apstore.Transaction  // by container
	tries   map[string][]apstore.CallTreeNode // by container/transaction
	// onGet runs before the nth GetCallTree call reads, without the lock.
	onGet func(n int)
	gets  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{txs: map[string][]apstore.Transaction{}, tries: map[string][]apstore.CallTreeNode{}}
}

// add stores a transaction of container with a call graph of edges.
func (s *fakeStore) add(ns, kind, name, container string, tx apstore.Transaction, edges ...[2]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src := apstore.SourceKey{Namespace: ns, WorkloadKind: kind, WorkloadName: name, ContainerName: container}
	if !slices.ContainsFunc(s.sources, func(k apstore.SourceKey) bool {
		return k.ContainerName == container && k.WorkloadName == name && k.Namespace == ns
	}) {
		s.sources = append(s.sources, src)
	}
	key := ns + "/" + kind + "/" + name + "/" + container
	s.txs[key] = append(s.txs[key], tx)
	s.tries[key+"/"+tx.ID] = trie(edges...)
}

// grow adds edges to a stored call graph.
func (s *fakeStore) grow(key string, edges ...[2]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tries[key] = append(s.tries[key], trieWithPrefix(fmt.Sprintf("g%d-", len(s.tries[key])), edges...)...)
}

func (s *fakeStore) ListSourcesWithTransactions(context.Context) ([]apstore.SourceKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sources), nil
}

func (s *fakeStore) ListTransactions(_ context.Context, k apstore.SourceKey) ([]apstore.Transaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.txs[k.Namespace+"/"+k.WorkloadKind+"/"+k.WorkloadName+"/"+k.ContainerName]), nil
}

func (s *fakeStore) GetCallTree(_ context.Context, k apstore.SourceKey, id string) ([]apstore.CallTreeNode, error) {
	s.mu.Lock()
	s.gets++
	n, hook := s.gets, s.onGet
	s.mu.Unlock()
	if hook != nil {
		hook(n)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.tries[k.Namespace+"/"+k.WorkloadKind+"/"+k.WorkloadName+"/"+k.ContainerName+"/"+id]), nil
}

// trie is a call tree whose application call graph is edges.
func trie(edges ...[2]string) []apstore.CallTreeNode {
	return trieWithPrefix("", edges...)
}

func trieWithPrefix(prefix string, edges ...[2]string) []apstore.CallTreeNode {
	var nodes []apstore.CallTreeNode
	for i, e := range edges {
		parent := fmt.Sprintf("%s%d-caller", prefix, i)
		nodes = append(nodes,
			apstore.CallTreeNode{ID: parent, Name: e[0]},
			apstore.CallTreeNode{ID: fmt.Sprintf("%s%d-callee", prefix, i), ParentID: parent, Name: e[1]})
	}
	return nodes
}

// scriptedModel replies in turn with what the test scripted.
type scriptedModel struct {
	clock *testClock
	// onCall runs before call n (0-based) is answered.
	onCall func(n int)

	mu       sync.Mutex
	replies  []modelReply
	requests []string
}

type modelReply struct {
	text string
	err  error
}

func (m *scriptedModel) Name() string { return "scripted" }

func (m *scriptedModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.mu.Lock()
		n := len(m.requests)
		var user string
		if len(req.Contents) > 0 && len(req.Contents[len(req.Contents)-1].Parts) > 0 {
			user = req.Contents[len(req.Contents)-1].Parts[0].Text
		}
		m.requests = append(m.requests, user)
		reply := modelReply{err: errors.New("no reply scripted")}
		if n < len(m.replies) {
			reply = m.replies[n]
		}
		hook := m.onCall
		m.mu.Unlock()
		if hook != nil {
			hook(n)
		}
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		m.clock.Advance(20 * time.Second)
		if reply.err != nil {
			yield(nil, reply.err)
			return
		}
		yield(&model.LLMResponse{
			Content:       genai.NewContentFromText(reply.text, genai.RoleModel),
			FinishReason:  genai.FinishReasonStop,
			UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 8000 + int32(n), CandidatesTokenCount: 400},
			ModelVersion:  "gpt-4o-mini",
			TurnComplete:  true,
		}, nil)
	}
}

func (m *scriptedModel) calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.requests)
}

func replyJSON(t *testing.T, edit func(*Reply)) modelReply {
	t.Helper()
	r := validReply()
	if edit != nil {
		edit(&r)
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return modelReply{text: string(b)}
}

func done(certainty int) func(*Reply) {
	return func(r *Reply) { r.Done, r.Certainty, r.Recommendations = true, certainty, nil }
}

const (
	testNamespace = "claims-demo"
	testKind      = "Deployment"
	testWorkload  = "claims-intake"
	testTrieKey   = testNamespace + "/" + testKind + "/" + testWorkload + "/app/8841"
)

var testEdges = [][2]string{
	{"com.claims.ClaimController.submit", "com.claims.IntakeService.validate"},
	{"com.claims.IntakeService.validate", "com.claims.PolicyRepository.find"},
}

// harness runs one interrogation on the real graph and ADK's in-memory
// session service, sending messages as the supervisor does.
type harness struct {
	t       *testing.T
	clock   *testClock
	loop    *fakeLoop
	store   *fakeStore
	changes *fakeChanges
	model   *scriptedModel
	svc     session.Service
	runner  *runner.Runner
	id      string

	// ctl is the run's control session; resume goes with the next plain
	// message only.
	ctl     Control
	resume  *Resume
	started bool
	// cancel stops the invocation in flight, as the supervisor does on a stop.
	cancel  context.CancelFunc
	errs    []error
	answers int
}

type harnessOption func(*harness, *repo.InterrogationRequest, *Deps)

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	clock := &testClock{t: testStart}
	changes := &fakeChanges{clock: clock, rows: map[string]*repo.InterrogationChange{}, runs: map[string]string{}, reads: map[string]int{}, placeAfter: 2}
	h := &harness{
		t:       t,
		clock:   clock,
		changes: changes,
		loop:    &fakeLoop{Manager: &loop.Manager{}, changes: changes},
		store:   newFakeStore(),
		model:   &scriptedModel{clock: clock},
		svc:     session.InMemoryService(),
		id:      testRunID,
	}
	h.store.add(testNamespace, testKind, testWorkload, "app", apstore.Transaction{ID: "8841", RootSpanName: "POST /claims", RootSpanKind: "server"}, testEdges...)
	req := newScript().st.Request
	dp := Deps{Model: h.model, Loop: h.loop, Store: h.store, Changes: h.changes, MaxModelCalls: 12, Now: clock.Now,
		Control: func(context.Context, string) (Control, error) {
			return Control{Stop: h.ctl.Stop, ExtendedSeconds: h.ctl.ExtendedSeconds}, nil
		},
	}
	for _, opt := range opts {
		opt(h, &req, &dp)
	}
	// As the real insert, from the control session: an extension the run has
	// not applied yet counts.
	changes.expiresAt = func() time.Time {
		st, _ := h.state()
		return st.StartedAt.Add(time.Duration(st.Request.Budget.TimeLimitSeconds+h.ctl.ExtendedSeconds) * time.Second)
	}
	wf, err := BuildWorkflow(dp)
	if err != nil {
		t.Fatal(err)
	}
	h.runner, err = runner.New(runner.Config{AppName: AppName, Agent: wf, SessionService: h.svc})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.Create(context.Background(), &session.CreateRequest{AppName: AppName, UserID: h.id, SessionID: h.id, State: map[string]any{
		"workload": req.Source.Workload(), openKey: true, requestKey: req,
	}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.checkRoutes)
	return h
}

func withRequest(edit func(*repo.InterrogationRequest)) harnessOption {
	return func(_ *harness, req *repo.InterrogationRequest, _ *Deps) { edit(req) }
}

func withMaxModelCalls(n int) harnessOption {
	return func(_ *harness, _ *repo.InterrogationRequest, dp *Deps) { dp.MaxModelCalls = n }
}

func (h *harness) reply(replies ...modelReply) {
	h.model.mu.Lock()
	defer h.model.mu.Unlock()
	h.model.replies = append(h.model.replies, replies...)
}

// send runs one invocation with the control attached, as the supervisor's
// messages carry it.
func (h *harness) send(msg *genai.Content) error {
	ctl := h.ctl
	ctl.Resume, h.resume = h.resume, nil
	// A graph that loops without parking fails the invocation, not the test
	// binary.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h.cancel = cancel
	var last error
	for _, err := range h.runner.Run(ctx, h.id, h.id, msg, agent.RunConfig{}, runner.WithStateDelta(map[string]any{ctlKey: ctl})) {
		if err != nil {
			last = err
		}
	}
	return last
}

func plainMessage(text string) *genai.Content { return genai.NewContentFromText(text, genai.RoleUser) }

func answerMessage(interruptID string) *genai.Content {
	return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: interruptID, Name: workflow.WorkflowInputFunctionCallName, Response: map[string]any{"response": "wake"},
	}}}}
}

// session reads the run's session.
func (h *harness) session() session.Session {
	h.t.Helper()
	got, err := h.svc.Get(context.Background(), &session.GetRequest{AppName: AppName, UserID: h.id, SessionID: h.id})
	if err != nil {
		h.t.Fatal(err)
	}
	return got.Session
}

// state is the run's stored state and whether the run is open.
func (h *harness) state() (*State, bool) {
	sess := h.session()
	st, _, err := load(sess)
	if err != nil {
		h.t.Fatal(err)
	}
	open, _ := sess.State().Get(openKey)
	return st, open == true
}

// step sends the run its next message: the answer to its wait once the
// clock reaches the wait's end, or a plain message. It reports whether the
// run has ended.
func (h *harness) step() bool {
	st, open := h.state()
	if !open {
		return true
	}
	var err error
	switch {
	case st.Wait != nil:
		h.clock.Set(st.Wait.Until)
		err = h.deliver()
	case !h.started:
		h.started = true
		err = h.send(plainMessage("start"))
	default:
		err = h.send(plainMessage("resume"))
	}
	h.failed(err)
	return false
}

// deliver answers the run's wait now, as the supervisor does when the
// control changed.
func (h *harness) deliver() error {
	st, _ := h.state()
	h.answers++
	return h.send(answerMessage(st.Wait.InterruptID))
}

// failed records an invocation's error and resumes the run after it the way
// the supervisor does.
func (h *harness) failed(err error) {
	if err == nil {
		return
	}
	h.errs = append(h.errs, err)
	if errors.Is(err, context.Canceled) {
		return
	}
	var callErr *ModelCallError
	h.resume = &Resume{Error: err.Error(), ModelCall: errors.As(err, &callErr)}
}

// run drives the run until it ends and returns its state.
func (h *harness) run() *State {
	h.t.Helper()
	for range 100 {
		if h.step() {
			st, _ := h.state()
			return st
		}
	}
	h.t.Fatal("run did not end in 100 messages")
	return nil
}

// runUntil drives the run until cond holds for its state.
func (h *harness) runUntil(cond func(*State) bool) *State {
	h.t.Helper()
	for range 100 {
		st, open := h.state()
		if cond(st) {
			return st
		}
		if !open {
			h.t.Fatalf("run ended %s/%s before the condition held", st.Status, st.EndReason)
		}
		h.step()
	}
	h.t.Fatal("condition did not hold in 100 messages")
	return nil
}

// events is the run's API event log.
func (h *harness) events() []Event {
	h.t.Helper()
	var out []Event
	evs := h.session().Events()
	for ev := range evs.All() {
		raw, ok := ev.CustomMetadata[eventsKey]
		if !ok {
			continue
		}
		b, err := json.Marshal(raw)
		if err != nil {
			h.t.Fatal(err)
		}
		var items []Event
		if err := json.Unmarshal(b, &items); err != nil {
			h.t.Fatal(err)
		}
		out = append(out, items...)
	}
	return out
}

func (h *harness) eventsOf(typ string) []Event {
	var out []Event
	for _, e := range h.events() {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// checkRoutes fails the test if a node emitted a route the table does not
// have: ADK would drop the run at that node.
func (h *harness) checkRoutes() {
	checked := 0
	for ev := range h.session().Events().All() {
		if ev.NodeInfo == nil || len(ev.Routes) == 0 {
			continue
		}
		name := nodeOf(ev.NodeInfo.Path)
		for _, route := range ev.Routes {
			checked++
			if _, ok := routes[name][route]; !ok {
				h.t.Errorf("node %s emitted route %q, which routes does not have", name, route)
			}
		}
	}
	if h.started && checked == 0 {
		h.t.Error("no routed event found: the route check reads no node paths")
	}
}

// nodeOf is the node name of an event's node path, e.g. "interrogation@1/gate@2".
func nodeOf(path string) string {
	name := path[strings.LastIndex(path, "/")+1:]
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	return name
}

func (h *harness) checkEnded(st *State, status Status, reason EndReason) {
	h.t.Helper()
	if st.Status != status || st.EndReason != reason {
		h.t.Fatalf("run ended %s/%s (%s), want %s/%s; errors %v", st.Status, st.EndReason, st.Error, status, reason, h.errs)
	}
	evs := h.events()
	if last := evs[len(evs)-1]; last.Type != EventEnded {
		h.t.Errorf("last event %s, want %s", last.Type, EventEnded)
	}
	if st.Node != "" || st.Wait != nil {
		h.t.Errorf("ended run in node %q, wait %+v", st.Node, st.Wait)
	}
}

func TestRoutes_MatchGraph(t *testing.T) {
	names := map[string]bool{}
	for from, to := range routes {
		names[from] = true
		for _, next := range to {
			names[next] = true
		}
	}
	nodes := map[string]workflow.Node{}
	for name := range names {
		nodes[name] = workflow.NewFunctionNode(name, func(agent.Context, any) (any, error) { return nil, nil }, workflow.NodeConfig{})
	}
	edges, err := buildEdges(nodes)
	if err != nil {
		t.Fatal(err)
	}
	type edge struct{ from, route, to string }
	built := map[edge]bool{}
	for _, e := range edges {
		route := always
		switch r := e.Route.(type) {
		case nil:
		case workflow.StringRoute:
			route = string(r)
		default:
			if e.Route == workflow.Default {
				route = otherwise
			} else {
				t.Fatalf("edge %s -> %s has route %T", e.From.Name(), e.To.Name(), e.Route)
			}
		}
		built[edge{e.From.Name(), route, e.To.Name()}] = true
	}
	want := map[edge]bool{{workflow.Start.Name(), always, nodeEntry}: true}
	for from, to := range routes {
		for route, next := range to {
			want[edge{from, route, next}] = true
		}
	}
	if !maps.Equal(built, want) {
		t.Fatalf("edges %v, want %v", built, want)
	}
	if _, err := buildEdges(map[string]workflow.Node{nodeEntry: nodes[nodeEntry]}); err == nil {
		t.Fatal("edges built for a route to a missing node")
	}
}

// TestGraph_NodeNames fails when a node or step is renamed or removed without
// a GraphVersion bump: sessions store them.
func TestGraph_NodeNames(t *testing.T) {
	type graph struct {
		Version int      `json:"graph_version"`
		Nodes   []string `json:"nodes"`
		Steps   []string `json:"steps"`
	}
	names := map[string]bool{}
	steps := map[string]bool{}
	for from, to := range routes {
		names[from] = true
		for _, next := range to {
			names[next] = true
		}
		if from == nodeEntry {
			for step := range to {
				steps[step] = true
			}
		}
	}
	got := graph{Version: GraphVersion, Nodes: slices.Sorted(maps.Keys(names)), Steps: slices.Sorted(maps.Keys(steps))}

	path := filepath.Join("testdata", "graph.json")
	var stored graph
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &stored); err != nil {
			t.Fatal(err)
		}
		if stored.Version == GraphVersion && (!slices.Equal(stored.Nodes, got.Nodes) || !slices.Equal(stored.Steps, got.Steps)) {
			t.Fatalf("nodes %v and steps %v differ from graph version %d's %v and %v: bump GraphVersion", got.Nodes, got.Steps, GraphVersion, stored.Nodes, stored.Steps)
		}
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "graph.json", append(b, '\n'))
}

// A run of two rounds: the first adds a probe and observes, the second ends
// on the model's done.
func TestRun_TwoRounds(t *testing.T) {
	h := newHarness(t)
	h.reply(replyJSON(t, nil), replyJSON(t, done(80)))
	// The new probe's frames show up in the call tree during the observe window.
	h.store.onGet = func(n int) {
		if n == 2 {
			h.store.grow(testTrieKey, [2]string{"com.claims.IntakeService.validate", "com.claims.RulesEngine.check"})
		}
	}
	st := h.run()
	h.checkEnded(st, StatusCompleted, EndDone)

	if len(st.Rounds) != 2 || st.Round != 2 || st.ModelCalls != 2 {
		t.Fatalf("rounds %d, round %d, model calls %d; want 2, 2, 2", len(st.Rounds), st.Round, st.ModelCalls)
	}
	if st.InputTokens != 16001 || st.OutputTokens != 800 || st.Model != "gpt-4o-mini" {
		t.Errorf("usage %d/%d %q, want the two calls' usage and the served model", st.InputTokens, st.OutputTokens, st.Model)
	}
	if p := st.Probes; len(p) != 1 || p[0].Status != ProbeApplied || p[0].ExpiresAt != st.DeadlineAt {
		t.Errorf("probes %+v, want one applied until the deadline", p)
	}
	if st.Answer == nil || !st.Answer.Final || st.Answer.Certainty != 80 {
		t.Errorf("answer %+v, want the final answer of round 2", st.Answer)
	}
	if after := st.Rounds[0].ObservedAfter; after == nil || after.NewEdges != 1 || after.Edges != 3 {
		t.Errorf("round 1 observed after %+v, want the new edge", after)
	}
	// Round 2's request says the graph changed and names the applied probe.
	var second Request
	if err := json.Unmarshal([]byte(h.model.calls()[1]), &second); err != nil {
		t.Fatal(err)
	}
	if !second.Transactions[0].EvidenceChanged || len(second.AddedProbes) != 1 || second.AddedProbes[0].State != string(ProbeApplied) || second.Round != 2 {
		t.Errorf("round 2 request %+v, want changed evidence and the applied probe", second)
	}

	evs := h.events()
	for i, e := range evs {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(b, &item); err != nil {
			t.Fatal(err)
		}
		item["sequence"] = item["seq"]
		delete(item, "seq")
		wire, _ := json.Marshal(item)
		decodeStrict(t, e.Type+" event", wire, &apitypes.InterrogationEvent{})
		decodeStrict(t, e.Type+" payload", item["payload"], payloadTypes[e.Type]())
	}
	for what, v := range map[string]any{"rounds": st.Rounds, "answer": st.Answer} {
		b, _ := json.Marshal(v)
		switch what {
		case "rounds":
			decodeStrict(t, what, b, &[]apitypes.InterrogationRound{})
		case "answer":
			decodeStrict(t, what, b, &apitypes.InterrogationAnswer{})
		}
	}

	// The API's mapping of a recorded run (apimap) reads these events.
	lines := make([]string, len(evs))
	for i, e := range evs {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		lines[i] = "  " + string(b)
	}
	golden(t, "run_two_rounds.json", []byte("{\"events\": [\n"+strings.Join(lines, ",\n")+"\n]}\n"))
}

// probeTimings is n spans of the probe in transaction 8841, each in a request
// of 3 spans.
func probeTimings(n int) measure.Timings {
	var site, entry measure.Measure
	for range n {
		site.Add(measure.Sample{DurUs: 4000, SelfUs: 3000})
		entry.Add(measure.Sample{DurUs: 10000, SelfUs: 2000, Spans: 3})
	}
	return measure.Timings{"8841": {Entry: entry, Sites: map[string]*measure.Measure{"com.claims.IntakeService.validate": &site}}}
}

// observe ends once the probe has observeSpans spans, and after observeTimeout
// with what there is; either way the next round is sent what was measured.
func TestRun_ObserveMeasures(t *testing.T) {
	tests := []struct {
		name    string
		spans   int
		wait    time.Duration
		outcome string
	}{
		{name: "enough spans", spans: observeSpans, wait: observeMin, outcome: WaitSatisfied},
		{name: "too few", spans: 5, wait: observeTimeout, outcome: WaitTimedOut},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.loop.timings = probeTimings(tt.spans)
			h.reply(replyJSON(t, nil), replyJSON(t, done(70)))
			st := h.runUntil(parkedIn("wait_observe"))
			started := st.ObserveStarted
			st = h.run()
			h.checkEnded(st, StatusCompleted, EndDone)
			w := h.eventsOf(EventWaitFinished)
			last := w[len(w)-1]
			if last.Payload["reason"] != string(WaitObserve) || last.Payload["outcome"] != tt.outcome || !last.Time.Equal(started.Add(tt.wait)) {
				t.Errorf("observe wait finished %+v at %v, want %s after %v", last.Payload, last.Time, tt.outcome, tt.wait)
			}
			if m := h.eventsOf(EventChangeMeasured); len(m) != 1 || m[0].ChangeID != probeID || m[0].Payload["spans"] != float64(tt.spans) {
				t.Errorf("change.measured %+v, want one with %d spans", m, tt.spans)
			}
			if got := st.Probes[0].Measured; got == nil || got.Spans != int64(tt.spans) || got.AvgMs != 4 || got.SelfAvgMs != 3 {
				t.Errorf("measured %+v, want %d spans averaging 4 ms, 3 ms of it self", got, tt.spans)
			}
			if got := st.Rounds[0].Timings; len(got) != 1 || got[0].Requests != int64(tt.spans) || got[0].UncoveredAvgMs != 2 || got[0].AvgSpans != 3 {
				t.Errorf("round timings %+v, want %d requests with 2 ms uncovered", got, tt.spans)
			}
			var second Request
			if err := json.Unmarshal([]byte(h.model.calls()[1]), &second); err != nil {
				t.Fatal(err)
			}
			if m := second.AddedProbes[0].Measured; m == nil || m.Spans != int64(tt.spans) {
				t.Errorf("round 2 added probes %+v, want the probe measured", second.AddedProbes)
			}
			if tm := second.Transactions[0].Timings; tm == nil || len(tm.Probes) != 1 || tm.Probes[0].Spans != int64(tt.spans) {
				t.Errorf("round 2 transactions %+v, want timings with the probe", second.Transactions)
			}
		})
	}
}

// A probe whose source goes while the run observes is reported reverted in
// the next round's request.
func TestRun_RevertedBetweenRounds(t *testing.T) {
	h := newHarness(t)
	h.reply(replyJSON(t, nil), replyJSON(t, done(70)))
	h.runUntil(parkedIn("wait_observe"))
	h.changes.sourceGone(probeID)
	st := h.run()
	h.checkEnded(st, StatusCompleted, EndDone)
	if p := st.Probes[0]; p.Status != ProbeReverted || p.EndReason != "source_gone" {
		t.Fatalf("probe %s/%s, want reverted as its source is gone", p.Status, p.EndReason)
	}
	var second Request
	if err := json.Unmarshal([]byte(h.model.calls()[1]), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.AddedProbes) != 1 || second.AddedProbes[0].State != string(ProbeReverted) {
		t.Errorf("round 2 added probes %+v, want the probe reverted", second.AddedProbes)
	}
	if r := h.eventsOf(EventChangeReverted); len(r) != 1 || r[0].Round != 2 {
		t.Errorf("change.reverted %+v, want one in round 2", r)
	}
}

// A run that a binary with another graph left is not continued.
func TestRun_GraphVersionChanged(t *testing.T) {
	h := newHarness(t)
	st := h.runUntil(parkedIn("wait_baseline"))
	st.Graph = GraphVersion + 1
	if err := runnerAppend(context.Background(), h.svc, h.id, map[string]any{runKey: *st}); err != nil {
		t.Fatal(err)
	}
	h.failed(h.send(plainMessage("resume")))
	st = h.run()
	h.checkEnded(st, StatusFailed, EndInterrupted)
	if !strings.Contains(st.Error, "graph version") || len(h.model.calls()) != 0 {
		t.Fatalf("error %q after %d model calls, want the graph version named and no call", st.Error, len(h.model.calls()))
	}
}

func withoutEvidence() harnessOption {
	return func(h *harness, _ *repo.InterrogationRequest, _ *Deps) {
		h.store.mu.Lock()
		defer h.store.mu.Unlock()
		h.store.sources = nil
	}
}

// withGateway puts the model behind the gateway wrapper and the loop's pause.
func withGateway() harnessOption {
	return func(h *harness, _ *repo.InterrogationRequest, dp *Deps) {
		dp.Model = &gatewayModel{LLM: h.model, pause: h.loop, now: h.clock.Now}
	}
}

func withLimit(seconds int) harnessOption {
	return withRequest(func(r *repo.InterrogationRequest) { r.Budget.TimeLimitSeconds = seconds })
}

// parkedIn is a condition: the run waits in node.
func parkedIn(node string) func(*State) bool {
	return func(st *State) bool { return st.Wait != nil && st.Wait.Node == node }
}

var probeID = loop.ChangeID(testRunID, apschema.LangJava, "com.claims.IntakeService.validate")

// recommend replies with a probe on function of class.
func recommend(class, method string) func(*Reply) {
	return func(r *Reply) {
		r.Recommendations[0].Function = class + "." + method
		r.Recommendations[0].Probe = apschema.Probe{ClassName: class, MethodName: method}
	}
}

func TestRun_SourceGone(t *testing.T) {
	tests := []struct {
		name string
		// goneFrom is the Instrumented call from which the workload is gone:
		// prepare makes the first, each gather one more.
		goneFrom   int
		wantCalls  int
		wantRounds int
	}{
		{name: "at prepare", goneFrom: 1, wantCalls: 0, wantRounds: 0},
		{name: "before the first reading", goneFrom: 2, wantCalls: 0, wantRounds: 1},
		{name: "in round 2", goneFrom: 3, wantCalls: 1, wantRounds: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.loop.goneFrom = tt.goneFrom
			h.reply(replyJSON(t, nil))
			st := h.run()
			h.checkEnded(st, StatusFailed, EndSourceGone)
			if len(h.model.calls()) != tt.wantCalls || len(st.Rounds) != tt.wantRounds {
				t.Fatalf("%d model calls, %d rounds; want %d, %d", len(h.model.calls()), len(st.Rounds), tt.wantCalls, tt.wantRounds)
			}
		})
	}
}

func TestRun_NoEvidence(t *testing.T) {
	h := newHarness(t, withoutEvidence())
	st := h.run()
	h.checkEnded(st, StatusCompleted, EndNoEvidence)
	if n := len(h.model.calls()); n != 0 {
		t.Fatalf("model called %d times without evidence", n)
	}
	if !st.EndedAt.Equal(testStart.Add(baselineWait)) {
		t.Errorf("ended at %v, want after the baseline wait", st.EndedAt)
	}
	if e := h.eventsOf(EventEvidence); len(e) != 1 || e[0].Payload["transactions"] != 0.0 {
		t.Fatalf("evidence events %+v, want one reading of nothing", e)
	}
}

// A quota error pauses every model call, the loop's included; the run parks
// until the pause ends, or fails at its deadline.
func TestRun_GlobalPauseParks(t *testing.T) {
	quota := &apmodel.HTTPError{StatusCode: 429, Body: "rate limited"}
	tests := []struct {
		name       string
		retryAfter time.Duration
		status     Status
		reason     EndReason
	}{
		{name: "resumes after the pause", retryAfter: 10 * time.Minute, status: StatusCompleted, reason: EndDone},
		{name: "pause outlasts the deadline", retryAfter: 2 * time.Hour, status: StatusFailed, reason: EndModelUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, withGateway())
			err := *quota
			err.RetryAfter = tt.retryAfter
			h.reply(modelReply{err: &err}, replyJSON(t, done(70)))

			for len(h.errs) == 0 {
				h.step()
			}
			failedAt := h.clock.Now()
			if until := h.loop.ModelPausedUntil(failedAt); !until.Equal(failedAt.Add(tt.retryAfter)) {
				t.Fatalf("loop paused until %v, want %v", until, failedAt.Add(tt.retryAfter))
			}
			var callErr *ModelCallError
			if !errors.As(h.errs[0], &callErr) || !callErr.Paused {
				t.Fatalf("invocation error %v, want a paused model call", h.errs[0])
			}
			st := h.runUntil(parkedIn("wait_model"))
			if want := minTime(failedAt.Add(tt.retryAfter), st.DeadlineAt); !st.Wait.Until.Equal(want) {
				t.Fatalf("parked until %v, want %v", st.Wait.Until, want)
			}
			st = h.run()
			h.checkEnded(st, tt.status, tt.reason)
			if tt.reason == EndModelUnavailable && !strings.Contains(st.Error, "paused until") {
				t.Errorf("error %q, want the pause named", st.Error)
			}
			if w := h.eventsOf(EventWaitStarted); !slices.ContainsFunc(w, func(e Event) bool { return e.Payload["reason"] == string(WaitModelPaused) }) {
				t.Errorf("no model_paused wait in %+v", w)
			}
		})
	}
}

func TestRun_ParseRetry(t *testing.T) {
	bad := map[string]modelReply{
		"prose":  {text: "The intake is slow because of validation."},
		"fenced": {text: "```json\n" + replyJSON(t, nil).text + "\n```"},
	}
	for name, reply := range bad {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.reply(reply, reply, reply)
			st := h.run()
			h.checkEnded(st, StatusFailed, EndError)
			if !strings.HasPrefix(st.Error, "model reply unusable: ") || st.ModelCalls != maxReplyAttempts {
				t.Fatalf("error %q after %d calls, want unusable after %d", st.Error, st.ModelCalls, maxReplyAttempts)
			}
			called := h.eventsOf(EventModelCalled)
			for i, e := range called {
				if e.Payload["outcome"] != OutcomeInvalidReply || e.Payload["attempt"] != float64(i+1) {
					t.Errorf("model.called %d: %v", i, e.Payload)
				}
			}
			if len(called) != maxReplyAttempts {
				t.Errorf("%d model.called events, want %d", len(called), maxReplyAttempts)
			}
		})
	}
	t.Run("usable on the last attempt", func(t *testing.T) {
		h := newHarness(t)
		h.reply(bad["prose"], bad["fenced"], replyJSON(t, done(60)))
		st := h.run()
		h.checkEnded(st, StatusCompleted, EndDone)
		if st.ParseFailures != 0 || st.ModelCalls != 3 || len(h.eventsOf(EventModelCalled)) != 3 {
			t.Fatalf("parse failures %d, model calls %d", st.ParseFailures, st.ModelCalls)
		}
	})
}

func TestRun_AllDenied(t *testing.T) {
	target := "com.claims.IntakeService.validate"
	tests := []struct {
		name       string
		deny       loop.DenyReason
		noOptIn    bool
		wantReason EndReason
		wantDeny   loop.DenyReason
		wantAsked  int
	}{
		{name: "not in the call graph", deny: loop.DenyNotInCallGraph, wantReason: EndChangesDenied, wantDeny: loop.DenyNotInCallGraph, wantAsked: 1},
		{name: "already probed", deny: loop.DenyAlreadyProbed, wantReason: EndChangesDenied, wantDeny: loop.DenyAlreadyProbed, wantAsked: 1},
		{name: "run budget", deny: loop.DenyRunBudget, wantReason: EndProbeBudget, wantDeny: loop.DenyRunBudget, wantAsked: 1},
		{name: "no opt-in", noOptIn: true, wantReason: EndDone, wantDeny: loop.DenyChangesNotAllowed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, withRequest(func(r *repo.InterrogationRequest) {
				if tt.noOptIn {
					r.AllowChanges = nil
				}
			}))
			if tt.deny != "" {
				h.loop.deny = map[string]*loop.ProbeDeniedError{target: {Reason: tt.deny, Detail: "detail"}}
			}
			h.reply(replyJSON(t, nil))
			st := h.run()
			h.checkEnded(st, StatusCompleted, tt.wantReason)
			if n := len(h.loop.probeRequests()); n != tt.wantAsked {
				t.Fatalf("RequestProbe called %d times, want %d", n, tt.wantAsked)
			}
			// A denied proposal exists only in state, under its change id.
			if len(st.Probes) != 1 || st.Probes[0].ChangeID != probeID || st.Probes[0].Status != ProbeDenied || st.Probes[0].DenyReason != string(tt.wantDeny) {
				t.Fatalf("probes %+v, want %s denied for %s", st.Probes, probeID, tt.wantDeny)
			}
			if _, ok := h.changes.row(probeID); ok {
				t.Fatal("a denied proposal has a row")
			}
			if d := h.eventsOf(EventChangeDenied); len(d) != 1 || d[0].ChangeID != probeID || d[0].Payload["deny_reason"] != string(tt.wantDeny) {
				t.Fatalf("change.denied %+v", d)
			}
			if r := st.Rounds[0]; !slices.Equal(r.Proposed, []string{probeID}) || len(r.Applied) != 0 {
				t.Fatalf("round %+v, want the proposal listed and nothing applied", r)
			}
		})
	}
}

// A model that is not done but asks only for a probe the run already added
// ends the run: the repeat is denied, not requested again.
func TestRun_RepeatedProbeDenied(t *testing.T) {
	h := newHarness(t)
	h.reply(replyJSON(t, nil), replyJSON(t, nil))
	st := h.run()
	h.checkEnded(st, StatusCompleted, EndChangesDenied)
	if n := len(h.loop.probeRequests()); n != 1 {
		t.Fatalf("RequestProbe called %d times, want once", n)
	}
	if len(st.Probes) != 1 || st.Probes[0].Status != ProbeApplied {
		t.Fatalf("probes %+v, want the one applied probe", st.Probes)
	}
	if r := st.Rounds[1]; r.Reply == nil || r.Reply.Done || len(r.Proposed) != 0 {
		t.Fatalf("round 2 %+v, want a reply that is not done and no new proposal", r)
	}
	d := h.eventsOf(EventChangeDenied)
	if len(d) != 1 || d[0].ChangeID != probeID || d[0].Round != 2 || d[0].Payload["deny_reason"] != string(loop.DenyAlreadyRequested) {
		t.Fatalf("change.denied %+v, want the repeat in round 2", d)
	}
}

func TestRun_ProbeNotApplied(t *testing.T) {
	tests := []struct {
		name string
		// edit sets up the reconciler, or acts once the run waits for it.
		setup      func(h *harness)
		whileWait  func(h *harness)
		wantStatus ProbeStatus
		wantReason string
		wantReads  int
		wantEnded  []string
	}{
		{
			name:       "not in the rule in time",
			setup:      func(h *harness) { h.changes.placeAfter = 0 },
			wantStatus: ProbeFailed, wantReason: repo.ChangeNotAppliedInTime,
			wantReads: int(applyTimeout / applyPoll), wantEnded: []string{probeID + ":" + repo.ChangeNotAppliedInTime},
		},
		{
			name:       "blocked",
			setup:      func(h *harness) { h.changes.placeAfter, h.changes.block = 0, loop.BlockRuleDisabled },
			wantStatus: ProbeFailed, wantReason: loop.BlockRuleDisabled,
			wantReads: 1, wantEnded: []string{probeID + ":" + loop.BlockRuleDisabled},
		},
		{
			name:       "source gone",
			setup:      func(h *harness) { h.changes.placeAfter = 0 },
			whileWait:  func(h *harness) { h.changes.sourceGone(probeID) },
			wantStatus: ProbeReverted, wantReason: "source_gone",
			wantReads: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			tt.setup(h)
			h.reply(replyJSON(t, nil), replyJSON(t, done(60)))
			if tt.whileWait != nil {
				h.runUntil(parkedIn("wait_apply"))
				tt.whileWait(h)
			}
			st := h.run()
			h.checkEnded(st, StatusCompleted, EndDone)
			p := st.Probes[0]
			if p.Status != tt.wantStatus || p.EndReason != tt.wantReason {
				t.Fatalf("probe %s/%s, want %s/%s", p.Status, p.EndReason, tt.wantStatus, tt.wantReason)
			}
			if reads := h.changes.reads[probeID]; reads != tt.wantReads {
				t.Errorf("rule polled %d times, want %d", reads, tt.wantReads)
			}
			if !slices.Equal(h.changes.ended, tt.wantEnded) {
				t.Errorf("rows ended %v, want %v", h.changes.ended, tt.wantEnded)
			}
			if r := st.Rounds[0]; r.Outcome != RoundNoChangeApplied || len(st.Rounds) != 2 {
				t.Errorf("round 1 %s, %d rounds; want the next round after no change applied", r.Outcome, len(st.Rounds))
			}
		})
	}
}

func TestStop_Workflow(t *testing.T) {
	stopAt := testStart.Add(2 * time.Minute)
	tests := []struct {
		name    string
		drive   func(t *testing.T, h *harness) *State
		reverts int
		// probe is the probe's status at the end, "" for no probe.
		probe ProbeStatus
	}{
		{
			name: "parked, revert",
			drive: func(t *testing.T, h *harness) *State {
				h.runUntil(parkedIn("wait_observe"))
				h.ctl.Stop = &Stop{Changes: ChangesRevert, Actor: "bob", At: stopAt}
				h.failed(h.deliver())
				st, open := h.state()
				if open {
					t.Fatal("run still open after the stop was delivered at its wait")
				}
				if w := h.eventsOf(EventWaitFinished); w[len(w)-1].Payload["outcome"] != WaitStopped {
					t.Errorf("last wait.finished %v, want stopped", w[len(w)-1].Payload)
				}
				return st
			},
			reverts: 1, probe: ProbeReverted,
		},
		{
			name: "parked, by a plain message",
			drive: func(t *testing.T, h *harness) *State {
				h.runUntil(parkedIn("wait_observe"))
				h.ctl.Stop = &Stop{Changes: ChangesRevert, Actor: "bob", At: stopAt}
				h.failed(h.send(plainMessage("resume")))
				st, open := h.state()
				if open {
					t.Fatal("run still open after the stop reached it")
				}
				if w := h.eventsOf(EventWaitFinished); w[len(w)-1].Payload["reason"] != string(WaitObserve) || w[len(w)-1].Payload["outcome"] != WaitStopped {
					t.Errorf("last wait.finished %v, want the observe wait stopped", w[len(w)-1].Payload)
				}
				for _, n := range st.Rounds[0].Nodes {
					if n.Name == "wait_observe" && n.State != NodeDone {
						t.Errorf("wait_observe %s, want done", n.State)
					}
				}
				return st
			},
			reverts: 1, probe: ProbeReverted,
		},
		{
			name: "mid-step, at entry",
			drive: func(t *testing.T, h *harness) *State {
				h.model.onCall = func(n int) {
					if n == 0 {
						h.ctl.Stop = &Stop{Changes: ChangesKeep, Actor: "bob", At: stopAt}
						h.cancel()
					}
				}
				st := h.run()
				if len(h.errs) != 1 || !errors.Is(h.errs[0], context.Canceled) {
					t.Fatalf("errors %v, want the cancelled step", h.errs)
				}
				return st
			},
		},
		{
			name: "twice, conclude repeated",
			drive: func(t *testing.T, h *harness) *State {
				h.runUntil(parkedIn("wait_observe"))
				h.ctl.Stop = &Stop{Changes: ChangesRevert, Actor: "bob", At: stopAt}
				// The revert is done and then the step fails: the next
				// invocation gets the same stop and repeats conclude.
				h.changes.revertErr = errors.New("connection reset")
				return h.run()
			},
			reverts: 2, probe: ProbeReverted,
		},
		{
			name: "keep, then a revert after the end",
			drive: func(t *testing.T, h *harness) *State {
				h.runUntil(parkedIn("wait_observe"))
				h.ctl.Stop = &Stop{Changes: ChangesKeep, Actor: "bob", At: stopAt}
				st := h.run()
				// A message reaching the closed run changes nothing.
				n := len(h.events())
				h.ctl.Stop = &Stop{Changes: ChangesRevert, Actor: "carol", At: stopAt.Add(time.Minute)}
				if err := h.send(plainMessage("resume")); err != nil {
					t.Fatal(err)
				}
				after, open := h.state()
				if open || len(h.events()) != n || after.Stop.Changes != ChangesKeep || after.Status != StatusStopped {
					t.Fatalf("closed run changed: open %v, %d events (was %d), stop %+v", open, len(h.events()), n, after.Stop)
				}
				return st
			},
			probe: ProbeApplied,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			h.reply(replyJSON(t, nil), replyJSON(t, done(60)))
			st := tt.drive(t, h)
			h.checkEnded(st, StatusStopped, EndStoppedByUser)
			if n := len(h.eventsOf(EventStopRequested)); n != 1 {
				t.Errorf("%d stop_requested events, want 1", n)
			}
			if h.changes.reverts != tt.reverts {
				t.Errorf("RevertInterrogationProbes called %d times, want %d", h.changes.reverts, tt.reverts)
			}
			if tt.probe == "" {
				if len(st.Probes) != 0 {
					t.Fatalf("probes %+v, want none", st.Probes)
				}
				return
			}
			if p := st.Probes[0]; p.Status != tt.probe {
				t.Fatalf("probe %s, want %s", p.Status, tt.probe)
			}
			reverted := h.eventsOf(EventChangeReverted)
			if tt.probe == ProbeReverted {
				if len(reverted) != 1 || reverted[0].Payload["end_reason"] != endReasonStop || reverted[0].Payload["by"] != "bob" {
					t.Fatalf("change.reverted %+v, want one by the stop", reverted)
				}
				if row, _ := h.changes.row(probeID); row.Desired != repo.ChangeReverted {
					t.Fatalf("row %s, want reverted", row.Desired)
				}
			} else if len(reverted) != 0 {
				t.Fatalf("change.reverted %+v with keep", reverted)
			}
		})
	}
}

// cancelSecond cancels the invocation in its second RequestProbe, as the
// supervisor does when a stop is recorded, after the first stored a row.
type cancelSecond struct {
	*fakeLoop
	cancel func()
	n      int
}

func (l *cancelSecond) RequestProbe(ctx context.Context, req loop.ProbeRequest) (apstore.RequestedProbe, error) {
	if l.n++; l.n == 2 {
		l.cancel()
		<-ctx.Done()
		return apstore.RequestedProbe{}, ctx.Err()
	}
	return l.fakeLoop.RequestProbe(ctx, req)
}

// A stop that cancels apply after one of its probes got a row ends the run
// with that probe recorded as requested, and reverted when the stop asks.
func TestStop_DuringApply(t *testing.T) {
	for _, changes := range []string{ChangesKeep, ChangesRevert} {
		t.Run(changes, func(t *testing.T) {
			h := newHarness(t, func(h *harness, _ *repo.InterrogationRequest, dp *Deps) {
				dp.Loop = &cancelSecond{fakeLoop: h.loop, cancel: func() { h.cancel() }}
			})
			h.reply(replyJSON(t, func(r *Reply) {
				second := r.Recommendations[0]
				recommend("com.claims.PolicyRepository", "find")(r)
				r.Recommendations = append(r.Recommendations, second)
			}))
			for len(h.errs) == 0 {
				if h.step() {
					t.Fatal("run ended before apply was cancelled")
				}
			}
			h.ctl.Stop = &Stop{Changes: changes, Actor: "bob", At: h.clock.Now()}
			st := h.run()
			h.checkEnded(st, StatusStopped, EndStoppedByUser)
			want, event := ProbeRequested, EventChangeRequested
			if changes == ChangesRevert {
				want, event = ProbeReverted, EventChangeReverted
			}
			p := st.Probes[0]
			if _, ok := h.changes.row(p.ChangeID); !ok || p.Status != want || p.ExpiresAt.IsZero() {
				t.Fatalf("probe with a row %s %s, expires %v; want %s", p.Target, p.Status, p.ExpiresAt, want)
			}
			if n := len(h.eventsOf(event)); n != 1 {
				t.Fatalf("%d %s events, want 1", n, event)
			}
		})
	}
}

// An extension delivered twice moves the deadline once, and a wait the old
// deadline cut short parks again until its own end.
func TestExtend_Idempotent(t *testing.T) {
	// The observe wait starts 60 s in and would end 80 s in.
	h := newHarness(t, withLimit(70))
	h.reply(replyJSON(t, nil), replyJSON(t, done(60)))
	st := h.runUntil(parkedIn("wait_observe"))
	if !st.Wait.Until.Equal(st.DeadlineAt) {
		t.Fatalf("parked until %v, want the deadline %v", st.Wait.Until, st.DeadlineAt)
	}
	first, since := st.Wait.InterruptID, st.Wait.Since
	h.ctl.ExtendedSeconds = 600
	h.clock.Advance(10 * time.Second)
	for range 2 {
		h.failed(h.deliver())
	}
	st, _ = h.state()
	if want := testStart.Add(670 * time.Second); !st.DeadlineAt.Equal(want) {
		t.Fatalf("deadline %v, want %v", st.DeadlineAt, want)
	}
	if st.Wait == nil || st.Wait.InterruptID == first || !st.Wait.Until.Equal(st.WakeAt) || !st.Wait.Since.Equal(since) {
		t.Fatalf("wait %+v, want parked again since %v until the window ends (%v)", st.Wait, since, st.WakeAt)
	}
	ext := h.eventsOf(EventExtended)
	if len(ext) != 1 || ext[0].Payload["seconds"] != 600.0 {
		t.Fatalf("extended events %+v, want one of 600 s", ext)
	}
	st = h.run()
	h.checkEnded(st, StatusCompleted, EndDone)
}

// An extension recorded while a model call runs past the old deadline reaches
// the run before it counts a probe late or ends on the time limit.
func TestExtend_DuringModelCall(t *testing.T) {
	tests := []struct {
		name string
		// reply is the reply to the call the extension lands in.
		reply func(t *testing.T) modelReply
	}{
		{name: "confirm", reply: func(t *testing.T) modelReply { return replyJSON(t, recommend("com.claims.PolicyRepository", "find")) }},
		{name: "gate", reply: func(*testing.T) modelReply { return modelReply{text: "not json"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, withLimit(300))
			h.reply(replyJSON(t, nil), tt.reply(t), replyJSON(t, done(60)))
			st := h.runUntil(parkedIn("wait_observe"))
			deadline := st.DeadlineAt
			h.model.onCall = func(n int) {
				if n == 1 {
					h.ctl.ExtendedSeconds = 1800
				}
			}
			h.clock.Set(deadline.Add(-10 * time.Second))
			st = h.run()
			h.checkEnded(st, StatusCompleted, EndDone)
			if want := deadline.Add(1800 * time.Second); !st.DeadlineAt.Equal(want) {
				t.Fatalf("deadline %v, want %v", st.DeadlineAt, want)
			}
			if n := len(h.eventsOf(EventExtended)); n != 1 {
				t.Fatalf("%d extended events, want 1", n)
			}
			for _, p := range st.Probes {
				if p.Status == ProbeFailed {
					t.Fatalf("probe %s failed: %s", p.Target, p.EndReason)
				}
			}
		})
	}
}

func TestRun_TimeLimitWhileParked(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		node  string
		calls int
	}{
		{name: "baseline", limit: 20, node: "wait_baseline", calls: 0},
		{name: "observe", limit: 70, node: "wait_observe", calls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, withLimit(tt.limit))
			h.reply(replyJSON(t, nil))
			st := h.runUntil(parkedIn(tt.node))
			if !st.Wait.Until.Equal(st.DeadlineAt) {
				t.Fatalf("parked in %s until %v, want the deadline %v", tt.node, st.Wait.Until, st.DeadlineAt)
			}
			st = h.run()
			h.checkEnded(st, StatusCompleted, EndTimeLimit)
			if !st.EndedAt.Equal(st.DeadlineAt) || len(h.model.calls()) != tt.calls {
				t.Fatalf("ended %v (deadline %v) after %d calls, want at the deadline after %d", st.EndedAt, st.DeadlineAt, len(h.model.calls()), tt.calls)
			}
		})
	}
}

func TestRun_Limits(t *testing.T) {
	invalid := modelReply{text: "not json"}
	tests := []struct {
		name     string
		opts     []harnessOption
		replies  func(t *testing.T) []modelReply
		reason   EndReason
		calls    int
		requests int
		// final is what each call's request says of the round.
		final []bool
	}{
		{
			name:    "one round",
			opts:    []harnessOption{withRequest(func(r *repo.InterrogationRequest) { r.Budget.MaxRounds = 1 })},
			replies: func(t *testing.T) []modelReply { return []modelReply{replyJSON(t, nil)} },
			reason:  EndMaxRounds, calls: 1, final: []bool{true},
		},
		{
			name: "max rounds",
			opts: []harnessOption{withRequest(func(r *repo.InterrogationRequest) { r.Budget.MaxRounds = 2 })},
			replies: func(t *testing.T) []modelReply {
				return []modelReply{replyJSON(t, nil), replyJSON(t, recommend("com.claims.PolicyRepository", "find"))}
			},
			reason: EndMaxRounds, calls: 2, requests: 1, final: []bool{false, true},
		},
		{
			name: "probe budget",
			opts: []harnessOption{withRequest(func(r *repo.InterrogationRequest) { r.Budget.MaxProbes = 1 })},
			replies: func(t *testing.T) []modelReply {
				return []modelReply{replyJSON(t, nil), replyJSON(t, recommend("com.claims.PolicyRepository", "find"))}
			},
			reason: EndProbeBudget, calls: 2, requests: 1, final: []bool{false, true},
		},
		{
			name:    "model calls, final round",
			opts:    []harnessOption{withMaxModelCalls(1)},
			replies: func(t *testing.T) []modelReply { return []modelReply{replyJSON(t, nil)} },
			reason:  EndMaxModelCalls, calls: 1, final: []bool{true},
		},
		{
			name:    "model calls, at the gate",
			opts:    []harnessOption{withMaxModelCalls(2)},
			replies: func(*testing.T) []modelReply { return []modelReply{invalid, invalid} },
			// The retry is the run's last call.
			reason: EndMaxModelCalls, calls: 2, final: []bool{false, true},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.opts...)
			h.reply(tt.replies(t)...)
			st := h.run()
			h.checkEnded(st, StatusCompleted, tt.reason)
			calls := h.model.calls()
			if len(calls) != tt.calls || len(h.loop.probeRequests()) != tt.requests {
				t.Fatalf("%d model calls and %d probe requests, want %d and %d", len(calls), len(h.loop.probeRequests()), tt.calls, tt.requests)
			}
			for i, raw := range calls {
				var req Request
				if err := json.Unmarshal([]byte(raw), &req); err != nil {
					t.Fatal(err)
				}
				if req.Final != tt.final[i] {
					t.Errorf("call %d (round %d): final %v, want %v", i, req.Round, req.Final, tt.final[i])
				}
			}
			if tt.reason != EndMaxModelCalls || tt.calls == 1 {
				denied := h.eventsOf(EventChangeDenied)
				if len(denied) != 1 || denied[0].Payload["deny_reason"] != string(loop.DenyFinalRound) {
					t.Fatalf("change.denied %+v, want the final round's proposal", denied)
				}
			}
		})
	}
}

func TestRun_ModelCallsCountedBeforeSend(t *testing.T) {
	h := newHarness(t)
	h.reply(replyJSON(t, nil), replyJSON(t, done(60)))
	var counted []int
	h.model.onCall = func(int) {
		st, _ := h.state()
		counted = append(counted, st.ModelCalls)
	}
	h.run()
	if !slices.Equal(counted, []int{1, 2}) {
		t.Fatalf("model_calls stored when each call was sent: %v, want [1 2]", counted)
	}
}
