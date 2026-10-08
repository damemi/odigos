package deployer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	smtypes "github.com/aws/aws-sdk-go-v2/service/secretsmanager/types"

	baseapi "github.com/odigos-io/odigos-enterprise/connectors/base/api"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorcredential"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorownership"
	"github.com/odigos-io/odigos-enterprise/connectors/base/domain/collectorstatus"
)

// Tags recording WHICH value a secret currently holds.
//
// They exist because the connector cannot read its own secrets back — it holds no
// secretsmanager:GetSecretValue at all, by design — so without them there is no way to tell an
// unchanged value from a replaced one, and the only safe assumption would be "replaced", which writes a
// new secret version on every reconcile pass for ever.
//
// The value is a digest, never a prefix of the secret: enough to tell two values apart and useless for
// recovering either. The same thing collectorcredential.Secret.Fingerprint documents as safe to log.
//
// One tag per CONCERN rather than one shared "fingerprint" tag, because the two live on different
// secrets with different lifetimes and a shared spelling would invite the mistake of comparing one
// against the other. Neither is part of the ownership tag set — they authorize nothing and join
// nothing — but both obey the same one-spelling-on-every-cloud rule, so the Azure and GCP deployers can
// use them unchanged.
const (
	// TagLicenseFingerprint marks the licence token a secret holds.
	TagLicenseFingerprint = "odigos-license-fingerprint"
	// TagBootstrapFingerprint marks the Collector configuration a bootstrap secret holds. It is the
	// configuration's own hash — the same value managed_collectors.config_hash records — so a reader can
	// see at a glance whether the document a cold start would serve is the one Odigos last pushed.
	TagBootstrapFingerprint = "odigos-bootstrap-fingerprint"
)

// SecretsClient is the slice of Secrets Manager this package uses.
//
// GetSecretValue is deliberately absent, and its absence is a designed property rather than an
// oversight: the connector keeps only a credential's hash and never reads the value back, so no code
// path here can be made to return one. RestoreSecret is present because a deleted secret's NAME stays
// reserved for its recovery window — up to 30 days — and without it, tearing a Collector down and
// enabling it again would be refused for a month.
type SecretsClient interface {
	DescribeSecret(ctx context.Context, in *secretsmanager.DescribeSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.DescribeSecretOutput, error)
	CreateSecret(ctx context.Context, in *secretsmanager.CreateSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.CreateSecretOutput, error)
	PutSecretValue(ctx context.Context, in *secretsmanager.PutSecretValueInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.PutSecretValueOutput, error)
	TagResource(ctx context.Context, in *secretsmanager.TagResourceInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.TagResourceOutput, error)
	RestoreSecret(ctx context.Context, in *secretsmanager.RestoreSecretInput, opts ...func(*secretsmanager.Options)) (*secretsmanager.RestoreSecretOutput, error)
}

// SecretInput is what converging one secret depends on.
type SecretInput struct {
	Name        string
	Description string
	Connector   string
	ConnectorID string
	// Region identifies the regional Collector. Empty only for connector-wide objects.
	Region string
	// NetworkID is host metadata. Empty for the region-wide licence secret, which belongs to no single
	// host network.
	NetworkID string
	Identity  collectorownership.Identity
	Mode      string
	// FingerprintTag is which of the fingerprint tags above to read back into SecretState.Fingerprint.
	// Empty for a secret whose value nothing compares — the OpAMP credential, whose replacement is
	// decided by the credential row rather than by what the secret holds.
	FingerprintTag string
}

// SecretState is what an existing secret is holding.
type SecretState struct {
	// ARN is what a task definition references and what a credential row records as its secret_ref.
	ARN string
	// HasValue reports whether any version exists. A secret is created EMPTY — see EnsureSecret — so
	// false means nothing has been written to it yet.
	HasValue bool
	// Fingerprint is the value of SecretInput.FingerprintTag, empty when nothing recorded one or when no
	// tag was asked for.
	Fingerprint string
	// Restored reports whether this pass brought the secret back from scheduled deletion.
	Restored bool
}

