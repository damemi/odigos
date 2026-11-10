package instrumentor

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

type testLayerEntry struct{ name, data string }

// testAgentCoreLayer is one image layer; a name ending in "/" is a directory.
func testAgentCoreLayer(t *testing.T, entries []testLayerEntry) v1.Layer {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0644, Size: int64(len(e.data)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(e.name, "/") {
			h = &tar.Header{Name: e.name, Mode: 0755, Typeflag: tar.TypeDir}
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = w.Write([]byte(e.data))
		}
	}
	_ = w.Close()
	raw := b.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil })
	if err != nil {
		t.Fatal(err)
	}
	return layer
}

// flattenedAgentCoreScan is the reference: the image's flattened filesystem read whole, as detection used to.
func flattenedAgentCoreScan(t *testing.T, image v1.Image, want func(string) bool) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	stream := mutate.Extract(image)
	defer stream.Close()
	reader := tar.NewReader(stream)
	for {
		h, err := reader.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		n := strings.TrimPrefix(strings.TrimPrefix(h.Name, "./"), "/")
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if keep, _ := agentCoreLayerKeeps(n); !keep || !want(n) {
			continue
		}
		data, _ := io.ReadAll(reader)
		out[n] = data
	}
}

// TestAgentCoreLayerScanMatchesTheFlattenedImage: reading layers one by one, with whiteouts and opaque directories
// applied, gives detection the same entries as reading the flattened filesystem.
func TestAgentCoreLayerScanMatchesTheFlattenedImage(t *testing.T) {
	site := "usr/local/lib/python3.12/site-packages/"
	base := testAgentCoreLayer(t, []testLayerEntry{
		{site + "pip-24.0.dist-info/METADATA", ""},
		{site + "opentelemetry_distro-0.66b1.dist-info/METADATA", ""},
		{site + "aws_opentelemetry_distro-0.10.0.dist-info/METADATA", ""},
		{site + "aws_opentelemetry_distro-0.10.0.dist-info/RECORD", ""},
		{"app/package.json", `{"type":"commonjs"}`},
		{"app/sitecustomize.py", "old"},
		{"app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json", `{"version":"0.12.0"}`},
		{"usr/lib/unrelated.txt", "x"},
	})
	app := testAgentCoreLayer(t, []testLayerEntry{
		// pip uninstall of the upstream distro: the whole dist-info directory is whited out.
		{site + ".wh.opentelemetry_distro-0.66b1.dist-info", ""},
		// One member of ADOT's dist-info removed; the distribution itself is still installed.
		{site + "aws_opentelemetry_distro-0.10.0.dist-info/.wh.RECORD", ""},
		{site + "odigos_agentcore-0.1.0.dist-info/METADATA", ""},
		{"app/node_modules/@odigos/agentcore/package.json", `{"version":"0.1.0"}`},
		// A file replaced in place.
		{"app/sitecustomize.py", "new"},
		{"opt/odigos/agentcore/manifest.json", `{"package":{"v":1}}`},
	})
	image, err := mutate.AppendLayers(empty.Image, base, app)
	if err != nil {
		t.Fatal(err)
	}
	seen := func(names *[]string, content map[string]bool) func(string) bool {
		return func(n string) bool { *names = append(*names, n); return content[n] }
	}
	content := map[string]bool{
		"app/package.json": true, "app/sitecustomize.py": true, "opt/odigos/agentcore/manifest.json": true,
		"app/node_modules/@odigos/agentcore/package.json":                                      true,
		"app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json": true,
	}
	var gotNames, wantNames []string
	got, err := agentCoreImageScan(context.Background(), image, seen(&gotNames, content))
	if err != nil {
		t.Fatal(err)
	}
	want := flattenedAgentCoreScan(t, image, seen(&wantNames, content))
	sort.Strings(gotNames)
	sort.Strings(wantNames)
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("detection saw\n%v\nwant\n%v", gotNames, wantNames)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("returned\n%q\nwant\n%q", got, want)
	}
	if string(got["app/sitecustomize.py"]) != "new" {
		t.Errorf("the upper layer's file did not replace the lower one: %q", got["app/sitecustomize.py"])
	}
}

type countingLayer struct {
	v1.Layer
	reads *atomic.Int32
}

func (l countingLayer) Uncompressed() (io.ReadCloser, error) {
	l.reads.Add(1)
	return l.Layer.Uncompressed()
}

// TestAgentCoreLayerIndexesAreCached: a layer already read is never read again, whichever image it is in.
func TestAgentCoreLayerIndexesAreCached(t *testing.T) {
	var reads atomic.Int32
	layer := countingLayer{testAgentCoreLayer(t, []testLayerEntry{{"cache-test/package.json", `{"name":"cached"}`}}), &reads}
	image, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		files, err := agentCoreImageScan(context.Background(), image, func(n string) bool { return n == "cache-test/package.json" })
		if err != nil || string(files["cache-test/package.json"]) != `{"name":"cached"}` {
			t.Fatalf("scan: %q %v", files, err)
		}
	}
	if reads.Load() != 1 {
		t.Fatalf("layer read %d times, want once", reads.Load())
	}
}

// TestAgentCoreLayerScanHonorsOpaqueDirectories: an opaque directory hides everything the layers below put in it
// (OCI image spec, "Opaque Whiteout"). go-containerregistry's mutate.Extract does not apply this, so the flattened
// reference cannot be used here: an SDK removed by replacing node_modules wholesale must not be detected.
func TestAgentCoreLayerScanHonorsOpaqueDirectories(t *testing.T) {
	base := testAgentCoreLayer(t, []testLayerEntry{
		{"app/node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/package.json", `{"version":"0.12.0"}`},
		{"app/package.json", `{"name":"app"}`},
	})
	app := testAgentCoreLayer(t, []testLayerEntry{
		{"app/node_modules/", ""},
		{"app/node_modules/.wh..wh..opq", ""},
		{"app/node_modules/@odigos/agentcore/package.json", `{"version":"0.1.0"}`},
	})
	image, err := mutate.AppendLayers(empty.Image, base, app)
	if err != nil {
		t.Fatal(err)
	}
	got, err := agentCoreImageScan(context.Background(), image, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range got {
		names = append(names, n)
	}
	sort.Strings(names)
	if want := []string{"app/node_modules/@odigos/agentcore/package.json", "app/package.json"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("got %v, want %v", names, want)
	}
}
