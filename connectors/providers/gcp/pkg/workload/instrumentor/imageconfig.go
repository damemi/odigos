package instrumentor

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	ggcrgoogle "github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/oauth2"
	oauth2google "golang.org/x/oauth2/google"

	"github.com/odigos-io/odigos/connectors/providers/gcp/pkg/config"
)

// ImageConfig is the subset of an OCI image config used for language detection: the CNB buildpack labels
// plus the process-level signals (entrypoint/cmd/env/working-dir) that are the static analog of what OSS
// Odigos' procdiscovery reads from a live process (/proc/<pid>/exe, cmdline, environ).
type ImageConfig struct {
	Labels     map[string]string
	Entrypoint []string
	Cmd        []string
	Env        []string // raw KEY=VALUE entries from the image config
	WorkingDir string
}

// ImageInspector reads the OCI image config for a container image reference. It is an interface so the
// language resolver can be unit-tested with a fake and so the (network + registry auth) implementation
// stays isolated. Exported so the provider can build one from the connector credentials and inject it.
type ImageInspector interface {
	// ImageConfig returns the image's config (labels + process signals). An error is returned when the
	// image cannot be fetched or read; callers treat that as "no signal" rather than a hard failure.
	ImageConfig(ctx context.Context, imageRef string) (*ImageConfig, error)
}

// craneImageInspector reads image config labels from a registry (Artifact Registry / GCR) with
// go-containerregistry. It authenticates either with an explicit service-account token source (from the
// connector credentials) or, when the connector runs on ambient credentials, the GKE/metadata keychain.
type craneImageInspector struct {
	keychain authn.Keychain
	auth     authn.Authenticator
}

// NewImageInspector builds an image inspector from the connector credentials. Empty credentials use the
// ambient Google keychain (Application Default Credentials / metadata server); an explicit service-account
// key is turned into an OAuth token source scoped for registry reads.
func NewImageInspector(ctx context.Context, creds config.Credentials) (ImageInspector, error) {
	if creds.CredentialsJSON == "" {
		return &craneImageInspector{keychain: ggcrgoogle.Keychain}, nil
	}
	c, err := oauth2google.CredentialsFromJSON(ctx, []byte(creds.CredentialsJSON), "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, fmt.Errorf("parse gcp credentials for image inspection: %w", err)
	}
	return &craneImageInspector{auth: tokenSourceAuthenticator{ts: c.TokenSource}}, nil
}

func (c *craneImageInspector) ImageConfig(ctx context.Context, imageRef string) (*ImageConfig, error) {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return nil, fmt.Errorf("parse image reference %q: %w", imageRef, err)
	}
	opts := []remote.Option{remote.WithContext(ctx)}
	switch {
	case c.auth != nil:
		opts = append(opts, remote.WithAuth(c.auth))
	case c.keychain != nil:
		opts = append(opts, remote.WithAuthFromKeychain(c.keychain))
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

// tokenSourceAuthenticator adapts an OAuth2 token source to a go-containerregistry authenticator. Artifact
// Registry and GCR accept an OAuth access token as the password under a fixed username.
type tokenSourceAuthenticator struct {
	ts oauth2.TokenSource
}

func (a tokenSourceAuthenticator) Authorization() (*authn.AuthConfig, error) {
	t, err := a.ts.Token()
	if err != nil {
		return nil, err
	}
	return &authn.AuthConfig{Username: "oauth2accesstoken", Password: t.AccessToken}, nil
}
