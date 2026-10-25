package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

const day = 24 * time.Hour

// soon is an expiry whose two reminders both fall inside the 48h arm window.
var soon = t0.Add(30 * time.Hour)

// ---------------------------------------------------------------- fakes

type slackCall struct {
	Method string
	Body   map[string]any
}

type fakeSlack struct {
	mu        sync.Mutex
	calls     []slackCall
	nextID    int
	fail429   int    // respond 429 this many times first
	failError string // respond ok=false with this error this many times (failErrorN)
	failN     int
}

func (f *fakeSlack) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			http.Error(w, "bad token", 401)
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		method := strings.TrimPrefix(r.URL.Path, "/")
		f.calls = append(f.calls, slackCall{method, body})
		if f.fail429 > 0 {
			f.fail429--
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
			return
		}
		if f.failN > 0 {
			f.failN--
			_, _ = fmt.Fprintf(w, `{"ok":false,"error":%q}`, f.failError)
			return
		}
		switch method {
		case "chat.scheduleMessage":
			f.nextID++
			_, _ = fmt.Fprintf(w, `{"ok":true,"channel":%q,"scheduled_message_id":"Q%d","post_at":%v}`, body["channel"], f.nextID, body["post_at"])
		case "chat.postMessage":
			_, _ = w.Write([]byte(`{"ok":true,"ts":"1.2"}`))
		case "chat.deleteScheduledMessage":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			_, _ = w.Write([]byte(`{"ok":false,"error":"unknown_method"}`))
		}
	})
}

func (f *fakeSlack) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Method)
	}
	return out
}

type fakeIssue struct {
	Number   int      `json:"number"`
	HTMLURL  string   `json:"html_url"`
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	State    string   `json:"state"`
	Labels   []string `json:"-"`
	Comments []string `json:"-"`
	IsPR     bool     `json:"-"`
}

type fakeGitHub struct {
	mu         sync.Mutex
	issues     map[int]*fakeIssue
	next       int
	labels     map[string]bool
	failPatch  bool // PATCH /issues/{n} answers 502
	dropLabels bool // POST /issues ignores labels, as GitHub does without permission
}

func newFakeGitHub() *fakeGitHub {
	return &fakeGitHub{issues: map[int]*fakeIssue{}, labels: map[string]bool{}}
}

func (g *fakeGitHub) add(is *fakeIssue) *fakeIssue {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	is.Number = g.next
	is.HTMLURL = "https://github.test/o/r/issues/" + strconv.Itoa(is.Number)
	if is.State == "" {
		is.State = "open"
	}
	g.issues[is.Number] = is
	return is
}

func (g *fakeGitHub) render(is *fakeIssue) map[string]any {
	labels := []map[string]any{}
	for _, l := range is.Labels {
		labels = append(labels, map[string]any{"name": l})
	}
	m := map[string]any{"number": is.Number, "html_url": is.HTMLURL, "title": is.Title, "body": is.Body, "state": is.State, "labels": labels}
	if is.IsPR {
		m["pull_request"] = map[string]any{"url": "x"}
	}
	return m
}

