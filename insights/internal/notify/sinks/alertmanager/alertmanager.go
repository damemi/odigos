// Package alertmanager delivers findings to Prometheus Alertmanager via
// POST /api/v2/alerts. A finding is a sticky event, not a condition that must
// keep firing: one post stays open (endsAt a year ahead) until the finding is
// accepted or dismissed, which posts endsAt=now and resolves it.
package alertmanager

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/odigos-io/odigos/insights/internal/notify"
	"github.com/odigos-io/odigos/insights/internal/notify/httpsink"
	"github.com/odigos-io/odigos/insights/internal/repo"
)

// Type is the catalog id.
const Type = "alertmanager"

// firingHold is how long a firing alert stays open without another post.
// Alertmanager drops an alert once endsAt passes, so a short endsAt would
// expire a finding that fired once and then went quiet. A year is the
// backstop; an accepted or dismissed finding is resolved explicitly.
const firingHold = 365 * 24 * time.Hour

const (
	fieldURL      = "url"
	fieldUsername = "username"
	fieldPassword = "password"
	fieldLabels   = "extra_labels"
)

// Descriptor is the catalog entry.
var Descriptor = notify.Descriptor{
	Type:        Type,
	Name:        "Prometheus Alertmanager",
	Description: "Posts findings as alerts to Alertmanager's v2 API. An alert stays open until the finding is accepted or dismissed.",
	DocsURL:     "https://prometheus.io/docs/alerting/latest/clients/",
	Fields: []notify.FieldSpec{
		{Key: fieldURL, Label: "Alertmanager URL", Type: notify.FieldURL, Required: true, Placeholder: "http://alertmanager.monitoring:9093", Help: "Base URL; /api/v2/alerts is appended."},
		{Key: fieldUsername, Label: "Basic auth username", Type: notify.FieldString},
		{Key: fieldPassword, Label: "Basic auth password", Type: notify.FieldSecret},
		{Key: fieldLabels, Label: "Extra labels", Type: notify.FieldString, Placeholder: "team=platform,env=prod", Help: "Comma-separated key=value pairs added to every alert, for routing in Alertmanager."},
	},
	Capabilities: notify.Capabilities{SupportsResolve: true, MaxBatch: 100},
}

type sink struct {
	client  *http.Client
	url     string
	headers http.Header
	extra   map[string]string
}

// New is the registered Factory.
func New(cfg notify.Config, deps notify.Deps) (notify.Sink, error) {
	return &sink{
		client:  deps.HTTPClient,
		url:     strings.TrimRight(cfg.String(fieldURL), "/") + "/api/v2/alerts",
		headers: httpsink.BasicAuth(cfg.String(fieldUsername), cfg.String(fieldPassword)),
		extra:   parseLabels(cfg.String(fieldLabels)),
	}, nil
}

func alertName(kind notify.Kind) string {
	if kind == notify.KindViolation {
		return "OdigosInsightsViolation"
	}
	return "OdigosInsightsAnomaly"
}

func parseLabels(s string) map[string]string {
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

// alert is Alertmanager's postable alert.
type alert struct {
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL,omitempty"`
}

func (s *sink) Deliver(ctx context.Context, events []notify.Event, _ notify.ExternalIDHint) []notify.DeliveryResult {
	now := time.Now()
	alerts := make([]alert, len(events))
	for i, ev := range events {
		alerts[i] = s.toAlert(ev, now)
	}
	resp, err := httpsink.PostJSON(ctx, s.client, s.url, alerts, s.headers)
	return notify.AllResults(events, resp.Status, err)
}

func (s *sink) toAlert(ev notify.Event, now time.Time) alert {
	labels := map[string]string{
		"alertname":   alertName(ev.Kind),
		"odigos_kind": string(ev.Kind),
		"finding_key": ev.Key,
		"severity":    string(ev.Severity),
		"service":     ev.Service,
		"namespace":   ev.Namespace,
	}
	switch ev.Kind {
	case notify.KindAnomaly:
		labels["transaction_id"] = strconv.FormatInt(ev.TransactionID, 10)
		labels["operation"] = ev.Labels["operation"]
	case notify.KindViolation:
		labels["rule"] = ev.RuleKey
	}
	for k, v := range s.extra {
		labels[k] = v
	}
	ann := map[string]string{
		"summary":     ev.Title,
		"description": ev.Summary,
		"offending":   ev.Offending,
		"score":       strconv.Itoa(ev.Score),
		"occurrences": strconv.FormatInt(ev.Occurrences, 10),
		"status":      ev.Status,
	}
	if ev.LastTraceID != "" {
		ann["trace_id"] = ev.LastTraceID
	}
	for k, v := range ev.Labels {
		if _, taken := labels[k]; !taken && v != "" {
			ann[k] = v
		}
	}
	a := alert{Labels: labels, Annotations: ann, StartsAt: ev.FirstSeen}
	if a.StartsAt.IsZero() {
		a.StartsAt = now
	}
	if ev.Action == repo.DispatchResolved {
		a.EndsAt = now
	} else {
		a.EndsAt = now.Add(firingHold)
	}
	return a
}

// Test posts an empty alert list, which exercises URL, auth, and reachability
// without creating an alert.
func (s *sink) Test(ctx context.Context) error {
	if _, err := httpsink.PostJSON(ctx, s.client, s.url, []alert{}, s.headers); err != nil {
		return err
	}
	return nil
}
