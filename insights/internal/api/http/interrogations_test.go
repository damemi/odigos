package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/adk/v2/session"

	commonlogger "github.com/odigos-io/odigos/common/logger"
	"github.com/odigos-io/odigos/insights/internal/api/http/apitypes"
	"github.com/odigos-io/odigos/insights/internal/autopilot/loop"
	apstore "github.com/odigos-io/odigos/insights/internal/autopilot/store"
	"github.com/odigos-io/odigos/insights/internal/db/postgres"
	"github.com/odigos-io/odigos/insights/internal/db/postgres/pgtest"
	"github.com/odigos-io/odigos/insights/internal/engine"
	"github.com/odigos-io/odigos/insights/internal/interrogation"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// interrogationServer is the API on a migrated database of the test's own,
// with no leader: nothing picks a run up unless the test does.
type interrogationServer struct {
	h        http.Handler
	api      *API
	repo     *postgres.Repo
	sessions *interrogation.Sessions
	probes   *apstore.Client
	sql      *pgxpool.Pool
	// offset moves the handlers' clock.
	offset atomic.Int64
}

func newInterrogationServer(t *testing.T) *interrogationServer {
	t.Helper()
	dsn := pgtest.NewDatabase(t)
	ctx := context.Background()
	r, err := postgres.Open(dsn, engine.NewBaselineCodec())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	sessions, err := interrogation.OpenSessions(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sessions.Close)
	probes, err := apstore.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probes.Close() })
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	commonlogger.Init("error", "insights-test")
	h, api := NewRouter(r, commonlogger.LoggerCompat())
	s := &interrogationServer{h: h, api: api, repo: r, sessions: sessions, probes: probes, sql: pool}
	s.serve(sessions)
	return s
}

func (s *interrogationServer) serve(sessions InterrogationSessions) {
	s.api.SetInterrogations(InterrogationsConfig{
		Enabled:  true,
		Sessions: sessions,
		now:      func() time.Time { return time.Now().Add(time.Duration(s.offset.Load())) },
	})
}

// refusingSessions refuses one kind of write as a write that lost to another
// writer on every retry; before runs first, as that other writer.
type refusingSessions struct {
	*interrogation.Sessions
	refuse string
	before func(id string)
}

func (r refusingSessions) refused(write, id string) error {
	if write != r.refuse {
		return nil
	}
	if r.before != nil {
		r.before(id)
	}
	return fmt.Errorf("%s interrogation %s: %w", write, id, &pgconn.PgError{Code: "40001"})
}

func (r refusingSessions) Start(ctx context.Context, id string, req repo.InterrogationRequest) error {
	if err := r.refused("start", id); err != nil {
		return err
	}
	return r.Sessions.Start(ctx, id, req)
}

func (r refusingSessions) CloseQueued(ctx context.Context, id string, c interrogation.Closed) (bool, error) {
	if err := r.refused("close", id); err != nil {
		return false, err
	}
	return r.Sessions.CloseQueued(ctx, id, c)
}

func (r refusingSessions) Stop(ctx context.Context, id string, stop interrogation.Stop) error {
	if err := r.refused("stop", id); err != nil {
		return err
	}
	return r.Sessions.Stop(ctx, id, stop)
}

func (r refusingSessions) Extend(ctx context.Context, id string, seconds, maxTotal int, now time.Time) error {
	if err := r.refused("extend", id); err != nil {
		return err
	}
	return r.Sessions.Extend(ctx, id, seconds, maxTotal, now)
}

func (s *interrogationServer) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return do(t, s.h, method, path, body)
}

func (s *interrogationServer) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := s.sql.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

// evidence gives workload ns/Deployment/<name> a call graph for each
// transaction, keyed by a source id derived from the name.
func (s *interrogationServer) evidence(t *testing.T, name string, txs ...int64) {
	t.Helper()
	s.exec(t, `INSERT INTO sources (source_id, platform) VALUES (hashtextextended($1, 0), 'k8s') ON CONFLICT DO NOTHING`, name)
	s.exec(t, `INSERT INTO k8s_source_identity (source_id, namespace, workload_kind, workload_name, container_name)
		VALUES (hashtextextended($1, 0), 'ns', 'Deployment', $1, 'app') ON CONFLICT DO NOTHING`, name)
	for _, tx := range txs {
		s.exec(t, `INSERT INTO autopilot_call_tree_nodes (source_id, transaction_id, sample_type, node_id, parent, function_name, frame_type)
			VALUES (hashtextextended($1, 0), $2, 'samples', 'n1', 'root', 'a.B.c', 'jvm')`, name, tx)
		s.exec(t, `INSERT INTO transactions (transaction_id, source_id, service, operation, entry_kind)
			VALUES ($1, hashtextextended($2, 0), $2, 'GET /x', 'HTTP') ON CONFLICT DO NOTHING`, tx, name)
	}
}

func startBody(name string, extra string) string {
	return `{"source":{"namespace":"ns","kind":"Deployment","name":"` + name + `"}` + extra + `}`
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return v
}

// start starts a run on workload name and returns it.
func (s *interrogationServer) start(t *testing.T, name, extra string) apitypes.Interrogation {
	t.Helper()
	w := s.do(t, http.MethodPost, "/api/v1/interrogations", startBody(name, extra))
	if w.Code != http.StatusCreated {
		t.Fatalf("start %s: %d %s", name, w.Code, w.Body.String())
	}
	return decodeBody[apitypes.Interrogation](t, w)
}

func (s *interrogationServer) get(t *testing.T, id string) apitypes.Interrogation {
	t.Helper()
	w := s.do(t, http.MethodGet, "/api/v1/interrogations/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get %s: %d %s", id, w.Code, w.Body.String())
	}
	return decodeBody[apitypes.Interrogation](t, w)
}

// appendRun appends delta to the run's session as the workflow's runner does:
// on a copy it read before, with ADK's own check against a newer write.
func (s *interrogationServer) appendRun(ctx context.Context, id string, delta map[string]any) error {
	svc := s.sessions.Service()
	got, err := svc.Get(ctx, &session.GetRequest{AppName: interrogation.AppName, UserID: id, SessionID: id})
	if err != nil {
		return err
	}
	ev := session.NewEvent(ctx, "runner")
	ev.Author = "interrogation"
	ev.Actions.StateDelta = delta
	return svc.AppendEvent(ctx, got.Session, ev)
}

