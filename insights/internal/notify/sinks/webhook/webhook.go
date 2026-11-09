// Package webhook posts each finding event as a JSON document to an operator
// URL, optionally signed with HMAC-SHA256 so the receiver can verify origin.
// It is the escape hatch for backends without a dedicated sink.
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/insights/internal/notify"
	"github.com/odigos-io/odigos-enterprise/insights/internal/notify/httpsink"
)

// Type is the catalog id.
const Type = "webhook"

const (
	fieldURL     = "url"
	fieldSecret  = "secret"
	fieldHeaders = "headers"

	// HeaderSignature carries "sha256=<hex hmac of body>" when a secret is set.
	HeaderSignature = "X-Odigos-Signature"
	// HeaderEventID carries Event.ID so receivers can dedup redeliveries.
	HeaderEventID = "X-Odigos-Event-Id"
	// HeaderEventType is "finding.firing" / "finding.resolved" / "ping".
	HeaderEventType = "X-Odigos-Event"
)

// Descriptor is the catalog entry.
var Descriptor = notify.Descriptor{
	Type:        Type,
	Name:        "Generic webhook",
	Description: "POSTs one JSON document per finding event to your endpoint. Set a secret to receive an HMAC-SHA256 signature header.",
	Fields: []notify.FieldSpec{
		{Key: fieldURL, Label: "Endpoint URL", Type: notify.FieldURL, Required: true, Placeholder: "https://hooks.example.com/odigos"},
		{Key: fieldSecret, Label: "Signing secret", Type: notify.FieldSecret, Help: "When set, requests carry " + HeaderSignature + ": sha256=<hmac> over the raw body."},
		{Key: fieldHeaders, Label: "Extra headers", Type: notify.FieldString, Placeholder: "Authorization=Bearer xyz, X-Team=platform", Help: "Comma-separated Name=Value pairs added to every request."},
	},
	Capabilities: notify.Capabilities{MaxBatch: 1},
}

type sink struct {
	client  *http.Client
	url     string
	secret  []byte
	headers http.Header
}

// New is the registered Factory.
func New(cfg notify.Config, deps notify.Deps) (notify.Sink, error) {
	s := &sink{client: deps.HTTPClient, url: cfg.String(fieldURL), headers: http.Header{}}
	if sec := cfg.String(fieldSecret); sec != "" {
		s.secret = []byte(sec)
	}
	for _, pair := range strings.Split(cfg.String(fieldHeaders), ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if ok && k != "" {
			s.headers.Set(strings.TrimSpace(k), strings.TrimSpace(v))
		}
	}
	return s, nil
}

// Payload is the webhook body. Field names are the public contract.
type Payload struct {
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	SentAt    time.Time `json:"sent_at"`
	Finding   Finding   `json:"finding"`
}

// Finding is the finding as seen by webhook receivers.
type Finding struct {
	Key           string            `json:"key"`
	Kind          string            `json:"kind"`
	Service       string            `json:"service"`
	Namespace     string            `json:"namespace"`
	Title         string            `json:"title"`
	Summary       string            `json:"summary"`
	Offending     string            `json:"offending,omitempty"`
	Score         int               `json:"score"`
	Severity      string            `json:"severity"`
	Status        string            `json:"status"`
	Occurrences   int64             `json:"occurrences"`
	FirstSeen     time.Time         `json:"first_seen"`
	LastSeen      time.Time         `json:"last_seen"`
	LastTraceID   string            `json:"last_trace_id,omitempty"`
	TransactionID int64             `json:"transaction_id,omitempty"`
	Signature     string            `json:"signature,omitempty"`
	ScopeKey      string            `json:"scope_key,omitempty"`
	RuleKey       string            `json:"rule_key,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// ToPayload builds the public JSON document for ev; other JSON sinks reuse it
// so every backend sees the same schema.
func ToPayload(ev notify.Event, now time.Time) Payload {
	return Payload{
		EventID:   ev.ID(),
		EventType: "finding." + string(ev.Action),
		SentAt:    now,
		Finding: Finding{
			Key: ev.Key, Kind: string(ev.Kind), Service: ev.Service, Namespace: ev.Namespace,
			Title: ev.Title, Summary: ev.Summary, Offending: ev.Offending, Score: ev.Score,
			Severity: string(ev.Severity), Status: ev.Status, Occurrences: ev.Occurrences,
			FirstSeen: ev.FirstSeen, LastSeen: ev.LastSeen, LastTraceID: ev.LastTraceID,
			TransactionID: ev.TransactionID, Signature: ev.Signature, ScopeKey: ev.ScopeKey, RuleKey: ev.RuleKey,
			Labels: ev.Labels,
		},
	}
}

func (s *sink) Deliver(ctx context.Context, events []notify.Event, _ notify.ExternalIDHint) []notify.DeliveryResult {
	now := time.Now()
	out := make([]notify.DeliveryResult, len(events))
	for i, ev := range events {
		p := ToPayload(ev, now)
		status, err := s.post(ctx, p, p.EventID, p.EventType)
		out[i] = notify.DeliveryResult{FindingKey: ev.Key, HTTPStatus: status, Err: err}
	}
	return out
}

func (s *sink) post(ctx context.Context, body any, eventID, eventType string) (int, *notify.DeliveryError) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, notify.Permanent("encode payload: %v", err)
	}
	h := s.headers.Clone()
	h.Set(HeaderEventID, eventID)
	h.Set(HeaderEventType, eventType)
	if s.secret != nil {
		h.Set(HeaderSignature, Sign(s.secret, raw))
	}
	resp, derr := httpsink.PostRaw(ctx, s.client, s.url, raw, h)
	return resp.Status, derr
}

// Sign returns the signature header value for body: "sha256=<hex>".
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Test posts a ping document so the receiver sees the real headers and
// signature scheme.
func (s *sink) Test(ctx context.Context) error {
	ping := map[string]any{"event_type": "ping", "sent_at": time.Now()}
	if _, err := s.post(ctx, ping, "ping", "ping"); err != nil {
		return err
	}
	return nil
}
