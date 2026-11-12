package collectorimage

import (
	"fmt"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

const EnvImage = "ODIGOS_COLLECTOR_IMAGE"

// Selection is the image override in collector.defaults.image or collector.regions.<region>.image.
// Presence matters: setting a tag clears an inherited digest, including an explicitly empty digest.
type Selection map[string]any

func (s Selection) Validate() error {
	if err := s.validateFields(); err != nil {
		return err
	}
	_, err := Configure(Pin{}, CloudAWS, s)
	return err
}

func (s Selection) validateFields() error {
	for key, value := range s {
		switch key {
		case "reference", "registry", "repository", "repositoryOverride", "tag", "digest":
		default:
			return fmt.Errorf("unknown Collector image setting %q", key)
		}
		if _, ok := value.(string); !ok {
			return fmt.Errorf("Collector image %s must be a string", key)
		}
	}
	return nil
}

func Overlay(base, override Selection) Selection {
	out := Selection{}
	if _, replaces := override["reference"]; !replaces {
		for key, value := range base {
			out[key] = value
		}
	}
	if _, changesTag := override["tag"]; changesTag {
		delete(out, "digest")
	}
	for key, value := range override {
		out[key] = value
	}
	return out
}

// FromReference accepts an explicit tag or digest, never an implicit latest or a source default.
func FromReference(ref string) (Pin, error) {
	if strings.TrimSpace(ref) != ref || (!strings.Contains(ref, "@") && strings.LastIndex(ref, ":") <= strings.LastIndex(ref, "/")) {
		return Pin{}, fmt.Errorf("Collector image must include an explicit tag or digest: %q", ref)
	}
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return Pin{}, fmt.Errorf("invalid Collector image %q: %w", ref, err)
	}
	p := Pin{Repositories: map[Cloud]string{}, Platforms: []Arch{ArchAMD64, ArchARM64}}
	for _, cloud := range []Cloud{CloudAWS, CloudAzure, CloudGCP} {
		p.Repositories[cloud] = parsed.Context().Name()
	}
	switch parsed.(type) {
	case name.Digest:
		p.Digest = parsed.Identifier()
		if !digestPattern.MatchString(p.Digest) {
			return Pin{}, fmt.Errorf("digest %q is not a full sha256 OCI digest", p.Digest)
		}
	case name.Tag:
		p.Tag = parsed.Identifier()
	}
	return p, nil
}

// Configure resolves runtime overrides over the installation's selection without mutating either.
// It permits partial settings; Resolve refuses a missing repository/version before deployment.
func Configure(base Pin, cloud Cloud, selection Selection) (Pin, error) {
	if err := selection.validateFields(); err != nil {
		return Pin{}, err
	}
	p := base
	get := func(key string) string { value, _ := selection[key].(string); return value }
	if ref := get("reference"); ref != "" {
		var err error
		p, err = FromReference(ref)
		if err != nil {
			return Pin{}, err
		}
	}
	if len(p.Platforms) == 0 {
		p.Platforms = []Arch{ArchAMD64, ArchARM64}
	}
	repositories := map[Cloud]string{}
	for key, value := range p.Repositories {
		repositories[key] = value
	}
	p.Repositories = repositories
	repo := p.Repositories[cloud]
	if value := get("repositoryOverride"); value != "" {
		repo = value
	}
	if value := get("repository"); value != "" {
		repo = value
	}
	if repo != "" {
		if problem := malformedRepo(repo); problem != "" {
			return Pin{}, fmt.Errorf("Collector image repository %q %s", repo, problem)
		}
	}
	if registry := get("registry"); registry != "" {
		if strings.ContainsAny(registry, "/@ \t\r\n") {
			return Pin{}, fmt.Errorf("invalid Collector image registry %q: use a registry host, optionally with a port", registry)
		}
		if _, err := name.NewRegistry(registry, name.StrictValidation); err != nil {
			return Pin{}, fmt.Errorf("invalid Collector image registry: %w", err)
		}
		if repo != "" {
			parsed, err := name.NewRepository(repo)
			if err != nil {
				return Pin{}, err
			}
			repo = registry + "/" + parsed.RepositoryStr()
		}
	}
	p.Repositories[cloud] = repo
	if tag, exists := selection["tag"]; exists {
		p.Tag, _ = tag.(string)
		p.Digest = ""
		if p.Tag != "" {
			if _, err := name.NewTag("example.com/collector:" + p.Tag); err != nil {
				return Pin{}, fmt.Errorf("invalid Collector image tag %q", p.Tag)
			}
		}
	}
	if digest, exists := selection["digest"]; exists {
		p.Digest, _ = digest.(string)
		if p.Digest != "" && !digestPattern.MatchString(p.Digest) {
			return Pin{}, fmt.Errorf("digest %q is not a full sha256 OCI digest", p.Digest)
		}
	}
	return p, nil
}
