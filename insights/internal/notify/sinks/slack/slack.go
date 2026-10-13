// Package slack posts findings to a Slack Incoming Webhook as Block Kit
// messages. Incoming Webhooks are fire-and-forget: there is no update or
// resolve, so a resolved event (when the integration opts in) is a second,
// shorter message.
package slack

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/notify"
	"github.com/odigos-io/odigos-enterprise/insights/internal/notify/httpsink"
	"github.com/odigos-io/odigos-enterprise/insights/internal/repo"
)

// Type is the catalog id.
const Type = "slack"

const (
	fieldWebhookURL = "webhook_url"
	fieldMention    = "mention"
)

// Descriptor is the catalog entry.
var Descriptor = notify.Descriptor{
	Type:        Type,
	Name:        "Slack",
	Description: "Posts a message per finding to a Slack channel through an Incoming Webhook.",
	DocsURL:     "https://api.slack.com/messaging/webhooks",
	Fields: []notify.FieldSpec{
		{Key: fieldWebhookURL, Label: "Incoming webhook URL", Type: notify.FieldSecret, Required: true, Placeholder: "https://hooks.slack.com/services/T000/B000/XXXX"},
		{Key: fieldMention, Label: "Mention", Type: notify.FieldString, Placeholder: "<!channel> or <@U12345>", Help: "Prepended to critical findings."},
	},
	Capabilities: notify.Capabilities{MaxBatch: 1},
}

type sink struct {
	client  *http.Client
	url     string
	mention string
}

// New is the registered Factory.
func New(cfg notify.Config, deps notify.Deps) (notify.Sink, error) {
	// The webhook URL is a secret (it embeds the token) so FieldURL validation
	// does not run on it; check the shape here.
	u := cfg.String(fieldWebhookURL)
	if parsed, err := url.Parse(u); err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, &notify.ValidationError{Fields: map[string]string{fieldWebhookURL: "must be an http(s) URL"}}
	}
	return &sink{client: deps.HTTPClient, url: u, mention: cfg.String(fieldMention)}, nil
}

type message struct {
	Text   string  `json:"text"`
	Blocks []block `json:"blocks,omitempty"`
}

type block struct {
	Type     string `json:"type"`
	Text     *text  `json:"text,omitempty"`
	Fields   []text `json:"fields,omitempty"`
	Elements []text `json:"elements,omitempty"`
}

type text struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func mrkdwn(s string) text { return text{Type: "mrkdwn", Text: s} }

var severityEmoji = map[string]string{
	"critical": ":rotating_light:",
	"high":     ":red_circle:",
	"medium":   ":large_orange_circle:",
	"low":      ":large_yellow_circle:",
	"info":     ":white_circle:",
}

func (s *sink) render(ev notify.Event) message {
	sev := string(ev.Severity)
	kind := "Anomaly"
	if ev.Kind == notify.KindViolation {
		kind = "Policy violation"
	}
	if ev.Action == repo.DispatchResolved {
		head := fmt.Sprintf(":white_check_mark: *%s resolved* (%s) — `%s/%s` %s", kind, ev.Status, ev.Namespace, ev.Service, ev.Title)
		return message{Text: head, Blocks: []block{{Type: "section", Text: ptr(mrkdwn(head))}}}
	}
	head := fmt.Sprintf("%s *%s %s* — `%s/%s`", severityEmoji[sev], strings.ToUpper(sev), kind, ev.Namespace, ev.Service)
	if s.mention != "" && ev.Severity == "critical" {
		head = s.mention + " " + head
	}
	fields := []text{
		mrkdwn("*Title*\n" + ev.Title),
		mrkdwn(fmt.Sprintf("*Score*\n%d", ev.Score)),
		mrkdwn(fmt.Sprintf("*Occurrences*\n%d", ev.Occurrences)),
		mrkdwn("*First seen*\n" + ev.FirstSeen.UTC().Format(time.RFC3339)),
	}
	if ev.Offending != "" {
		fields = append(fields, mrkdwn("*Offending*\n`"+ev.Offending+"`"))
	}
	if ev.LastTraceID != "" {
		fields = append(fields, mrkdwn("*Trace*\n`"+ev.LastTraceID+"`"))
	}
	blocks := []block{
		{Type: "section", Text: ptr(mrkdwn(head))},
		{Type: "section", Text: ptr(mrkdwn(ev.Summary))},
		{Type: "section", Fields: fields},
		{Type: "context", Elements: []text{mrkdwn("finding `" + ev.Key + "` · event `" + ev.ID()[:12] + "`")}},
	}
	return message{Text: fmt.Sprintf("%s %s: %s/%s %s", strings.ToUpper(sev), kind, ev.Namespace, ev.Service, ev.Title), Blocks: blocks}
}

func ptr(t text) *text { return &t }

func (s *sink) Deliver(ctx context.Context, events []notify.Event, _ notify.ExternalIDHint) []notify.DeliveryResult {
	out := make([]notify.DeliveryResult, len(events))
	for i, ev := range events {
		resp, err := httpsink.PostJSON(ctx, s.client, s.url, s.render(ev), nil)
		out[i] = notify.DeliveryResult{FindingKey: ev.Key, HTTPStatus: resp.Status, Err: err}
	}
	return out
}

// Test posts a short message so the operator sees it land in the channel.
func (s *sink) Test(ctx context.Context) error {
	msg := message{Text: ":wave: Odigos Insights is connected to this channel."}
	if _, err := httpsink.PostJSON(ctx, s.client, s.url, msg, nil); err != nil {
		return err
	}
	return nil
}
