package deployer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorgateway"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
)

// CredentialStore is the slice of the connector's database this package uses.
//
// Note what is NOT here: nothing that returns a credential VALUE, because no such accessor exists —
// only the hash is persisted, so the one-time reveal at issuance is structural rather than a policy
// somebody could relax.
type CredentialStore interface {
	// GetManagedCollector reads the row, or ErrCollectorNotFound.
	GetManagedCollector(ctx context.Context, key basestore.CollectorKey) (basestore.CollectorRecord, error)
	// UpsertManagedCollector creates the row a credential is recorded against.
	UpsertManagedCollector(ctx context.Context, key basestore.CollectorKey, networkID, kind, mode, origin string, adoptedAt *time.Time) error
	// CollectorCredential reads one issuance.
	CollectorCredential(ctx context.Context, collectorID, credentialID string) (collectorcredential.Record, error)
	// IssueCollectorCredential mints, persists the verifier and binding, and audits — all or nothing.
	IssueCollectorCredential(ctx context.Context, req collectorcredential.IssueRequest) (collectorcredential.Issued, error)
	// MarkCollectorCredentialActive records that the value reached the secret store.
	MarkCollectorCredentialActive(ctx context.Context, collectorID, credentialID, secretVersion string) error
	// RecordCollectorIdentity writes the Collector's identity and credential refs onto its row.
	RecordCollectorIdentity(ctx context.Context, key basestore.CollectorKey, collectorID, credentialRef, activeCredentialID string) error
}

// CredentialInput is what installing one Collector's credential depends on.
type CredentialInput struct {
	Key  basestore.CollectorKey
	Kind string
	Mode string
	// Binding is the immutable tuple the credential will authenticate, minus the CollectorID, which the
	// store allocates for a Collector that has none. Every field is authoritative somewhere else — the
	// CR UID from config, the account from the provider, the network from placement — so it is supplied
	// whole rather than assembled here (collectorcredential.IssueRequest says why).
	Binding collectorcredential.Binding
	// SecretARN is where the value goes. The secret must already exist: a credential row with a
	// secret_ref pointing at nothing describes a value that was minted and had nowhere to go.
	SecretARN string
	// SecretHasValue reports whether that secret already holds a version.
	SecretHasValue bool
}

// Credential is the installed credential's identity. No field can carry a value.
type Credential struct {
	CollectorID  string
	CredentialID string
	SecretRef    string
	// Issued reports whether THIS pass minted one. False is the steady state and the interesting case:
	// see EnsureCredential.
	Issued bool
}