func runState(status string, startedAt time.Time) map[string]any {
	return map[string]any{
		"v": interrogation.StateVersion, "status": status, "node": "gate", "round": 1, "started_at": startedAt,
	}
}

// tryPickUp is what the leader does with a queued run, as far as the API can
// tell: its runner reads the session and, when the run is still queued, the
// first node writes the workflow's state. ADK refuses that write when the
// session changed since the read. The supervisor is built elsewhere; the
// races here need no more of it.
func (s *interrogationServer) tryPickUp(ctx context.Context, id string) (bool, error) {
	svc := s.sessions.Service()
	got, err := svc.Get(ctx, &session.GetRequest{AppName: interrogation.AppName, UserID: id, SessionID: id})
	if err != nil {
		return false, err
	}
	open, _ := got.Session.State().Get("open")
	if _, err := got.Session.State().Get("run"); open != true || err == nil {
		return false, nil
	}
	ev := session.NewEvent(ctx, "runner")
	ev.Author = "interrogation"
	ev.Actions.StateDelta = map[string]any{"run": runState("running", time.Now())}
	if err := svc.AppendEvent(ctx, got.Session, ev); err != nil {
		return false, err
	}
	return true, nil
}

func (s *interrogationServer) pickUp(t *testing.T, id string) {
	t.Helper()
	if ran, err := s.tryPickUp(context.Background(), id); !ran || err != nil {
		t.Fatalf("pick up %s: %v %v", id, ran, err)
	}
}

// end appends the workflow's last event.
func (s *interrogationServer) end(t *testing.T, id string, startedAt time.Time) {
	t.Helper()
	run := runState("completed", startedAt)
	run["end_reason"], run["ended_at"], run["node"] = "done", time.Now(), ""
	if err := s.appendRun(context.Background(), id, map[string]any{"run": run, "open": false}); err != nil {
		t.Fatal(err)
	}
}

// probe stores a probe of the run as RequestProbe does.
func (s *interrogationServer) probe(t *testing.T, id, name, target string) string {
	t.Helper()
	class, method, _ := strings.Cut(target, "#")
	p, err := s.probes.InsertRequestedProbe(context.Background(), apstore.RequestedProbe{
		Recommendation: apstore.Recommendation{
			Namespace: "ns", WorkloadKind: "Deployment", WorkloadName: name, ContainerName: "app",
			TransactionID: "1", Language: "java", Target: target, Function: target, Category: "observability",
			Why: "called from " + target, ClassName: class, MethodName: method,
		},
		ChangeID:        loop.ChangeID(id, "java", target),
		InterrogationID: id,
		Round:           1,
	}, 5, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return p.ChangeID
}

type errorBody struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

// engineStartingPhrases are what the frontend reads in a 503 message as the
// engine starting (frontend/services/insights/errors.go).
var engineStartingPhrases = []string{"not ready", "is starting", "engine starting"}

// wantError checks the status and details.reason of an error response; its
// message may read as the engine starting only for not_ready.
func wantError(t *testing.T, w *httptest.ResponseRecorder, status int, reason string) errorBody {
	t.Helper()
	if w.Code != status {
		t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
	}
	body := decodeBody[errorBody](t, w)
	if got, _ := body.Error.Details["reason"].(string); got != reason {
		t.Fatalf("reason %q, want %q: %s", got, reason, w.Body.String())
	}
	if reason != reasonNotReady {
		for _, p := range engineStartingPhrases {
			if strings.Contains(strings.ToLower(body.Error.Message), p) {
				t.Errorf("message %q contains %q", body.Error.Message, p)
			}
		}
	}
	return body
}

// interrogationEndpoints is one request to every operation but the options.
var interrogationEndpoints = []struct{ method, path, body string }{
	{http.MethodGet, "/api/v1/interrogations", ""},
	{http.MethodPost, "/api/v1/interrogations", startBody("app", "")},
	{http.MethodGet, "/api/v1/interrogations/summary", ""},
	{http.MethodGet, "/api/v1/interrogations/sources", ""},
	{http.MethodGet, "/api/v1/interrogations/sources/transactions?namespace=ns&kind=Deployment&name=app", ""},
	{http.MethodGet, "/api/v1/interrogations/x", ""},
	{http.MethodGet, "/api/v1/interrogations/x/events", ""},
	{http.MethodPost, "/api/v1/interrogations/x/stop", `{"changes":"revert"}`},
	{http.MethodPost, "/api/v1/interrogations/x/extend", `{"seconds":600}`},
	{http.MethodGet, "/api/v1/interrogations/x/changes", ""},
	{http.MethodGet, "/api/v1/interrogation-changes", ""},
	{http.MethodGet, "/api/v1/interrogation-changes/summary", ""},
	{http.MethodPost, "/api/v1/interrogation-changes/revert-all", ""},
	{http.MethodGet, "/api/v1/interrogation-changes/x", ""},
	{http.MethodPost, "/api/v1/interrogation-changes/x/keep", ""},
	{http.MethodPost, "/api/v1/interrogation-changes/x/revert", ""},
	{http.MethodPost, "/api/v1/interrogation-changes/x/remove", ""},
	{http.MethodPost, "/api/v1/interrogation-changes/x/undo", ""},
}

func TestInterrogations_NotReadyBeforeSet(t *testing.T) {
	h := newTestRouter(t)
	for _, e := range interrogationEndpoints {
		t.Run(e.method+" "+e.path, func(t *testing.T) {
			wantError(t, do(t, h, e.method, e.path, e.body), http.StatusServiceUnavailable, reasonNotReady)
		})
	}
	w := do(t, h, http.MethodGet, "/api/v1/interrogations/options", "")
	if w.Code != http.StatusOK || decodeBody[apitypes.InterrogationOptions](t, w).Enabled {
		t.Errorf("options: %d %s", w.Code, w.Body.String())
	}
}

func TestInterrogations_BodyTooLarge(t *testing.T) {
	h := newTestRouter(t)
	body := `{"question":"` + strings.Repeat("a", 2<<20) + `"}`
	for _, path := range []string{"/api/v1/interrogations", "/api/v1/interrogations/x/stop", "/api/v1/interrogations/x/extend",
		"/api/v1/interrogation-changes/revert-all", "/api/v1/interrogation-changes/x/keep"} {
		w := do(t, h, http.MethodPost, path, body)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("POST %s: %d, want 413", path, w.Code)
		}
		if got := decodeBody[errorBody](t, w).Error.Code; got != string(apitypes.ErrorBodyCodePayloadTooLarge) {
			t.Errorf("POST %s: code %q", path, got)
		}
	}
}

