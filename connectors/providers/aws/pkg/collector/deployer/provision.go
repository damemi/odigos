package deployer

import (
	"context"
	"fmt"

	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorimage"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
	basestore "github.com/odigos-io/odigos-enterprise/connectors/base/store"
)

// Clients supplies the AWS clients this package needs, per region where that matters.
type Clients interface {
	Logs(ctx context.Context, region string) (LogsClient, error)
	Secrets(ctx context.Context, region string) (SecretsClient, error)
}

// Provisioner provisions Collector identity, logging and credentials for one connector.
type Provisioner struct {
	// Identity is this connector, as collectorownership reads it: the name is the adoption key and the
	// CR UID is the authority key.
	Identity collectorownership.Identity
	Provider string
	// AccountID is the AWS account boundary. Every policy resource is scoped to it, and it is half of
	// every credential binding.
	AccountID string
	// License is the connector's own ODIGOS_ONPREM_TOKEN, wrapped so it cannot reach a log or a
	// serialized structure. the licence installed into a customer's account is THEIRS — the
	// connector copies the value every other enterprise component in that install already uses, and
	// Odigos-the-vendor's own licence material never enters a customer account.
	License collectorcredential.Secret

	Store   CredentialStore
	Clients Clients
}

// SharedInput is what the connector-wide objects depend on.
type SharedInput struct {
	// Regions is every region with a Collector. Used only to derive the ARN partition, which one
	// account is entirely inside.
	Regions []string
	Mode    string
}

// Shared is what every Collector in this connector uses.
type Shared struct {
	Partition string
	Problems  []collectorstatus.Problem
}

// Ready reports whether the shared objects are usable.
func (s Shared) Ready() bool {
	return s.Partition != "" && !collectorstatus.HasError(s.Problems)
}

// EnsureShared resolves the account partition shared by every regional Collector. IAM roles are
// customer-owned prerequisites and are never read or converged here.
func (p *Provisioner) EnsureShared(_ context.Context, in SharedInput) (Shared, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return Shared{Problems: problems}, nil
	}
	if len(in.Regions) == 0 {
		// No Collector anywhere. Nothing to provision, and nothing to report: the reason there are no
		// networks belongs to placement, which already said it.
		return Shared{}, nil
	}

	return Shared{Partition: Partition(in.Regions[0])}, nil
}

// NetworkInput is what one regional Collector's own prerequisites depend on. The name remains for
// provider compatibility; Key identifies the region and HostNetworkID is only its physical placement.
type NetworkInput struct {
	Key           basestore.CollectorKey
	HostNetworkID string
	Kind          string
	Mode          string
	// ExecutionRoleARN is the required customer-owned ECS task execution role.
	ExecutionRoleARN string
	// Shared is the result of EnsureShared and carries the account partition.
	Shared Shared
	// RetentionDays is the resolved `logRetentionDays`.
	RetentionDays int
}

// Prerequisites is everything one Collector needs before its ECS service can be created.
type Prerequisites struct {
	ExecutionRoleARN string
	LogGroup         string
	// Delivery carries the two secret REFS, and has no field a value could live in.
	Delivery   collectorimage.Delivery
	Credential Credential
	Problems   []collectorstatus.Problem
}

// Ready reports whether a Collector can now be deployed. What #12 gates on.
func (p Prerequisites) Ready() bool {
	return p.ExecutionRoleARN != "" && p.LogGroup != "" && p.Credential.CollectorID != "" &&
		len(collectorimage.DeliveryProblems(p.Delivery)) == 0 &&
		!collectorstatus.HasError(p.Problems)
}

