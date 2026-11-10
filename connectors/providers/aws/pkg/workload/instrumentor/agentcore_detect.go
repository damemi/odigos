package instrumentor

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"path"
	"sort"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/odigos-io/odigos-enterprise/connectors/providers/aws/pkg/agentcore"
)

// agentCoreArtifactFacts is what a deployed artifact carries, read from member names and a few small metadata
// files. Nothing is extracted or executed: the customer's code is never run to find out what it contains.
type agentCoreArtifactFacts struct {
	Language, RuntimeVersion string // ZIP: from the managed runtime; image: from overrides, env and signals
	OdigosPackage            *agentCoreOdigosPackage
	PreparedBundle           bool // carrier layout: the bundle manifest at its known path
	// BundlePackage is the prepared bundle's manifest contract (language, distros, module format).
	BundlePackage agentcore.Package
	ADOT          *agentCoreSDK
	OtherSDK      bool // an upstream OpenTelemetry distribution without ADOT
	// LauncherPresent: the agent already starts ADOT itself, through the opentelemetry-instrument entry point prefix
	// (ZIP) or command (image), or through NODE_OPTIONS requiring the ADOT register hook.
	LauncherPresent bool
	// LauncherInEnv: node only, NODE_OPTIONS already requires the ADOT register hook.
	LauncherInEnv bool
	// UpstreamPreload: node only, NODE_OPTIONS or the image command already requires the upstream
	// auto-instrumentations register hook (the AgentCore starter toolkit's Node.js Dockerfile does).
	UpstreamPreload bool
	Sitecustomize   bool
	ESM             bool
	// StartWrapper is the ZIP member the Odigos package's entry point starts through, "" when the artifact has none.
	StartWrapper string
	// StartWrapperReadsEnv: the wrapper finds the agent's script in agentcore.EntrypointEnv, so it can be the entry
	// point alone. Wrappers from earlier releases only read it from a second entry point element, which AgentCore
	// rejects.
	StartWrapperReadsEnv bool
	// Image only.
	Image      bool
	ImageEnv   map[string]string
	WorkingDir string
}

type agentCoreOdigosPackage struct {
	Distros []string // python: from the SDK dist-infos installed next to it; node: both profiles
	Dir     string   // image: directory holding node_modules / site-packages; ZIP: ""
}

type agentCoreSDK struct{ Dir string } // Dir: image: directory holding node_modules / site-packages; ZIP: ""

const (
	agentCorePythonPackageDist     = "odigos_agentcore"
	agentCorePythonADOTDist        = "aws_opentelemetry_distro"
	agentCorePythonOpenLLMetryDist = "opentelemetry_instrumentation_bedrock"
	agentCorePythonUpstreamDist    = "opentelemetry_distro"

	agentCoreNodePackage  = "node_modules/@odigos/agentcore/"
	agentCoreNodeADOT     = "node_modules/@aws/aws-distro-opentelemetry-node-autoinstrumentation/"
	agentCoreNodeUpstream = "node_modules/@opentelemetry/auto-instrumentations-node/"
	// agentCoreNodeADOTRegister is the hook a Node agent already requires when it starts ADOT itself.
	agentCoreNodeADOTRegister = "@aws/aws-distro-opentelemetry-node-autoinstrumentation/register"
	// agentCoreNodeUpstreamRegister is the upstream hook an agent requires when it starts OpenTelemetry itself.
	agentCoreNodeUpstreamRegister = "@opentelemetry/auto-instrumentations-node/register"
	// agentCoreNodeADOTRegisterFile is the file that export maps to (package.json "exports": {"./register": …}).
	agentCoreNodeADOTRegisterFile = "build/src/register.js"

	// agentCoreLauncher is AgentCore's documented direct-code prefix for both languages: for Node.js AgentCore
	// starts the ADOT package in node_modules itself.
	agentCoreLauncher         = "opentelemetry-instrument"
	agentCorePythonStart      = "odigos_agentcore_start.py"
	agentCoreNodePackageStart = agentCoreNodePackage + "odigos_agentcore_start.js"
	agentCoreNodeBundleStart  = "odigos_agentcore_start.js"

	// agentCoreZIPRoot is where a carrier bundle the customer prepared sits in a ZIP.
	agentCoreZIPRoot       = "odigos-agentcore/"
	agentCoreZIPManifest   = agentCoreZIPRoot + "manifest.json"
	agentCoreImageManifest = "opt/odigos/agentcore/manifest.json"

	agentCoreMetadataLimit = 1 << 20
)

