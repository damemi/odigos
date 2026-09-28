// Package collectorgateway is the contract between `connector-opamp-gateway` and a connector's OpAMP
// server: what it means for a managed Collector's connection to be authenticated, and how that
// assertion survives the hop between the two processes.
//
// Pure — no I/O. Both sides import it so neither can hold a private idea of who a Collector is.
//
// The split is deliberate. The gateway authenticates and routes; the connector owns desired
// configuration. That keeps the credential value inside exactly one process: the gateway hashes what
// the Collector presented, resolves the row, and forwards the resulting *identity* — never the value,
// never the hash. See OPAMP_ROUTING.md and MANAGED-COLLECTOR.md §6.3.
//
// # Why the connector may trust a header
//
// The forwarded identity is unauthenticated on the wire. It is trustworthy because the connector's
// OpAMP port admits only `connector-opamp-gateway` pods (a NetworkPolicy — OPAMP_ROUTING, "Connector
// runtime"), so nothing else can present one. The gateway upholds the other half by stripping every
// inbound header in this namespace before setting its own (StripForwardedIdentity): a Collector that
// sets X-Odigos-Connector-Uid itself must not be able to pick its own connector.
//
// If that boundary is ever weakened — a shared cluster, a mesh that admits more than the gateway —
// the fix is to sign the assertion, not to re-send the credential. Re-sending it would put a value
// worth a customer's destination credential on a second hop for no gain.
package collectorgateway

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/opamp"
)

// ForwardedHeaderPrefix is the namespace the gateway owns on a proxied request. Every header under it
// is removed from the inbound request and re-set from authenticated state, so a client cannot forge
// one and cannot smuggle one through a name the gateway does not happen to set today.
const ForwardedHeaderPrefix = "X-Odigos-"

// The identity the gateway asserts to the connector. Canonical (http.Header) spelling.
const (
	// HeaderCollectorID is the Collector identity Central allocated. The routing subject.
	HeaderCollectorID = "X-Odigos-Collector-Id"
	// HeaderCredentialID names which ISSUANCE authenticated. A rotation has two live credentials, so
	// the connector needs this — not just the Collector id — to record activated_at on the right row
	// and let the outgoing credential be revoked (§6.4).
	HeaderCredentialID = "X-Odigos-Credential-Id"
	// HeaderConnectorUID is the connector CR UID the credential is bound to. The connector compares it
	// against its own identity: matching it is the whole point of the route, and a mismatch here means
	// the gateway routed wrongly.
	HeaderConnectorUID = "X-Odigos-Connector-Uid"
	HeaderProvider     = "X-Odigos-Provider"
	HeaderAccountID    = "X-Odigos-Account-Id"
	HeaderRegion       = "X-Odigos-Region"
	HeaderNetworkID    = "X-Odigos-Network-Id"
)

// Identity is one authenticated Collector connection.
type Identity struct {
	// Binding is the immutable tuple the credential authenticates. Every field is set — Authorize
	// refuses an incomplete binding rather than forwarding a partial identity.
	Binding collectorcredential.Binding
	// CredentialID is the issuance that authenticated, not the Collector. Two are live during a rotation.
	CredentialID string
}

// SetHeaders writes the identity onto an outbound request. Callers must StripForwardedIdentity first;
// Apply does both and is the one to prefer.
func (i Identity) SetHeaders(h http.Header) {
	h.Set(HeaderCollectorID, i.Binding.CollectorID)
	h.Set(HeaderCredentialID, i.CredentialID)
	h.Set(HeaderConnectorUID, i.Binding.ConnectorUID)
	h.Set(HeaderProvider, i.Binding.Provider)
	h.Set(HeaderAccountID, i.Binding.AccountID)
	h.Set(HeaderRegion, i.Binding.Region)
	h.Set(HeaderNetworkID, i.Binding.NetworkID)
}

// Apply prepares an outbound request: drop whatever the client sent in this namespace, drop the
// credential itself, then assert the authenticated identity.
//
// Removing Authorization is not tidiness. The header carries a value that buys the customer's
// destination credential out of a configuration push, and the connector has no use for it — it keeps
// only hashes and never reads a credential back.
func (i Identity) Apply(h http.Header) {
	StripForwardedIdentity(h)
	h.Del("Authorization")
	i.SetHeaders(h)
}

