// Command onprem-token-reminders keeps Slack informed about expiring on-prem tokens.
//
//	register  run by the Generate On-Prem Token workflow: records the token in a
//	          tracker issue and schedules the reminders Slack can already hold.
//	sweep     run daily: arms reminders that have come inside Slack's scheduling
//	          two-day window, posts the ones a missed sweep left behind, closes trackers
//	          of expired tokens and cancels reminders of trackers closed by hand.
//
// The tracker is one GitHub issue per token, labelled onprem-token, whose body
// carries the machine state inside an HTML comment. Slack's chat.scheduleMessage
// is the timer and delivers at the exact second, but Slack only ever holds the
// reminders due within the next two days: everything further out stays
// "pending" in the tracker and the daily sweep hands it to Slack the day before
// it fires. That keeps Slack's queue tiny (it caps post_at at 120 days and at 30
// messages per 5-minute window per channel) and makes cancelling cheap.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// Reminders are handed to Slack only when due within this window: the sweep
	// runs daily, so two days covers the cadence plus any cron drift.
	armWindow = 48 * time.Hour
	// A reminder closer than this is not worth scheduling: it is "now".
	minLead         = time.Minute
	statusPending   = "pending"   // not due soon; the sweep hands it to Slack the day before
	statusScheduled = "scheduled" // Slack holds it
	statusSent      = "sent"      // posted directly by the sweep after its time passed while pending
	statusSkipped   = "skipped"   // already in the past when the token was registered
	statusCancelled = "cancelled" // tracker closed by hand before it fired
	statusMuted     = "muted"     // the token was issued with notify off; tracked, never reminded

	stateOpen  = "<!-- onprem-token-state\n"
	stateClose = "\n-->"
)

var reminderOffsets = []struct {
	seconds int64
	label   string
}{
	{86400, "24 hours"},
	{3600, "1 hour"},
}

type reminder struct {
	OffsetSeconds      int64  `json:"offset_seconds"`
	Label              string `json:"label"`
	PostAt             int64  `json:"post_at"`
	Status             string `json:"status"`
	ScheduledMessageID string `json:"scheduled_message_id,omitempty"`
}

type tracker struct {
	Audience  string     `json:"audience"`
	IssuedAt  int64      `json:"iat"`
	Exp       int64      `json:"exp"`
	Profiles  string     `json:"profiles"`
	IssuedBy  string     `json:"issued_by"`
	RunURL    string     `json:"run_url"`
	Channel   string     `json:"channel"`
	Reminders []reminder `json:"reminders"`
}

type config struct {
	command      string
	slackToken   string
	slackChannel string
	mentions     []string // Slack user IDs tagged at the end of every reminder
	githubToken  string
	githubAPI    string
	repo         string
	label        string
	summaryPath  string

	// register only
	audience string
	notify   bool
	iat      string
	exp      string
	profiles string
	issuedBy string
	runURL   string
}

type deps struct {
	now   func() time.Time
	sleep func(time.Duration)
	http  *http.Client
}

func main() {
	cfg := config{
		command:      strings.Join(os.Args[1:], " "),
		slackToken:   os.Getenv("SLACK_BOT_TOKEN"),
		slackChannel: os.Getenv("SLACK_CHANNEL_ID"),
		mentions:     splitIDs(os.Getenv("SLACK_MENTIONS")),
		githubToken:  os.Getenv("GITHUB_TOKEN"),
		githubAPI:    envOr("GITHUB_API_URL", "https://api.github.com"),
		repo:         os.Getenv("GITHUB_REPOSITORY"),
		label:        envOr("TRACKER_LABEL", "onprem-token"),
		summaryPath:  os.Getenv("GITHUB_STEP_SUMMARY"),
		audience:     os.Getenv("AUD"),
		notify:       os.Getenv("NOTIFY") != "false", // the tracker always exists; this only decides reminders
		iat:          os.Getenv("IAT"),
		exp:          os.Getenv("EXP"),
		profiles:     os.Getenv("PROFILES"),
		issuedBy:     os.Getenv("ISSUED_BY"),
		runURL:       os.Getenv("RUN_URL"),
	}
	d := deps{now: time.Now, sleep: time.Sleep, http: &http.Client{Timeout: 30 * time.Second}}
	summary, err := run(cfg, d, "https://slack.com/api")
	if summary != "" {
		writeSummary(cfg.summaryPath, summary)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
		os.Exit(1)
	}
}