func (g *fakeGitHub) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer ghs-test" {
			http.Error(w, "bad token", 401)
			return
		}
		var body map[string]any
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		p := strings.TrimPrefix(r.URL.Path, "/repos/o/r/")
		switch {
		case r.Method == "POST" && p == "labels":
			name, _ := body["name"].(string)
			if g.labels[name] {
				http.Error(w, `{"message":"Validation Failed"}`, 422)
				return
			}
			g.labels[name] = true
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == "POST" && p == "issues":
			var labels []string
			for _, l := range body["labels"].([]any) {
				labels = append(labels, l.(string))
			}
			if g.dropLabels {
				labels = nil
			}
			g.mu.Unlock()
			is := g.add(&fakeIssue{Title: body["title"].(string), Body: body["body"].(string), Labels: labels})
			g.mu.Lock()
			w.WriteHeader(201)
			_ = json.NewEncoder(w).Encode(g.render(is))
		case r.Method == "GET" && p == "issues":
			q := r.URL.Query()
			var out []map[string]any
			for n := 1; n <= g.next; n++ {
				is := g.issues[n]
				if is.State != q.Get("state") {
					continue
				}
				has := false
				for _, l := range is.Labels {
					has = has || l == q.Get("labels")
				}
				if has {
					out = append(out, g.render(is))
				}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == "PATCH" && strings.HasPrefix(p, "issues/"):
			if g.failPatch {
				http.Error(w, "bad gateway", 502)
				return
			}
			n, _ := strconv.Atoi(strings.TrimPrefix(p, "issues/"))
			is := g.issues[n]
			if b, ok := body["body"].(string); ok {
				is.Body = b
			}
			if s, ok := body["state"].(string); ok {
				is.State = s
			}
			_ = json.NewEncoder(w).Encode(g.render(is))
		case r.Method == "POST" && strings.HasSuffix(p, "/comments"):
			n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(p, "issues/"), "/comments"))
			g.issues[n].Comments = append(g.issues[n].Comments, body["body"].(string))
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected GitHub call %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected", 500)
		}
	})
}

// ---------------------------------------------------------------- harness

type harness struct {
	slack  *fakeSlack
	gh     *fakeGitHub
	slackS *httptest.Server
	ghS    *httptest.Server
	slept  []time.Duration
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	h := &harness{slack: &fakeSlack{}, gh: newFakeGitHub(), now: t0}
	h.slackS = httptest.NewServer(h.slack.handler())
	h.ghS = httptest.NewServer(h.gh.handler(t))
	t.Cleanup(h.slackS.Close)
	t.Cleanup(h.ghS.Close)
	return h
}

func (h *harness) cfg(command string) config {
	return config{
		command: command, notify: true, slackToken: "xoxb-test", slackChannel: "C123",
		githubToken: "ghs-test", githubAPI: h.ghS.URL, repo: "o/r", label: "onprem-token",
		audience: "Acme", issuedBy: "aviad", runURL: "https://gh.test/run/1",
	}
}

func (h *harness) run(cfg config) (string, error) {
	d := deps{now: func() time.Time { return h.now }, sleep: func(d time.Duration) { h.slept = append(h.slept, d) }, http: http.DefaultClient}
	return run(cfg, d, h.slackS.URL)
}

func (h *harness) register(t *testing.T, exp time.Time, profiles string) (string, error) {
	cfg := h.cfg("register")
	cfg.iat = strconv.FormatInt(t0.Add(-time.Hour).Unix(), 10)
	cfg.exp = strconv.FormatInt(exp.Unix(), 10)
	cfg.profiles = profiles
	return h.run(cfg)
}

func (h *harness) state(t *testing.T, n int) *tracker {
	t.Helper()
	tr, err := parseBody(h.gh.issues[n].Body)
	if err != nil {
		t.Fatalf("issue #%d body unparsable: %v\n%s", n, err, h.gh.issues[n].Body)
	}
	return tr
}

func statuses(tr *tracker) string {
	var s []string
	for _, r := range tr.Reminders {
		s = append(s, r.Status)
	}
	return strings.Join(s, ",")
}

func seed(h *harness, exp time.Time, state string, rs ...reminder) *fakeIssue {
	tr := tracker{Audience: "Seeded", Exp: exp.Unix(), IssuedBy: "x", RunURL: "u", Channel: "C123", Reminders: rs}
	return h.gh.add(&fakeIssue{Title: issueTitle(tr), Body: renderBody(tr), State: state, Labels: []string{"onprem-token"}})
}

// ---------------------------------------------------------------- register

