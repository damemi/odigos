package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBoard is the GraphQL side of a Projects v2 board with the three fields the
// tool needs. It records every mutation.
type fakeBoard struct {
	mu        sync.Mutex
	resolves  int              // project/field lookups served
	calls     []map[string]any // variables of each mutation, with "op" added
	noSoon    bool             // omit the Soon option, as a misconfigured board would
	nextItem  int
	authToken string
}

func (f *fakeBoard) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+f.authToken {
			http.Error(w, "bad token", 401)
			return
		}
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		switch {
		case strings.Contains(req.Query, "projectV2(number:"):
			f.resolves++
			options := `[{"id":"opt-active","name":"Active"},{"id":"opt-soon","name":"Expires soon"},{"id":"opt-expired","name":"Expired"}]`
			if f.noSoon {
				options = `[{"id":"opt-active","name":"Active"},{"id":"opt-expired","name":"Expired"}]`
			}
			_, _ = io.WriteString(w, `{"data":{"organization":{"projectV2":{"id":"PVT_1","fields":{"nodes":[{"id":"F_title","name":"Title"},{"id":"F_status","name":"Status","options":`+options+`},{"id":"F_issued","name":"Issued"},{"id":"F_expires","name":"Expires"}]}}}}}`)
		case strings.Contains(req.Query, "addProjectV2ItemById"):
			f.nextItem++
			req.Variables["op"] = "add"
			f.calls = append(f.calls, req.Variables)
			_, _ = io.WriteString(w, `{"data":{"addProjectV2ItemById":{"item":{"id":"PVTI_`+strconv.Itoa(f.nextItem)+`"}}}}`)
		case strings.Contains(req.Query, "updateProjectV2ItemFieldValue"):
			if _, ok := req.Variables["date"]; ok {
				req.Variables["op"] = "date"
			} else {
				req.Variables["op"] = "status"
			}
			f.calls = append(f.calls, req.Variables)
			_, _ = io.WriteString(w, `{"data":{"updateProjectV2ItemFieldValue":{"projectV2Item":{"id":"x"}}}}`)
		default:
			t.Errorf("unexpected GraphQL query: %s", req.Query)
			_, _ = io.WriteString(w, `{"errors":[{"message":"unexpected query"}]}`)
		}
	})
}

func (f *fakeBoard) ops() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		op := c["op"].(string)
		switch op {
		case "date":
			out = append(out, "date:"+c["field"].(string)+"="+c["date"].(string))
		case "status":
			out = append(out, "status:"+c["option"].(string))
		default:
			out = append(out, op)
		}
	}
	return strings.Join(out, " ")
}

func withBoard(t *testing.T, h *harness) *fakeBoard {
	t.Helper()
	fb := &fakeBoard{authToken: "ghs-project"}
	srv := httptest.NewServer(fb.handler(t))
	t.Cleanup(srv.Close)
	h.boardURL = srv.URL
	return fb
}

func TestBoardStatus(t *testing.T) {
	for exp, want := range map[time.Time]string{
		t0.Add(60 * day):             boardActive,
		t0.Add(30 * day):             boardSoon,
		t0.Add(10 * day):             boardSoon,
		t0.Add(-1 * time.Hour):       "",
		t0.Add(30*day + time.Minute): boardActive,
	} {
		if got := boardStatus(tracker{Exp: exp.Unix()}, t0); got != want {
			t.Errorf("exp %v: got %q want %q", exp.Sub(t0), got, want)
		}
	}
}

func TestRegisterSyncsBoardActive(t *testing.T) {
	h := newHarness(t)
	fb := withBoard(t, h)
	exp := t0.Add(60 * day)
	summary, err := h.register(t, exp, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "add date:F_issued=2026-10-25 date:F_expires=2026-12-24 status:opt-active"
	if got := fb.ops(); !sameOps(got, want) {
		t.Fatalf("board ops = %s, want %s", got, want)
	}
	if fb.calls[0]["content"] != "NODE_1" {
		t.Fatalf("added content %v, want the issue node id", fb.calls[0]["content"])
	}
	if h.state(t, 1).BoardStatus != boardActive || !strings.Contains(summary, "board: Active") {
		t.Fatalf("state=%q summary=%s", h.state(t, 1).BoardStatus, summary)
	}
}

func TestRegisterSyncsBoardSoonUnderAMonth(t *testing.T) {
	h := newHarness(t)
	fb := withBoard(t, h)
	if _, err := h.register(t, t0.Add(10*day), ""); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fb.ops(), "status:opt-soon") || h.state(t, 1).BoardStatus != boardSoon {
		t.Fatalf("ops=%s state=%s", fb.ops(), h.state(t, 1).BoardStatus)
	}
}

