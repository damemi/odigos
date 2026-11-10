package instrumentor

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/awsclient"
)

type agentCoreArtifactIO interface {
	// OpenCode opens a deployed ZIP for ranged reads; detection never downloads the whole archive.
	OpenCode(context.Context, string, agentCoreArtifactRef) (agentCoreCode, error)
	Image(context.Context, string) (v1.Image, error)
}

// agentCoreAWSArtifacts only reads the customer's artifact: the ZIP from S3 and the image from its registry.
type agentCoreAWSArtifacts struct {
	config awsclient.ConfigSource
	images *ecrImageInspector
}

func newAgentCoreAWSArtifacts(cache *awsclient.Cache) *agentCoreAWSArtifacts {
	return &agentCoreAWSArtifacts{config: cache, images: &ecrImageInspector{cache: cache, tokens: map[string]ecrToken{}}}
}
func (a *agentCoreAWSArtifacts) OpenCode(ctx context.Context, region string, source agentCoreArtifactRef) (agentCoreCode, error) {
	cfg, err := a.config.Config(ctx, region)
	if err != nil {
		return nil, err
	}
	return openAgentCoreS3Code(ctx, s3.NewFromConfig(cfg), source)
}
func (a *agentCoreAWSArtifacts) options(ctx context.Context, ref name.Reference) ([]remote.Option, error) {
	opts := []remote.Option{remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: "arm64"})}
	if m := ecrHostRe.FindStringSubmatch(ref.Context().RegistryStr()); m != nil {
		auth, err := a.images.ecrAuth(ctx, m[2])
		if err != nil {
			return nil, err
		}
		opts = append(opts, remote.WithAuth(auth))
	} else {
		opts = append(opts, remote.WithAuthFromKeychain(authn.DefaultKeychain))
	}
	return opts, nil
}
func (a *agentCoreAWSArtifacts) Image(ctx context.Context, image string) (v1.Image, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return nil, err
	}
	opts, err := a.options(ctx, ref)
	if err != nil {
		return nil, err
	}
	return remote.Image(ref, opts...)
}
