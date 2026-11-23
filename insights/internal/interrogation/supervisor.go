package interrogation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/workflow"
	"google.golang.org/genai"

	commonlogger "github.com/odigos-io/odigos/common/logger"

	"github.com/odigos-io/odigos/insights/internal/repo"
)

const (
	pollInterval = 2 * time.Second
	// maxRunning bounds the invocations in flight. A parked run holds none.
	maxRunning = 4
	// resumeAfter is how long a run that is not parked may go without a
	// session update before a leader that is not running it resumes it. It
	// is longer than the leader lease, so the old leader has lost it.
	resumeAfter = 20 * time.Second
	// closeGrace is how long after its deadline or its stop a run that the
	// graph could not close is closed without it. Longer than a model call
	// plus the last steps of a healthy run.
	closeGrace = 5 * time.Minute

	retryInitial = 5 * time.Second
	retryMax     = time.Minute
	// retryStale is the backoff after an append refused because another
	// writer overtook the session.
	retryStale = 2 * time.Second

	// maxModelCalls bounds the model calls of one run, every attempt counted.
	maxModelCalls = 12

	graphChanged = "graph version changed"

	// msgStart picks a queued run up; msgResume continues a run from state.
	msgStart  = "start"
	msgResume = "resume"
)

// Runs is the part of the Insights repo the supervisor uses.
type Runs interface {
	ListOpenInterrogations(ctx context.Context) ([]repo.OpenInterrogation, error)
	// RevertStoppedInterrogationProbes reverts the live temporary probes of
	// closed runs whose stop asked for a revert. Safe to repeat.
	RevertStoppedInterrogationProbes(ctx context.Context, now time.Time) (int64, error)
}

// SupervisorConfig is what the leader gives the supervisor.
type SupervisorConfig struct {
	// Deps are the workflow's. MaxModelCalls zero means the server's limit;
	// Control nil reads the control sessions in Sessions.
	Deps     Deps
	Runs     Runs
	Sessions *Sessions
	Log      *commonlogger.OdigosLogger
}

// Supervisor runs the open interrogations on the leader. It remembers nothing
// the database does not say, except which invocations it has in flight and
// their retry backoff.
type Supervisor struct {
	runner *runner.Runner
	closer interface {
		CloseOverdue(ctx context.Context, id string, c Closed) error
	}
	runs Runs
	log  *commonlogger.OdigosLogger
	now  func() time.Time
	poll time.Duration

	results chan result
	// running and retries are owned by Run's goroutine.
	running map[string]activeRun
	retries map[string]*retry
}

// activeRun is an invocation in flight: its cancel, and the stop its
// message carried.
type activeRun struct {
	cancel context.CancelFunc
	stop   *Stop
}

// carries reports whether the invocation's message delivers stop.
func (f activeRun) carries(stop *Stop) bool {
	return f.stop != nil && f.stop.Changes == stop.Changes && f.stop.At.Equal(stop.At)
}

type result struct {
	id  string
	err error
	// answered is the interrupt the invocation answered, if it did.
	answered string
}

// retry is what this process knows of a run after one of its invocations
// ended: when the next message may go, and the failure it reports.
type retry struct {
	at      time.Time
	delay   time.Duration
	lastErr string
	// resume goes with the next plain resume.
	resume *Resume
	// unmatched is an interrupt whose answer ADK matched to no waiting node.
	unmatched string
}

// NewSupervisor builds the workflow and its runner on the session store.
func NewSupervisor(cfg SupervisorConfig) (*Supervisor, error) {
	if cfg.Deps.MaxModelCalls == 0 {
		cfg.Deps.MaxModelCalls = maxModelCalls
	}
	if cfg.Deps.Now == nil {
		cfg.Deps.Now = time.Now
	}
	if cfg.Deps.Control == nil {
		cfg.Deps.Control = cfg.Sessions.Control
	}
	if cfg.Deps.Log == nil {
		cfg.Deps.Log = cfg.Log
	}
	wf, err := BuildWorkflow(cfg.Deps)
	if err != nil {
		return nil, fmt.Errorf("build interrogation workflow: %w", err)
	}
	r, err := runner.New(runner.Config{AppName: AppName, Agent: wf, SessionService: cfg.Sessions.Service()})
	if err != nil {
		return nil, fmt.Errorf("build interrogation runner: %w", err)
	}
	return &Supervisor{
		runner:  r,
		closer:  cfg.Sessions,
		runs:    cfg.Runs,
		log:     cfg.Log,
		now:     cfg.Deps.Now,
		poll:    pollInterval,
		results: make(chan result),
		running: map[string]activeRun{},
		retries: map[string]*retry{},
	}, nil
}

