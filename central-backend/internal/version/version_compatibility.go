package version

import "golang.org/x/mod/semver"

const (
	MinSupportedVersion = "v1.0.170"
	MaxSupportedVersion = ""
)

func IsVersionSupported(version string) bool {
	if version == "" || !semver.IsValid(version) {
		return false
	}

	if semver.Compare(version, MinSupportedVersion) < 0 {
		return false
	}

	if MaxSupportedVersion != "" && semver.IsValid(MaxSupportedVersion) {
		if semver.Compare(version, MaxSupportedVersion) > 0 {
			return false
		}
	}

	return true
}