func TestRegisterSchedulesWhenDueSoon(t *testing.T) {
	h := newHarness(t)
	exp := soon
	summary, err := h.register(t, exp, "kratos, semconv")
	if err != nil {
		t.Fatal(err)
	}
	if got := h.slack.methods(); strings.Join(got, ",") != "chat.scheduleMessage,chat.scheduleMessage" {
		t.Fatalf("slack calls = %v", got)
	}
	if got := h.slack.calls[0].Body["post_at"].(float64); int64(got) != exp.Add(-day).Unix() {
		t.Fatalf("24h post_at = %v, want %d", got, exp.Add(-day).Unix())
	}
	if got := h.slack.calls[1].Body["post_at"].(float64); int64(got) != exp.Add(-time.Hour).Unix() {
		t.Fatalf("1h post_at = %v", got)
	}
	text := h.slack.calls[0].Body["text"].(string)
	for _, want := range []string{"*Acme*", "*24 hours*", "kratos, semconv", "issues/1|#1"} {
		if !strings.Contains(text, want) {
			t.Errorf("reminder text missing %q: %s", want, text)
		}
	}
	is := h.gh.issues[1]
	if is.Title != "On-prem token: Acme (expires 2026-10-26)" || is.Labels[0] != "onprem-token" {
		t.Fatalf("issue = %+v", is)
	}
	tr := h.state(t, 1)
	if statuses(tr) != "scheduled,scheduled" || tr.Reminders[0].ScheduledMessageID != "Q1" || tr.Reminders[1].ScheduledMessageID != "Q2" {
		t.Fatalf("state = %+v", tr.Reminders)
	}
	if tr.IssuedAt != t0.Add(-time.Hour).Unix() || !strings.Contains(is.Body, "| Issued | 2026-10-25 11:00 UTC") {
		t.Fatalf("issued at not recorded: %d\n%s", tr.IssuedAt, is.Body)
	}
	if !strings.Contains(summary, "scheduled, id `Q1`") || !strings.Contains(summary, "[#1](") {
		t.Fatalf("summary = %s", summary)
	}
	if !h.gh.labels["onprem-token"] {
		t.Fatal("label was not created")
	}
}

func TestRegisterNotDueSoonStaysPending(t *testing.T) {
	h := newHarness(t)
	summary, err := h.register(t, t0.Add(10*day), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.slack.calls) != 0 {
		t.Fatalf("expected no slack calls, got %v", h.slack.methods())
	}
	if statuses(h.state(t, 1)) != "pending,pending" {
		t.Fatalf("state = %s", statuses(h.state(t, 1)))
	}
	if !strings.Contains(summary, "daily sweep hands it to Slack") {
		t.Fatalf("summary = %s", summary)
	}
}

func TestRegisterWindowBoundary(t *testing.T) {
	h := newHarness(t)
	// 24h reminder lands inside the 48h window (+36h), the 1h reminder outside it (+59h).
	exp := t0.Add(60 * time.Hour)
	if _, err := h.register(t, exp, ""); err != nil {
		t.Fatal(err)
	}
	if statuses(h.state(t, 1)) != "scheduled,pending" {
		t.Fatalf("state = %s", statuses(h.state(t, 1)))
	}
}

func TestRegisterPastRemindersSkipped(t *testing.T) {
	h := newHarness(t)
	summary, err := h.register(t, t0.Add(30*time.Minute), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(h.slack.calls) != 0 || statuses(h.state(t, 1)) != "skipped,skipped" {
		t.Fatalf("calls=%v state=%s", h.slack.methods(), statuses(h.state(t, 1)))
	}
	if !strings.Contains(summary, "already in the past") {
		t.Fatalf("summary = %s", summary)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	for _, exp := range []string{"", "null", "abc", "-5"} {
		h := newHarness(t)
		cfg := h.cfg("register")
		cfg.exp = exp
		if _, err := h.run(cfg); err == nil || !strings.Contains(err.Error(), "EXP") {
			t.Errorf("exp=%q: err = %v, want EXP error", exp, err)
		}
		if len(h.gh.issues) != 0 || len(h.slack.calls) != 0 {
			t.Errorf("exp=%q: side effects happened", exp)
		}
	}
	h := newHarness(t)
	cfg := h.cfg("register")
	cfg.slackToken = ""
	if _, err := h.run(cfg); err == nil || !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Errorf("missing token: err = %v", err)
	}
	h = newHarness(t)
	cfg = h.cfg("register")
	cfg.exp, cfg.iat = strconv.FormatInt(soon.Unix(), 10), "yesterday"
	if _, err := h.run(cfg); err == nil || !strings.Contains(err.Error(), "IAT") {
		t.Errorf("bad iat: err = %v", err)
	}
	h = newHarness(t)
	cfg = h.cfg("register")
	cfg.exp = strconv.FormatInt(soon.Unix(), 10) // no IAT: falls back to now
	if _, err := h.run(cfg); err != nil {
		t.Fatal(err)
	}
	if got := h.state(t, 1).IssuedAt; got != t0.Unix() {
		t.Errorf("iat fallback = %d, want %d", got, t0.Unix())
	}
}

func TestRegisterSurfacesSlackFailureButKeepsTracker(t *testing.T) {
	h := newHarness(t)
	h.slack.failError, h.slack.failN = "channel_not_found", 10
	_, err := h.register(t, soon, "")
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("err = %v", err)
	}
	if statuses(h.state(t, 1)) != "pending,pending" {
		t.Fatalf("tracker should keep the reminders pending for the sweep, got %s", statuses(h.state(t, 1)))
	}
}