// agentCoreStartsThrough reports whether an entry point's first element starts the agent's script rather than
// being the script: the ADOT launcher or an Odigos start wrapper.
func agentCoreStartsThrough(first string) bool {
	switch first {
	case agentCoreLauncher, agentCorePythonStart, agentCoreNodePackageStart, agentCoreNodeBundleStart:
		return true
	}
	return false
}

// agentCoreEntryScript is the agent's own script in a direct-code entry point. A start wrapper alone reads it from
// agentcore.EntrypointEnv.
func agentCoreEntryScript(entry []string, env map[string]string) string {
	if len(entry) == 2 && agentCoreStartsThrough(entry[0]) {
		return entry[1]
	}
	if len(entry) == 1 && agentCoreStartsThrough(entry[0]) && env[agentcore.EntrypointEnv] != "" {
		return env[agentcore.EntrypointEnv]
	}
	if len(entry) > 0 {
		return entry[0]
	}
	return ""
}

// detectAgentCoreZIP reads a direct-code ZIP's member names and a few small members. Dependencies sit at the ZIP
// root (pip install --target, npm install), so markers are matched there only.
func detectAgentCoreZIP(code agentCoreCode, language, version string, entry []string, env map[string]string) (agentCoreArtifactFacts, error) {
	f := agentCoreArtifactFacts{Language: language, RuntimeVersion: version}
	r, err := zip.NewReader(code, code.Size())
	if err != nil {
		return f, fmt.Errorf("%w: AgentCore code is not a readable ZIP: %v", ErrUnsupportedInstrumentation, err)
	}
	script := agentCoreEntryScript(entry, env)
	scriptDir := path.Dir(script)
	members := map[string]bool{}
	wrappers := map[string][]byte{}
	dists := map[string]bool{}
	var manifest []byte
	for _, zf := range r.File {
		n := strings.TrimPrefix(strings.TrimSuffix(zf.Name, "/"), "./")
		members[n] = true
		if first, _, _ := strings.Cut(n, "/"); strings.HasSuffix(first, ".dist-info") {
			if name, ok := agentCoreDistInfo(first); ok {
				dists[name] = true
			}
		}
		switch {
		case n == agentCoreZIPManifest:
			if manifest, err = agentCoreZIPFile(zf); err != nil {
				return f, err
			}
		case n == agentCorePythonStart || n == agentCoreNodePackageStart || n == agentCoreNodeBundleStart:
			if wrappers[n], err = agentCoreZIPFile(zf); err != nil {
				return f, err
			}
		case language == "nodejs" && n == agentCoreNodePackage+"package.json":
			f.OdigosPackage = &agentCoreOdigosPackage{Distros: agentCoreDistroNames("nodejs")}
		case language == "nodejs" && n == agentCoreNodeADOT+"package.json":
			f.ADOT = &agentCoreSDK{}
		case language == "nodejs" && n == agentCoreNodeUpstream+"package.json":
			f.OtherSDK = true
		case language == "python" && (n == "sitecustomize.py" || n == path.Join(scriptDir, "sitecustomize.py")):
			f.Sitecustomize = true
		case language == "nodejs" && path.Base(n) == "package.json" && !strings.Contains(n, "node_modules/"):
			// A package scope at or above the entry script controls .js module interpretation.
			dir := path.Dir(n)
			if dir == "." || scriptDir == dir || strings.HasPrefix(scriptDir, dir+"/") {
				data, err := agentCoreZIPFile(zf)
				if err != nil {
					return f, err
				}
				if agentCoreModulePackage(data) && !strings.HasSuffix(script, ".cjs") {
					f.ESM = true
				}
			}
		}
	}
	if language == "nodejs" && strings.HasSuffix(script, ".mjs") {
		f.ESM = true
	}
	if language == "python" {
		if dists[agentCorePythonPackageDist] {
			f.OdigosPackage = &agentCoreOdigosPackage{Distros: agentCorePythonDistros(dists)}
		}
		if dists[agentCorePythonADOTDist] {
			f.ADOT = &agentCoreSDK{}
		}
		f.OtherSDK = dists[agentCorePythonUpstreamDist]
	} else {
		f.LauncherInEnv = strings.Contains(env["NODE_OPTIONS"], agentCoreNodeADOTRegister)
		f.UpstreamPreload = strings.Contains(env["NODE_OPTIONS"], agentCoreNodeUpstreamRegister)
	}
	f.LauncherPresent = f.LauncherInEnv || len(entry) == 2 && path.Base(entry[0]) == agentCoreLauncher
	if manifest != nil {
		if f.BundlePackage, err = agentCoreBundleManifest(manifest, language); err != nil {
			return f, err
		}
		f.PreparedBundle = true
	}
	switch {
	case language == "python" && members[agentCorePythonStart]:
		f.StartWrapper = agentCorePythonStart
	case language == "nodejs" && f.OdigosPackage != nil && members[agentCoreNodePackageStart]:
		f.StartWrapper = agentCoreNodePackageStart
	case language == "nodejs" && f.PreparedBundle && members[agentCoreNodeBundleStart]:
		f.StartWrapper = agentCoreNodeBundleStart
	}
	f.StartWrapperReadsEnv = f.StartWrapper != "" && bytes.Contains(wrappers[f.StartWrapper], []byte(agentcore.EntrypointEnv))
	return f, nil
}

