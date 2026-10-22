// Package collectorimage defines which managed-Collector image a Collector runs, where each cloud
// pulls it from, and which architectures it may deploy on.
//
// Pure domain: no store, provider SDK, or registry calls.
//
// The installation supplies a versioned reference; runtime settings may override it. Automatic and
// manual mode resolve the same selection. Cloud-specific pull restrictions still apply.
//
// Registry authentication and runtime enterprise authorization are separate steps. The same
// ODIGOS_ONPREM_TOKEN may serve as the registry password, but a provider must supply it as image-pull
// credentials before the container starts. This package carries an optional credential reference,
// never the credential material.
package collectorimage

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

// Cloud is a target platform for a managed Collector.
type Cloud string

const (
	CloudAWS   Cloud = "aws"
	CloudAzure Cloud = "azure"
	CloudGCP   Cloud = "gcp"
)

// Arch is a CPU architecture in OCI spelling. Provider-native spellings belong in adapters.
type Arch string

const (
	ArchAMD64 Arch = "amd64"
	ArchARM64 Arch = "arm64"
)

// DefaultArch is used when a network names none. AMD64 is the portability default; not every
// provider/region has ARM64 capacity.
const DefaultArch = ArchAMD64

// digestPattern matches a full sha256 OCI digest. Truncated digests are rejected.
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Pin is the installation-selected image, optionally overridden by runtime settings.
// A digest takes precedence over a tag; no image version is compiled into the connector.
type Pin struct {
	// Digest is the manifest-list digest. Pinning a per-platform digest would break other arches.
	Digest string
	// Tag selects the release when Digest is empty.
	Tag string
	// Repositories is the bare repo each cloud pulls from (no tag/digest).
	Repositories map[Cloud]string
	// Platforms are the arches the manifest list at Digest carries. Declared so unsupported arch is
	// refused without a registry call.
	Platforms []Arch
}

// Override customizes resolution for one deployment. Zero value: pull Pin.Repositories[cloud]
// anonymously.
type Override struct {
	// Registry, if set, replaces Pin.Repositories[cloud]. Bare repo only.
	Registry string
	// PullSecretRef is an opaque provider credential reference (imagePullSecrets name, ECS secret ARN,
	// etc.). Carried through; never resolved here.
	PullSecretRef string
}

// Resolution is the configured versioned ref a provider deploys, plus an optional pull credential.
type Resolution struct {
	// Ref is repo@digest or repo:tag.
	Ref string
	// PullSecretRef is Override.PullSecretRef unchanged. Empty means anonymous pull.
	PullSecretRef string
}

// IsPublicECR identifies the anonymous-pull endpoint, not an AWS private ECR repository.
func IsPublicECR(reference string) bool {
	return strings.HasPrefix(reference, "public.ecr.aws/")
}

// Supports reports whether the pinned manifest list carries an architecture.
func (p Pin) Supports(a Arch) bool {
	for _, have := range p.Platforms {
		if have == a {
			return true
		}
	}
	return false
}

// Validate reports structural problems with a pin. Intended for tests so a bad pin cannot ship.
func (p Pin) Validate() []string {
	var problems []string
	if p.Digest == "" && p.Tag == "" {
		problems = append(problems, "no tag or digest: this pins nothing")
	} else if p.Digest != "" && !digestPattern.MatchString(p.Digest) {
		problems = append(problems, fmt.Sprintf("digest %q is not a full sha256 OCI digest", p.Digest))
	}
	if p.Tag != "" {
		if _, err := name.NewTag("example.com/collector:" + p.Tag); err != nil {
			problems = append(problems, fmt.Sprintf("invalid tag %q", p.Tag))
		}
	}
	if len(p.Platforms) == 0 {
		problems = append(problems, "no platforms declared, so every architecture would be refused")
	}
	for _, a := range p.Platforms {
		if a != ArchAMD64 && a != ArchARM64 {
			problems = append(problems, fmt.Sprintf("unknown platform %q", a))
		}
	}
	for _, c := range []Cloud{CloudAWS, CloudAzure, CloudGCP} {
		repo, ok := p.Repositories[c]
		if !ok || repo == "" {
			problems = append(problems, fmt.Sprintf("no repository for %s: that cloud could not pull the image at all", c))
			continue
		}
		if problem := malformedRepo(repo); problem != "" {
			problems = append(problems, fmt.Sprintf("repository for %s (%q) %s", c, repo, problem))
		}
	}
	// Cloud Run only accepts AR, public Docker Hub, and public GHCR.
	if repo := p.Repositories[CloudGCP]; repo != "" && !cloudRunPullable(repo) {
		problems = append(problems, fmt.Sprintf(
			"repository for gcp (%q) is not a source Cloud Run can pull from: it accepts Artifact Registry, "+
				"public Docker Hub and public GHCR, and has no credential mechanism for anything else", repo))
	}
	sort.Strings(problems)
	return problems
}

