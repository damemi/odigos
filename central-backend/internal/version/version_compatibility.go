package version

import (
	"strings"

	"golang.org/x/mod/semver"
)

const (
	MinSupportedVersion = "v1.4.0"
	MaxSupportedVersion = ""
)

func IsVersionSupported(v string) bool {
	v = normalize(v)
	min := normalize(MinSupportedVersion)
	max := normalize(MaxSupportedVersion)

	if v == "" || !semver.IsValid(v) || !semver.IsValid(min) {
		return false
	}

	if semver.Compare(v, min) >= 0 {
		return max == "" || !semver.IsValid(max) || semver.Compare(v, max) <= 0
	}

	// special-case: allow pre-releases that share the same base as min
	// e.g., v1.2.0-rc.1 is allowed when min is v1.2.0
	vBase := baseNoPrerelease(v)
	minBase := baseNoPrerelease(min)
	if vBase == minBase && hasPrerelease(v) {
		return max == "" || !semver.IsValid(max) || semver.Compare(v, max) <= 0
	}

	return false
}

func normalize(v string) string {
	if v == "" {
		return v
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	// Canonical keeps valid semver, including prerelease/build metadata.
	// If not valid, Canonical returns an empty string.
	return semver.Canonical(v)
}

func baseNoPrerelease(v string) string {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i]
	}
	return v
}

func hasPrerelease(v string) bool {
	return strings.Contains(v, "-")
}