// detectAgentCoreImage reads an image's flattened filesystem once. The language comes from the source override,
// then from the image's env and command, and only then from which SDK markers it carries.
func detectAgentCoreImage(ctx context.Context, image v1.Image, cfg *v1.ConfigFile, language, version string, liveEnv map[string]string) (agentCoreArtifactFacts, error) {
	imageEnv := envSliceToMap(cfg.Config.Env)
	f := agentCoreArtifactFacts{Image: true, ImageEnv: imageEnv, WorkingDir: path.Clean("/" + cfg.Config.WorkingDir)}
	wdFile := func(name string) string { return strings.TrimPrefix(path.Join(f.WorkingDir, name), "/") }
	small := map[string]bool{wdFile("package.json"): true,
		wdFile("sitecustomize.py"): true, agentCoreImageManifest: true}
	nodeMarkers := []string{agentCoreNodePackage, agentCoreNodeADOT, agentCoreNodeUpstream}
	sites := map[string]map[string]bool{} // site-packages directory -> distributions installed there
	files, err := agentCoreImageScan(ctx, image, func(name string) bool {
		if small[name] {
			return true
		}
		if dir, dist, ok := agentCoreSitePackagesDist(name); ok {
			if sites[dir] == nil {
				sites[dir] = map[string]bool{}
			}
			sites[dir][dist] = true
			return false
		}
		for _, marker := range nodeMarkers {
			if name == marker+"package.json" || strings.HasSuffix(name, "/"+marker+"package.json") {
				return true
			}
		}
		return false
	})
	if err != nil {
		return f, err
	}
	nodeDirs := map[string]agentCoreSDK{}
	for _, marker := range nodeMarkers {
		if found, ok := agentCoreNodeInstall(files, marker, f.WorkingDir); ok {
			nodeDirs[marker] = found
		}
	}

	if language == "" {
		language = normalizeLanguage(guessStrongFromSignals(signalsFromImageConfig(&ImageConfig{Entrypoint: cfg.Config.Entrypoint, Cmd: cfg.Config.Cmd, Env: cfg.Config.Env, Labels: cfg.Config.Labels})))
	}
	if language == "" {
		// Images without the official base images' version variables still say what they run through the SDK
		// they carry.
		for _, dists := range sites {
			pkg, adot := dists[agentCorePythonPackageDist], dists[agentCorePythonADOTDist]
			if pkg || adot {
				language = "python"
			}
		}
		_, pkg := nodeDirs[agentCoreNodePackage]
		_, adot := nodeDirs[agentCoreNodeADOT]
		if language == "" && (pkg || adot) {
			language = "nodejs"
		}
	}
	if language != "python" && language != "nodejs" {
		return f, fmt.Errorf("%w: cannot identify a Python or Node image; set the source runtime override", ErrUnsupportedRuntime)
	}
	detectedVersion := runtimeVersionFromEnv(language, imageEnv)
	if version == "" {
		version = detectedVersion
	} else if detectedVersion != "" && !agentCoreVersionMatches(language, version, detectedVersion) {
		return f, fmt.Errorf("%w: runtime version override disagrees with container metadata", ErrUnsupportedRuntime)
	}
	f.Language, f.RuntimeVersion = language, version

	env := maps.Clone(imageEnv)
	maps.Copy(env, liveEnv)
	args := append(append([]string(nil), cfg.Config.Entrypoint...), cfg.Config.Cmd...)
	if language == "python" {
		_, f.Sitecustomize = files[wdFile("sitecustomize.py")]
		dirs := make([]string, 0, len(sites))
		for dir := range sites {
			dirs = append(dirs, dir)
		}
		sort.Strings(dirs)
		for _, dir := range dirs {
			dists := sites[dir]
			if dists[agentCorePythonPackageDist] && f.OdigosPackage == nil {
				f.OdigosPackage = &agentCoreOdigosPackage{Distros: agentCorePythonDistros(dists), Dir: dir}
			}
			if dists[agentCorePythonADOTDist] && f.ADOT == nil {
				f.ADOT = &agentCoreSDK{Dir: dir}
			}
			if dists[agentCorePythonUpstreamDist] {
				f.OtherSDK = true
			}
		}
		for _, arg := range args {
			if strings.Contains(arg, agentCoreLauncher) {
				f.LauncherPresent = true
			}
		}
	} else {
		if found, ok := nodeDirs[agentCoreNodePackage]; ok {
			f.OdigosPackage = &agentCoreOdigosPackage{Distros: agentCoreDistroNames("nodejs"), Dir: found.Dir}
		}
		if found, ok := nodeDirs[agentCoreNodeADOT]; ok {
			f.ADOT = &found
		}
		_, f.OtherSDK = nodeDirs[agentCoreNodeUpstream]
		if data := files[wdFile("package.json")]; agentCoreModulePackage(data) {
			f.ESM = true
		}
		for _, arg := range args {
			if strings.HasSuffix(arg, ".mjs") {
				f.ESM = true
			}
			if strings.Contains(arg, agentCoreNodeADOTRegister) {
				f.LauncherPresent = true
			}
			if strings.Contains(arg, agentCoreNodeUpstreamRegister) {
				f.UpstreamPreload = true
			}
		}
		if strings.Contains(env["NODE_OPTIONS"], agentCoreNodeADOTRegister) {
			f.LauncherPresent, f.LauncherInEnv = true, true
		}
		if strings.Contains(env["NODE_OPTIONS"], agentCoreNodeUpstreamRegister) {
			f.UpstreamPreload = true
		}
	}
	if manifest, ok := files[agentCoreImageManifest]; ok {
		if f.BundlePackage, err = agentCoreBundleManifest(manifest, language); err != nil {
			return f, err
		}
		f.PreparedBundle = true
	}
	return f, nil
}