// Run is a leader job. It returns when ctx is cancelled, after its
// invocations have stopped. A failed poll or invocation is logged and
// retried.
func (s *Supervisor) Run(ctx context.Context) {
	defer s.stopAll()
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	s.pollOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case res := <-s.results:
			s.finished(res)
		case <-ticker.C:
			s.pollOnce(ctx)
		}
	}
}

func (s *Supervisor) stopAll() {
	for _, f := range s.running {
		f.cancel()
	}
	for len(s.running) > 0 {
		s.finished(<-s.results)
	}
}

// pollOnce reads the open runs, acts on each, then sweeps the probes of runs
// stopped with revert.
func (s *Supervisor) pollOnce(ctx context.Context) {
	now := s.now()
	runs, err := s.runs.ListOpenInterrogations(ctx)
	if err != nil {
		s.log.Warn("failed to list open interrogations", "error", err)
	} else {
		s.decide(ctx, runs, now)
	}
	// The sweep runs whatever the poll found: a run closed before this leader
	// started is not among the open ones.
	if n, err := s.runs.RevertStoppedInterrogationProbes(ctx, now); err != nil {
		s.log.Warn("failed to revert the probes of stopped interrogations", "error", err)
	} else if n > 0 {
		s.log.Info("reverted the probes of stopped interrogations", "probes", n)
	}
}

func (s *Supervisor) decide(ctx context.Context, runs []repo.OpenInterrogation, now time.Time) {
	open := make(map[string]bool, len(runs))
	for _, o := range runs {
		open[o.ID] = true
		r, err := readOpenRun(o)
		if err != nil {
			s.log.Warn("failed to read open interrogation", "interrogation", o.ID, "error", err)
			continue
		}
		var flight *activeRun
		if f, ok := s.running[o.ID]; ok {
			flight = &f
		}
		act := next(r, flight, s.retries[o.ID], now)
		switch act.kind {
		case actNone:
		case actCancel:
			flight.cancel()
		case actClose:
			if err := s.closer.CloseOverdue(ctx, o.ID, act.closed); err != nil {
				s.log.Warn("failed to close overdue interrogation", "interrogation", o.ID, "error", err)
				continue
			}
			s.log.Info("closed overdue interrogation", "interrogation", o.ID, "reason", act.closed.Reason, "error", act.closed.Error)
		default:
			if len(s.running) < maxRunning {
				s.launch(ctx, r, act)
			}
		}
	}
	for id := range s.retries {
		if _, inFlight := s.running[id]; !open[id] && !inFlight {
			delete(s.retries, id)
		}
	}
}

// launch sends a run its next message in an invocation of its own.
func (s *Supervisor) launch(ctx context.Context, r openRun, act action) {
	ctl := Control{Stop: r.ctl.Stop, ExtendedSeconds: r.ctl.ExtendSeconds}
	var msg *genai.Content
	var answered string
	switch act.kind {
	case actStart:
		msg = plain(msgStart)
	case actResume:
		if rt := s.retries[r.id]; rt != nil {
			ctl.Resume = rt.resume
		}
		msg = plain(msgResume)
	case actFail:
		ctl.Fail = graphChanged
		msg = plain(msgResume)
	case actAnswer:
		answered = r.run.Wait.InterruptID
		msg = answer(answered)
	default:
		return
	}
	if rt := s.retries[r.id]; rt != nil {
		rt.resume, rt.unmatched = nil, ""
	}

	ictx, cancel := context.WithCancel(ctx)
	s.running[r.id] = activeRun{cancel: cancel, stop: ctl.Stop}
	go func() {
		defer cancel()
		err := s.invoke(ictx, r.id, msg, ctl)
		if err != nil && ictx.Err() != nil {
			// A cancel that lands inside an append surfaces as the driver's
			// error, not as context.Canceled.
			err = ictx.Err()
		}
		s.results <- result{id: r.id, err: err, answered: answered}
	}()
}