// splitIDs turns "U1, U2" into Slack user IDs, dropping anything that does not
// look like one so a typo cannot inject text into the message.
func splitIDs(s string) []string {
	var ids []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if (strings.HasPrefix(f, "U") || strings.HasPrefix(f, "W")) && len(f) >= 9 && !strings.ContainsAny(f, "<>|&") {
			ids = append(ids, f)
		}
	}
	return ids
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func writeSummary(path, text string) {
	if path == "" {
		fmt.Print(text)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Print(text)
		return
	}
	defer f.Close()
	_, _ = f.WriteString(text)
}

// run dispatches a command. It returns the job-summary text (possibly partial on
// error) and the first fatal error.
func run(cfg config, d deps, slackAPI string) (string, error) {
	var missing []string
	for _, kv := range [][2]string{
		{"SLACK_BOT_TOKEN", cfg.slackToken}, {"SLACK_CHANNEL_ID", cfg.slackChannel},
		{"GITHUB_TOKEN", cfg.githubToken}, {"GITHUB_REPOSITORY", cfg.repo},
	} {
		if kv[1] == "" {
			missing = append(missing, kv[0])
		}
	}
	if len(missing) > 0 {
		return "", fmt.Errorf("missing required environment: %s", strings.Join(missing, ", "))
	}
	slack := &slackClient{base: slackAPI, token: cfg.slackToken, channel: cfg.slackChannel, mentions: cfg.mentions, http: d.http, sleep: d.sleep}
	gh := &githubClient{base: strings.TrimRight(cfg.githubAPI, "/"), token: cfg.githubToken, repo: cfg.repo, http: d.http}
	switch cfg.command {
	case "register":
		return register(cfg, d, slack, gh)
	case "sweep":
		return sweep(cfg, d, slack, gh)
	default:
		return "", fmt.Errorf("usage: onprem-token-reminders register|sweep (got %q)", cfg.command)
	}
}

// ---------------------------------------------------------------- register

func register(cfg config, d deps, slack *slackClient, gh *githubClient) (string, error) {
	if cfg.audience == "" {
		return "", errors.New("AUD is empty")
	}
	exp, err := strconv.ParseInt(strings.TrimSpace(cfg.exp), 10, 64)
	if err != nil || exp <= 0 {
		return "", fmt.Errorf("EXP must be the token's unix expiry, got %q", cfg.exp)
	}
	now := d.now()
	iat := now.Unix() // the JWT's iat when given; the run time is a fine stand-in
	if strings.TrimSpace(cfg.iat) != "" {
		if iat, err = strconv.ParseInt(strings.TrimSpace(cfg.iat), 10, 64); err != nil || iat <= 0 {
			return "", fmt.Errorf("IAT must be the token's unix issue time, got %q", cfg.iat)
		}
	}
	t := tracker{
		Audience: cfg.audience, IssuedAt: iat, Exp: exp, Profiles: cfg.profiles,
		IssuedBy: cfg.issuedBy, RunURL: cfg.runURL, Channel: cfg.slackChannel,
	}
	for _, o := range reminderOffsets {
		r := reminder{OffsetSeconds: o.seconds, Label: o.label, PostAt: exp - o.seconds, Status: statusPending}
		switch {
		case !cfg.notify:
			r.Status = statusMuted
		case time.Unix(r.PostAt, 0).Before(now.Add(minLead)):
			r.Status = statusSkipped
		}
		t.Reminders = append(t.Reminders, r)
	}

	if err := gh.ensureLabel(cfg.label); err != nil {
		return "", err
	}
	issue, err := gh.createIssue(issueTitle(t), renderBody(t), cfg.label)
	if err != nil {
		return "", err
	}

	changed, lines, newIDs, errs := armReminders(&t, issue, now, slack)
	if changed {
		if err := gh.updateIssue(issue.Number, map[string]any{"body": renderBody(t)}); err != nil {
			errs = append(errs, err, rollback(slack, newIDs))
		}
	}
	summary := fmt.Sprintf("\n### Slack reminders\nTracker: [#%d](%s) · channel `%s`\n%s\n", issue.Number, issue.HTMLURL, cfg.slackChannel, strings.Join(lines, "\n"))
	return summary, errors.Join(errs...)
}

