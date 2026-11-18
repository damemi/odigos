// Package notify delivers findings (anomaly issues and guardrail violations)
// to operator-configured backends such as Alertmanager, Slack, or a webhook.
//
// It is a background poller over the database, deliberately off the ingest hot
// path: the Dispatcher wakes every PollInterval, diffs recent findings against
// the notification_dispatch ledger, and hands the resulting Events to the
// Sink of each routed Integration. Exactly one replica runs the Dispatcher
// (see cmd wiring and the leader package); the ledger and per-event
// idempotency keys are the safety net for a leader crash mid-tick.
package notify

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/odigos-io/odigos/insights/internal/repo"
	"github.com/odigos-io/odigos/insights/internal/risk"
)

// Kind is the finding family an Event describes.
type Kind string

const (
	KindAnomaly   Kind = "anomaly"
	KindViolation Kind = "violation"
)

// Event is one backend-agnostic notification: a finding plus the action the
// backend should take for it. Sinks render it into their wire format; they
// never read the repo types directly so a new backend only depends on this.
type Event struct {
	// Key is the stable finding identity (repo.AnomalyFindingKey /
	// repo.ViolationFindingKey). Backends that dedup use it as their key.
	Key    string
	Kind   Kind
	Action repo.DispatchAction

	Service   string
	Namespace string
	// Title is the finding headline (operation name or rule label).
	Title string
	// Summary is the render-ready one-line sentence from the product API.
	Summary   string
	Offending string
	Score     int
	Severity  risk.Severity
	// Status is the finding lifecycle status at send time (open/accepted/dismissed).
	Status      string
	Occurrences int64
	FirstSeen   time.Time
	LastSeen    time.Time
	LastTraceID string

	// Kind-specific identity, exposed so a backend can link back or label.
	TransactionID int64  // anomaly
	Signature     string // anomaly
	PolicyID      int64  // anomaly: the alert policy it fired under (routing)
	ScopeKey      string // violation
	RuleKey       string // violation
	// Labels carries small kind-specific extras (triggered classes, operation).
	Labels map[string]string
}

// ID is the idempotency key for this exact send: the same finding, action,
// and observed state hash to the same id so a redelivery after a leader crash
// is recognisable by the backend (webhook X-Odigos-Event-Id, Slack dedup).
func (e Event) ID() string {
	h := sha256.New()
	h.Write([]byte(e.Key))
	h.Write([]byte{0})
	h.Write([]byte(e.Action))
	h.Write([]byte{0})
	h.Write([]byte(e.Status))
	h.Write([]byte{0})
	h.Write([]byte(strconv.FormatInt(e.Occurrences, 10)))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// ExternalIDHint is the backend-side identifier a previous delivery returned
// for this finding, when the ledger has one. Sinks with
// Capabilities.ReturnsExternalID use it to update/resolve the existing object.
type ExternalIDHint map[string]string