// invoke runs one invocation and returns the last error it yielded. A panic
// that reaches it is the invocation's error; ADK turns a node's panic into
// an error itself.
func (s *Supervisor) invoke(ctx context.Context, id string, msg *genai.Content, ctl Control) (err error) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("interrogation invocation panicked", "interrogation", id, "panic", p, "stack", string(debug.Stack()))
			err = fmt.Errorf("invocation panicked: %v", p)
		}
	}()
	cfg := agent.RunConfig{MaxLLMCalls: maxReplyAttempts + 1}
	for _, e := range s.runner.Run(ctx, id, id, msg, cfg, runner.WithStateDelta(map[string]any{ctlKey: ctl})) {
		if e != nil {
			err = e
		}
	}
	return err
}

// finished records how an invocation ended and when the run may get its next
// message.
func (s *Supervisor) finished(res result) {
	delete(s.running, res.id)
	rt := s.retries[res.id]
	if rt == nil {
		rt = &retry{}
		s.retries[res.id] = rt
	}
	now := s.now()
	err := res.err
	var callErr *ModelCallError
	switch {
	case err == nil:
		*rt = retry{at: now}
		return
	case errors.Is(err, context.Canceled):
		// Stopped by this process: a stop, an overdue run or shutdown. The
		// next poll decides.
		rt.at = now
		return
	case errors.Is(err, workflow.ErrNothingToResume):
		// The next poll tells the two causes apart: the run still parked on
		// this interrupt lost its wait node's event, and a run that has left
		// it was answered from an old read.
		*rt = retry{at: now, unmatched: res.answered}
		return
	case errors.As(err, &callErr) && callErr.Paused:
		// Not a failure of the step: gate sees the pause and parks the run in
		// wait_model until it ends.
		*rt = retry{at: now, lastErr: err.Error()}
		s.log.Info("interrogation model calls paused", "interrogation", res.id, "until", callErr.Until)
		return
	case serializationFailure(err) || strings.Contains(err.Error(), "stale session error"):
		// ADK has no error value for the stale check.
		rt.delay = retryStale
		rt.at = now.Add(rt.delay)
	default:
		rt.delay = min(max(2*rt.delay, retryInitial), retryMax)
		rt.at = now.Add(rt.delay)
	}
	rt.lastErr = err.Error()
	rt.resume = &Resume{Error: err.Error(), ModelCall: callErr != nil, RetryAfter: int(rt.at.Sub(now) / time.Second)}
	s.log.Warn("interrogation invocation failed", "interrogation", res.id, "retry_in", rt.at.Sub(now).String(), "error", err)
}

// openRun is an open run as the supervisor reads it.
type openRun struct {
	id      string
	updated time.Time
	ctl     control
	// limit is the request's time limit in seconds.
	limit  int
	queued bool
	// unreadable is set when this binary cannot run the stored state: why.
	unreadable string
	run        runHead
}

// runHead is what the supervisor reads of the workflow's state.
type runHead struct {
	Graph      int       `json:"graph"`
	StartedAt  time.Time `json:"started_at"`
	DeadlineAt time.Time `json:"deadline_at"`
	Wait       *Wait     `json:"wait"`
	Stop       *Stop     `json:"stop"`
	LastError  string    `json:"last_error"`
}

func readOpenRun(o repo.OpenInterrogation) (openRun, error) {
	r := openRun{id: o.ID, updated: o.UpdateTime}
	// A request or control this binary cannot decode makes the run unreadable
	// too: skipped instead of closed, it would hold its workload forever.
	if len(o.Control) > 0 {
		if err := json.Unmarshal(o.Control, &r.ctl); err != nil {
			r.unreadable = "control unreadable: " + err.Error()
		}
	}
	var state struct {
		Request json.RawMessage `json:"request"`
		Run     json.RawMessage `json:"run"`
	}
	if err := json.Unmarshal(o.State, &state); err != nil {
		return r, err
	}
	var req repo.InterrogationRequest
	if len(state.Request) > 0 {
		if err := json.Unmarshal(state.Request, &req); err != nil {
			r.unreadable = "request unreadable: " + err.Error()
		}
	}
	r.limit = req.Budget.TimeLimitSeconds
	if len(state.Run) == 0 || string(state.Run) == "null" {
		r.queued = true
		return r, nil
	}
	if why := decodeRunHead(state.Run, &r.run); why != "" {
		r.unreadable = why
	}
	return r, nil
}

// decodeRunHead decodes the workflow's state into head. It returns why this
// binary cannot run the state, empty when it can.
func decodeRunHead(raw json.RawMessage, head *runHead) string {
	// The version is read first: a newer binary may have changed any field.
	var v struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return "state unreadable: " + err.Error()
	}
	if v.V > StateVersion {
		return fmt.Sprintf("state version %d, this server reads %d", v.V, StateVersion)
	}
	if err := json.Unmarshal(raw, head); err != nil {
		return "state unreadable: " + err.Error()
	}
	return ""
}

