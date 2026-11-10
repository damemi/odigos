package instrumentor

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"io"
	"maps"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ac "github.com/aws/aws-sdk-go-v2/service/bedrockagentcorecontrol"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	basestore "github.com/odigos-io/odigos/connectors/base/store"
	"github.com/odigos-io/odigos/connectors/providers/aws/pkg/agentcore"
)

func testAgentCoreZIP(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for n, data := range files {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// agentCoreInstallerFake stands in for detection and preparation. Without an SDK it starts the Odigos package.
type agentCoreInstallerFake struct {
	calls    int
	artifact agentCoreArtifactRef
	inputs   []agentCoreArtifactRef
	envs     []map[string]string
	sources  []basestore.SourceRecord
	mode     agentCoreSDKKind
	waiting  *agentCoreWaiting
	err      error
	// pkg is the contract the installation declares; nil uses the runtime's own prepared contract, or a Python
	// package with both distros when it has none.
	pkg    *agentcore.Package
	loader map[string]string
}

func (f *agentCoreInstallerFake) Resolve(_ context.Context, live *ac.GetAgentRuntimeOutput, src basestore.SourceRecord) (agentCoreInstallation, *agentCoreWaiting, error) {
	f.calls++
	f.sources = append(f.sources, src)
	input, _ := agentCoreArtifact(live.AgentRuntimeArtifact)
	f.inputs = append(f.inputs, input)
	f.envs = append(f.envs, maps.Clone(live.EnvironmentVariables))
	if f.err != nil || f.waiting != nil {
		return agentCoreInstallation{}, f.waiting, f.err
	}
	mode := f.mode
	if mode == "" {
		mode = agentCoreSDKOdigos
	}
	p := agentcore.ParsePackage(live.EnvironmentVariables[agentcore.PackageEnv])
	if f.pkg != nil {
		p = *f.pkg
	} else if p.Version != 1 {
		p = agentcore.Package{Version: 1, Language: "python", Distros: []string{"python-agentcore-adot", "python-agentcore-openllmetry"}}
	}
	p.Mode = string(mode)
	artifact := f.artifact
	if artifact.Image == "" && artifact.Bucket == "" {
		artifact = input
	}
	return agentCoreInstallation{SDK: mode, Artifact: artifact, Package: p, LoaderEnv: maps.Clone(f.loader)}, nil, nil
}

// agentCoreLocalArtifacts serves a ZIP or image from memory. It exercises the same installer as production,
// replacing only S3/ECR reads.
type agentCoreLocalArtifacts struct {
	reads    int
	code     []byte
	image    v1.Image
	imageErr error
}

func (f *agentCoreLocalArtifacts) OpenCode(context.Context, string, agentCoreArtifactRef) (agentCoreCode, error) {
	f.reads++
	return bytes.NewReader(f.code), nil
}
func (f *agentCoreLocalArtifacts) Image(context.Context, string) (v1.Image, error) {
	f.reads++
	return f.image, f.imageErr
}

// Keep the imports and helpers usable for synthetic OCI mutation tests below.
func testAgentCoreImage(t *testing.T, cfg v1.ConfigFile, files map[string]string) v1.Image {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for n, data := range files {
		if err := w.WriteHeader(&tar.Header{Name: n, Mode: 0644, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(data))
	}
	_ = w.Close()
	raw := b.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil })
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	img, err = mutate.ConfigFile(img, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestAgentCoreEnvironmentPlatformLimits(t *testing.T) {
	for _, tc := range []struct {
		platform string
		image    bool
		limit    int
	}{{"V1", false, 4096}, {"V2", false, 1536}, {"V2", true, 2560}} {
		artifact := agentCoreArtifactRef{Bucket: "test", Key: "app.zip", Runtime: "PYTHON_3_12", EntryPoint: []string{"app.py"}}
		if tc.image {
			artifact = agentCoreArtifactRef{Image: "repo:tag"}
		}
		live := &ac.GetAgentRuntimeOutput{PlatformVersion: aws.String(tc.platform), AgentRuntimeArtifact: artifact.sdk()}
		if err := validateAgentCoreRuntimeEnv(live, map[string]string{"K": strings.Repeat("x", tc.limit-1)}); err != nil {
			t.Fatal(err)
		}
		if err := validateAgentCoreRuntimeEnv(live, map[string]string{"K": strings.Repeat("x", tc.limit)}); err == nil {
			t.Fatalf("%s image=%v accepted oversized environment", tc.platform, tc.image)
		}
	}
}

// TestAgentCoreManualStartsSDK: a self-managed ZIP must start the SDK its contract declares in the form AgentCore
// accepts. An image's command is not visible, so it is trusted.
func TestAgentCoreManualStartsSDK(t *testing.T) {
	zip := func(runtime string, entry []string, env map[string]string) *ac.GetAgentRuntimeOutput {
		return &ac.GetAgentRuntimeOutput{AgentRuntimeArtifact: (agentCoreArtifactRef{Bucket: "b", Key: "k", Runtime: runtime, EntryPoint: entry}).sdk(), EnvironmentVariables: env}
	}
	script := map[string]string{agentcore.EntrypointEnv: "app.py"}
	for _, tc := range []struct {
		name string
		live *ac.GetAgentRuntimeOutput
		sdk  agentCoreSDKKind
		want bool
	}{
		{"odigos python wrapper alone with the script", zip("PYTHON_3_12", []string{agentCorePythonStart}, script), agentCoreSDKOdigos, true},
		{"odigos node package wrapper alone with the script", zip("NODE_22", []string{agentCoreNodePackageStart}, map[string]string{agentcore.EntrypointEnv: "index.js"}), agentCoreSDKOdigos, true},
		{"odigos bundle wrapper in place", zip("PYTHON_3_12", []string{"odigos-agentcore/bootstrap/python/odigos_agentcore_start.py"}, script), agentCoreSDKOdigos, true},
		{"odigos wrapper without the script", zip("PYTHON_3_12", []string{agentCorePythonStart}, nil), agentCoreSDKOdigos, false},
		{"odigos two-element wrapper, which AgentCore rejects", zip("PYTHON_3_12", []string{agentCorePythonStart, "app.py"}, script), agentCoreSDKOdigos, false},
		{"odigos without the wrapper", zip("PYTHON_3_12", []string{"app.py"}, script), agentCoreSDKOdigos, false},
		{"adot python through the launcher", zip("PYTHON_3_12", []string{agentCoreLauncher, "app.py"}, nil), agentCoreSDKADOT, true},
		{"adot python without the launcher", zip("PYTHON_3_12", []string{"app.py"}, nil), agentCoreSDKADOT, false},
		{"adot node through the launcher", zip("NODE_22", []string{agentCoreLauncher, "index.js"}, nil), agentCoreSDKADOT, true},
		{"adot node through the register hook", zip("NODE_22", []string{"index.js"}, map[string]string{"NODE_OPTIONS": "--require " + agentCoreNodeADOTRegister}), agentCoreSDKADOT, true},
		{"adot node without either", zip("NODE_22", []string{"index.js"}, nil), agentCoreSDKADOT, false},
		{"image is trusted", &ac.GetAgentRuntimeOutput{AgentRuntimeArtifact: (&agentCoreArtifactRef{Image: "repo/app:1"}).sdk()}, agentCoreSDKOdigos, true},
	} {
		if got := agentCoreManualStartsSDK(tc.live, tc.sdk); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