// ---------------------------------------------------------------- sweep

func TestSweepArmsPendingDueSoon(t *testing.T) {
	h := newHarness(t)
	exp := t0.Add(40 * time.Hour) // 24h reminder at +16h, 1h reminder at +39h
	seed(h, exp, "open",
		reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusPending},
		reminder{OffsetSeconds: 3600, Label: "1 hour", PostAt: exp.Add(-time.Hour).Unix(), Status: statusPending})
	far := t0.Add(10 * day) // not due for days: must stay pending
	seed(h, far, "open",
		reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: far.Add(-day).Unix(), Status: statusPending},
		reminder{OffsetSeconds: 3600, Label: "1 hour", PostAt: far.Add(-time.Hour).Unix(), Status: statusPending})
	summary, err := h.run(h.cfg("sweep"))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.slack.methods(); strings.Join(got, ",") != "chat.scheduleMessage,chat.scheduleMessage" {
		t.Fatalf("slack calls = %v", got)
	}
	if statuses(h.state(t, 1)) != "scheduled,scheduled" || statuses(h.state(t, 2)) != "pending,pending" {
		t.Fatalf("states = %s / %s", statuses(h.state(t, 1)), statuses(h.state(t, 2)))
	}
	if !strings.Contains(summary, "#1 Seeded") || strings.Contains(summary, "#2 Seeded") {
		t.Fatalf("summary = %s", summary)
	}
}

func TestSweepPostsOverduePending(t *testing.T) {
	h := newHarness(t)
	exp := t0.Add(5 * time.Hour) // 24h reminder is overdue, 1h reminder still ahead
	seed(h, exp, "open",
		reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusPending},
		reminder{OffsetSeconds: 3600, Label: "1 hour", PostAt: exp.Add(-time.Hour).Unix(), Status: statusPending})
	if _, err := h.run(h.cfg("sweep")); err != nil {
		t.Fatal(err)
	}
	if got := h.slack.methods(); strings.Join(got, ",") != "chat.postMessage,chat.scheduleMessage" {
		t.Fatalf("slack calls = %v", got)
	}
	if text := h.slack.calls[0].Body["text"].(string); !strings.Contains(text, "expires in about *5 hours*") {
		t.Fatalf("late text = %s", text)
	}
	if statuses(h.state(t, 1)) != "sent,scheduled" {
		t.Fatalf("state = %s", statuses(h.state(t, 1)))
	}
}

