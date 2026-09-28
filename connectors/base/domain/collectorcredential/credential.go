// Package collectorcredential is the lifecycle of the bearer credential a managed Collector presents
// to the OpAMP gateway: binding, persistence, expiry rules, rotation and revocation order.
//
// Pure — no I/O.
//
// The credential is not "permission to send telemetry". An authenticated OpAMP connection receives
// the Collector's desired configuration, which includes destination headers (the destination
// credential — see domain/destination.ResolveOTLPHeaders). Blast radius from that: per-Collector
// credentials, never in a workload definition or artifact, connector keeps only the hash, compromise
// revokes immediately even though that takes the Collector down.
//
// v1 credentials do not expire. The Supervisor sets accepts_opamp_connection_settings: false, so the
// static Authorization header cannot be replaced over OpAMP — a new credential needs a new deployment.
// Expiry is representable (CheckLifetime enforces the rules) but issuing one requires an unattended
// rotation path; v1 has none. Do not describe these credentials as short-lived.
package collectorcredential

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// ErrCredentialSerialized is returned by every marshaller on Secret. Failing at the call site beats
// writing a "<redacted>" placeholder into the secret store and injecting it into a task definition.
var ErrCredentialSerialized = errors.New("collector credential cannot be serialized; use Reveal at the one call site that writes it to the secret store")

// Secret holds a credential value so it cannot reach a log, report, or serialized structure by accident.
//
//	fmt %v / %s / %q  -> "<redacted>" via String
//	fmt %#v           -> "<redacted>" via GoString
//	encoding/json     -> ErrCredentialSerialized
//	encoding/text     -> ErrCredentialSerialized
//	zap / slog        -> reflected encoding routes through MarshalJSON and fails
//
// The only way out is Reveal. Redaction is a property of the type, not a convention.
type Secret struct {
	v string
}

// NewSecret wraps a credential value. Call it as soon as the value is minted or received.
func NewSecret(v string) Secret { return Secret{v: v} }

// Reveal returns the credential value. Legitimate callers: write to the provider secret store, and
// one-time reveal to the customer in manual mode.
func (s Secret) Reveal() string { return s.v }

// Empty reports whether there is no value, without revealing.
func (s Secret) Empty() bool { return s.v == "" }

func (s Secret) String() string   { return "<redacted>" }
func (s Secret) GoString() string { return "<redacted>" }

// MarshalJSON always fails. See ErrCredentialSerialized.
func (s Secret) MarshalJSON() ([]byte, error) { return nil, ErrCredentialSerialized }

// MarshalText always fails, closing encoders that prefer it to MarshalJSON.
func (s Secret) MarshalText() ([]byte, error) { return nil, ErrCredentialSerialized }

// Hash is the SHA-256 hex digest — the only representation the connector persists.
func (s Secret) Hash() string {
	sum := sha256.Sum256([]byte(s.v))
	return hex.EncodeToString(sum[:])
}

// Fingerprint is the first 8 hex characters of the hash: enough to correlate across logs and rows,
// not enough to brute-force. Safe to log.
func (s Secret) Fingerprint() string { return FingerprintOfHash(s.Hash()) }

// FingerprintOfHash renders a stored hash the same way as Fingerprint.
func FingerprintOfHash(hash string) string {
	if len(hash) < 8 {
		return hash
	}
	return hash[:8]
}

// Binding is the immutable association a credential authenticates (OPAMP_ROUTING "Identity
// provisioning"). Fixed at issue; never edited — a rewritable binding could be pointed at another
// connector's Collector.
type Binding struct {
	CollectorID string // globally unique; Central allocates
	// ConnectorUID is the connector CR UID at issue. A recreated CR gets a new UID and does not
	// inherit credentials — unlike infrastructure, which is adopted by tag.
	ConnectorUID string
	Provider     string
	AccountID    string
	Region       string
	// NetworkID binds the credential to the VPC/VNet hosting the regional Collector. It is transport
	// binding metadata, not the Collector's regional identity.
	NetworkID string
}

// Complete reports whether every required field is set.
func (b Binding) Complete() bool {
	return b.CollectorID != "" && b.ConnectorUID != "" && b.Provider != "" &&
		b.AccountID != "" && b.Region != "" && b.NetworkID != ""
}

// Matches reports whether a presented identity is the one this binding authorizes.
// Every field must agree. Matching CollectorID with a different ConnectorUID is a refusal, not a
// re-binding — that is a credential surviving connector recreation.
func (b Binding) Matches(presented Binding) bool {
	return b.Complete() && presented.Complete() && b == presented
}

// State is where a credential is in its life. Persisted as `state`.
type State string

const (
	// StatePending: issued by Central, not yet written to the secret store.
	StatePending State = "pending"
	// StateActive: written to the secret store and expected to be in use.
	StateActive State = "active"
	// StateSuperseded: replacement is active; this one stays accepted while old tasks drain (PlanRotation).
	StateSuperseded State = "superseded"
	// StateRevoked: refused by the gateway. Terminal.
	StateRevoked State = "revoked"
)

// Reason records why a credential left service. Persisted as `revoked_reason`.
type Reason string

const (
	ReasonTeardown           Reason = "teardown"
	ReasonRotation           Reason = "rotation"
	ReasonCompromise         Reason = "compromise"
	ReasonConnectorRecreated Reason = "connector-recreated"
	ReasonCapabilityRemoved  Reason = "capability-removed"
)

// Record is one row of `collector_credentials`: everything the connector keeps about a credential,
// which deliberately does not include the credential value.
type Record struct {
	// CredentialID is unique per issuance, not per Collector. Rotation produces a second record;
	// revocation names this.
	CredentialID string
	Binding      Binding
	// SecretRef is where the value lives (ARN / Key Vault id / Secret Manager name). Stable across
	// rotations — a rotation writes a new version of the same secret.
	SecretRef string
	// SecretVersion is the version this credential was written as.
	SecretVersion string
	// TokenSHA256 is Secret.Hash. The connector never stores or reads back the value.
	TokenSHA256 string
	State       State
	IssuedAt    time.Time
	// ActivatedAt is the first successful OpAMP connection under this credential. Until set, a
	// rotation has not completed and the previous credential must not be revoked.
	ActivatedAt *time.Time
	// ExpiresAt is nil in v1. See CheckLifetime.
	ExpiresAt *time.Time
	// SupersededBy is the CredentialID of the replacement, set when a rotation starts.
	SupersededBy  string
	RevokedAt     *time.Time
	RevokedReason Reason
}

// Live reports whether the gateway should still accept this credential. Superseded counts — the
// rotation window exists so the outgoing credential keeps working while old tasks drain.
func (r Record) Live() bool { return r.State == StateActive || r.State == StateSuperseded }

var legalTransitions = map[State][]State{
	StatePending:    {StateActive, StateRevoked},
	StateActive:     {StateSuperseded, StateRevoked},
	StateSuperseded: {StateRevoked},
	StateRevoked:    nil,
}

// Transition reports whether a state change is allowed.
// StateRevoked is terminal: un-revoking after compromise would hand the credential back to whoever took it.
func Transition(from, to State) error {
	for _, allowed := range legalTransitions[from] {
		if allowed == to {
			return nil
		}
	}
	if from == StateRevoked {
		return errors.New("a revoked credential is never reinstated; issue a new one and redeploy the Collector")
	}
	return errors.New("illegal credential state transition: " + string(from) + " -> " + string(to))
}