func TestInterrogations_NotEnabled(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	h, api := NewRouter(nil, commonlogger.LoggerCompat())
	api.SetInterrogations(InterrogationsConfig{})
	for _, e := range interrogationEndpoints {
		t.Run(e.method+" "+e.path, func(t *testing.T) {
			body := wantError(t, do(t, h, e.method, e.path, e.body), http.StatusServiceUnavailable, reasonNotEnabled)
			if body.Error.Details["requires"] != "insights.autopilot.enabled" {
				t.Errorf("details %v", body.Error.Details)
			}
		})
	}
}

// Writes need the session store; reads do not.
func TestInterrogations_NoSessionStore(t *testing.T) {
	commonlogger.Init("error", "insights-test")
	h, api := NewRouter(nil, commonlogger.LoggerCompat())
	api.SetInterrogations(InterrogationsConfig{Enabled: true})
	for _, path := range []string{"/api/v1/interrogations", "/api/v1/interrogations/x/stop", "/api/v1/interrogations/x/extend"} {
		body := startBody("app", "")
		if !strings.HasSuffix(path, "interrogations") {
			body = `{"seconds":600}`
		}
		wantError(t, do(t, h, http.MethodPost, path, body), http.StatusServiceUnavailable, reasonNotReady)
	}
}

// Before the migration created ADK's schema, the endpoints say to retry.
func TestInterrogations_SchemaMissing(t *testing.T) {
	s := newInterrogationServer(t)
	s.exec(t, `DROP SCHEMA adk CASCADE`)
	wantError(t, s.do(t, http.MethodGet, "/api/v1/interrogations", ""), http.StatusServiceUnavailable, reasonNotReady)
	wantError(t, s.do(t, http.MethodGet, "/api/v1/interrogations/"+uuid.NewString(), ""), http.StatusServiceUnavailable, reasonNotReady)
}

func TestInterrogationOptions(t *testing.T) {
	s := newInterrogationServer(t)
	w := s.do(t, http.MethodGet, "/api/v1/interrogations/options", "")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var got, want any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// The example of the contract.
	if err := json.Unmarshal([]byte(`{
  "enabled": true,
  "goals": [{"value": "observability", "supported": true, "default": true}, {"value": "security", "supported": false, "default": false}],
  "change_kinds": [
    {"value": "custom_instrumentation", "supported": true, "default": true},
    {"value": "head_sampling", "supported": false, "default": false},
    {"value": "payload_collection", "supported": false, "default": false},
    {"value": "kernel_spans", "supported": false, "default": false}],
  "budget": {
    "max_probes": {"supported": true, "default": 5, "min": 1, "max": 20, "choices": []},
    "max_added_spans_per_min": {"supported": false, "choices": [500, 1000, 5000]},
    "time_limit_seconds": {"supported": true, "default": 3600, "min": 300, "max": 14400, "choices": [900, 3600, 14400]}},
  "max_rounds": 5,
  "extend": {"default_seconds": 1800, "max_total_seconds": 28800},
  "max_open": 10,
  "max_transaction_ids": 20
}`), &want); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("options\n got %v\nwant %v", got, want)
	}
}

// Every refusal of a start: status, reason and the field, value or option it
// names.
func TestStart_Refusals(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1, 2)
	long := strings.Repeat("x", 2001)
	tests := []struct {
		name, body    string
		status        int
		reason, field string
		value, option string
	}{
		{name: "malformed", body: `{"source":`, status: http.StatusBadRequest},
		{name: "no source", body: `{}`, status: 422, reason: reasonInvalidArgument, field: "source.namespace"},
		{name: "no name", body: `{"source":{"namespace":"ns","kind":"Deployment"}}`, status: 422, reason: reasonInvalidArgument, field: "source.name"},
		{name: "unknown goal", body: startBody("app", `,"goal":"speed"`), status: 422, reason: reasonInvalidArgument, field: "goal"},
		{name: "long question", body: startBody("app", `,"question":"`+long+`"`), status: 422, reason: reasonInvalidArgument, field: "question"},
		{name: "too many transactions", body: startBody("app", `,"transaction_ids":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21]`),
			status: 422, reason: reasonInvalidArgument, field: "transaction_ids"},
		{name: "repeated transaction", body: startBody("app", `,"transaction_ids":[1,2,1]`),
			status: 422, reason: reasonInvalidArgument, field: "transaction_ids", value: "1"},
		{name: "unknown change kind", body: startBody("app", `,"allow_changes":["custom_instrumentation","rewrite"]`),
			status: 422, reason: reasonInvalidArgument, field: "allow_changes", value: "rewrite"},
		{name: "max probes", body: startBody("app", `,"budget":{"max_probes":21}`), status: 422, reason: reasonInvalidArgument, field: "budget.max_probes"},
		{name: "negative max probes", body: startBody("app", `,"budget":{"max_probes":-1}`), status: 422, reason: reasonInvalidArgument, field: "budget.max_probes"},
		{name: "time limit", body: startBody("app", `,"budget":{"time_limit_seconds":299}`), status: 422, reason: reasonInvalidArgument, field: "budget.time_limit_seconds"},
		{name: "long actor", body: startBody("app", `,"actor":"`+strings.Repeat("a", 201)+`"`), status: 422, reason: reasonInvalidArgument, field: "actor"},
		{name: "nul in source", body: `{"source":{"namespace":"ns","kind":"Deployment","name":"app\u0000"}}`, status: 422, reason: reasonInvalidArgument, field: "source.name"},
		{name: "nul in question", body: startBody("app", `,"question":"why\u0000"`), status: 422, reason: reasonInvalidArgument, field: "question"},
		{name: "nul in actor", body: startBody("app", `,"actor":"a\u0000b"`), status: 422, reason: reasonInvalidArgument, field: "actor"},
		{name: "security", body: startBody("app", `,"goal":"security"`), status: 422, reason: reasonNotSupportedYet, option: "goal=security"},
		{name: "head sampling", body: startBody("app", `,"allow_changes":["head_sampling"]`), status: 422, reason: reasonNotSupportedYet, option: "allow_changes=head_sampling"},
		{name: "payload collection", body: startBody("app", `,"allow_changes":["payload_collection"]`), status: 422, reason: reasonNotSupportedYet, option: "allow_changes=payload_collection"},
		{name: "kernel spans", body: startBody("app", `,"allow_changes":["kernel_spans"]`), status: 422, reason: reasonNotSupportedYet, option: "allow_changes=kernel_spans"},
		{name: "added spans", body: startBody("app", `,"budget":{"max_added_spans_per_min":1000}`), status: 422, reason: reasonNotSupportedYet, option: "budget.max_added_spans_per_min"},
		{name: "no evidence", body: startBody("billing", ""), status: 422, reason: reasonNoEvidence},
		{name: "transaction without a call graph", body: startBody("app", `,"transaction_ids":[1,3]`),
			status: 422, reason: reasonInvalidArgument, field: "transaction_ids", value: "3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := s.do(t, http.MethodPost, "/api/v1/interrogations", tt.body)
			if tt.reason == "" {
				if w.Code != tt.status {
					t.Fatalf("status %d: %s", w.Code, w.Body.String())
				}
				return
			}
			d := wantError(t, w, tt.status, tt.reason).Error.Details
			for k, v := range map[string]string{"field": tt.field, "value": tt.value, "option": tt.option} {
				if got, _ := d[k].(string); got != v {
					t.Errorf("details.%s %q, want %q", k, got, v)
				}
			}
		})
	}
	if n := s.openRuns(t); n != 0 {
		t.Errorf("refused starts stored %d runs", n)
	}
}