// malformedRepo reports why a repository is not a bare repo (no tag, digest, or space), or "" if ok.
func malformedRepo(repo string) string {
	if strings.ContainsAny(repo, "@ \t\r\n") || strings.LastIndex(repo, ":") > strings.LastIndex(repo, "/") {
		return "carries a tag, digest or space; use a bare repository"
	}
	if _, err := name.NewRepository(repo); err != nil {
		return fmt.Sprintf("is not a valid image repository: %v", err)
	}
	return ""
}

// cloudRunPullable reports whether a repository is a Cloud Run-accepted image source.
func cloudRunPullable(repo string) bool {
	switch {
	case strings.HasSuffix(firstSegment(repo), "-docker.pkg.dev"), firstSegment(repo) == "docker.pkg.dev":
		return true // Artifact Registry
	case firstSegment(repo) == "ghcr.io":
		return true // public GitHub Container Registry
	case !strings.Contains(firstSegment(repo), "."):
		return true // unqualified Docker Hub repo, e.g. keyval/odigos-enterprise-collector
	case firstSegment(repo) == "docker.io", firstSegment(repo) == "index.docker.io":
		return true
	}
	return false
}

func firstSegment(repo string) string {
	if i := strings.Index(repo, "/"); i >= 0 {
		return repo[:i]
	}
	return repo
}

// Resolve uses an explicitly configured digest when present, otherwise the selected release tag.
func Resolve(p Pin, c Cloud, a Arch, o Override) (Resolution, error) {
	if p.Digest == "" && p.Tag == "" {
		return Resolution{}, fmt.Errorf("no managed-Collector image is configured; set cloudConnectors.managedCollectors.collectorImage in Helm or collector.defaults.image at runtime")
	}
	if p.Digest != "" && !digestPattern.MatchString(p.Digest) {
		return Resolution{}, fmt.Errorf("digest %q is not a full sha256 OCI digest", p.Digest)
	}
	if a == "" {
		a = DefaultArch
	}
	if !p.Supports(a) {
		return Resolution{}, fmt.Errorf("the pinned Collector image (%s) does not carry %s; it carries %s. Set collector.regions.<region>.arch to one of those, or leave it unset for %s",
			p.Tag, a, joinArch(p.Platforms), DefaultArch)
	}
	repo, ok := p.Repositories[c]
	if !ok || repo == "" {
		return Resolution{}, fmt.Errorf("no Collector image repository is configured for %s", c)
	}
	if o.Registry != "" {
		repo = o.Registry
		if problem := malformedRepo(repo); problem != "" {
			return Resolution{}, fmt.Errorf("registry override for %s (%q) %s", c, repo, problem)
		}
	}
	if problem := malformedRepo(repo); problem != "" {
		return Resolution{}, fmt.Errorf("repository for %s (%q) %s", c, repo, problem)
	}
	if c == CloudGCP && !cloudRunPullable(repo) {
		return Resolution{}, fmt.Errorf(
			"registry for gcp (%q) is not a source Cloud Run can pull from: it accepts Artifact Registry, "+
				"public Docker Hub and public GHCR, and a pull credential does not change that", repo)
	}
	ref := repo + "@" + p.Digest
	if p.Digest == "" {
		ref = repo + ":" + p.Tag
	}
	if _, err := name.ParseReference(ref); err != nil {
		return Resolution{}, fmt.Errorf("invalid Collector image %q: %w", ref, err)
	}
	return Resolution{Ref: ref, PullSecretRef: o.PullSecretRef}, nil
}

func joinArch(as []Arch) string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, string(a))
	}
	return strings.Join(out, " and ")
}

// Describe renders the selected tag or a short digest prefix for status.
func Describe(p Pin, c Cloud) string {
	if p.Digest == "" {
		if p.Tag == "" {
			return "no image configured"
		}
		return fmt.Sprintf("%s from %s", p.Tag, firstSegment(p.Repositories[c]))
	}
	short := p.Digest
	if len(short) > 19 {
		short = short[:19]
	}
	return fmt.Sprintf("%s (%s) from %s", p.Tag, short, firstSegment(p.Repositories[c]))
}