// StripForwardedIdentity removes every header in the gateway's namespace.
//
// Deliberately prefix-matched rather than a list of the seven names above: a header added to this
// contract later is stripped by this code as it stands, so the strip side can never fall behind the
// set side. Getting that ordering wrong is how a client picks its own connector.
func StripForwardedIdentity(h http.Header) {
	for name := range h {
		if !hasForwardedPrefix(name) {
			continue
		}
		// delete rather than Header.Del: Del canonicalizes its argument, so it misses a key stored in
		// any other casing. Go's server canonicalizes what it parses off the wire, but a header map
		// built in code need not be, and a strip that depends on the caller's spelling is not a strip.
		delete(h, name)
	}
}

// hasForwardedPrefix matches the namespace without depending on canonicalization, which leaves a key
// containing bytes it considers invalid exactly as it found it.
func hasForwardedPrefix(name string) bool {
	return len(name) >= len(ForwardedHeaderPrefix) &&
		strings.EqualFold(name[:len(ForwardedHeaderPrefix)], ForwardedHeaderPrefix)
}

// ParseIdentity reads the identity a gateway asserted. The connector side of SetHeaders.
//
// It reports ok only for a complete binding: a half-populated identity is a gateway that changed
// without the connector, and continuing on it would serve a configuration to something whose network
// or account was never established.
func ParseIdentity(h http.Header) (Identity, bool) {
	id := Identity{
		CredentialID: h.Get(HeaderCredentialID),
		Binding: collectorcredential.Binding{
			CollectorID:  h.Get(HeaderCollectorID),
			ConnectorUID: h.Get(HeaderConnectorUID),
			Provider:     h.Get(HeaderProvider),
			AccountID:    h.Get(HeaderAccountID),
			Region:       h.Get(HeaderRegion),
			NetworkID:    h.Get(HeaderNetworkID),
		},
	}
	return id, id.CredentialID != "" && id.Binding.Complete()
}

// Connector is what the gateway resolved for a credential's ConnectorUID: the registration it routes
// to. Populated from the OdigosCloudConnector CR, never from anything the client sent.
type Connector struct {
	UID       string
	Name      string
	Namespace string
	Provider  string
	AccountID string
	// Terminating reports a CR with a deletion timestamp. Teardown revokes credentials before deleting
	// the connector (§6.4), so a connection arriving during that window is one the revoke has not
	// reached yet — refuse it rather than hand out another configuration on the way down.
	Terminating bool
}

// Outcome is what the gateway does with a handshake.
type Outcome int

const (
	// OutcomeRefuse is the zero value on purpose: a Decision nobody filled in denies.
	OutcomeRefuse Outcome = iota
	// OutcomeAccept proxies the connection to the bound connector.
	OutcomeAccept
	// OutcomeUnavailable is a valid credential with no connector to route it to. Distinguished from a
	// refusal because it is not the Collector's fault and it may cure itself: the connector is being
	// deleted, or was recreated under a new UID and this Collector is waiting to be redeployed (§6.4).
	OutcomeUnavailable
)

// Refusal is why a handshake did not proceed. Safe to log — it names a class, never a value.
//
// It is deliberately NOT sent to the client. Telling a caller apart "unknown credential" from
// "revoked" hands whoever is probing a confirmation that a token was once real, and the Collector
// cannot act on the difference: every one of these is "you are not getting configuration".
type Refusal string

const (
	RefusalNone Refusal = ""
	// RefusalNoCredential: no Authorization header, not a Bearer scheme, or an empty value.
	RefusalNoCredential Refusal = "no-credential"
	// RefusalEmptyDigest: the presented hash is SHA-256 of "". Nothing legitimate produces it —
	// collectorcredential.Hash refuses to — so it is either a probe or a row written by a failed mint,
	// and it must never match one.
	RefusalEmptyDigest Refusal = "empty-digest"
	// RefusalUnknownCredential: no row for this hash.
	RefusalUnknownCredential Refusal = "unknown-credential"
	// RefusalHashMismatch: a row came back whose hash is not the one presented. The store looked up by
	// something other than the hash, which is a bug worth its own name.
	RefusalHashMismatch Refusal = "hash-mismatch"
	// RefusalIncompleteBinding: a row missing a binding field. Never authenticate a partial identity.
	RefusalIncompleteBinding Refusal = "incomplete-binding"
	// RefusalNotYetActive: issued but never written to the secret store. A Collector presenting one has
	// a value that was not supposed to have left Central.
	RefusalNotYetActive Refusal = "not-yet-active"
	// RefusalRevoked: terminal. Never reinstated (§6.4).
	RefusalRevoked Refusal = "revoked"
	// RefusalExpired: past expires_at. NULL in v1, so this is dormant until an unattended rotation path
	// exists — but the gateway enforces the column rather than trusting that it stayed NULL.
	RefusalExpired Refusal = "expired"
	// RefusalProviderMismatch / RefusalAccountMismatch: the credential's binding disagrees with the
	// connector it names. Cross-account routing is the thing this whole check exists to stop.
	RefusalProviderMismatch Refusal = "provider-mismatch"
	RefusalAccountMismatch  Refusal = "account-mismatch"
	// RefusalConnectorUnknown: the bound UID is not registered. A recreated connector has a new UID and
	// does not inherit credentials, so this is the expected steady state until the Collector is
	// redeployed — reported, not repaired.
	RefusalConnectorUnknown Refusal = "connector-unknown"
	// RefusalConnectorTerminating: the connector is being deleted.
	RefusalConnectorTerminating Refusal = "connector-terminating"
)