func (s *interrogationServer) openRuns(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.sql.QueryRow(context.Background(),
		`SELECT count(*) FROM adk.sessions WHERE app_name = 'interrogation' AND state->>'open' = 'true'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The unsupported options of one request are reported one at a time, the
// first in field order.
func TestStart_UnsupportedOneAtATime(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	steps := []struct{ goal, kinds, budget, option string }{
		{`"security"`, `["custom_instrumentation","kernel_spans","head_sampling"]`, `{"max_added_spans_per_min":500}`, "goal=security"},
		{`"observability"`, `["custom_instrumentation","kernel_spans","head_sampling"]`, `{"max_added_spans_per_min":500}`, "allow_changes=kernel_spans"},
		{`"observability"`, `["custom_instrumentation","head_sampling"]`, `{"max_added_spans_per_min":500}`, "allow_changes=head_sampling"},
		{`"observability"`, `["custom_instrumentation"]`, `{"max_added_spans_per_min":500}`, "budget.max_added_spans_per_min"},
	}
	for _, st := range steps {
		w := s.do(t, http.MethodPost, "/api/v1/interrogations",
			startBody("app", `,"goal":`+st.goal+`,"allow_changes":`+st.kinds+`,"budget":`+st.budget))
		if got := wantError(t, w, 422, reasonNotSupportedYet).Error.Details["option"]; got != st.option {
			t.Errorf("option %v, want %s", got, st.option)
		}
	}
}

// Validation runs in the documented order: bounds, unsupported options,
// evidence, transaction ids, capacity, then the workload.
func TestStart_ValidationOrder(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	for i := range interrogationMaxOpen {
		name := fmt.Sprintf("full-%d", i)
		s.evidence(t, name, 1)
		s.start(t, name, "")
	}
	steps := []struct {
		body   string
		status int
		reason string
	}{
		// A bound and everything after it.
		{startBody("billing", `,"goal":"security","budget":{"max_probes":30}`), 422, reasonInvalidArgument},
		// An unsupported option, no evidence, a full cluster.
		{startBody("billing", `,"goal":"security"`), 422, reasonNotSupportedYet},
		// No evidence and a full cluster.
		{startBody("billing", `,"transaction_ids":[9]`), 422, reasonNoEvidence},
		// A transaction without a call graph and a full cluster.
		{startBody("app", `,"transaction_ids":[9]`), 422, reasonInvalidArgument},
		// A full cluster and a busy workload.
		{startBody("full-0", ""), http.StatusServiceUnavailable, reasonAtCapacity},
	}
	for _, st := range steps {
		wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations", st.body), st.status, st.reason)
	}
}

// With no leader a run stays queued; it holds its workload until a stop ends
// it, which needs no leader either.
func TestStart_QueuedLifecycle(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1, 2)
	run := s.start(t, "app", `,"question":"why?","transaction_ids":[2],"allow_changes":["custom_instrumentation"],"budget":{"max_probes":3},"actor":"alon"`)
	if run.Status != repo.InterrogationQueued || run.Title != "why?" || run.Budget.MaxProbes != 3 || run.Budget.TimeLimitSeconds != 3600 ||
		run.Budget.MaxRounds != 5 || run.Trigger.Kind != "manual" || run.Trigger.Actor != "alon" || run.StartedAt != nil ||
		run.DisplayId != (repo.Interrogation{ID: run.Id}).DisplayID() {
		t.Fatalf("started %+v", run)
	}
	if got := s.get(t, run.Id); got.Status != repo.InterrogationQueued || got.Id != run.Id {
		t.Fatalf("get %+v", got)
	}
	w := s.do(t, http.MethodGet, "/api/v1/interrogations", "")
	if list := decodeBody[apitypes.InterrogationList](t, w); list.Count != 1 || list.Items[0].Id != run.Id {
		t.Fatalf("list %s", w.Body.String())
	}

	busy := wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations", startBody("app", "")), http.StatusConflict, reasonSourceBusy)
	if busy.Error.Details["interrogation_id"] != run.Id || busy.Error.Details["display_id"] != run.DisplayId {
		t.Errorf("busy details %v", busy.Error.Details)
	}

	w = s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/stop", "")
	if w.Code != http.StatusOK {
		t.Fatalf("stop %d %s", w.Code, w.Body.String())
	}
	stopped := decodeBody[apitypes.Interrogation](t, w)
	if stopped.Status != repo.InterrogationStopped || stopped.EndReason != repo.InterrogationStoppedByUser ||
		stopped.Stop == nil || stopped.Stop.Changes != "keep" || stopped.EndedAt == nil {
		t.Fatalf("stopped %+v", stopped)
	}
	w = s.do(t, http.MethodGet, "/api/v1/interrogations/"+run.Id+"/events?after=0", "")
	events := decodeBody[apitypes.InterrogationEventList](t, w)
	if w.Code != http.StatusOK || events.Count != 0 || !events.Complete {
		t.Fatalf("events %d %s", w.Code, w.Body.String())
	}
	// The workload is free again.
	s.start(t, "app", "")
}

// A queued run that waited for a leader past the timeout loses its workload
// to the next start.
func TestStart_ReplacesStaleQueuedRun(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	old := s.start(t, "app", "")
	s.offset.Store(int64(interrogationQueueTimeout))
	next := s.start(t, "app", "")
	if next.Status != repo.InterrogationQueued {
		t.Errorf("new run %s", next.Status)
	}
	got := s.get(t, old.Id)
	if got.Status != repo.InterrogationFailed || got.EndReason != repo.InterrogationInterrupted || got.Error != "was not started within 5m0s" {
		t.Errorf("old run %s %s %q", got.Status, got.EndReason, got.Error)
	}
}

func TestStart_QueuedRunWithinTimeoutIsBusy(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	old := s.start(t, "app", "")
	s.offset.Store(int64(interrogationQueueTimeout - 5*time.Second))
	wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations", startBody("app", "")), http.StatusConflict, reasonSourceBusy)
	// A run the leader picked up is never taken, however old.
	s.pickUp(t, old.Id)
	s.offset.Store(int64(time.Hour))
	wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations", startBody("app", "")), http.StatusConflict, reasonSourceBusy)
	if got := s.get(t, old.Id); got.Status != repo.InterrogationRunning {
		t.Errorf("old run %s", got.Status)
	}
}

// A start that takes the workload of a stale queued run races the leader
// picking that run up. Either the old run is closed and never runs, and the
// start is accepted, or it runs and the start is refused; never both.
func TestStart_ReplacesStaleQueuedRun_RacesPickup(t *testing.T) {
	s := newInterrogationServer(t)
	ctx := context.Background()
	var replaced, ran int
	for i := range 200 {
		name := fmt.Sprintf("app-%d", i)
		s.evidence(t, name, 1)
		s.offset.Store(0)
		old := s.start(t, name, "")
		s.offset.Store(int64(interrogationQueueTimeout))

		var picked bool
		var w *httptest.ResponseRecorder
		var wg sync.WaitGroup
		wg.Go(func() {
			// The start reads three times before it closes the old run; the
			// delays spread the pickup over that window.
			time.Sleep(time.Duration(i%40) * 250 * time.Microsecond)
			// A refused write is a pickup that did not happen.
			picked, _ = s.tryPickUp(ctx, old.Id)
		})
		wg.Go(func() {
			w = do(t, s.h, http.MethodPost, "/api/v1/interrogations", startBody(name, ""))
		})
		wg.Wait()

		got, ok, err := s.repo.GetInterrogation(ctx, old.Id, time.Now())
		if err != nil || !ok {
			t.Fatalf("race %d: %v %v", i, ok, err)
		}
		if got.Started != picked {
			t.Fatalf("race %d: run state %v, pickup %v", i, got.Started, picked)
		}
		switch {
		case got.Started && !got.Open:
			t.Fatalf("race %d: the old run was closed and also run", i)
		case got.Started:
			if w.Code != http.StatusConflict {
				t.Fatalf("race %d: the old run runs and the start answered %d %s", i, w.Code, w.Body.String())
			}
			ran++
			// Leave room under the open-run limit for the next race.
			s.end(t, old.Id, time.Now())
		case !got.Open:
			if w.Code != http.StatusCreated {
				t.Fatalf("race %d: the old run was closed and the start answered %d %s", i, w.Code, w.Body.String())
			}
			replaced++
			s.stop(t, decodeBody[apitypes.Interrogation](t, w).Id, "")
		default:
			t.Fatalf("race %d: the old run is still queued; start %d %s", i, w.Code, w.Body.String())
		}
		// And under the daily limit.
		s.exec(t, `UPDATE adk.sessions SET create_time = create_time - interval '1 day' WHERE state->>'open' = 'false'`)
	}
	t.Logf("200 races: replaced %d, ran %d", replaced, ran)
}

func TestStart_Capacity(t *testing.T) {
	s := newInterrogationServer(t)
	for i := range interrogationMaxOpen + 1 {
		s.evidence(t, fmt.Sprintf("app-%d", i), 1)
	}
	for i := range interrogationMaxOpen {
		s.start(t, fmt.Sprintf("app-%d", i), "")
	}
	w := s.do(t, http.MethodPost, "/api/v1/interrogations", startBody(fmt.Sprintf("app-%d", interrogationMaxOpen), ""))
	d := wantError(t, w, http.StatusServiceUnavailable, reasonAtCapacity).Error.Details
	if d["open"] != float64(10) || d["max_open"] != float64(10) || w.Header().Get("Retry-After") != "30" {
		t.Errorf("details %v retry-after %q", d, w.Header().Get("Retry-After"))
	}

	// With no leader the queued runs go stale; a start on one's workload
	// takes it and leaves the count as it is.
	s.offset.Store(int64(interrogationQueueTimeout))
	first := decodeBody[apitypes.InterrogationList](t, s.do(t, http.MethodGet, "/api/v1/interrogations?limit=200", "")).Items[interrogationMaxOpen-1]
	s.start(t, "app-0", "")
	if got := s.get(t, first.Id); got.Status != repo.InterrogationFailed || got.EndReason != repo.InterrogationInterrupted {
		t.Errorf("old run %s %s", got.Status, got.EndReason)
	}
	if n := s.openRuns(t); n != interrogationMaxOpen {
		t.Errorf("%d open", n)
	}
	wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations", startBody(fmt.Sprintf("app-%d", interrogationMaxOpen), "")),
		http.StatusServiceUnavailable, reasonAtCapacity)
}

// Concurrent starts on distinct workloads open no more runs than the cap.
func TestStart_CapacityConcurrent(t *testing.T) {
	s := newInterrogationServer(t)
	const n = 30
	for i := range n {
		s.evidence(t, fmt.Sprintf("app-%d", i), 1)
	}
	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			codes[i] = s.do(t, http.MethodPost, "/api/v1/interrogations", startBody(fmt.Sprintf("app-%d", i), "")).Code
		})
	}
	wg.Wait()
	created := 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusServiceUnavailable:
		default:
			t.Errorf("start answered %d", c)
		}
	}
	if open := s.openRuns(t); created != interrogationMaxOpen || open != interrogationMaxOpen {
		t.Errorf("%d created, %d open, want %d", created, open, interrogationMaxOpen)
	}
}

// Starts are refused once the day's runs reach the limit, however they ended,
// and accepted again as the oldest leave the last 24 hours.
func TestStart_DailyLimit(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	s.exec(t, `INSERT INTO adk.sessions (app_name, user_id, id, state, create_time, update_time)
		SELECT 'interrogation', 'old-' || g, 'old-' || g, '{"open": false}', now() - interval '23 hours', now() - interval '23 hours'
		FROM generate_series(1, $1::int) g`, interrogationMaxPerDay)
	w := s.do(t, http.MethodPost, "/api/v1/interrogations", startBody("app", ""))
	if d := wantError(t, w, http.StatusServiceUnavailable, reasonAtCapacity).Error.Details; d["max_per_day"] != float64(interrogationMaxPerDay) {
		t.Errorf("details %v", d)
	}
	s.exec(t, `UPDATE adk.sessions SET create_time = create_time - interval '1 hour' WHERE id LIKE 'old-%'`)
	s.start(t, "app", "")
}

func (s *interrogationServer) stop(t *testing.T, id, body string) (apitypes.Interrogation, int) {
	t.Helper()
	w := s.do(t, http.MethodPost, "/api/v1/interrogations/"+id+"/stop", body)
	if w.Code != http.StatusOK && w.Code != http.StatusAccepted {
		t.Fatalf("stop %s: %d %s", id, w.Code, w.Body.String())
	}
	return decodeBody[apitypes.Interrogation](t, w), w.Code
}

func TestStop(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)

	t.Run("a picked up run is stopping", func(t *testing.T) {
		run := s.start(t, "app", "")
		s.pickUp(t, run.Id)
		got, code := s.stop(t, run.Id, `{"changes":"keep","actor":"alon"}`)
		if code != http.StatusAccepted || got.Status != repo.InterrogationStopping || got.Stop == nil || got.Stop.Actor != "alon" {
			t.Fatalf("%d %+v", code, got)
		}
		// A later revert overrides the keep and keeps the first stop's actor.
		got, code = s.stop(t, run.Id, `{"changes":"revert","actor":"dana"}`)
		if code != http.StatusAccepted || got.Stop.Changes != "revert" || got.Stop.Actor != "alon" {
			t.Fatalf("%d %+v", code, got.Stop)
		}
		s.end(t, run.Id, time.Now())
	})

	t.Run("an ended run keeps how it ended", func(t *testing.T) {
		run := s.start(t, "app", "")
		s.pickUp(t, run.Id)
		s.end(t, run.Id, time.Now())
		got, code := s.stop(t, run.Id, "")
		if code != http.StatusOK || got.Status != "completed" || got.EndReason != "done" || got.Stop == nil {
			t.Fatalf("%d %+v", code, got)
		}
	})

	t.Run("revert on an ended run reverts its probes", func(t *testing.T) {
		run := s.start(t, "app", `,"allow_changes":["custom_instrumentation"]`)
		s.pickUp(t, run.Id)
		change := s.probe(t, run.Id, "app", "a.B.c")
		s.end(t, run.Id, time.Now())
		got, code := s.stop(t, run.Id, `{"changes":"revert","actor":"alon"}`)
		if code != http.StatusOK || got.ChangeCounts.Live != 0 || got.ChangeCounts.Reverted != 1 {
			t.Fatalf("%d %+v", code, got.ChangeCounts)
		}
		c := s.change(t, run.Id, change)
		if c.State != repo.ChangeReverted || c.EndReason != "stop" {
			t.Errorf("change %s %s", c.State, c.EndReason)
		}
	})

	t.Run("a run stopped while queued keeps its first stop", func(t *testing.T) {
		run := s.start(t, "app", "")
		first, _ := s.stop(t, run.Id, `{"changes":"keep","actor":"alon"}`)
		got, code := s.stop(t, run.Id, `{"changes":"revert","actor":"dana"}`)
		if code != http.StatusOK || got.Status != repo.InterrogationStopped || got.Stop == nil || *got.Stop != *first.Stop {
			t.Fatalf("%d %s %+v, first %+v", code, got.Status, got.Stop, first.Stop)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		if w := s.do(t, http.MethodPost, "/api/v1/interrogations/nope/stop", ""); w.Code != http.StatusNotFound {
			t.Errorf("%d %s", w.Code, w.Body.String())
		}
	})

	t.Run("bad changes", func(t *testing.T) {
		run := s.start(t, "app", "")
		wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/stop", `{"changes":"later"}`), 422, reasonInvalidArgument)
		s.stop(t, run.Id, "")
	})
}

// A write that kept losing to other writers is answered from the run as it
// is read afterwards.
func TestInterrogations_RefusedWrites(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	refuse := func(t *testing.T, write string, before func(id string)) {
		s.serve(refusingSessions{Sessions: s.sessions, refuse: write, before: before})
		t.Cleanup(func() { s.serve(s.sessions) })
	}
	stopPath := func(id string) string { return "/api/v1/interrogations/" + id + "/stop" }
	extendPath := func(id string) string { return "/api/v1/interrogations/" + id + "/extend" }

	t.Run("start", func(t *testing.T) {
		refuse(t, "start", nil)
		wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations", startBody("app", "")), http.StatusServiceUnavailable, reasonNotReady)
		if n := s.openRuns(t); n != 0 {
			t.Errorf("%d runs stored", n)
		}
	})

	t.Run("stop on a running run", func(t *testing.T) {
		run := s.start(t, "app", "")
		s.pickUp(t, run.Id)
		refuse(t, "stop", nil)
		wantError(t, s.do(t, http.MethodPost, stopPath(run.Id), ""), http.StatusServiceUnavailable, reasonNotReady)
		if got := s.get(t, run.Id); got.Status != repo.InterrogationRunning {
			t.Errorf("status %s", got.Status)
		}
		s.end(t, run.Id, time.Now())
	})

	t.Run("stop on an ended run", func(t *testing.T) {
		run := s.start(t, "app", "")
		s.pickUp(t, run.Id)
		s.end(t, run.Id, time.Now())
		refuse(t, "stop", nil)
		if got, code := s.stop(t, run.Id, ""); code != http.StatusOK || got.Status != "completed" {
			t.Errorf("%d %s", code, got.Status)
		}
	})

	t.Run("revert on an ended run with no stop recorded", func(t *testing.T) {
		run := s.start(t, "app", `,"allow_changes":["custom_instrumentation"]`)
		s.pickUp(t, run.Id)
		change := s.probe(t, run.Id, "app", "a.B.refused")
		s.end(t, run.Id, time.Now())
		refuse(t, "stop", nil)
		if got, code := s.stop(t, run.Id, `{"changes":"revert","actor":"alon"}`); code != http.StatusOK || got.ChangeCounts.Reverted != 1 {
			t.Errorf("%d %+v", code, got.ChangeCounts)
		}
		if c := s.change(t, run.Id, change); c.State != repo.ChangeReverted || c.EndReason != "stop" {
			t.Errorf("change %s %s", c.State, c.EndReason)
		}
	})

	t.Run("stop on a queued run nothing picks up", func(t *testing.T) {
		run := s.start(t, "app", "")
		refuse(t, "close", nil)
		wantError(t, s.do(t, http.MethodPost, stopPath(run.Id), `{"actor":"alon"}`), http.StatusServiceUnavailable, reasonNotReady)
		if got := s.get(t, run.Id); got.Status != repo.InterrogationQueued {
			t.Errorf("status %s", got.Status)
		}
		s.serve(s.sessions)
		// The retry closes it; the stop recorded by the refused one is the first.
		got, code := s.stop(t, run.Id, `{"actor":"dana"}`)
		if code != http.StatusOK || got.Status != repo.InterrogationStopped || got.Stop == nil || got.Stop.Actor != "alon" {
			t.Errorf("%d %s %+v", code, got.Status, got.Stop)
		}
	})

	t.Run("stop on a queued run the leader picks up meanwhile", func(t *testing.T) {
		run := s.start(t, "app", "")
		refuse(t, "close", func(id string) { s.pickUp(t, id) })
		if got, code := s.stop(t, run.Id, ""); code != http.StatusAccepted || got.Status != repo.InterrogationStopping {
			t.Errorf("%d %s", code, got.Status)
		}
		s.end(t, run.Id, time.Now())
	})

	t.Run("extend", func(t *testing.T) {
		run := s.start(t, "app", "")
		s.pickUp(t, run.Id)
		refuse(t, "extend", nil)
		wantError(t, s.do(t, http.MethodPost, extendPath(run.Id), `{"seconds":600}`), http.StatusServiceUnavailable, reasonNotReady)
		if w := s.do(t, http.MethodPost, extendPath("nope"), `{"seconds":600}`); w.Code != http.StatusNotFound {
			t.Errorf("unknown: %d %s", w.Code, w.Body.String())
		}
		s.end(t, run.Id, time.Now())
		wantError(t, s.do(t, http.MethodPost, extendPath(run.Id), `{"seconds":600}`), http.StatusConflict, reasonNotRunning)
	})

	t.Run("extend on a stopping run", func(t *testing.T) {
		run := s.start(t, "app", "")
		s.pickUp(t, run.Id)
		s.stop(t, run.Id, "")
		refuse(t, "extend", nil)
		wantError(t, s.do(t, http.MethodPost, extendPath(run.Id), `{"seconds":600}`), http.StatusConflict, reasonNotRunning)
		s.end(t, run.Id, time.Now())
	})

	t.Run("start over a stale queued run", func(t *testing.T) {
		old := s.start(t, "app", "")
		t.Cleanup(func() { s.offset.Store(0) })
		s.offset.Store(int64(interrogationQueueTimeout))
		refuse(t, "close", nil)
		body := wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations", startBody("app", "")), http.StatusConflict, reasonSourceBusy)
		if body.Error.Details["interrogation_id"] != old.Id {
			t.Errorf("details %v", body.Error.Details)
		}
		if got := s.get(t, old.Id); got.Status != repo.InterrogationQueued {
			t.Errorf("old run %s", got.Status)
		}
	})
}

func TestExtend(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	run := s.start(t, "app", "")
	s.pickUp(t, run.Id)
	before := s.get(t, run.Id)

	w := s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/extend", `{"seconds":1800,"actor":"alon"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	got := decodeBody[apitypes.Interrogation](t, w)
	if got.ExtendedSeconds != 1800 || !got.DeadlineAt.Equal(before.DeadlineAt.Add(1800*time.Second)) || got.Budget.TimeLimitSeconds != 3600 {
		t.Errorf("extended %d deadline %v (was %v)", got.ExtendedSeconds, got.DeadlineAt, before.DeadlineAt)
	}

	for _, body := range []string{`{"seconds":59}`, `{"seconds":14401}`} {
		if got := wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/extend", body), 422, reasonInvalidArgument); got.Error.Details["field"] != "seconds" {
			t.Errorf("%s: %v", body, got.Error.Details)
		}
	}
	// 3600 + 1800 + 14400 + 14400 > 28800.
	if w := s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/extend", `{"seconds":14400}`); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/extend", `{"seconds":14400}`), 422, reasonInvalidArgument)
	if w := s.do(t, http.MethodPost, "/api/v1/interrogations/nope/extend", `{"seconds":600}`); w.Code != http.StatusNotFound {
		t.Errorf("unknown: %d", w.Code)
	}
	if w := s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/extend", `{`); w.Code != http.StatusBadRequest {
		t.Errorf("malformed: %d", w.Code)
	}

	// The run ended between the page's read and the extend.
	s.end(t, run.Id, time.Now())
	wantError(t, s.do(t, http.MethodPost, "/api/v1/interrogations/"+run.Id+"/extend", `{"seconds":600}`), http.StatusConflict, reasonNotRunning)
}