func TestSweepClosesExpired(t *testing.T) {
	h := newHarness(t)
	exp := t0.Add(-2 * time.Hour)
	seed(h, exp, "open",
		reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusScheduled, ScheduledMessageID: "Q9"},
		reminder{OffsetSeconds: 3600, Label: "1 hour", PostAt: exp.Add(-time.Hour).Unix(), Status: statusPending})
	summary, err := h.run(h.cfg("sweep"))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.slack.calls) != 0 {
		t.Fatalf("expired token must not ping Slack, got %v", h.slack.methods())
	}
	is := h.gh.issues[1]
	if is.State != "closed" || len(is.Comments) != 1 || !strings.Contains(is.Comments[0], "Token expired on") {
		t.Fatalf("issue = %+v", is)
	}
	if statuses(h.state(t, 1)) != "scheduled,skipped" || !strings.Contains(summary, "tracker closed") {
		t.Fatalf("state=%s summary=%s", statuses(h.state(t, 1)), summary)
	}
}

func TestSweepCancelsTrackerClosedByHand(t *testing.T) {
	h := newHarness(t)
	exp := t0.Add(10 * day)
	seed(h, exp, "closed",
		reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusScheduled, ScheduledMessageID: "Q7"},
		reminder{OffsetSeconds: 3600, Label: "1 hour", PostAt: exp.Add(-time.Hour).Unix(), Status: statusScheduled, ScheduledMessageID: "Q8"})
	summary, err := h.run(h.cfg("sweep"))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.slack.methods(); strings.Join(got, ",") != "chat.deleteScheduledMessage,chat.deleteScheduledMessage" {
		t.Fatalf("slack calls = %v", got)
	}
	if id := h.slack.calls[0].Body["scheduled_message_id"]; id != "Q7" {
		t.Fatalf("deleted %v, want Q7", id)
	}
	if statuses(h.state(t, 1)) != "cancelled,cancelled" || !strings.Contains(summary, "closed by hand") {
		t.Fatalf("state=%s summary=%s", statuses(h.state(t, 1)), summary)
	}
}

func TestSweepIgnoresPullRequestsAndForeignIssues(t *testing.T) {
	h := newHarness(t)
	h.gh.add(&fakeIssue{Title: "a PR", Body: "x", Labels: []string{"onprem-token"}, IsPR: true})
	h.gh.add(&fakeIssue{Title: "hand written", Body: "no state here", Labels: []string{"onprem-token"}})
	summary, err := h.run(h.cfg("sweep"))
	if err != nil {
		t.Fatal(err)
	}
	if len(h.slack.calls) != 0 || !strings.Contains(summary, "#2: no tracker state block") {
		t.Fatalf("calls=%v summary=%s", h.slack.methods(), summary)
	}
	if h.gh.issues[2].Body != "no state here" {
		t.Fatal("hand-written issue was rewritten")
	}
}

func TestSweepContinuesPastOneFailure(t *testing.T) {
	h := newHarness(t)
	exp := soon
	h.slack.failError, h.slack.failN = "channel_not_found", 1
	for i := 0; i < 2; i++ {
		seed(h, exp, "open", reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusPending})
	}
	_, err := h.run(h.cfg("sweep"))
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("err = %v", err)
	}
	if statuses(h.state(t, 1)) != "pending" || statuses(h.state(t, 2)) != "scheduled" {
		t.Fatalf("states = %s / %s", statuses(h.state(t, 1)), statuses(h.state(t, 2)))
	}
}

func TestRegisterRollsBackWhenTrackerUpdateFails(t *testing.T) {
	h := newHarness(t)
	h.gh.failPatch = true
	_, err := h.register(t, soon, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") || !strings.Contains(err.Error(), "were deleted and stay pending") {
		t.Fatalf("err = %v", err)
	}
	got := strings.Join(h.slack.methods(), ",")
	if got != "chat.scheduleMessage,chat.scheduleMessage,chat.deleteScheduledMessage,chat.deleteScheduledMessage" {
		t.Fatalf("slack calls = %s", got)
	}
	if ids := []any{h.slack.calls[2].Body["scheduled_message_id"], h.slack.calls[3].Body["scheduled_message_id"]}; ids[0] != "Q1" || ids[1] != "Q2" {
		t.Fatalf("rolled back %v", ids)
	}
	if statuses(h.state(t, 1)) != "pending,pending" {
		t.Fatalf("tracker should still read pending, got %s", statuses(h.state(t, 1)))
	}
}