// armReminders schedules every pending reminder due within armWindow,
// posts those whose time already passed (a missed sweep) and reports each one.
// newIDs lists the Slack messages scheduled by this call so the caller can
// roll them back if recording the new state fails; otherwise the next sweep
// would see "pending" again and schedule duplicates.
func armReminders(t *tracker, issue *issue, now time.Time, slack *slackClient) (changed bool, lines []string, newIDs []string, errs []error) {
	for i := range t.Reminders {
		r := &t.Reminders[i]
		when := humanTime(r.PostAt)
		switch r.Status {
		case statusPending:
			postAt := time.Unix(r.PostAt, 0)
			switch {
			case postAt.Before(now.Add(minLead)):
				// Its moment passed while it waited for its window. Say so now
				// rather than never, unless the token itself is already gone.
				if time.Unix(t.Exp, 0).Before(now) {
					r.Status = statusSkipped
					lines = append(lines, fmt.Sprintf("- %s before expiry (%s): token already expired, skipped", r.Label, when))
				} else if err := slack.postMessage(lateText(*t, *r, issue, now) + slack.cc()); err != nil {
					errs = append(errs, err)
					lines = append(lines, fmt.Sprintf("- %s before expiry (%s): posting failed, will retry next sweep", r.Label, when))
					continue
				} else {
					r.Status = statusSent
					lines = append(lines, fmt.Sprintf("- %s before expiry (%s): was overdue, posted now", r.Label, when))
				}
				changed = true
			case postAt.After(now.Add(armWindow)):
				lines = append(lines, fmt.Sprintf("- %s before expiry (%s): not due within two days, the daily sweep hands it to Slack the day before", r.Label, when))
			default:
				id, err := slack.scheduleMessage(r.PostAt, reminderText(*t, *r, issue)+slack.cc())
				if err != nil {
					errs = append(errs, err)
					lines = append(lines, fmt.Sprintf("- %s before expiry (%s): scheduling failed, will retry next sweep", r.Label, when))
					continue
				}
				r.Status, r.ScheduledMessageID = statusScheduled, id
				changed = true
				newIDs = append(newIDs, id)
				lines = append(lines, fmt.Sprintf("- %s before expiry (%s): scheduled, id `%s`", r.Label, when, id))
			}
		case statusSkipped:
			lines = append(lines, fmt.Sprintf("- %s before expiry (%s): already in the past, skipped", r.Label, when))
		case statusMuted:
			lines = append(lines, fmt.Sprintf("- %s before expiry (%s): muted, the token was issued with notify off", r.Label, when))
		default:
			lines = append(lines, fmt.Sprintf("- %s before expiry (%s): %s", r.Label, when, r.Status))
		}
	}
	return changed, lines, newIDs, errs
}

// rollback deletes Slack messages scheduled in a run whose state could not be
// recorded. It returns a combined error so the failure stays visible.
func rollback(slack *slackClient, ids []string) error {
	var errs []error
	for _, id := range ids {
		if err := slack.deleteScheduledMessage(id); err != nil {
			errs = append(errs, fmt.Errorf("rollback of scheduled message %s failed, it may fire twice: %w", id, err))
		}
	}
	if len(ids) > 0 && len(errs) == 0 {
		return fmt.Errorf("tracker update failed; %d freshly scheduled reminder(s) were deleted and stay pending for the next sweep", len(ids))
	}
	return errors.Join(errs...)
}

// ---------------------------------------------------------------- sweep