// EnsureNetwork converges one Collector's log group, its two secrets, and its OpAMP credential.
//
// Order is not arbitrary. The licence secret and the credential secret are created before anything is
// written into either, because a credential row records where its value WILL live and a secret_ref
// pointing at nothing describes a value that was minted and had nowhere to go. The credential is
// installed last, because it is the only step that is not repeatable: everything before it can be run
// again with no effect, and minting again would rotate a Collector that had no reason to rotate.
func (p *Provisioner) EnsureNetwork(ctx context.Context, in NetworkInput) (Prerequisites, error) {
	var out Prerequisites
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		out.Problems = problems
		return out, nil
	}
	if !in.Shared.Ready() {
		out.Problems = []collectorstatus.Problem{{
			Code:     ProblemObjectNotOurs,
			Severity: collectorstatus.SeverityError,
			Message: fmt.Sprintf("no Collector is provisioned on %s in %s: its AWS partition could not be resolved",
				in.HostNetworkID, in.Key.Region),
		}}
		return out, nil
	}
	role, roleProblems := ValidateExecutionRoleARN(in.ExecutionRoleARN, in.Shared.Partition, p.AccountID, in.Key.Region)
	out.ExecutionRoleARN = role
	out.Problems = append(out.Problems, roleProblems...)
	if role == "" {
		return out, nil
	}

	logsAPI, err := p.Clients.Logs(ctx, in.Key.Region)
	if err != nil {
		return out, fmt.Errorf("build a CloudWatch Logs client for %s: %w", in.Key.Region, err)
	}
	logGroup, logProblems, err := EnsureLogGroup(ctx, logsAPI, LogGroupInput{
		Connector:     p.Identity.ConnectorName,
		ConnectorID:   p.Identity.ConnectorID,
		Partition:     in.Shared.Partition,
		AccountID:     p.AccountID,
		Region:        in.Key.Region,
		RetentionDays: in.RetentionDays,
		Mode:          in.Mode,
	})
	out.LogGroup = logGroup
	out.Problems = append(out.Problems, logProblems...)
	if err != nil {
		return out, err
	}

	secretsAPI, err := p.Clients.Secrets(ctx, in.Key.Region)
	if err != nil {
		return out, fmt.Errorf("build a Secrets Manager client for %s: %w", in.Key.Region, err)
	}

	licenseRef, licenseProblems, err := p.ensureLicense(ctx, secretsAPI, in)
	out.Problems = append(out.Problems, licenseProblems...)
	if err != nil {
		return out, err
	}
	out.Delivery.LicenseSecretRef = licenseRef

	credentialSecret, secretProblems, err := EnsureSecret(ctx, secretsAPI, SecretInput{
		Name:        CredentialSecretName(p.Identity.ConnectorName, in.Key.Region),
		Description: "Odigos managed Collector — OpAMP credential for " + in.Key.Region + " (host " + in.HostNetworkID + ")",
		Connector:   p.Identity.ConnectorName,
		ConnectorID: p.Identity.ConnectorID,
		Region:      in.Key.Region,
		NetworkID:   in.HostNetworkID,
		Identity:    p.Identity,
		Mode:        in.Mode,
	})
	out.Problems = append(out.Problems, secretProblems...)
	if err != nil {
		return out, err
	}
	if credentialSecret.ARN == "" {
		// Refused, and the reason is already in Problems. Returning here rather than falling through:
		// EnsureCredential with no secret ref would add "the binding is incomplete" on top of a specific,
		// actionable cause, and Ready() computes the delivery problems itself, so the gate still holds
		// without reporting them twice.
		return out, nil
	}
	out.Delivery.CredentialSecretRef = credentialSecret.ARN

	credential, credProblems, err := EnsureCredential(ctx, p.Store, secretsAPI, CredentialInput{
		Key:            in.Key,
		Kind:           in.Kind,
		Mode:           in.Mode,
		Binding:        BindingFor(p.Identity.ConnectorID, p.Provider, p.AccountID, in.Key, in.HostNetworkID),
		SecretARN:      credentialSecret.ARN,
		SecretHasValue: credentialSecret.HasValue,
	})
	out.Credential = credential
	out.Problems = append(out.Problems, credProblems...)
	if err != nil {
		return out, err
	}

	// The shared check both paths run: the deployer against the delivery it is about to build a task
	// definition from, the artifact guard against every published variant. Reported last so a specific
	// cause above it reads first.
	out.Problems = append(out.Problems, collectorimage.DeliveryProblems(out.Delivery)...)
	return out, nil
}

// ensureLicense converges the region's licence secret and returns its ARN.
//
// The value is written when the secret holds none, and rewritten only when the connector's token has
// actually changed — which is what TagLicenseFingerprint exists to answer, since the connector holds no
// permission to read the secret back. Writing unconditionally would mint a new secret version on every
// reconcile pass; never rewriting would mean a renewed licence never reaches the Collector, and
// makes that a crash loop rather than a degradation.
//
// Rewriting the secret does NOT get the new licence into a running task: like a credential rotation, a
// running task holds what was injected at start, so the redeploy is what installs it. That redeploy is
// #12's, driven by the task definition changing when the image or the secret version does.
func (p *Provisioner) ensureLicense(ctx context.Context, api SecretsClient, in NetworkInput) (string, []collectorstatus.Problem, error) {
	state, problems, err := EnsureSecret(ctx, api, SecretInput{
		Name:        LicenseSecretName(p.Identity.ConnectorName, in.Key.Region),
		Description: "Odigos managed Collector — enterprise licence token for " + in.Key.Region,
		Connector:   p.Identity.ConnectorName,
		ConnectorID: p.Identity.ConnectorID,
		Region:      in.Key.Region,
		// No network: one licence secret serves every Collector in the region.
		Identity: p.Identity,
		Mode:     in.Mode,
	})
	if err != nil || state.ARN == "" {
		return "", problems, err
	}

	if p.License.Empty() {
		// Reported rather than written empty, and the ref is withheld so nothing downstream builds a
		// task definition around a secret with nothing in it. The Collector's enterprise auth extension
		// refuses to start without a valid token, so deploying anyway produces a crash loop.
		return "", append(problems, collectorstatus.Problem{
			Code:     ProblemLicenseTokenMissing,
			Severity: collectorstatus.SeverityError,
			Message: "the connector holds no " + collectorimage.EnvOnPremToken + ", so there is nothing to copy " +
				"into this account's licence secret. The managed Collector refuses to start without one. The " +
				"connector reads the same value every other enterprise component in this install uses, from the " +
				"odigos-pro secret — check that it is present and that the connector Deployment is stamped with it",
		}), nil
	}

	fingerprint := p.License.Fingerprint()
	if state.HasValue && state.LicenseFingerprint == fingerprint {
		return state.ARN, problems, nil
	}
	if _, err := PutSecretValue(ctx, api, state.ARN, p.License); err != nil {
		return "", problems, err
	}
	if err := RecordLicenseFingerprint(ctx, api, state.ARN, fingerprint); err != nil {
		// The value landed and the marker did not, so the next pass rewrites the value: one wasted
		// secret version per pass until the tag write succeeds. Not fatal, and not silent.
		log.Warn("wrote the collector licence secret but could not record its fingerprint",
			"region", in.Key.Region, "secret", state.ARN, "error", err)
	}
	log.Info("installed collector licence secret",
		"region", in.Key.Region, "secret", state.ARN, "fingerprint", fingerprint)
	return state.ARN, problems, nil
}