// EnsureSecret converges one secret's existence, tags and recoverability, and returns what it holds.
//
// It creates the secret with NO VALUE, which is the ordering that makes the rest work: the credential
// row records where its value will live, so the ARN has to exist
// before anything is minted. An empty secret is harmless — the only thing that would reference it is a
// task definition, and nothing registers one until the value is in place.
//
// The three states an existing secret can be in are handled separately because their fixes are:
//
//   - absent — create it;
//   - scheduled for deletion — restore it, because the NAME is reserved for the recovery window and a
//     Collector torn down last week could otherwise not be re-provisioned for a month;
//   - live — verify Odigos may write to it, and refresh its tags.
//
// The ownership check here is strict, unlike the log group's. Overwriting a secret that is not ours
// destroys whatever the customer kept in it, and no amount of "the name looked like ours" makes that
// recoverable.
func EnsureSecret(ctx context.Context, api SecretsClient, in SecretInput) (SecretState, []collectorstatus.Problem, error) {
	if problems := modeGuard(in.Mode); len(problems) > 0 {
		return SecretState{}, problems, nil
	}
	tags := baseapi.CollectorManagedTags(in.Connector, in.ConnectorID, in.Region, in.NetworkID)

	described, err := api.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(in.Name)})
	switch {
	case isSecretNotFound(err):
		created, err := api.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
			Name:        aws.String(in.Name),
			Description: aws.String(in.Description),
			Tags:        secretTags(tags),
			// No SecretString. See the function comment: the ARN is needed before a value exists.
		})
		if err == nil {
			log.Info("created collector secret", "secret", in.Name)
			return SecretState{ARN: aws.ToString(created.ARN)}, nil, nil
		}
		if !isSecretExists(err) && !isSecretInvalidRequest(err) {
			return SecretState{}, nil, fmt.Errorf("create secret %s: %w", in.Name, err)
		}
		// Either another replica just created it, or the name is held by a secret scheduled for
		// deletion — CreateSecret reports the second as an InvalidRequest rather than a distinct type.
		// Re-describing tells the two apart without matching on an error message.
		described, err = api.DescribeSecret(ctx, &secretsmanager.DescribeSecretInput{SecretId: aws.String(in.Name)})
		if err != nil {
			return SecretState{}, nil, fmt.Errorf("describe secret %s after a create conflict: %w", in.Name, err)
		}

	case err != nil:
		return SecretState{}, nil, fmt.Errorf("describe secret %s: %w", in.Name, err)
	}

	arn := aws.ToString(described.ARN)
	object := collectorownership.Object{Ref: arn, Tags: baseapi.ParseCollectorTags(describedTags(described))}
	if !collectorownership.MayMutate(object, in.Identity, in.Mode) {
		return SecretState{}, []collectorstatus.Problem{notOurs("Secrets Manager secret", in.Name, object, in.Identity)}, nil
	}

	state := SecretState{
		ARN:         arn,
		HasValue:    len(described.VersionIdsToStages) > 0,
		Fingerprint: fingerprintOf(described, in.FingerprintTag),
	}

	if described.DeletedDate != nil {
		if _, err := api.RestoreSecret(ctx, &secretsmanager.RestoreSecretInput{SecretId: aws.String(arn)}); err != nil {
			return SecretState{}, nil, fmt.Errorf("restore secret %s from scheduled deletion: %w", in.Name, err)
		}
		log.Info("restored collector secret from scheduled deletion", "secret", in.Name)
		state.Restored = true
		// A restored secret keeps its old versions, and the credential they hold was revoked at
		// teardown. Reported as valueless so the caller issues a fresh credential and writes it, rather
		// than deploying a Collector that authenticates with something the gateway now refuses.
		state.HasValue = false
	}

	if _, err := api.TagResource(ctx, &secretsmanager.TagResourceInput{
		SecretId: aws.String(arn),
		Tags:     secretTags(tags),
	}); err != nil {
		return SecretState{}, nil, fmt.Errorf("tag secret %s: %w", in.Name, err)
	}
	return state, nil, nil
}