func sweep(cfg config, d deps, slack *slackClient, gh *githubClient) (string, error) {
	now := d.now()
	var lines []string
	var errs []error

	open, err := gh.listIssues("open", cfg.label)
	if err != nil {
		return "", err
	}
	for _, is := range open {
		t, err := parseBody(is.Body)
		if err != nil {
			lines = append(lines, fmt.Sprintf("- #%d: %v, left alone", is.Number, err))
			continue
		}
		changed, rl, newIDs, rerrs := armReminders(t, is, now, slack)
		errs = append(errs, rerrs...)
		expired := time.Unix(t.Exp, 0).Before(now)
		if changed || expired {
			patch := map[string]any{"body": renderBody(*t)}
			if expired {
				patch["state"] = "closed"
				patch["state_reason"] = "completed"
			}
			if err := gh.updateIssue(is.Number, patch); err != nil {
				errs = append(errs, err, rollback(slack, newIDs))
				continue
			}
			if expired {
				if err := gh.comment(is.Number, fmt.Sprintf("Token expired on %s. Closing the tracker.", humanTime(t.Exp))); err != nil {
					errs = append(errs, err)
				}
				rl = append(rl, "- token expired, tracker closed")
			}
		}
		if changed || expired {
			lines = append(lines, fmt.Sprintf("- #%d %s (expires %s)", is.Number, t.Audience, humanTime(t.Exp)))
			for _, l := range rl {
				lines = append(lines, "  "+l)
			}
		}
	}

	// Trackers closed by a human while Slack still held reminders: cancel them.
	// All closed trackers are listed (one per token ever issued, cheap) so a run
	// of failed sweeps cannot let a cancelled token's reminders slip through.
	closed, err := gh.listIssues("closed", cfg.label)
	if err != nil {
		errs = append(errs, err)
	}
	for _, is := range closed {
		t, err := parseBody(is.Body)
		if err != nil {
			continue
		}
		changed := false
		for i := range t.Reminders {
			r := &t.Reminders[i]
			if r.Status != statusScheduled || !time.Unix(r.PostAt, 0).After(now) {
				continue
			}
			if err := slack.deleteScheduledMessage(r.ScheduledMessageID); err != nil {
				errs = append(errs, err)
				continue
			}
			r.Status, r.ScheduledMessageID, changed = statusCancelled, "", true
			lines = append(lines, fmt.Sprintf("- #%d %s: cancelled the %s reminder (tracker closed by hand)", is.Number, t.Audience, r.Label))
		}
		if changed {
			if err := gh.updateIssue(is.Number, map[string]any{"body": renderBody(*t)}); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if len(lines) == 0 {
		lines = []string{fmt.Sprintf("- %d open tracker(s), nothing to do", len(open))}
	}
	return fmt.Sprintf("\n### On-prem token reminder sweep\n%s\n", strings.Join(lines, "\n")), errors.Join(errs...)
}

// ---------------------------------------------------------------- texts

func issueTitle(t tracker) string {
	return fmt.Sprintf("On-prem token: %s (expires %s)", t.Audience, time.Unix(t.Exp, 0).UTC().Format("2006-01-02"))
}

// Bodies edited in GitHub's web editor come back with CRLF line endings, so the
// markers tolerate an optional \r; json.Unmarshal ignores the \r inside.
var stateRE = regexp.MustCompile(`(?s)<!-- onprem-token-state\r?\n(.*?)\r?\n-->`)

func renderBody(t tracker) string {
	state, _ := json.MarshalIndent(t, "", "  ")
	profiles := t.Profiles
	if profiles == "" {
		profiles = "none"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "| Field | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Audience | `%s` |\n", t.Audience)
	fmt.Fprintf(&b, "| Issued | %s (`%d`) |\n", humanTime(t.IssuedAt), t.IssuedAt)
	fmt.Fprintf(&b, "| Expires | %s (`%d`) |\n", humanTime(t.Exp), t.Exp)
	fmt.Fprintf(&b, "| Profiles | %s |\n", profiles)
	fmt.Fprintf(&b, "| Issued by | %s via [workflow run](%s) |\n", t.IssuedBy, t.RunURL)
	fmt.Fprintf(&b, "| Reminders | ")
	for i, r := range t.Reminders {
		if i > 0 {
			b.WriteString(" · ")
		}
		fmt.Fprintf(&b, "%s before (%s): %s", r.Label, humanTime(r.PostAt), r.Status)
	}
	b.WriteString(" |\n\n")
	b.WriteString("Reminders go to the Slack channel the workflow is configured with. Closing this issue cancels reminders that have not fired yet; the daily sweep closes it once the token has expired.\n\n")
	b.WriteString(stateOpen)
	b.Write(state)
	b.WriteString(stateClose)
	b.WriteString("\n")
	return b.String()
}

func parseBody(body string) (*tracker, error) {
	m := stateRE.FindStringSubmatch(body)
	if m == nil {
		return nil, errors.New("no tracker state block in body")
	}
	var t tracker
	if err := json.Unmarshal([]byte(m[1]), &t); err != nil {
		return nil, fmt.Errorf("tracker state is not valid JSON: %w", err)
	}
	return &t, nil
}

func humanTime(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04 UTC")
}

func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
}

var mrkdwnEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func reminderText(t tracker, r reminder, is *issue) string {
	return fmt.Sprintf(":hourglass_flowing_sand: On-prem token for *%s* expires in *%s* (%s). Profiles: %s. Issued by %s via <%s|this run>. Tracker: <%s|#%d>. Generate a replacement before then.",
		mrkdwnEscaper.Replace(t.Audience), r.Label, humanTime(t.Exp), mrkdwnEscaper.Replace(orNone(t.Profiles)), mrkdwnEscaper.Replace(t.IssuedBy), t.RunURL, is.HTMLURL, is.Number)
}

func lateText(t tracker, r reminder, is *issue, now time.Time) string {
	remaining := humanDuration(time.Unix(t.Exp, 0).Sub(now))
	return fmt.Sprintf(":warning: On-prem token for *%s* expires in about *%s* (%s); its %s reminder was not delivered in time. Profiles: %s. Issued by %s via <%s|this run>. Tracker: <%s|#%d>.",
		mrkdwnEscaper.Replace(t.Audience), remaining, humanTime(t.Exp), r.Label, mrkdwnEscaper.Replace(orNone(t.Profiles)), mrkdwnEscaper.Replace(t.IssuedBy), t.RunURL, is.HTMLURL, is.Number)
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// ---------------------------------------------------------------- Slack

type slackClient struct {
	base, token, channel string
	mentions             []string
	http                 *http.Client
	sleep                func(time.Duration)
}

// cc renders the configured mentions as a trailing " cc <@U…> <@U…>", or nothing.
func (s *slackClient) cc() string {
	if len(s.mentions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" cc")
	for _, id := range s.mentions {
		fmt.Fprintf(&b, " <@%s>", id)
	}
	return b.String()
}

func (s *slackClient) scheduleMessage(postAt int64, text string) (string, error) {
	resp, err := s.call("chat.scheduleMessage", map[string]any{"channel": s.channel, "post_at": postAt, "text": text})
	if err != nil {
		return "", err
	}
	id, _ := resp["scheduled_message_id"].(string)
	if id == "" {
		return "", errors.New("slack chat.scheduleMessage returned no scheduled_message_id")
	}
	return id, nil
}

func (s *slackClient) postMessage(text string) error {
	_, err := s.call("chat.postMessage", map[string]any{"channel": s.channel, "text": text})
	return err
}

func (s *slackClient) deleteScheduledMessage(id string) error {
	_, err := s.call("chat.deleteScheduledMessage", map[string]any{"channel": s.channel, "scheduled_message_id": id})
	if err != nil && strings.Contains(err.Error(), "invalid_scheduled_message_id") {
		return nil // already gone (fired or deleted), nothing to cancel
	}
	return err
}

// call posts JSON to a Slack Web API method and retries rate limits: HTTP 429
// honours Retry-After and an ok=false "ratelimited" backs off a minute per
// attempt. Slack's restricted_too_many (more than 30 messages scheduled to post
// in the same 5-minute window of one channel) is not a rate limit and is not
// retried: the reminder stays pending and the next sweep tries again.
func (s *slackClient) call(method string, body map[string]any) (map[string]any, error) {
	payload, _ := json.Marshal(body)
	const attempts = 5
	for attempt := 1; ; attempt++ {
		req, _ := http.NewRequest(http.MethodPost, s.base+"/"+method, bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+s.token)
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		res, err := s.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("slack %s: %w", method, err)
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusTooManyRequests && attempt < attempts {
			wait := 30 * time.Second
			if ra, err := strconv.Atoi(res.Header.Get("Retry-After")); err == nil && ra > 0 {
				wait = time.Duration(min(ra, 300)) * time.Second
			}
			s.sleep(wait)
			continue
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("slack %s: non-JSON response (HTTP %d): %.200s", method, res.StatusCode, raw)
		}
		if ok, _ := out["ok"].(bool); ok {
			return out, nil
		}
		slackErr, _ := out["error"].(string)
		if attempt < attempts && (slackErr == "ratelimited" || slackErr == "rate_limited") {
			s.sleep(time.Duration(attempt) * time.Minute)
			continue
		}
		if slackErr == "" {
			slackErr = "unknown error"
		}
		return nil, fmt.Errorf("slack %s failed: %s", method, slackErr)
	}
}

// ---------------------------------------------------------------- GitHub

type issue struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	State   string `json:"state"`
	PR      any    `json:"pull_request,omitempty"`
	Labels  []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type githubClient struct {
	base, token, repo string
	http              *http.Client
}

func (g *githubClient) do(method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, g.base+path, body)
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := g.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("github %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode >= 300 {
		return res.StatusCode, fmt.Errorf("github %s %s: HTTP %d: %.300s", method, path, res.StatusCode, raw)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return res.StatusCode, fmt.Errorf("github %s %s: bad JSON: %w", method, path, err)
		}
	}
	return res.StatusCode, nil
}