func agentCoreZIPFile(f *zip.File) ([]byte, error) {
	reader, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, agentCoreMetadataLimit+1))
	if err != nil {
		return nil, err
	}
	if len(data) > agentCoreMetadataLimit {
		return nil, fmt.Errorf("%w: ZIP metadata file %s is too large", ErrUnsupportedInstrumentation, f.Name)
	}
	return data, nil
}

// agentCoreDistInfo splits a wheel's dist-info directory name. Wheels escape the distribution name with
// underscores, so the first hyphen separates it from the version.
func agentCoreDistInfo(dir string) (name string, ok bool) {
	name, _, ok = strings.Cut(strings.TrimSuffix(dir, ".dist-info"), "-")
	if !ok || name == "" {
		return "", false
	}
	return strings.ReplaceAll(strings.ToLower(name), ".", "_"), true
}

// agentCoreSitePackagesDist recognizes a member of an installed distribution's dist-info in a Python
// environment and names the environment's site-packages directory.
func agentCoreSitePackagesDist(name string) (dir, dist string, ok bool) {
	parts := strings.Split(name, "/")
	for i := 1; i < len(parts)-1; i++ {
		if !strings.HasSuffix(parts[i], ".dist-info") || (parts[i-1] != "site-packages" && parts[i-1] != "dist-packages") {
			continue
		}
		if dist, ok = agentCoreDistInfo(parts[i]); ok {
			return "/" + strings.Join(parts[:i], "/"), dist, true
		}
	}
	return "", "", false
}