// EnsureCredential installs a Collector's OpAMP credential, exactly once.
//
// The rule that matters is the one that does nothing. A Collector whose row already names a LIVE
// credential is left alone, because minting another is a rotation, a rotation is a redeploy,
// "the redeploy IS the rotation"), and a redeploy on every reconcile pass is a fleet that never stops
// rolling — every instrumented workload on the network churning for ever, for no reason anybody could
// see from the outside.
//
// So this issues in exactly three situations, and each is a case where there is provably no working
// credential to preserve:
//
//   - the Collector has no identity yet — nothing has ever been issued;
//   - its recorded credential is revoked, superseded away, or missing from the table — nothing the
//     gateway would accept;
//   - the secret holds no value — the credential exists on paper and never reached the Collector,
//     which is what a restored secret and a half-finished earlier pass both look like.
//
// A rotation is NOT this function. Rotation moves the outgoing credential aside first and revokes it
// only after the replacement connects (basestore.ReplaceCollectorCredential); issuing here on a live
// credential would revoke nothing and simply orphan it.
func EnsureCredential(ctx context.Context, store CredentialStore, secrets SecretsClient, in CredentialInput) (Credential, []collectorstatus.Problem, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return Credential{}, problems, nil
	}

	existing, needed, err := credentialNeeded(ctx, store, in)
	if err != nil {
		return Credential{}, nil, err
	}
	if !needed {
		return existing, nil, nil
	}

	req := collectorcredential.IssueRequest{
		Binding:   in.Binding,
		SecretRef: in.SecretARN,
		// No expiry, in both modes. The Supervisor sets accepts_opamp_connection_settings: false, so
		// nothing can replace the header in place and an expiry would stop the Collector at a wall-clock
		// time with no automatic cure. CheckLifetime refuses one anyway; not setting it is what
		// keeps this from depending on that.
		Path:  collectorcredential.RotationPath{},
		Actor: collectorcredential.ActorConnector,
	}
	// time.Now rather than an injected clock: the only thing Validate does with it is CheckLifetime,
	// and with no expiry to check that rule has nothing to compare against. A clock argument here would
	// be a seam with nothing behind it.
	if problems := req.Validate(time.Now()); len(problems) > 0 {
		return Credential{}, problems, nil
	}

	issued, err := store.IssueCollectorCredential(ctx, req)
	if err != nil {
		return Credential{}, []collectorstatus.Problem{{
			Code:     ProblemCredentialIssueFailed,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("could not issue an OpAMP credential for the Collector on %s in %s: %v. "+
				"Nothing is deployed — a Collector with no credential would start and serve the inert "+
				"bootstrap configuration for ever", in.Binding.NetworkID, in.Key.Region, err),
		}}, nil
	}

	// The row is pending until the value reaches the secret store, and the gateway refuses a pending
	// credential — so a value that never left the issuer cannot authenticate anything. These are two
	// failures that happen independently, which is exactly why they are two steps.
	// The COMPLETE Authorization header, not the bare token. The Supervisor copies
	// ODIGOS_OPAMP_AUTHORIZATION into the header verbatim, so a secret holding the bare value produces a
	// header with no scheme, which the gateway refuses — reported as "deployed but never connected", the
	// one failure the connector cannot diagnose because it does not read the secret back. The verifier
	// stays the hash of the bare token; this adds a transport scheme, not a second secret.
	version, err := PutSecretValue(ctx, secrets, in.SecretARN, collectorgateway.AuthorizationHeader(issued.Minted.Secret))
	if err != nil {
		// The credential stays pending and unusable. Left rather than cleaned up: the next pass sees a
		// non-live credential and issues a fresh one, and deleting a row whose value may or may not have
		// landed would throw away the only record that it was ever issued.
		return Credential{}, []collectorstatus.Problem{{
			Code:     ProblemCredentialIssueFailed,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("issued an OpAMP credential for the Collector on %s in %s but could not write "+
				"it to %s: %v. It stays pending, which the gateway refuses, and the next pass issues another",
				in.Binding.NetworkID, in.Key.Region, in.SecretARN, err),
		}}, nil
	}

	collectorID := issued.Record.Binding.CollectorID
	if err := store.MarkCollectorCredentialActive(ctx, collectorID, issued.Record.CredentialID, version); err != nil {
		return Credential{}, nil, fmt.Errorf("activate credential %s: %w", issued.Record.CredentialID, err)
	}
	if err := store.RecordCollectorIdentity(ctx, in.Key, collectorID, in.SecretARN, issued.Record.CredentialID); err != nil {
		return Credential{}, nil, fmt.Errorf("record collector identity for region %s: %w", in.Key.Region, err)
	}

	// The fingerprint, never the hash and never the value: eight hex characters correlate this line with
	// the credential row and the audit event, and are useless for anything else.
	log.Info("installed collector opamp credential",
		"region", in.Key.Region, "network", in.Binding.NetworkID,
		"collector_id", collectorID, "credential_id", issued.Record.CredentialID,
		"fingerprint", collectorcredential.FingerprintOfHash(issued.Record.TokenSHA256))

	return Credential{
		CollectorID:  collectorID,
		CredentialID: issued.Record.CredentialID,
		SecretRef:    in.SecretARN,
		Issued:       true,
	}, nil, nil
}

// credentialNeeded decides whether to issue, and returns the existing credential when not.
func credentialNeeded(ctx context.Context, store CredentialStore, in CredentialInput) (Credential, bool, error) {
	record, err := store.GetManagedCollector(ctx, in.Key)
	switch {
	case errors.Is(err, basestore.ErrCollectorNotFound):
		// First pass for this regional Collector. The row has to exist before an identity can be recorded against
		// it, and origin is odigos because this connector is about to create the infrastructure.
		if err := store.UpsertManagedCollector(ctx, in.Key, in.Binding.NetworkID, in.Kind, in.Mode, string(collectorownership.OriginOdigos), nil); err != nil {
			return Credential{}, false, fmt.Errorf("create the managed Collector row for region %s: %w", in.Key.Region, err)
		}
		return Credential{}, true, nil
	case err != nil:
		return Credential{}, false, fmt.Errorf("read the managed Collector row for region %s: %w", in.Key.Region, err)
	}

	if record.CollectorID == "" || record.ActiveCredentialID == "" {
		return Credential{}, true, nil
	}
	if !in.SecretHasValue {
		// The paperwork says a credential is active and the secret holds nothing, so whatever was issued
		// never reached the Collector. Trusting the row here is what would leave a Collector permanently
		// unable to connect while everything reported as provisioned.
		return Credential{}, true, nil
	}

	current, err := store.CollectorCredential(ctx, record.CollectorID, record.ActiveCredentialID)
	if err != nil {
		if errors.Is(err, basestore.ErrCredentialNotFound) {
			return Credential{}, true, nil
		}
		return Credential{}, false, fmt.Errorf("read credential %s: %w", record.ActiveCredentialID, err)
	}
	if !current.Live() {
		return Credential{}, true, nil
	}

	return Credential{
		CollectorID:  record.CollectorID,
		CredentialID: current.CredentialID,
		SecretRef:    record.CredentialRef,
	}, false, nil
}

// BindingFor assembles the immutable tuple a Collector's credential authenticates.
//
// CollectorID is left empty: the store allocates one for a Collector that has no identity yet, and
// inventing one here would mean two places could mint identities.
func BindingFor(connectorUID, provider, accountID string, key basestore.CollectorKey, hostNetworkID string) collectorcredential.Binding {
	return collectorcredential.Binding{
		ConnectorUID: connectorUID,
		Provider:     provider,
		AccountID:    accountID,
		Region:       key.Region,
		NetworkID:    hostNetworkID,
	}
}
