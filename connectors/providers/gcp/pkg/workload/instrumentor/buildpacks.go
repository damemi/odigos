package instrumentor

import (
	"encoding/json"
	"strings"
)

// Cloud Native Buildpacks stamp the set of buildpacks that ran into the image config labels. The default
// path onto Cloud Run — `gcloud run deploy --source .` — builds with Google Cloud buildpacks, which
// encode the detected runtime in the buildpack ids (e.g. google.go.runtime, google.python.runtime). This
// is an authoritative, language-labeled signal we can read straight from the image config (no Cloud Build
// or source access needed), so it is a stronger static hint than the image-name/command heuristics.
const (
	// labelBuildMetadata is the CNB build metadata label. Its JSON payload lists every buildpack that ran.
	labelBuildMetadata = "io.buildpacks.build.metadata"
)

// buildMetadata is the (partial) shape of the io.buildpacks.build.metadata label payload. Only the
// buildpack ids are needed to infer the language.
type buildMetadata struct {
	Buildpacks []struct {
		ID string `json:"id"`
	} `json:"buildpacks"`
}

// languageFromBuildpackLabels infers the workload language from CNB image config labels. It returns a raw
// language token (java/python/nodejs/dotnet/go/ruby/php) or "" when no buildpack signal is present; the
// caller normalizes it to the connector's supported set. Buildpacks with a "runtime" component are
// preferred as the definitive signal, then any recognizable buildpack id.
func languageFromBuildpackLabels(labels map[string]string) string {
	raw := labels[labelBuildMetadata]
	if raw == "" {
		return ""
	}
	var meta buildMetadata
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return ""
	}
	// First pass: a *.runtime buildpack is the authoritative language declaration.
	for _, bp := range meta.Buildpacks {
		if strings.Contains(strings.ToLower(bp.ID), "runtime") {
			if l := languageFromBuildpackID(bp.ID); l != "" {
				return l
			}
		}
	}
	// Second pass: any recognizable buildpack id (covers Paketo, whose ids don't use a "runtime" segment).
	for _, bp := range meta.Buildpacks {
		if l := languageFromBuildpackID(bp.ID); l != "" {
			return l
		}
	}
	return ""
}

// languageFromBuildpackID maps a single buildpack id to a raw language token. It handles Google Cloud
// buildpacks (google.<lang>.<component>) and, as a bonus, common Paketo ids (paketo-buildpacks/<name>).
// Returns "" for language-agnostic buildpacks (utility, base image, etc.).
func languageFromBuildpackID(id string) string {
	id = strings.ToLower(id)

	// Google Cloud buildpacks: the second dotted segment is the language (google.go.runtime).
	if strings.HasPrefix(id, "google.") {
		if parts := strings.Split(id, "."); len(parts) >= 2 {
			switch parts[1] {
			case "go":
				return "go"
			case "nodejs":
				return "nodejs"
			case "python":
				return "python"
			case "java":
				return "java"
			case "dotnet":
				return "dotnet"
			case "ruby":
				return "ruby"
			case "php":
				return "php"
			}
		}
	}

	// Paketo and other vendors: match on distinctive tokens in the id.
	switch {
	case strings.HasSuffix(id, "/go") || strings.Contains(id, "/go-") || strings.Contains(id, "go-dist"):
		return "go"
	case strings.Contains(id, "node"):
		return "nodejs"
	case strings.Contains(id, "python") || strings.Contains(id, "cpython"):
		return "python"
	case strings.Contains(id, "java") || strings.Contains(id, "jvm") || strings.Contains(id, "liberica") ||
		strings.Contains(id, "maven") || strings.Contains(id, "gradle") || strings.Contains(id, "sdkman"):
		return "java"
	case strings.Contains(id, "dotnet") || strings.Contains(id, ".net"):
		return "dotnet"
	case strings.Contains(id, "ruby"):
		return "ruby"
	case strings.Contains(id, "php"):
		return "php"
	}
	return ""
}
