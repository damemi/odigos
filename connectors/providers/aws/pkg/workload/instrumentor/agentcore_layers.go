package instrumentor

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// Image detection reads an image's layers one by one instead of its flattened filesystem. Each layer is reduced to
// an index of the few entries detection can use, cached by the layer's digest: base layers shared by many agent
// images are read once, and a pass that times out halfway keeps the layers it finished for the next one.

const (
	// agentCoreLayerConcurrency is how many layers of one image are read at once.
	agentCoreLayerConcurrency = 4
	// agentCoreLayerCacheSize bounds the cached layer indexes. An index holds a few hundred names at most.
	agentCoreLayerCacheSize = 512
)

// agentCoreLayerIndex is what one layer contributes to detection.
type agentCoreLayerIndex struct {
	// files are the entries detection may use, by path without a leading slash. Content is kept for the small
	// files detection reads (package manifests, sitecustomize.py, the carrier manifest) and is nil for the
	// dist-info members only their names matter for.
	files map[string][]byte
	// oversized are kept files whose content exceeded agentCoreMetadataLimit.
	oversized map[string]bool
	// deleted are paths this layer whites out, and opaque the directories whose lower contents it hides.
	deleted, opaque []string
}

// agentCoreLayerKeeps reports whether a layer entry can matter to detection, whatever the image's working directory
// is: a Python dist-info member, the package.json of a Node SDK in node_modules, an application package.json or
// sitecustomize.py, or the carrier bundle's manifest. The second result says whether its content is needed.
func agentCoreLayerKeeps(name string) (keep, content bool) {
	if name == agentCoreImageManifest {
		return true, true
	}
	for _, marker := range []string{agentCoreNodePackage, agentCoreNodeADOT, agentCoreNodeUpstream} {
		if name == marker+"package.json" || strings.HasSuffix(name, "/"+marker+"package.json") {
			return true, true
		}
	}
	if base := path.Base(name); (base == "package.json" || base == "sitecustomize.py") && !strings.Contains("/"+name, "/node_modules/") {
		return true, true
	}
	if _, _, ok := agentCoreSitePackagesDist(name); ok {
		return true, false
	}
	return false, false
}

// indexAgentCoreLayer reads one layer's tar stream.
func indexAgentCoreLayer(ctx context.Context, layer v1.Layer) (*agentCoreLayerIndex, error) {
	rc, err := layer.Uncompressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	idx := &agentCoreLayerIndex{files: map[string][]byte{}, oversized: map[string]bool{}}
	reader := tar.NewReader(rc)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		h, err := reader.Next()
		if err == io.EOF {
			return idx, nil
		}
		if err != nil {
			return nil, err
		}
		n := strings.TrimPrefix(strings.TrimPrefix(h.Name, "./"), "/")
		dir, base := path.Split(n)
		switch {
		case base == ".wh..wh..opq":
			idx.opaque = append(idx.opaque, strings.TrimSuffix(dir, "/"))
			continue
		case strings.HasPrefix(base, ".wh."):
			idx.deleted = append(idx.deleted, dir+strings.TrimPrefix(base, ".wh."))
			continue
		case h.Typeflag != tar.TypeReg:
			continue
		}
		keep, content := agentCoreLayerKeeps(n)
		if !keep {
			continue
		}
		if !content {
			// Every member is kept, so a later layer's whiteout of one of them cannot hide a distribution that is
			// still installed.
			idx.files[n] = nil
			continue
		}
		if h.Size > agentCoreMetadataLimit {
			idx.oversized[n] = true
			continue
		}
		var b bytes.Buffer
		if _, err := io.Copy(&b, reader); err != nil {
			return nil, err
		}
		idx.files[n] = b.Bytes()
	}
}

// agentCoreLayerCache holds layer indexes by digest, evicting the oldest.
type agentCoreLayerCache struct {
	mu    sync.Mutex
	byKey map[v1.Hash]*agentCoreLayerIndex
	order []v1.Hash
}

var agentCoreLayers = &agentCoreLayerCache{byKey: map[v1.Hash]*agentCoreLayerIndex{}}

func (c *agentCoreLayerCache) get(h v1.Hash) (*agentCoreLayerIndex, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx, ok := c.byKey[h]
	return idx, ok
}

func (c *agentCoreLayerCache) put(h v1.Hash, idx *agentCoreLayerIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byKey[h]; ok {
		return
	}
	c.byKey[h] = idx
	c.order = append(c.order, h)
	for len(c.order) > agentCoreLayerCacheSize {
		delete(c.byKey, c.order[0])
		c.order = c.order[1:]
	}
}

// agentCoreImageScan reads the entries of an image's flattened filesystem that detection can use. want sees every
// such path and says whether its content is needed; only those are returned. A wanted file over the metadata size
// limit is an error, as when the flattened filesystem was read whole.
func agentCoreImageScan(ctx context.Context, image v1.Image, want func(name string) bool) (map[string][]byte, error) {
	layers, err := image.Layers()
	if err != nil {
		return nil, err
	}
	started := time.Now()
	var read atomic.Int32
	indexes := make([]*agentCoreLayerIndex, len(layers))
	errs := make([]error, len(layers))
	sem := make(chan struct{}, agentCoreLayerConcurrency)
	var wg sync.WaitGroup
	for i, layer := range layers {
		digest, err := layer.Digest()
		if err != nil {
			return nil, err
		}
		if idx, ok := agentCoreLayers.get(digest); ok {
			indexes[i] = idx
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			idx, err := indexAgentCoreLayer(ctx, layer)
			if err != nil {
				errs[i] = err
				return
			}
			read.Add(1)
			agentCoreLayers.put(digest, idx)
			indexes[i] = idx
		})
	}
	wg.Wait()
	agentCoreLog.Debug("agentcore image layers read", "layers", len(layers), "read", read.Load(),
		"cached", len(layers)-int(read.Load()), "duration", time.Since(started).String())
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	merged, oversized := mergeAgentCoreLayers(indexes)
	names := make([]string, 0, len(merged)+len(oversized))
	for n := range merged {
		names = append(names, n)
	}
	for n := range oversized {
		names = append(names, n)
	}
	sort.Strings(names)
	out := map[string][]byte{}
	for _, n := range names {
		if !want(n) {
			continue
		}
		if oversized[n] {
			return nil, fmt.Errorf("image metadata file too large")
		}
		out[n] = merged[n]
	}
	return out, nil
}

// mergeAgentCoreLayers applies the layers bottom to top: a layer's whiteouts and opaque directories hide what the
// layers below it put there, and its own entries replace theirs.
func mergeAgentCoreLayers(indexes []*agentCoreLayerIndex) (map[string][]byte, map[string]bool) {
	files, oversized := map[string][]byte{}, map[string]bool{}
	hide := func(prefix string, exact bool) {
		for n := range oversized {
			if (exact && n == prefix) || strings.HasPrefix(n, prefix+"/") {
				delete(oversized, n)
			}
		}
		for n := range files {
			if (exact && n == prefix) || strings.HasPrefix(n, prefix+"/") {
				delete(files, n)
			}
		}
	}
	for _, idx := range indexes {
		for _, d := range idx.opaque {
			hide(d, false)
		}
		for _, d := range idx.deleted {
			hide(d, true)
		}
		for n, data := range idx.files {
			files[n] = data
			delete(oversized, n)
		}
		for n := range idx.oversized {
			oversized[n] = true
			delete(files, n)
		}
	}
	return files, oversized
}