// PutSecretValue writes a value into a secret and returns the version it became.
//
// The ONE function in this package that reveals a secret value, and the reason it is one function
// rather than an argument to the two calls that could carry one: CreateSecret can also take a value,
// and if it did, "where can a credential leave the process" would be two answers instead of one.
// deployer_secrecy_test.go pins that.
//
// Writing a value rotates nothing on its own. A running task holds what was injected at start, so the
// redeploy is the rotation — and on AWS the previous value stays retrievable as AWSPREVIOUS, so
// what actually invalidates the old credential is revocation at the gateway, never this call.
func PutSecretValue(ctx context.Context, api SecretsClient, secretARN string, value collectorcredential.Secret) (string, error) {
	return putSecretValue(ctx, api, secretARN, value, false)
}

// PutRegistryLicenseSecretValue writes the existing Collector licence token in ECS's private-registry
// credential format. ECS consumes the whole JSON object before a container starts; the task definition
// separately selects the `password` field for ODIGOS_ONPREM_TOKEN, so the Collector still receives the
// original token rather than the JSON document.
func PutRegistryLicenseSecretValue(ctx context.Context, api SecretsClient, secretARN string, value collectorcredential.Secret) (string, error) {
	return putSecretValue(ctx, api, secretARN, value, true)
}

func putSecretValue(ctx context.Context, api SecretsClient, secretARN string, value collectorcredential.Secret, registryCredentials bool) (string, error) {
	if value.Empty() {
		// Refused rather than written. An empty secret version is a Collector that starts, presents an
		// empty Authorization header and is reported as "deployed but never connected" — the one failure
		// that says the connector cannot diagnose, because it does not read the secret back.
		return "", errors.New("refusing to write an empty value to a Collector secret: the Collector would start, authenticate with nothing, and be reported as deployed but never connected")
	}
	secretString := value.Reveal()
	if registryCredentials {
		encoded, err := json.Marshal(struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}{Username: "odigos", Password: secretString})
		if err != nil {
			return "", fmt.Errorf("encode Collector registry credentials: %w", err)
		}
		secretString = string(encoded)
	}
	out, err := api.PutSecretValue(ctx, &secretsmanager.PutSecretValueInput{
		SecretId:     aws.String(secretARN),
		SecretString: aws.String(secretString),
	})
	if err != nil {
		// The error is returned unwrapped of the value by construction — nothing here interpolates it,
		// and the SDK does not echo a request body into an error.
		return "", fmt.Errorf("write value to secret %s: %w", secretARN, err)
	}
	return aws.ToString(out.VersionId), nil
}

// RecordSecretFingerprint stamps which value a secret now holds, so the next pass can tell an unchanged
// value from a replaced one. See the fingerprint tags above.
func RecordSecretFingerprint(ctx context.Context, api SecretsClient, secretARN, tag, fingerprint string) error {
	if _, err := api.TagResource(ctx, &secretsmanager.TagResourceInput{
		SecretId: aws.String(secretARN),
		Tags:     secretTags(map[string]string{tag: fingerprint}),
	}); err != nil {
		return fmt.Errorf("record the %s on secret %s: %w", tag, secretARN, err)
	}
	return nil
}

// fingerprintOf reads one fingerprint tag off a described secret.
func fingerprintOf(described *secretsmanager.DescribeSecretOutput, tag string) string {
	if tag == "" {
		return ""
	}
	return describedTags(described)[tag]
}

func describedTags(out *secretsmanager.DescribeSecretOutput) map[string]string {
	if out == nil {
		return nil
	}
	tags := make(map[string]string, len(out.Tags))
	for _, t := range out.Tags {
		tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
	}
	return tags
}

func secretTags(tags map[string]string) []smtypes.Tag {
	out := make([]smtypes.Tag, 0, len(tags))
	for _, k := range sortedKeys(tags) {
		out = append(out, smtypes.Tag{Key: aws.String(k), Value: aws.String(tags[k])})
	}
	return out
}

func isSecretNotFound(err error) bool {
	var e *smtypes.ResourceNotFoundException
	return errors.As(err, &e)
}

func isSecretExists(err error) bool {
	var e *smtypes.ResourceExistsException
	return errors.As(err, &e)
}

// isSecretInvalidRequest covers the one case CreateSecret gives no dedicated type for: a name still
// held by a secret scheduled for deletion. Matched on the type rather than the message, and answered
// by re-describing rather than by trusting the classification.
func isSecretInvalidRequest(err error) bool {
	var e *smtypes.InvalidRequestException
	return errors.As(err, &e)
}