func (g *githubClient) ensureLabel(name string) error {
	code, err := g.do(http.MethodPost, "/repos/"+g.repo+"/labels",
		map[string]any{"name": name, "color": "0E8A16", "description": "On-prem token expiry tracker, managed by onprem-token-reminders"}, nil)
	if code == http.StatusUnprocessableEntity { // already exists
		return nil
	}
	return err
}

func (g *githubClient) createIssue(title, body, label string) (*issue, error) {
	var is issue
	_, err := g.do(http.MethodPost, "/repos/"+g.repo+"/issues", map[string]any{"title": title, "body": body, "labels": []string{label}}, &is)
	if err != nil {
		return nil, err
	}
	// GitHub silently drops labels the token may not set; the sweep finds
	// trackers only by label, so an unlabelled tracker would never be swept.
	for _, l := range is.Labels {
		if l.Name == label {
			return &is, nil
		}
	}
	return nil, fmt.Errorf("tracker issue #%d was created without the %q label (token lacks permission to label?); add it by hand so the sweep can see it", is.Number, label)
}

func (g *githubClient) updateIssue(number int, patch map[string]any) error {
	_, err := g.do(http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", g.repo, number), patch, nil)
	return err
}

func (g *githubClient) comment(number int, text string) error {
	_, err := g.do(http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", g.repo, number), map[string]any{"body": text}, nil)
	return err
}

// listIssues returns issues (never pull requests) with the label in the given state.
func (g *githubClient) listIssues(state, label string) ([]*issue, error) {
	var all []*issue
	for page := 1; ; page++ {
		q := url.Values{"state": {state}, "labels": {label}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var batch []*issue
		if _, err := g.do(http.MethodGet, "/repos/"+g.repo+"/issues?"+q.Encode(), nil, &batch); err != nil {
			return nil, err
		}
		for _, is := range batch {
			if is.PR == nil {
				all = append(all, is)
			}
		}
		if len(batch) < 100 {
			return all, nil
		}
	}
}