// agentCoreNodeInstall picks the install of one npm package to use: the application's own (in its working
// directory) when present, otherwise the shallowest, which is the one Node resolves first from the app root.
func agentCoreNodeInstall(files map[string][]byte, marker, workingDir string) (agentCoreSDK, bool) {
	var best agentCoreSDK
	found := false
	for name := range files {
		if name != marker+"package.json" && !strings.HasSuffix(name, "/"+marker+"package.json") {
			continue
		}
		candidate := agentCoreSDK{Dir: path.Clean("/" + strings.TrimSuffix(name, marker+"package.json"))}
		if !found || agentCoreNodeInstallBefore(candidate.Dir, best.Dir, workingDir) {
			best, found = candidate, true
		}
	}
	return best, found
}

func agentCoreNodeInstallBefore(a, b, workingDir string) bool {
	if (a == workingDir) != (b == workingDir) {
		return a == workingDir
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}

// agentCoreModulePackage reports a package.json that makes .js files native ES modules. An unreadable one is
// not treated as ESM: Node itself would fail on it, which is the customer's deployment error to see.
func agentCoreModulePackage(data []byte) bool {
	var pkg struct {
		Type string `json:"type"`
	}
	return len(data) > 0 && json.Unmarshal(data, &pkg) == nil && pkg.Type == "module"
}

// agentCorePythonDistros maps the SDK distributions installed beside the Odigos package (its extras) to the
// distros it can start.
func agentCorePythonDistros(dists map[string]bool) []string {
	var out []string
	if dists[agentCorePythonADOTDist] {
		out = append(out, "python-agentcore-adot")
	}
	if dists[agentCorePythonOpenLLMetryDist] {
		out = append(out, "python-agentcore-openllmetry")
	}
	return out
}

func agentCoreDistroNames(language string) []string {
	var out []string
	for _, d := range AgentCoreDistros(language) {
		out = append(out, d.Name)
	}
	return out
}

func agentCoreBundleManifest(data []byte, language string) (agentcore.Package, error) {
	var manifest struct {
		Package agentcore.Package `json:"package"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil || manifest.Package.Version != 1 || manifest.Package.Language != language {
		return agentcore.Package{}, fmt.Errorf("%w: the artifact's Odigos bundle manifest is not a %s bundle", ErrUnsupportedInstrumentation, language)
	}
	return manifest.Package, nil
}
