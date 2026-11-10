package collectorcredential

import (
	"fmt"
	"time"

	"github.com/odigos-io/odigos/connectors/base/domain/collectorstatus"
)

// Audit events written to connector_platform.installation_audit. A credential's whole life is
// invisible otherwise: the value is never stored, the hash is not reversible, and a revoked row looks
// much like one that was never used. The audit trail is the only account of who asked for what.
const (
	EventCredentialIssued     = "collector-credential-issued"
	EventCredentialSuperseded = "collector-credential-superseded"
	EventCredentialRevoked    = "collector-credential-revoked"
)

// Actor is who asked for a credential operation. Recorded so a revocation can be traced to a person or
// a control loop — "revoked" alone cannot tell a routine teardown from something somebody should look
// at, which is the same reason RevokedReason is kept.
type Actor string

const (
	// ActorConnector is the connector's own reconcile loop (automatic mode).
	ActorConnector Actor = "connector"
	// ActorCentral is the Central issuance API.
	ActorCentral Actor = "central"
	// ActorCLI is odictl, which serves manual-mode rotation until the Central surface exists.
	ActorCLI Actor = "odictl"
)

// IssueRequest is a request to bring a credential into existence.
//
// The binding is supplied whole by the caller rather than assembled here: every field is authoritative
// somewhere else (the CR UID from config, the account and region from the provider, the network from
// placement), and a default invented in this package would be a binding that authorizes something
// nobody chose.
type IssueRequest struct {
	// Binding is the immutable tuple the credential will authenticate. CollectorID may be empty for a
	// Collector that does not have an identity yet; every other field is required.
	Binding Binding
	// SecretRef is where the value will live. Required at issue rather than filled in afterwards: a
	// credential row with no secret_ref describes a value that was minted and has nowhere to go.
	SecretRef string
	// ExpiresAt is nil in v1. CheckLifetime decides whether a non-nil one may be issued at all.
	ExpiresAt *time.Time
	// Path is how a replacement could reach the Collector, which is what makes an expiry issuable.
	Path RotationPath
	// Actor is who asked.
	Actor Actor
}

// NeedsCollectorID reports whether this request is for a Collector with no identity yet.
func (r IssueRequest) NeedsCollectorID() bool { return r.Binding.CollectorID == "" }

// Validate reports every reason a credential must not be issued.
//
// Refusing to mint is always safe: the Collector is not deployed yet, or keeps running on the
// credential it has. Issuing on an incomplete binding is not — the gateway compares the whole tuple, so
// a row missing a field authenticates a Collector whose account or network was never established.
func (r IssueRequest) Validate(now time.Time) []collectorstatus.Problem {
	var problems []collectorstatus.Problem

	// CollectorID is absent from this list because it is the one field this package may allocate.
	// A slice, not a map: the messages are reported in a fixed order so the same bad request produces
	// the same output twice.
	required := []struct {
		field string
		value string
	}{
		{"connector UID", r.Binding.ConnectorUID},
		{"provider", r.Binding.Provider},
		{"account id", r.Binding.AccountID},
		{"region", r.Binding.Region},
		// NetworkID is the host-network binding for the regional Collector.
		{"network id", r.Binding.NetworkID},
		{"secret ref", r.SecretRef},
	}
	for _, req := range required {
		if req.value != "" {
			continue
		}
		field := req.field
		problems = append(problems, collectorstatus.Problem{
			Code:     "CredentialBindingIncomplete",
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("refusing to issue a Collector credential with no %s: the gateway matches the "+
				"whole binding, so a credential missing one field authenticates a Collector whose identity was "+
				"never established", field),
		})
	}
	if r.Actor == "" {
		problems = append(problems, collectorstatus.Problem{
			Code:     "CredentialIssuerUnknown",
			Severity: collectorstatus.SeverityError,
			Message: "refusing to issue a Collector credential with no actor recorded: the audit trail is the " +
				"only account of a credential's life, because the value is never stored and the hash is not reversible",
		})
	}
	return append(problems, CheckLifetime(r.ExpiresAt, now, r.Path)...)
}

