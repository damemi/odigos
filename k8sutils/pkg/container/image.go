package container

import "github.com/distribution/reference"

// ImageTag returns the tag of a container image reference. It is parsed like the
// collector's k8sattributes processor so both report the same value: "latest" when
// the reference has neither a tag nor a digest, and empty for a digest-only
// reference or one that does not parse.
// https://github.com/open-telemetry/opentelemetry-collector-contrib/blob/v0.121.0/processor/k8sattributesprocessor/internal/kube/client.go#L678
func ImageTag(image string) string {
	ref, err := reference.Parse(image)
	if err != nil {
		return ""
	}
	if tagged, ok := ref.(reference.Tagged); ok {
		return tagged.Tag()
	}
	if _, ok := ref.(reference.Digested); ok {
		return ""
	}
	return "latest"
}