// deadline is the run's deadline with every extension recorded, delivered to
// the workflow or not.
func (r openRun) deadline() time.Time {
	if r.run.StartedAt.IsZero() {
		return r.run.DeadlineAt
	}
	return r.run.StartedAt.Add(time.Duration(r.limit+r.ctl.ExtendSeconds) * time.Second)
}

// stopPending reports whether the control session holds a stop the run has
// not applied.
func (r openRun) stopPending() bool {
	s := r.ctl.Stop
	return s != nil && (r.run.Stop == nil || s.Changes == ChangesRevert && r.run.Stop.Changes != ChangesRevert)
}

// controlPending reports whether the control would change the run's state.
func (r openRun) controlPending() bool {
	return r.stopPending() || !r.run.StartedAt.IsZero() && !r.deadline().Equal(r.run.DeadlineAt)
}

// overdue reports whether the run should have ended closeGrace ago, and how
// to close it.
func (r openRun) overdue(now time.Time, lastErr string) (Closed, bool) {
	if s := r.ctl.Stop; s != nil && !now.Before(s.At.Add(closeGrace)) {
		return Closed{Reason: repo.InterrogationStoppedByUser, Actor: s.Actor, Changes: s.Changes, At: now}, true
	}
	if d := r.deadline(); !d.IsZero() && !now.Before(d.Add(closeGrace)) {
		return Closed{Reason: repo.InterrogationInterrupted, Error: "did not finish by its deadline: " + lastErr, At: now}, true
	}
	return Closed{}, false
}

type actionKind int

const (
	actNone actionKind = iota
	// actCancel cancels the invocation in flight; the next poll acts.
	actCancel
	// actStart picks a queued run up.
	actStart
	// actResume continues a run that is not parked from its recorded step.
	actResume
	// actAnswer answers the interrupt the run is parked on.
	actAnswer
	// actFail ends a run of another graph version through the graph.
	actFail
	// actClose closes the run without the graph.
	actClose
)

type action struct {
	kind   actionKind
	closed Closed
}

// next decides what to do with an open run. flight is the run's invocation in
// flight, nil when none; rt is nil until an invocation of this process for the
// run has ended.
func next(r openRun, flight *activeRun, rt *retry, now time.Time) action {
	lastErr := r.run.LastError
	if rt != nil && rt.lastErr != "" {
		lastErr = rt.lastErr
	}
	closed, overdue := r.overdue(now, lastErr)
	switch {
	case flight != nil && (overdue || r.stopPending() && !flight.carries(r.ctl.Stop)):
		// An invocation that carries the stop applies it in entry.
		return action{kind: actCancel}
	case flight != nil:
		return action{}
	case r.unreadable != "":
		// The graph loads the state first and would fail every step.
		return action{kind: actClose, closed: Closed{Reason: repo.InterrogationInterrupted, Error: r.unreadable, At: now}}
	case overdue:
		return action{kind: actClose, closed: closed}
	case rt != nil && now.Before(rt.at):
		return action{}
	case r.queued:
		return action{kind: actStart}
	case r.run.Graph != 0 && r.run.Graph != GraphVersion:
		// Answering an interrupt of another graph would restart the old
		// invocation at START; a plain message starts a new one.
		return action{kind: actFail}
	case r.run.Wait != nil && rt != nil && rt.unmatched == r.run.Wait.InterruptID:
		// The answer was stored by an invocation that ended before the wait
		// node's event. entry routes to the same wait, which parks again under
		// a new interrupt id or passes.
		return action{kind: actResume}
	case r.run.Wait != nil:
		if !now.Before(r.run.Wait.Until) || r.controlPending() {
			return action{kind: actAnswer}
		}
		return action{}
	case rt != nil || now.Sub(r.updated) >= resumeAfter:
		return action{kind: actResume}
	}
	return action{}
}

func plain(text string) *genai.Content { return genai.NewContentFromText(text, genai.RoleUser) }

func isStart(c *genai.Content) bool {
	return c != nil && len(c.Parts) == 1 && c.Parts[0].Text == msgStart
}

func answer(interruptID string) *genai.Content {
	return &genai.Content{Role: genai.RoleUser, Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
		ID: interruptID, Name: workflow.WorkflowInputFunctionCallName, Response: map[string]any{"response": "wake"},
	}}}}
}