// NewRecord assembles the row for a freshly minted credential.
//
// Deterministic: the caller supplies the ids, the hash and the clock. Minting and allocation happen
// outside so this — the part that decides what is persisted — is a value-to-value function that a test
// can pin exactly.
//
// State is pending, never active. The value exists but has not reached the provider secret store yet,
// and until it has, a Collector presenting it is presenting something that was not supposed to have
// left the issuer.
func NewRecord(req IssueRequest, collectorID, credentialID, tokenHash string, now time.Time) Record {
	binding := req.Binding
	binding.CollectorID = collectorID
	return Record{
		CredentialID: credentialID,
		Binding:      binding,
		SecretRef:    req.SecretRef,
		TokenSHA256:  tokenHash,
		State:        StatePending,
		IssuedAt:     now,
		ExpiresAt:    req.ExpiresAt,
	}
}

// Issued is the one and only time a credential value is available.
//
// There is no way to ask for it again, and that is a property rather than a gap: only the hash is
// persisted (§6.3), so no reveal API can exist even in principle. Manual-mode rotation reveals the
// value here, at issue, and a customer who loses it rotates again rather than retrieving it — which
// is the same answer as for a credential that leaked.
type Issued struct {
	// Minted is the value and its hash. Use it immediately: write to the provider secret store, or
	// reveal once through an authenticated surface, then let it go.
	Minted Minted
	// Record is what was persisted. It carries no value, by construction.
	Record Record
	// Superseded is the outgoing credential a rotation moved aside, if any.
	Superseded *Record
}

// RecreationVictims returns the credentials a connector recreation invalidates.
//
// A recreated CR gets a new UID, and a credential is bound to the UID at issue. So every live
// credential under a previous UID must be revoked — not because the gateway would accept one (it
// refuses a binding whose connector is not registered) but because leaving them live leaves rows that
// nothing will ever revoke, and revocation is the authority that has to be exercised while it still
// exists.
//
// This is the deliberate asymmetry with infrastructure: a Collector's cloud objects ARE adopted by tag
// across recreation (collectorownership.AdoptionOf), its credential is not. The infrastructure is
// identified by something stable; the credential authorizes a specific connector instance, and quietly
// re-pointing it at the replacement is exactly the confusion the binding exists to prevent.
func RecreationVictims(records []Record, currentConnectorUID string) []Record {
	if currentConnectorUID == "" {
		// No current UID means the caller does not know its own identity. Revoking every credential on
		// that basis would take a whole fleet's configuration plane down, so refuse to conclude anything.
		return nil
	}
	var victims []Record
	for _, r := range records {
		if r.Live() && r.Binding.ConnectorUID != currentConnectorUID {
			victims = append(victims, r)
		}
	}
	return victims
}

// AuditDetail renders the audit `detail` blob for one credential.
//
// The single place that decides what an audit row says about a credential, so there is one function to
// check rather than a JSON literal at every call site. It carries the FINGERPRINT, never the hash: the
// hash is the verifier, and an audit table is read by more people and exported to more places than the
// credentials table. Eight hex characters correlate a row with a log line and are useless for anything
// else.
func AuditDetail(r Record, actor Actor) map[string]any {
	detail := map[string]any{
		"collector_id":  r.Binding.CollectorID,
		"credential_id": r.CredentialID,
		"connector_uid": r.Binding.ConnectorUID,
		"provider":      r.Binding.Provider,
		"account_id":    r.Binding.AccountID,
		"region":        r.Binding.Region,
		"network_id":    r.Binding.NetworkID,
		"secret_ref":    r.SecretRef,
		"fingerprint":   FingerprintOfHash(r.TokenSHA256),
		"state":         string(r.State),
		"actor":         string(actor),
	}
	if r.ExpiresAt != nil {
		detail["expires_at"] = r.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if r.SupersededBy != "" {
		detail["superseded_by"] = r.SupersededBy
	}
	if r.RevokedReason != "" {
		detail["revoked_reason"] = string(r.RevokedReason)
	}
	return detail
}