func TestRegisterNotifyOffTracksButMutes(t *testing.T) {
	h := newHarness(t)
	cfg := h.cfg("register")
	cfg.notify = false
	cfg.exp = strconv.FormatInt(soon.Unix(), 10)
	summary, err := h.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.slack.calls) != 0 || statuses(h.state(t, 1)) != "muted,muted" {
		t.Fatalf("calls=%v state=%s", h.slack.methods(), statuses(h.state(t, 1)))
	}
	if !strings.Contains(summary, "muted") {
		t.Fatalf("summary = %s", summary)
	}
	// A later sweep must leave muted reminders alone, even when they are due.
	if _, err := h.run(h.cfg("sweep")); err != nil {
		t.Fatal(err)
	}
	if len(h.slack.calls) != 0 || statuses(h.state(t, 1)) != "muted,muted" {
		t.Fatalf("sweep touched muted reminders: calls=%v state=%s", h.slack.methods(), statuses(h.state(t, 1)))
	}
}

func TestRegisterFailsWhenLabelDidNotStick(t *testing.T) {
	h := newHarness(t)
	h.gh.dropLabels = true
	_, err := h.register(t, soon, "")
	if err == nil || !strings.Contains(err.Error(), "without the \"onprem-token\" label") {
		t.Fatalf("err = %v", err)
	}
	if len(h.slack.calls) != 0 {
		t.Fatalf("must not schedule for an unsweepable tracker, got %v", h.slack.methods())
	}
}

func TestRegisterTwiceReusesExistingLabel(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 2; i++ {
		if _, err := h.register(t, soon, ""); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}
	if len(h.gh.issues) != 2 {
		t.Fatalf("issues = %d", len(h.gh.issues))
	}
}

func TestSweepRollsBackWhenTrackerUpdateFails(t *testing.T) {
	h := newHarness(t)
	exp := soon
	seed(h, exp, "open", reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusPending})
	h.gh.failPatch = true
	_, err := h.run(h.cfg("sweep"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("err = %v", err)
	}
	if got := strings.Join(h.slack.methods(), ","); got != "chat.scheduleMessage,chat.deleteScheduledMessage" {
		t.Fatalf("slack calls = %s", got)
	}
}

func TestSlackGivesUpAfterRepeatedRateLimits(t *testing.T) {
	h := newHarness(t)
	h.slack.fail429 = 10
	_, err := h.register(t, soon, "")
	if err == nil || !strings.Contains(err.Error(), "ratelimited") {
		t.Fatalf("err = %v", err)
	}
	// 5 attempts per reminder, 4 sleeps each; never an endless loop.
	if len(h.slack.calls) != 10 || len(h.slept) != 8 {
		t.Fatalf("calls=%d slept=%d", len(h.slack.calls), len(h.slept))
	}
}

func TestSlackDoesNotRetryRestrictedTooMany(t *testing.T) {
	h := newHarness(t)
	h.slack.failError, h.slack.failN = "restricted_too_many", 10
	_, err := h.register(t, soon, "")
	if err == nil || !strings.Contains(err.Error(), "restricted_too_many") {
		t.Fatalf("err = %v", err)
	}
	if len(h.slack.calls) != 2 || len(h.slept) != 0 {
		t.Fatalf("calls=%d slept=%d; must fail fast", len(h.slack.calls), len(h.slept))
	}
	if statuses(h.state(t, 1)) != "pending,pending" {
		t.Fatalf("state = %s", statuses(h.state(t, 1)))
	}
}

func TestDeleteToleratesAlreadyGoneMessage(t *testing.T) {
	h := newHarness(t)
	exp := t0.Add(10 * day)
	seed(h, exp, "closed", reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusScheduled, ScheduledMessageID: "Q1"})
	h.slack.failError, h.slack.failN = "invalid_scheduled_message_id", 1
	if _, err := h.run(h.cfg("sweep")); err != nil {
		t.Fatal(err)
	}
	if statuses(h.state(t, 1)) != "cancelled" {
		t.Fatalf("state = %s", statuses(h.state(t, 1)))
	}
}

