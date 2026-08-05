package instrumentor

import (
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/awsclient"
)

// ImageConfig is the subset of an OCI image config used for language detection: the CNB buildpack labels
// plus the process-level signals (entrypoint/cmd/env/working-dir) that are the static analog of what OSS
// Odigos' procdiscovery reads from a live process.
type ImageConfig struct {
	Labels     map[string]string
	Entrypoint []string
	Cmd        []string
	Env        []string // raw KEY=VALUE entries from the image config
	WorkingDir string
}

// ImageInspector reads the OCI image config for a container image reference. It is an interface so the
// language resolver can be unit-tested with a fake and so the (network + registry auth) implementation
// stays isolated. Exported so the provider can build one from the shared AWS config cache and inject it.
type ImageInspector interface {
	// ImageConfig returns the image's config (labels + process signals). An error is returned when the
	// image cannot be fetched or read; callers treat that as "no signal" rather than a hard failure.
	ImageConfig(ctx context.Context, imageRef string) (*ImageConfig, error)
}

// ecrHostRe matches a private Amazon ECR registry host and captures (account id, region).
var ecrHostRe = regexp.MustCompile(`^(\d{12})\.dkr\.ecr\.([a-z0-9-]+)\.amazonaws\.com$`)

// ecrTokenSafetyMargin refreshes an ECR authorization token this long before its stated expiry (tokens
// are valid ~12h) so an in-flight fetch never rides an about-to-expire token.
const ecrTokenSafetyMargin = 10 * time.Minute

// ecrImageInspector reads image configs with go-containerregistry. Images in private ECR registries
// authenticate with an ecr:GetAuthorizationToken credential (cached per region until expiry); everything
// else (Docker Hub, public.ecr.aws, quay, …) uses the default keychain, which degrades to anonymous —
// matching how Fargate itself pulls public images.
type ecrImageInspector struct {
	cache *awsclient.Cache

	mu     sync.Mutex
	tokens map[string]ecrToken // by ECR region
}

type ecrToken struct {
	auth      authn.Authenticator
	expiresAt time.Time
}

// NewECRImageInspector builds the AWS image inspector over the shared per-region config cache.
func NewECRImageInspector(cache *awsclient.Cache) ImageInspector {
	return &ecrImageInspector{cache: cache, tokens: map[string]ecrToken{}}
}

func (x *ecrImageInspector) ImageConfig(ctx context.Context, imageRef string) (*ImageConfig, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("parse image reference %q: %w", imageRef, err)
	}
	opts := []remote.Option{remote.WithContext(ctx)}
	if m := ecrHostRe.FindStringSubmatch(ref.Context().RegistryStr()); m != nil {
		auth, err := x.ecrAuth(ctx, m[2])
		if err != nil {
			return nil, fmt.Errorf("ecr auth for %s: %w", ref.Context().RegistryStr(), err)
		}
		opts = append(opts, remote.WithAuth(auth))
	} else {
		opts = append(opts, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	}
	img, err := remote.Image(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("fetch image %q: %w", imageRef, err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("read image config %q: %w", imageRef, err)
	}
	if cfg == nil {
		return nil, nil
	}
	return &ImageConfig{
		Labels:     cfg.Config.Labels,
		Entrypoint: cfg.Config.Entrypoint,
		Cmd:        cfg.Config.Cmd,
		Env:        cfg.Config.Env,
		WorkingDir: cfg.Config.WorkingDir,
	}, nil
}

// ecrAuth returns a registry authenticator for the given ECR region, minting (and caching) an
// authorization token via ecr:GetAuthorizationToken. The token is caller-scoped, so one per region
// serves every ECR registry the connector's credentials can read.
func (x *ecrImageInspector) ecrAuth(ctx context.Context, region string) (authn.Authenticator, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if t, ok := x.tokens[region]; ok && time.Now().Before(t.expiresAt) {
		return t.auth, nil
	}
	cfg, err := x.cache.Config(ctx, region)
	if err != nil {
		return nil, err
	}
	out, err := ecr.NewFromConfig(cfg).GetAuthorizationToken(ctx, &ecr.GetAuthorizationTokenInput{})
	if err != nil {
		return nil, err
	}
	if len(out.AuthorizationData) == 0 {
		return nil, fmt.Errorf("ecr returned no authorization data")
	}
	ad := out.AuthorizationData[0]
	raw, err := base64.StdEncoding.DecodeString(aws.ToString(ad.AuthorizationToken))
	if err != nil {
		return nil, fmt.Errorf("decode ecr authorization token: %w", err)
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok {
		return nil, fmt.Errorf("malformed ecr authorization token")
	}
	auth := authn.FromConfig(authn.AuthConfig{Username: user, Password: pass})
	expires := time.Now().Add(time.Hour)
	if ad.ExpiresAt != nil {
		expires = ad.ExpiresAt.Add(-ecrTokenSafetyMargin)
	}
	x.tokens[region] = ecrToken{auth: auth, expiresAt: expires}
	return auth, nil
}