func TestSweepFlipsActiveToSoonOnce(t *testing.T) {
	h := newHarness(t)
	fb := withBoard(t, h)
	exp := t0.Add(20 * day)
	is := seed(h, exp, "open",
		reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusPending},
		reminder{OffsetSeconds: 3600, Label: "1 hour", PostAt: exp.Add(-time.Hour).Unix(), Status: statusPending})
	tr, _ := parseBody(is.Body)
	tr.BoardStatus = boardActive // synced a while ago, when more than a month remained
	is.Body = renderBody(*tr)
	summary, err := h.run(h.cfg("sweep"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fb.ops(), "status:opt-soon") || h.state(t, 1).BoardStatus != boardSoon || !strings.Contains(summary, "board: Expires soon") {
		t.Fatalf("ops=%s state=%s summary=%s", fb.ops(), h.state(t, 1).BoardStatus, summary)
	}
	before := len(fb.calls)
	if _, err := h.run(h.cfg("sweep")); err != nil {
		t.Fatal(err)
	}
	if len(fb.calls) != before {
		t.Fatalf("second sweep wrote to the board again: %s", fb.ops())
	}
}

func TestSweepSyncsTrackersThatNeverReachedTheBoard(t *testing.T) {
	h := newHarness(t)
	fb := withBoard(t, h)
	exp := t0.Add(90 * day)
	seed(h, exp, "open", reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusPending})
	if _, err := h.run(h.cfg("sweep")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fb.ops(), "date:F_expires=") || !strings.HasSuffix(fb.ops(), "status:opt-active") {
		t.Fatalf("ops = %s", fb.ops())
	}
}

func TestBoardDisabledWithoutToken(t *testing.T) {
	h := newHarness(t)
	fb := withBoard(t, h)
	cfg := h.cfg("register")
	cfg.projectToken = ""
	cfg.exp = strconv.FormatInt(t0.Add(60*day).Unix(), 10)
	summary, err := h.run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(fb.calls) != 0 || !strings.Contains(summary, "board: not configured") {
		t.Fatalf("calls=%d summary=%s", len(fb.calls), summary)
	}
}

func TestBoardMissingOptionFailsLoudly(t *testing.T) {
	h := newHarness(t)
	fb := withBoard(t, h)
	fb.noSoon = true
	_, err := h.register(t, t0.Add(60*day), "")
	if err == nil || !strings.Contains(err.Error(), "Status option Expires soon") {
		t.Fatalf("err = %v", err)
	}
	if len(h.gh.issues) != 1 {
		t.Fatal("the tracker must still exist when the board is misconfigured")
	}
}

func TestResolveFailureIsNotRepeatedPerTracker(t *testing.T) {
	h := newHarness(t)
	fb := withBoard(t, h)
	fb.noSoon = true
	for i := 0; i < 3; i++ {
		exp := t0.Add(time.Duration(60+i) * day)
		seed(h, exp, "open", reminder{OffsetSeconds: 86400, Label: "24 hours", PostAt: exp.Add(-day).Unix(), Status: statusPending})
	}
	_, err := h.run(h.cfg("sweep"))
	if err == nil || strings.Count(err.Error(), "Status option Expires soon") != 3 {
		t.Fatalf("each tracker should report the board error once: %v", err)
	}
	if fb.resolves != 1 {
		t.Fatalf("resolve ran %d times, want 1", fb.resolves)
	}
}

// sameOps compares op strings ignoring the order of the two date writes, which
// come from a map.
func sameOps(got, want string) bool {
	g, w := strings.Fields(got), strings.Fields(want)
	if len(g) != len(w) {
		return false
	}
	seen := map[string]int{}
	for _, x := range g {
		seen[x]++
	}
	for _, x := range w {
		seen[x]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}
