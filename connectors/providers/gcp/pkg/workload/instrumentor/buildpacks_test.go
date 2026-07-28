package instrumentor

import (
	"context"
	"encoding/json"
	"testing"

	runpb "cloud.google.com/go/run/apiv2/runpb"

	store "github.com/odigos-io/odigos-enterprise/connectors/providers/gcp/pkg/workload/store"
)

// buildpackLabels builds an io.buildpacks.build.metadata label map from a set of buildpack ids.
func buildpackLabels(ids ...string) map[string]string {
	type bp struct {
		ID string `json:"id"`
	}
	meta := struct {
		Buildpacks []bp `json:"buildpacks"`
	}{}
	for _, id := range ids {
		meta.Buildpacks = append(meta.Buildpacks, bp{ID: id})
	}
	b, _ := json.Marshal(meta)
	return map[string]string{labelBuildMetadata: string(b)}
}

func TestLanguageFromBuildpackID(t *testing.T) {
	cases := map[string]string{
		"google.go.runtime":                   "go",
		"google.nodejs.runtime":               "nodejs",
		"google.nodejs.npm":                   "nodejs",
		"google.python.runtime":               "python",
		"google.java.runtime":                 "java",
		"google.dotnet.publish":               "dotnet",
		"google.ruby.runtime":                 "ruby",
		"google.php.runtime":                  "php",
		"google.utils.label":                  "",
		"paketo-buildpacks/go":                "go",
		"paketo-buildpacks/node-engine":       "nodejs",
		"paketo-buildpacks/cpython":           "python",
		"paketo-buildpacks/bellsoft-liberica": "java",
		"paketo-buildpacks/dotnet-core":       "dotnet",
		"paketo-buildpacks/some-utility":      "",
	}
	for id, want := range cases {
		if got := languageFromBuildpackID(id); got != want {
			t.Errorf("languageFromBuildpackID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestLanguageFromBuildpackLabels(t *testing.T) {
	// A *.runtime buildpack is the authoritative signal even when utility buildpacks precede it.
	labels := buildpackLabels("google.config.flex", "google.python.runtime", "google.python.pip")
	if got := languageFromBuildpackLabels(labels); got != "python" {
		t.Errorf("runtime buildpack should win, got %q", got)
	}

	// Paketo (no "runtime" segment) resolves via the second pass.
	labels = buildpackLabels("paketo-buildpacks/ca-certificates", "paketo-buildpacks/go")
	if got := languageFromBuildpackLabels(labels); got != "go" {
		t.Errorf("paketo go should resolve, got %q", got)
	}

	// No buildpack metadata / unrecognized -> "".
	if got := languageFromBuildpackLabels(map[string]string{}); got != "" {
		t.Errorf("no label should be empty, got %q", got)
	}
	if got := languageFromBuildpackLabels(map[string]string{labelBuildMetadata: "not json"}); got != "" {
		t.Errorf("malformed label should be empty, got %q", got)
	}
	if got := languageFromBuildpackLabels(buildpackLabels("google.utils.label")); got != "" {
		t.Errorf("no language buildpack should be empty, got %q", got)
	}
}

// fakeInspector is a test ImageInspector returning a canned image config.
type fakeInspector struct {
	cfg *ImageConfig
	err error
}

func (f fakeInspector) ImageConfig(_ context.Context, _ string) (*ImageConfig, error) {
	return f.cfg, f.err
}

// inspectorWithLabels is a fakeInspector whose config carries only CNB buildpack labels.
func inspectorWithLabels(ids ...string) fakeInspector {
	return fakeInspector{cfg: &ImageConfig{Labels: buildpackLabels(ids...)}}
}

func TestResolveLanguagePrecedence(t *testing.T) {
	ctx := context.Background()
	// Opaque image so static guessing yields nothing unless we set image/command hints.
	pyLive := serviceWith(&runpb.Container{Image: "node:20"}) // node image, to prove buildpack beats static
	rec := store.CloudRunService{}

	// Buildpack (python) beats the static image guess (node).
	i := &CloudRunInstrumentor{images: inspectorWithLabels("google.python.runtime")}
	if got := i.resolveLanguage(ctx, enabledSource{}, rec, pyLive); got != LangPython {
		t.Errorf("buildpack should beat static image guess, got %q", got)
	}

	// Stored language beats buildpack.
	if got := i.resolveLanguage(ctx, enabledSource{}, store.CloudRunService{RuntimeLanguage: "java"}, pyLive); got != LangJava {
		t.Errorf("stored language should beat buildpack, got %q", got)
	}

	// Source override beats everything.
	if got := i.resolveLanguage(ctx, enabledSource{language: "nodejs"}, store.CloudRunService{RuntimeLanguage: "java"}, pyLive); got != LangNodeJS {
		t.Errorf("override should win, got %q", got)
	}

	// A buildpack-detected but unsupported runtime (Go) normalizes away, falling back to the static guess.
	goLive := serviceWith(&runpb.Container{Image: "gcr.io/x/opaque"})
	i = &CloudRunInstrumentor{images: inspectorWithLabels("google.go.runtime")}
	if got := i.resolveLanguage(ctx, enabledSource{}, rec, goLive); got != "" {
		t.Errorf("unsupported buildpack language should resolve to empty, got %q", got)
	}

	// No inspector -> static heuristics only.
	i = &CloudRunInstrumentor{}
	if got := i.resolveLanguage(ctx, enabledSource{}, rec, serviceWith(&runpb.Container{Image: "python:3.12"})); got != LangPython {
		t.Errorf("nil inspector should fall back to static guess, got %q", got)
	}

	// Inspector error is treated as no signal (falls back to static).
	i = &CloudRunInstrumentor{images: fakeInspector{err: context.DeadlineExceeded}}
	if got := i.resolveLanguage(ctx, enabledSource{}, rec, serviceWith(&runpb.Container{Image: "python:3.12"})); got != LangPython {
		t.Errorf("inspector error should fall back to static guess, got %q", got)
	}
}

// TestResolveLanguageFromImageConfig covers the image-config QuickScan layer (procdiscovery-style), which
// fires for non-buildpack images whose runtime is only evident from the image's own entrypoint/cmd/env —
// signals the Cloud Run service spec typically lacks.
func TestResolveLanguageFromImageConfig(t *testing.T) {
	ctx := context.Background()
	// Opaque image + empty service spec, so detection must come from the image config alone.
	live := serviceWith(&runpb.Container{Image: "gcr.io/x/opaque"})
	rec := store.CloudRunService{}

	cases := []struct {
		name string
		cfg  *ImageConfig
		want string
	}{
		{"entrypoint node", &ImageConfig{Entrypoint: []string{"node", "index.js"}}, LangNodeJS},
		{"cmd only java", &ImageConfig{Cmd: []string{"java", "-jar", "app.jar"}}, LangJava},
		{"python exe", &ImageConfig{Entrypoint: []string{"/usr/local/bin/python3.12", "app.py"}}, LangPython},
		{"dotnet exe", &ImageConfig{Entrypoint: []string{"dotnet", "MyApp.dll"}}, LangDotNet},
		{"env version key", &ImageConfig{Env: []string{"NODE_VERSION=20.11.0", "PATH=/usr/bin"}}, LangNodeJS},
		{"coreclr env", &ImageConfig{Env: []string{"CORECLR_ENABLE_PROFILING=1"}}, LangDotNet},
		{"unsupported go entrypoint", &ImageConfig{Entrypoint: []string{"/app/server"}}, ""},
	}
	for _, tc := range cases {
		i := &CloudRunInstrumentor{images: fakeInspector{cfg: tc.cfg}}
		if got := i.resolveLanguage(ctx, enabledSource{}, rec, live); got != tc.want {
			t.Errorf("%s: resolveLanguage = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Buildpack labels still win over the image's own entrypoint when both are present.
	mixed := &ImageConfig{Labels: buildpackLabels("google.python.runtime"), Entrypoint: []string{"node", "x.js"}}
	i := &CloudRunInstrumentor{images: fakeInspector{cfg: mixed}}
	if got := i.resolveLanguage(ctx, enabledSource{}, rec, live); got != LangPython {
		t.Errorf("buildpack labels should beat image entrypoint, got %q", got)
	}
}