// change is one change of a run, as the run's detail lists it.
func (s *interrogationServer) change(t *testing.T, runID, id string) apitypes.InterrogationChange {
	t.Helper()
	for _, c := range s.get(t, runID).Changes {
		if c.Id == id {
			return c
		}
	}
	t.Fatalf("run %s has no change %s", runID, id)
	return apitypes.InterrogationChange{}
}

// The detail lists a run's stored changes and the proposals it denied, which
// exist only in its state, and counts both.
func TestInterrogationChanges(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1)
	run := s.start(t, "app", `,"allow_changes":["custom_instrumentation"]`)
	s.pickUp(t, run.Id)
	stored := s.probe(t, run.Id, "app", "a.B.stored")
	denied := "chg_denied"
	st := runState("running", time.Now())
	st["probes"] = []map[string]any{{
		"change_id": denied, "language": "java", "target": "a.B.denied", "status": "denied", "deny_reason": "not_application_code",
		"round": 1, "proposed_at": time.Now(),
	}}
	if err := s.appendRun(context.Background(), run.Id, map[string]any{"run": st}); err != nil {
		t.Fatal(err)
	}

	got := s.get(t, run.Id)
	if len(got.Changes) != 2 || got.Changes[0].Id != stored || got.Changes[0].State != repo.ChangeRequested ||
		got.Changes[1].Id != denied || got.Changes[1].DenyReason != "not_application_code" || len(got.Changes[1].Checks) != 2 {
		t.Fatalf("changes %+v", got.Changes)
	}
	if got.ChangeCounts.Total != 2 || got.ChangeCounts.Denied != 1 || got.ChangeCounts.Live != 1 {
		t.Errorf("counts %+v", got.ChangeCounts)
	}
}

