// Package splunk sends findings to a Splunk HTTP Event Collector (HEC) as JSON
// events, batched into one request. HEC is an append-only log: there is no
// update or resolve, so a resolved event (when the integration opts in) is a
// second event with the same finding key.
package splunk

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/odigos-io/odigos/insights/internal/notify"
	"github.com/odigos-io/odigos/insights/internal/notify/httpsink"
	"github.com/odigos-io/odigos/insights/internal/notify/sinks/webhook"
)

// Type is the catalog id.
const Type = "splunk"

const (
	fieldURL        = "url"
	fieldToken      = "token"
	fieldIndex      = "index"
	fieldSourceType = "sourcetype"
	fieldInsecure   = "insecure_skip_verify"

	eventPath = "/services/collector/event"
	source    = "odigos-insights"
)

// Descriptor is the catalog entry.
var Descriptor = notify.Descriptor{
	Type:        Type,
	Name:        "Splunk",
	Description: "Sends each finding as a JSON event to a Splunk HTTP Event Collector.",
	DocsURL:     "https://docs.splunk.com/Documentation/Splunk/latest/Data/UsetheHTTPEventCollector",
	Fields: []notify.FieldSpec{
		{Key: fieldURL, Label: "HEC URL", Type: notify.FieldURL, Required: true, Placeholder: "https://http-inputs-<stack>.splunkcloud.com", Help: "Base URL of the collector; " + eventPath + " is appended when no path is given."},
		{Key: fieldToken, Label: "HEC token", Type: notify.FieldSecret, Required: true},
		{Key: fieldIndex, Label: "Index", Type: notify.FieldString, Placeholder: "main", Help: "Must be allowed for the token. Empty uses the token's default index."},
		{Key: fieldSourceType, Label: "Source type", Type: notify.FieldString, Default: "_json", Help: "_json makes Splunk extract the finding fields at search time."},
		{Key: fieldInsecure, Label: "Skip TLS verification", Type: notify.FieldBool, Help: "For collectors with a self-signed certificate, such as Splunk Cloud trial stacks on port 8088."},
	},
	Capabilities: notify.Capabilities{MaxBatch: 50},
}

type sink struct {
	client     *http.Client
	url        string
	headers    http.Header
	index      string
	sourceType string
}

// New is the registered Factory.
func New(cfg notify.Config, deps notify.Deps) (notify.Sink, error) {
	u, err := url.Parse(cfg.String(fieldURL))
	if err != nil {
		return nil, &notify.ValidationError{Fields: map[string]string{fieldURL: "must be an http(s) URL"}}
	}
	if strings.Trim(u.Path, "/") == "" {
		u.Path = eventPath
	}
	client := deps.HTTPClient
	if cfg.Bool(fieldInsecure) {
		client = insecureClient(client)
	}
	return &sink{
		client:     client,
		url:        u.String(),
		headers:    http.Header{"Authorization": {"Splunk " + cfg.String(fieldToken)}},
		index:      cfg.String(fieldIndex),
		sourceType: cfg.String(fieldSourceType),
	}, nil
}

// insecureClient copies base with certificate verification turned off.
func insecureClient(base *http.Client) *http.Client {
	tr, ok := base.Transport.(*http.Transport)
	if !ok || tr == nil {
		tr = http.DefaultTransport.(*http.Transport)
	}
	tr = tr.Clone()
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{}
	}
	tr.TLSClientConfig.InsecureSkipVerify = true
	c := *base
	c.Transport = tr
	return &c
}

// hecEvent is the HEC envelope; Time is epoch seconds.
type hecEvent struct {
	Time       float64 `json:"time"`
	Source     string  `json:"source"`
	SourceType string  `json:"sourcetype,omitempty"`
	Index      string  `json:"index,omitempty"`
	Event      any     `json:"event"`
}

func (s *sink) envelope(now time.Time, event any) hecEvent {
	return hecEvent{
		Time:       float64(now.UnixMilli()) / 1000,
		Source:     source,
		SourceType: s.sourceType,
		Index:      s.index,
		Event:      event,
	}
}

// post sends the events in one request; HEC accepts concatenated JSON
// objects rather than an array.
func (s *sink) post(ctx context.Context, events []hecEvent) (int, *notify.DeliveryError) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return 0, notify.Permanent("encode payload: %v", err)
		}
	}
	resp, derr := httpsink.PostRaw(ctx, s.client, s.url, buf.Bytes(), s.headers)
	return resp.Status, derr
}

func (s *sink) Deliver(ctx context.Context, events []notify.Event, _ notify.ExternalIDHint) []notify.DeliveryResult {
	now := time.Now()
	batch := make([]hecEvent, len(events))
	for i, ev := range events {
		batch[i] = s.envelope(now, webhook.ToPayload(ev, now))
	}
	status, err := s.post(ctx, batch)
	return notify.AllResults(events, status, err)
}

// Test sends a ping event, which also proves the token may write to the
// configured index.
func (s *sink) Test(ctx context.Context) error {
	now := time.Now()
	ping := map[string]any{"event_type": "ping", "sent_at": now, "message": "Odigos Insights is connected to this collector."}
	if _, err := s.post(ctx, []hecEvent{s.envelope(now, ping)}); err != nil {
		return err
	}
	return nil
}
