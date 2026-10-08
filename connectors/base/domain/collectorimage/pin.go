// Package collectorimage defines which managed-Collector image a Collector runs, where each cloud
// pulls it from, and which architectures it may deploy on.
//
// Pure domain: no store, provider SDK, or registry calls.
//
// The shared contract is the digest, not the reference. Automatic and manual mode assert the same
// digest so each cloud can pull from a registry it accepts without forking identity.
//
// Cloud Run only accepts Artifact Registry, public Docker Hub, and GHCR. Odigos already publishes to
// Artifact Registry, so GCP uses that ref; ECS and Container Apps can pull from any registry.
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

// Pin is an immutable reference to one released Collector image: digest, per-cloud repos, platforms
// in the manifest list, and human-facing tag.
//
// Compiled into the connector (not configured) so a published artifact and a deployed Collector
// always resolve to the same bytes. The tag and registry are stamped in at build time (see
// stampedTag) rather than hand-pasted into this file per release; the digest stays a literal — see
// releasedDigest.
type Pin struct {
	// Digest is the manifest-list digest. Pinning a per-platform digest would break other arches.
	Digest string
	// Tag is human-readable only; runtimes resolve by Digest.
	Tag string
	// Repositories is the bare repo each cloud pulls from (no tag/digest). Resolve appends the digest.
	Repositories map[Cloud]string
	// Platforms are the arches the manifest list at Digest carries. Declared so unsupported arch is
	// refused without a registry call.
	Platforms []Arch
}

// releasedDigest is the manifest-list digest of the Collector image this connector build deploys.
//
// Bumped by hand in a reviewed change whenever a new Collector image is actually released, exactly like
// config.DefaultFargateAgentsImage. It stays a source literal rather than a build-stamped value like
// stampedTag/stampedRegistry below: those two just name where and under what tag the already-built image
// lives (true for the whole release, mechanical to pass through), but the digest IS the image identity —
// stamping it the same way would let a build-arg typo silently re-pin every deployment to a different
// image with no review catching it.
const releasedDigest = "sha256:49b74d617355abc189e19e1d724f6fe2b6205796f83b4d55c657a32c2b68c410"

// stampedTag and stampedRegistry are the tag and registry the release Collector image was actually
// pushed under, injected at build time via:
//
//	-ldflags "-X .../collectorimage.stampedTag=v1.38.0 \
//	          -X .../collectorimage.stampedRegistry=registry.odigos.io/odigos-enterprise-collector"
//
// from the same TAG/ORG the Makefile and release-images.yml build every other image in this repo with
// (see build-connector-runtime) — so these two no longer have to be hand-pasted into this file per
// release the way releasedDigest still is.
//
// Unstamped (a local `go build`/`go test`/`go run`, or an IDE run — nothing sets ldflags there),
// Released falls back to fallbackTag/fallbackRegistry below: the last connector release reviewed into
// this file, kept as a pair (never mixing a stamped tag with the fallback registry or vice versa) so
// local dev and this package's own tests build and pass without needing the release pipeline's ldflags.
var (
	stampedTag      string
	stampedRegistry string
)

// Released is the pin this connector build deploys: releasedDigest plus the build-stamped tag/registry
// when present, otherwise the fallback pair. See stampedTag above for how the stamping works.
var Released = buildReleased()

func buildReleased() Pin {
	tag, registry := stampedTag, stampedRegistry
	if tag == "" || registry == "" {
		tag, registry = fallbackTag, fallbackRegistry
	}
	return Pin{
		Digest: releasedDigest,
		Tag:    tag,
		Repositories: map[Cloud]string{
			CloudAWS:   registry,
			CloudAzure: registry,
			CloudGCP:   "us-central1-docker.pkg.dev/odigos-cloud/components/odigos-enterprise-collector",
		},
		Platforms: []Arch{ArchAMD64, ArchARM64},
	}
}

// fallbackTag and fallbackRegistry are Released's tag/registry when the build did not stamp them in —
// see stampedTag. TestReleasedPinIsRunnable guards that this pair, combined with releasedDigest, is
// still a runnable pin.
const (
	fallbackTag = "v1.38.0-pre1"
	// Public Artifact Registry for all clouds: GHCR needs credentials, Docker Hub rate-limits anonymous
	// pulls at shared NAT when scaling out. Cloud Run accepts Artifact Registry (and public Docker
	// Hub/GHCR); use AR directly.
	fallbackRegistry = "registry.odigos.io/odigos-enterprise-collector"
)

// Override customizes resolution for one deployment. Zero value: pull Pin.Repositories[cloud]
// anonymously.
type Override struct {
	// Registry, if set, replaces Pin.Repositories[cloud]. Bare repo only; Resolve still appends Digest.
	Registry string
	// PullSecretRef is an opaque provider credential reference (imagePullSecrets name, ECS secret ARN,
	// etc.). Carried through; never resolved here.
	PullSecretRef string
}

// Resolution is the digest-pinned ref a provider deploys, plus an optional pull credential.
type Resolution struct {
	// Ref is repo@digest.
	Ref string
	// PullSecretRef is Override.PullSecretRef unchanged. Empty means anonymous pull.
	PullSecretRef string
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
	if p.Digest == "" {
		problems = append(problems, "no digest: the image identity is the digest, so a pin without one pins nothing")
	} else if !digestPattern.MatchString(p.Digest) {
		problems = append(problems, fmt.Sprintf("digest %q is not a full sha256 OCI digest", p.Digest))
	}
	if p.Tag == "" {
		problems = append(problems, "no tag: a status message and a published document both need something a human can read")
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
	if strings.ContainsAny(repo, "@: ") {
		return "carries a tag, digest or space; Resolve appends the digest"
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

// Resolve returns the digest-pinned ref (and optional pull credential) for a cloud, or why it cannot.
// Always uses repo@digest — tags are mutable and are not image identity, including for registry overrides.
func Resolve(p Pin, c Cloud, a Arch, o Override) (Resolution, error) {
	if p.Digest == "" {
		return Resolution{}, fmt.Errorf("no managed-Collector image is pinned in this connector build, so no Collector can be deployed")
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
	if c == CloudGCP && !cloudRunPullable(repo) {
		return Resolution{}, fmt.Errorf(
			"registry for gcp (%q) is not a source Cloud Run can pull from: it accepts Artifact Registry, "+
				"public Docker Hub and public GHCR, and a pull credential does not change that", repo)
	}
	return Resolution{Ref: repo + "@" + p.Digest, PullSecretRef: o.PullSecretRef}, nil
}

func joinArch(as []Arch) string {
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, string(a))
	}
	return strings.Join(out, " and ")
}

// Describe renders the pin for status: tag plus a short digest prefix, never a bare tag alone.
func Describe(p Pin, c Cloud) string {
	if p.Digest == "" {
		return "no image pinned"
	}
	short := p.Digest
	if len(short) > 19 {
		short = short[:19]
	}
	return fmt.Sprintf("%s (%s) from %s", p.Tag, short, firstSegment(p.Repositories[c]))
}