// Decision is the whole result of authenticating one handshake.
type Decision struct {
	Outcome  Outcome
	Refusal  Refusal
	Identity Identity // set only when Outcome is OutcomeAccept
}

// Accepted reports whether the connection may be proxied.
func (d Decision) Accepted() bool { return d.Outcome == OutcomeAccept }

// Authorize decides one handshake from already-resolved state.
//
// cred is nil when no row matched the presented hash; conn is nil when the bound UID is not
// registered. Both are pointers rather than (value, bool) pairs so a caller cannot pass a zero
// Record with ok=true and have it read as a credential.
//
// Credential checks run BEFORE connector checks. An unauthenticated caller must not be able to learn
// anything about which connectors exist, and the ordering is what guarantees it.
func Authorize(tokenHash string, cred *collectorcredential.Record, conn *Connector, now time.Time) Decision {
	switch {
	case tokenHash == "":
		return refuse(RefusalNoCredential)
	case tokenHash == collectorcredential.EmptyDigest:
		return refuse(RefusalEmptyDigest)
	case cred == nil:
		return refuse(RefusalUnknownCredential)
	}

	// Constant-time even though the lookup key IS the hash. It costs nothing, and it is the check that
	// fails loudly if the store is ever changed to fetch by collector_id and compare afterwards.
	if subtle.ConstantTimeCompare([]byte(cred.TokenSHA256), []byte(tokenHash)) != 1 {
		return refuse(RefusalHashMismatch)
	}
	if !cred.Binding.Complete() {
		return refuse(RefusalIncompleteBinding)
	}
	if !cred.Live() {
		if cred.State == collectorcredential.StatePending {
			return refuse(RefusalNotYetActive)
		}
		return refuse(RefusalRevoked)
	}
	// Boundary, not equality: a credential is dead AT expires_at, matching the JWT `exp` rule the
	// licence check uses, so the two cannot disagree about what an expiry instant means.
	if cred.ExpiresAt != nil && !now.Before(*cred.ExpiresAt) {
		return refuse(RefusalExpired)
	}

	if conn == nil {
		return unavailable(RefusalConnectorUnknown)
	}
	if conn.Terminating {
		return unavailable(RefusalConnectorTerminating)
	}
	if conn.Provider != cred.Binding.Provider {
		return refuse(RefusalProviderMismatch)
	}
	if conn.AccountID != cred.Binding.AccountID {
		return refuse(RefusalAccountMismatch)
	}

	return Decision{
		Outcome:  OutcomeAccept,
		Identity: Identity{Binding: cred.Binding, CredentialID: cred.CredentialID},
	}
}

func refuse(r Refusal) Decision      { return Decision{Outcome: OutcomeRefuse, Refusal: r} }
func unavailable(r Refusal) Decision { return Decision{Outcome: OutcomeUnavailable, Refusal: r} }

// How connector-opamp-gateway finds a connector's OpAMP server.
//
// Re-exported from connectors/opamp, which is where they are DEFINED: connector-runtime writes the label
// and the port and must not depend on the base's whole surface to learn three strings. Named here too so
// gateway code reads in one vocabulary rather than reaching across for a constant at a time.
const (
	LabelConnectorID = opamp.LabelConnectorID
	ServicePortName  = opamp.ServicePortName
	// DefaultServerPort is used when a Service declares no port by ServicePortName.
	DefaultServerPort = opamp.ServerPort
)
