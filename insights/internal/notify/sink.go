package notify

import (
	"context"
	"fmt"
)

// FieldType drives generic form rendering and validation of an integration's
// config without the UI knowing the backend.
type FieldType string

const (
	FieldString FieldType = "string"
	FieldSecret FieldType = "secret"
	FieldURL    FieldType = "url"
	FieldNumber FieldType = "number"
	FieldBool   FieldType = "bool"
	FieldSelect FieldType = "select"
)

// FieldSpec describes one key of a backend's JSON config.
type FieldSpec struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Type        FieldType `json:"type"`
	Required    bool      `json:"required"`
	Help        string    `json:"help,omitempty"`
	Placeholder string    `json:"placeholder,omitempty"`
	Default     string    `json:"default,omitempty"`
	// Options lists the allowed values for FieldSelect.
	Options []string `json:"options,omitempty"`
}

// Capabilities tells the Dispatcher how a backend models alerts so the send
// plan (what to send, when, how often) is derived rather than hard-coded per
// sink.
type Capabilities struct {
	// NeedsHeartbeat: the backend expires alerts that are not re-asserted
	// (Alertmanager endsAt). Open findings still active in the poll window are
	// re-sent every tick instead of once.
	NeedsHeartbeat bool `json:"needs_heartbeat"`
	// SupportsResolve: the backend tracks open/closed per key, so a finding
	// moving to accepted/dismissed is always sent as resolved. Backends without
	// it only get resolved events when the integration opts in
	// (IntegrationFilters.NotifyOnResolve).
	SupportsResolve bool `json:"supports_resolve"`
	// ReturnsExternalID: Deliver returns a backend id per finding that later
	// sends should reference (ticketing systems).
	ReturnsExternalID bool `json:"returns_external_id"`
	// MaxBatch is the most events one Deliver call may carry; 0 means one.
	MaxBatch int `json:"max_batch"`
}

// Descriptor is a backend's catalog entry: identity, the config form, and
// capabilities. It is what GET /integrations/catalog returns.
type Descriptor struct {
	// Type is the stable id stored on repo.Integration.Type ("alertmanager").
	Type         string       `json:"type"`
	Name         string       `json:"name"`
	Description  string       `json:"description"`
	DocsURL      string       `json:"docs_url,omitempty"`
	Fields       []FieldSpec  `json:"fields"`
	Capabilities Capabilities `json:"capabilities"`
}

// DeliveryError is why one event failed. Retryable drives the backoff: a
// retryable failure is re-attempted, a permanent one (4xx other than 429,
// malformed payload) goes straight to dead.
type DeliveryError struct {
	Retryable  bool
	HTTPStatus int
	Msg        string
}

func (e *DeliveryError) Error() string {
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("http %d: %s", e.HTTPStatus, e.Msg)
	}
	return e.Msg
}

// Permanent builds a non-retryable error.
func Permanent(format string, args ...any) *DeliveryError {
	return &DeliveryError{Msg: fmt.Sprintf(format, args...)}
}

// Retryable builds a retryable error.
func Retryable(format string, args ...any) *DeliveryError {
	return &DeliveryError{Retryable: true, Msg: fmt.Sprintf(format, args...)}
}

// DeliveryResult is the per-event outcome of Deliver. Sinks that send a batch
// in one request fan the request outcome out to every event.
type DeliveryResult struct {
	FindingKey string
	// ExternalID is the backend-side id for this finding, when the backend
	// returns one (Capabilities.ReturnsExternalID).
	ExternalID string
	HTTPStatus int
	Err        *DeliveryError
}

// Sink is a configured connection to one backend. Implementations are built
// per Integration by the registered Factory and must be safe for use from a
// single goroutine; they should not keep per-finding state (the ledger does).
type Sink interface {
	// Deliver sends events in one logical batch (len <= Capabilities.MaxBatch,
	// or 1) and returns exactly one result per event, in order. ExternalIDs
	// are the ledger's known backend ids for these findings, keyed by
	// finding key; empty when none.
	Deliver(ctx context.Context, events []Event, externalIDs ExternalIDHint) []DeliveryResult
	// Test performs a cheap connectivity check with the configured
	// credentials (a synthetic event or a backend ping). Used by
	// POST /integrations/{id}/test.
	Test(ctx context.Context) error
}

// Factory builds a Sink from a validated config (already typed by FieldSpec:
// strings, float64 for numbers, bool for bools). Deps carries shared
// infrastructure so sinks do not construct their own.
type Factory func(cfg Config, deps Deps) (Sink, error)

// Config is an integration's config after ValidateConfig: keys are FieldSpec
// keys, values are typed per FieldType.
type Config map[string]any

// String returns the string value for key or "".
func (c Config) String(key string) string {
	s, _ := c[key].(string)
	return s
}

// Bool returns the bool value for key or false.
func (c Config) Bool(key string) bool {
	b, _ := c[key].(bool)
	return b
}

// Number returns the numeric value for key or def when absent.
func (c Config) Number(key string, def float64) float64 {
	if f, ok := c[key].(float64); ok {
		return f
	}
	return def
}

// AllResults fans one outcome out to every event; the common shape for sinks
// that post the whole batch in one request.
func AllResults(events []Event, status int, err *DeliveryError) []DeliveryResult {
	out := make([]DeliveryResult, len(events))
	for i, e := range events {
		out[i] = DeliveryResult{FindingKey: e.Key, HTTPStatus: status, Err: err}
	}
	return out
}
