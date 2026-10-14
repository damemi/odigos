// Package collectorcredential is the lifecycle of the bearer credential a managed Collector presents
// to the OpAMP gateway: binding, persistence, expiry rules, rotation and revocation order.
//
// Pure, with one deliberate exception: Mint reads the CSPRNG, because the type that keeps a credential
// out of logs and serialized structures should be the only thing that constructs one. Everything else
// here is a computation over values the caller supplies — no database, no cloud API, no wall clock
// except where a `now` argument is passed in.
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

// EmptyDigest is SHA-256 of the empty string. Named so it can be recognised rather than looked up: it
// is a perfectly well-formed 64-character digest, so a row carrying it reads as a successfully issued
// credential, every failed mint produces the SAME one, and it is the most widely published SHA-256
// value in existence. Hash refuses to produce it and FingerprintOfHash renders it conspicuously.
const EmptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// noFingerprint is what a fingerprint renders as when there is no credential behind it.
const noFingerprint = "<none>"

// ErrEmptyCredential is returned by Hash for a Secret carrying no value.
var ErrEmptyCredential = errors.New("refusing to hash an empty credential: the digest of \"\" is a well-formed hash for a credential that does not exist, and persisting it would record a failed issuance as a successful one")

// Hash is the SHA-256 hex digest — the only representation of the value the connector persists.
//
// It returns an error for an empty Secret rather than hashing it, for the same reason MarshalJSON
// fails rather than emitting a placeholder: the danger is a plausible value reaching the store. A mint
// that failed, a config field left unset, an error swallowed upstream — any of those reach here with an
// empty Secret, and a digest that looks real is how that becomes an "issued" credential nothing can
// authenticate and every other failed mint collides with.
func (s Secret) Hash() (string, error) {
	if s.v == "" {
		return "", ErrEmptyCredential
	}
	sum := sha256.Sum256([]byte(s.v))
	return hex.EncodeToString(sum[:]), nil
}

// Fingerprint is the first 8 hex characters of the hash: enough to correlate across logs and rows, not
// enough to brute-force. Safe to log.
//
// Unlike Hash this does not fail on an empty Secret — it renders "<none>". A fingerprint is only ever
// shown to a human and never persisted or compared, so a placeholder is the right answer here and the
// wrong one there.
func (s Secret) Fingerprint() string {
	h, err := s.Hash()
	if err != nil {
		return noFingerprint
	}
	return FingerprintOfHash(h)
}

// FingerprintOfHash renders a stored hash the same way as Fingerprint, so a row read back correlates
// with what was logged at issue time. A hash that is EmptyDigest renders as "<none>": nothing should
// ever have stored it, and showing its first eight characters would make a broken row look like a real
// credential in exactly the place someone is trying to work out what went wrong.
func FingerprintOfHash(hash string) string {
	if hash == EmptyDigest {
		return noFingerprint
	}
	if len(hash) < 8 {
		return hash
	}
	return hash[:8]
}

// Binding is the immutable association a credential authenticates ("Identity
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
	ReasonNetworkChanged     Reason = "network-changed"
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

// SupersededCanRevoke requires proof that the replacement connected, is still
// live, and has had time to replace tasks that used the outgoing secret.
func SupersededCanRevoke(outgoing, replacement Record, now time.Time, grace time.Duration) bool {
	return outgoing.State == StateSuperseded && outgoing.SupersededBy == replacement.CredentialID &&
		replacement.Live() && replacement.ActivatedAt != nil &&
		!now.Before(replacement.ActivatedAt.Add(grace))
}

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