func TestParseBodyToleratesCRLF(t *testing.T) {
	tr := tracker{Audience: "Acme", Exp: 1800000000, Reminders: []reminder{{86400, "24 hours", 1799913600, statusPending, ""}}}
	crlf := strings.ReplaceAll(renderBody(tr), "\n", "\r\n")
	got, err := parseBody("edited by a human\r\n" + crlf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Audience != "Acme" || statuses(got) != "pending" {
		t.Fatalf("got %+v", got)
	}
}

// ---------------------------------------------------------------- pieces

func TestSlackRetriesRateLimits(t *testing.T) {
	h := newHarness(t)
	h.slack.fail429 = 1
	h.slack.failError, h.slack.failN = "ratelimited", 1
	if _, err := h.register(t, soon, ""); err != nil {
		t.Fatal(err)
	}
	// 429 then ok=false ratelimited then success for the first reminder, plus the second.
	if got := len(h.slack.calls); got != 4 {
		t.Fatalf("slack calls = %d (%v)", got, h.slack.methods())
	}
	// Retry-After honoured on the 429; the ok=false rate error is attempt two, so it backs off two minutes.
	if len(h.slept) != 2 || h.slept[0] != 7*time.Second || h.slept[1] != 2*time.Minute {
		t.Fatalf("slept = %v", h.slept)
	}
}

func TestMentionsAreTaggedOnReminders(t *testing.T) {
	h := newHarness(t)
	cfg := h.cfg("register")
	cfg.mentions = splitIDs("U08CUJYCXUY, U0A0SAD7CLS,bogus,<@U0EVIL>")
	cfg.exp = strconv.FormatInt(soon.Unix(), 10)
	if _, err := h.run(cfg); err != nil {
		t.Fatal(err)
	}
	text := h.slack.calls[0].Body["text"].(string)
	if !strings.HasSuffix(text, " cc <@U08CUJYCXUY> <@U0A0SAD7CLS>") {
		t.Fatalf("mentions missing or malformed: %s", text)
	}
	// No mentions configured: no trailing cc.
	h2 := newHarness(t)
	if _, err := h2.register(t, soon, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(h2.slack.calls[0].Body["text"].(string), " cc ") {
		t.Fatal("unexpected cc without mentions")
	}
}

func TestMrkdwnEscaping(t *testing.T) {
	tr := tracker{Audience: "AT&T <x>", Exp: t0.Unix(), Profiles: "a<b", IssuedBy: "o&o", RunURL: "https://r"}
	text := reminderText(tr, reminder{Label: "1 hour"}, &issue{Number: 3, HTMLURL: "https://i/3"})
	for _, want := range []string{"*AT&amp;T &lt;x&gt;*", "Profiles: a&lt;b", "Issued by o&amp;o", "<https://r|this run>"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in %s", want, text)
		}
	}
}

func TestBodyRoundTrip(t *testing.T) {
	tr := tracker{Audience: "Acme | Inc", IssuedAt: 1797000000, Exp: 1800000000, Profiles: "kratos", IssuedBy: "aviad", RunURL: "https://r", Channel: "C1",
		Reminders: []reminder{{86400, "24 hours", 1799913600, statusScheduled, "Q1"}, {3600, "1 hour", 1799996400, statusPending, ""}}}
	body := renderBody(tr)
	got, err := parseBody(body)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(*got) != fmt.Sprint(tr) {
		t.Fatalf("round trip mismatch:\n%v\n%v", *got, tr)
	}
	if !strings.Contains(body, "| Audience | `Acme | Inc` |") || !strings.Contains(body, "Closing this issue cancels") {
		t.Fatalf("body = %s", body)
	}
}

func TestHumanDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{3 * day: "3 days", 47 * time.Hour: "47 hours", 90 * time.Minute: "90 minutes"} {
		if got := humanDuration(d); got != want {
			t.Errorf("%v = %q, want %q", d, got, want)
		}
	}
}