// The list ignores what it does not know and pages with before.
func TestListInterrogations(t *testing.T) {
	s := newInterrogationServer(t)
	var ids []string
	for i := range 5 {
		name := fmt.Sprintf("app-%d", i)
		s.evidence(t, name, 1)
		ids = append([]string{s.start(t, name, "").Id}, ids...)
	}
	var got []string
	before := ""
	for {
		w := s.do(t, http.MethodGet, "/api/v1/interrogations?limit=2&status=failed&goal=security&before="+before, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		page := decodeBody[apitypes.InterrogationList](t, w)
		for _, it := range page.Items {
			got = append(got, it.Id)
		}
		if page.Count < 2 {
			break
		}
		before = page.Items[len(page.Items)-1].Id
	}
	if fmt.Sprint(got) != fmt.Sprint(ids) {
		t.Errorf("pages %v, want %v", got, ids)
	}
	if w := s.do(t, http.MethodGet, "/api/v1/interrogations?limit=abc", ""); w.Code != http.StatusBadRequest {
		t.Errorf("bad limit: %d", w.Code)
	}
}

func TestInterrogationReads(t *testing.T) {
	s := newInterrogationServer(t)
	s.evidence(t, "app", 1, 2)
	run := s.start(t, "app", "")
	s.pickUp(t, run.Id)

	w := s.do(t, http.MethodGet, "/api/v1/interrogations/summary", "")
	sum := decodeBody[apitypes.InterrogationSummary](t, w)
	if w.Code != http.StatusOK || sum.Running != 1 || sum.Total != 1 || len(sum.RunningSources) != 1 {
		t.Fatalf("summary %d %s", w.Code, w.Body.String())
	}
	w = s.do(t, http.MethodGet, "/api/v1/interrogations/sources", "")
	sources := decodeBody[apitypes.InterrogationSourceList](t, w)
	if w.Code != http.StatusOK || sources.Count != 1 || !sources.Items[0].Busy || sources.Items[0].ActiveInterrogationId != run.Id {
		t.Fatalf("sources %d %s", w.Code, w.Body.String())
	}
	w = s.do(t, http.MethodGet, "/api/v1/interrogations/sources/transactions?namespace=ns&kind=Deployment&name=app", "")
	if txs := decodeBody[apitypes.InterrogationSourceTransactionList](t, w); w.Code != http.StatusOK || txs.Count != 2 {
		t.Fatalf("transactions %d %s", w.Code, w.Body.String())
	}
	if w := s.do(t, http.MethodGet, "/api/v1/interrogations/sources/transactions?namespace=ns", ""); w.Code != http.StatusBadRequest {
		t.Errorf("transactions without a source: %d", w.Code)
	}
	if w := s.do(t, http.MethodGet, "/api/v1/interrogations/sources/transactions?namespace=n%00s&kind=Deployment&name=app", ""); w.Code != http.StatusBadRequest {
		t.Errorf("transactions with a NUL in the source: %d %s", w.Code, w.Body.String())
	}
	for _, q := range []string{"after=-1", "after=x", "limit=x"} {
		if w := s.do(t, http.MethodGet, "/api/v1/interrogations/"+run.Id+"/events?"+q, ""); w.Code != http.StatusBadRequest {
			t.Errorf("events?%s: %d", q, w.Code)
		}
	}
	for _, id := range []string{"nope", "%ff", "%00"} {
		for _, r := range []struct{ method, path, body string }{
			{http.MethodGet, "", ""},
			{http.MethodGet, "/events", ""},
			{http.MethodPost, "/stop", ""},
			{http.MethodPost, "/extend", `{"seconds":600}`},
		} {
			if w := s.do(t, r.method, "/api/v1/interrogations/"+id+r.path, r.body); w.Code != http.StatusNotFound {
				t.Errorf("%s run %s%s: %d %s", r.method, id, r.path, w.Code, w.Body.String())
			}
		}
	}
	if w := s.do(t, http.MethodGet, "/api/v1/interrogations?before=%ff", ""); w.Code != http.StatusBadRequest {
		t.Errorf("before=%%ff: %d %s", w.Code, w.Body.String())
	}
}
