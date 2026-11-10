package instrumentor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

// fakeInspector serves a canned image config (or an error) for any image reference.
type fakeInspector struct {
	cfg *ImageConfig
	err error
}

func (f fakeInspector) ImageConfig(context.Context, string) (*ImageConfig, error) {
	return f.cfg, f.err
}

func buildpackLabels(t *testing.T, ids ...string) map[string]string {
	t.Helper()
	meta := buildMetadata{}
	for _, id := range ids {
		meta.Buildpacks = append(meta.Buildpacks, struct {
			ID string `json:"id"`
		}{ID: id})
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{labelBuildMetadata: string(b)}
}

func appWith(image string, entryPoint []string, env map[string]string) *ecstypes.ContainerDefinition {
	c := &ecstypes.ContainerDefinition{Name: aws.String("app"), EntryPoint: entryPoint}
	if image != "" {
		c.Image = aws.String(image)
	}
	for k, v := range env {
		c.Environment = append(c.Environment, ecstypes.KeyValuePair{Name: aws.String(k), Value: aws.String(v)})
	}
	return c
}

func TestResolveECSRuntimePrecedence(t *testing.T) {
	ctx := context.Background()
	// The container's own signals say node; the image config's buildpack labels say python.
	app := appWith("some/image:latest", []string{"node", "server.js"}, nil)
	pythonBuildpack := fakeInspector{cfg: &ImageConfig{Labels: buildpackLabels(t, "google.python.runtime")}}

	// 1. Override beats everything.
	if rt := resolveECSRuntime(ctx, pythonBuildpack, "java", "", "python", "", app); rt.language != LangJava {
		t.Errorf("override should win, got %q", rt.language)
	}
	// 2. Stored beats detection.
	if rt := resolveECSRuntime(ctx, pythonBuildpack, "", "", "dotnet", "", app); rt.language != LangDotNet {
		t.Errorf("stored should win over detection, got %q", rt.language)
	}
	// 3. Buildpack labels beat the task-definition command.
	if rt := resolveECSRuntime(ctx, pythonBuildpack, "", "", "", "", app); rt.language != LangPython {
		t.Errorf("buildpack labels should win over command heuristics, got %q", rt.language)
	}
	// 4. Without an inspector, the task-definition command decides.
	if rt := resolveECSRuntime(ctx, nil, "", "", "", "", app); rt.language != LangNodeJS {
		t.Errorf("command heuristic should apply, got %q", rt.language)
	}
	// 5. An inspector error is best-effort — detection falls through.
	if rt := resolveECSRuntime(ctx, fakeInspector{err: context.DeadlineExceeded}, "", "", "", "", app); rt.language != LangNodeJS {
		t.Errorf("inspector failure should fall through, got %q", rt.language)
	}
}

func TestResolveECSRuntimeGoIsNamedUncoverable(t *testing.T) {
	// A definitive Go detection must surface as "go" (not "") so the report explains the eBPF constraint.
	app := appWith("registry/app:latest", nil, nil)
	goBuildpack := fakeInspector{cfg: &ImageConfig{Labels: buildpackLabels(t, "google.go.runtime")}}
	rt := resolveECSRuntime(context.Background(), goBuildpack, "", "", "", "", app)
	if rt.language != "go" {
		t.Fatalf("language = %q, want raw \"go\"", rt.language)
	}
	if CoverableLanguage(rt.language) {
		t.Error("go must not be coverable")
	}
}

func TestResolveECSRuntimeVersion(t *testing.T) {
	ctx := context.Background()
	// Override version wins and is normalized to major.minor.
	app := appWith("php:8.1-fpm", nil, nil)
	if rt := resolveECSRuntime(ctx, nil, "php", "8.3.2", "", "", app); rt.version != "8.3" {
		t.Errorf("override version = %q, want 8.3", rt.version)
	}
	// Container env version (official images declare it).
	app = appWith("mycorp/checkout:latest", nil, map[string]string{"PHP_VERSION": "8.2.19"})
	if rt := resolveECSRuntime(ctx, nil, "", "", "", "", app); rt.language != LangPHP || rt.version != "8.2" {
		t.Errorf("env detection = %q/%q, want php/8.2", rt.language, rt.version)
	}
	// Image config env version.
	insp := fakeInspector{cfg: &ImageConfig{Env: []string{"RUBY_VERSION=3.3.5"}, Entrypoint: []string{"ruby", "app.rb"}}}
	if rt := resolveECSRuntime(ctx, insp, "", "", "", "", appWith("mycorp/app:latest", nil, nil)); rt.language != LangRuby || rt.version != "3.3" {
		t.Errorf("image-config detection = %q/%q, want ruby/3.3", rt.language, rt.version)
	}
	// Image tag fallback.
	app = appWith("ruby:3.2-alpine", nil, nil)
	if rt := resolveECSRuntime(ctx, nil, "", "", "", "", app); rt.language != LangRuby || rt.version != "3.2" {
		t.Errorf("tag detection = %q/%q, want ruby/3.2", rt.language, rt.version)
	}
}

func TestVersionHelpers(t *testing.T) {
	mm := map[string]string{
		"8.3.13": "8.3", "3.3": "3.3", "v1.22.1": "1.22", "8": "", "": "", "latest": "",
	}
	for in, want := range mm {
		if got := majorMinor(in); got != want {
			t.Errorf("majorMinor(%q) = %q, want %q", in, got, want)
		}
	}
	tags := map[string]string{
		"php:8.3-fpm":           "8.3",
		"ruby:3.3.5":            "3.3",
		"php:8":                 "",
		"registry:5000/app":     "", // colon belongs to the registry port, not a tag
		"registry:5000/app:2.4": "2.4",
		"mycorp/app:latest":     "",
		"noTag":                 "",
	}
	for in, want := range tags {
		if got := runtimeVersionFromImageTag(in); got != want {
			t.Errorf("runtimeVersionFromImageTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuessHeuristics(t *testing.T) {
	if got := guessFromCommand([]string{"/usr/local/bin/php-fpm"}); got != LangPHP {
		t.Errorf("php-fpm command = %q", got)
	}
	if got := guessFromCommand([]string{"bundle", "exec", "puma"}); got != LangRuby {
		t.Errorf("bundle command = %q", got)
	}
	if got := guessFromEnv(map[string]string{"RUBY_VERSION": "3.3.0"}); got != LangRuby {
		t.Errorf("RUBY_VERSION env = %q", got)
	}
	if got := guessFromImageString(langSignals{image: "mycorp/shop-php:prod"}); got != LangPHP {
		t.Errorf("php image substring = %q", got)
	}
	if got := guessFromImageString(langSignals{image: "gcr.io/proj/svc"}); got != "" {
		t.Errorf("unrecognizable image should yield empty, got %q", got)
	}
}
