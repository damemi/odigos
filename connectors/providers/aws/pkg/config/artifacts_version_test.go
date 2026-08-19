package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// artifactsVersionFile is the single place the odigos-io/aws-connector-artifacts release this connector
// is built against is written down. Relative to this package.
const artifactsVersionFile = "../../ARTIFACTS_VERSION"

// dockerfilePath is the connector Dockerfile carrying the Lambda layer bundle pin.
const dockerfilePath = "../../Dockerfile"

// TestArtifactVersions is the thing that keeps the two artifact pins honest.
//
// One release of aws-connector-artifacts produces BOTH the Lambda layer bundle (consumed at build time
// via the Dockerfile's LAMBDA_AGENTS_IMAGE arg) and the Fargate agents bundle (consumed at runtime via
// DefaultFargateAgentsImage). They are the same version by construction on the publishing side, but on
// this side they live in two files of two different languages, and nothing about editing one prompts you
// to edit the other.
//
// A mismatch is close to invisible: the connector builds, starts, instruments Lambdas with one release's
// layers and ECS tasks with another's, and the only symptom is that the two behave differently in ways
// nobody attributes to a version skew. Hence a test rather than a comment.
//
// There is a THIRD place the Fargate image version appears — the published instrumentation spec at
// connectors/spec/data/aws.yaml, whose IaC snippets a customer copies. That one is covered by
// TestFargateInstrumentationArtifactsMatchPlans in pkg/workload/instrumentor, which compares the spec
// against what the connector actually applies. Between the two tests, every copy of the version is
// pinned to ARTIFACTS_VERSION — but only if both run, which is why connectors/providers/aws has its own
// entry in build.yaml's connector-unit-tests matrix rather than riding on unit-tests (that job is
// skipped precisely when a change is connector-only, i.e. on every version bump).
func TestArtifactVersions(t *testing.T) {
	want := artifactsVersion(t)

	t.Run("ARTIFACTS_VERSION is a vX.Y.Z tag", func(t *testing.T) {
		if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(want) {
			t.Fatalf("ARTIFACTS_VERSION = %q, want a vX.Y.Z release tag; the release workflow only ever "+
				"produces that form, so anything else cannot name a published artifact", want)
		}
	})

	t.Run("DefaultFargateAgentsImage", func(t *testing.T) {
		got := imageTag(t, DefaultFargateAgentsImage)
		if got != want {
			t.Errorf("DefaultFargateAgentsImage is tagged %q but ARTIFACTS_VERSION is %q.\n"+
				"Both bundles come from one aws-connector-artifacts release; update %s (config.go) or "+
				"ARTIFACTS_VERSION so they agree.",
				got, want, DefaultFargateAgentsImage)
		}
	})

	t.Run("Dockerfile LAMBDA_AGENTS_IMAGE default", func(t *testing.T) {
		got := imageTag(t, dockerfileLambdaAgentsImage(t))
		if got != want {
			t.Errorf("the Dockerfile's LAMBDA_AGENTS_IMAGE default is tagged %q but ARTIFACTS_VERSION is %q.\n"+
				"The repo-root Makefile passes ARTIFACTS_VERSION as the build arg, so a stale default only "+
				"bites a bare `docker build` — which is exactly when nobody is watching.",
				got, want)
		}
	})
}

func artifactsVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(artifactsVersionFile))
	if err != nil {
		t.Fatalf("read %s: %v", artifactsVersionFile, err)
	}
	return strings.TrimSpace(string(raw))
}

// dockerfileLambdaAgentsImage extracts the default value of the global LAMBDA_AGENTS_IMAGE ARG.
//
// A miss is a failure, not a skip: the ARG being absent or renamed means the Dockerfile no longer pins
// the bundle the way this test believes, and silently passing would be worse than a broken test.
func dockerfileLambdaAgentsImage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(dockerfilePath))
	if err != nil {
		t.Fatalf("read %s: %v", dockerfilePath, err)
	}
	m := regexp.MustCompile(`(?m)^ARG LAMBDA_AGENTS_IMAGE=(\S+)\s*$`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("no `ARG LAMBDA_AGENTS_IMAGE=<image>` line in %s — the pin moved or was renamed, so the "+
			"version binding this test enforces no longer exists", dockerfilePath)
	}
	return m[1]
}

// imageTag returns the tag of a <registry>/<repo>:<tag> reference. The last colon, not the first: a
// registry may carry a port (localhost:5000/x:v1).
func imageTag(t *testing.T, ref string) string {
	t.Helper()
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i+1:], "/") {
		t.Fatalf("image reference %q has no tag; it must be pinned to a released version, never floating", ref)
	}
	return ref[i+1:]
}
