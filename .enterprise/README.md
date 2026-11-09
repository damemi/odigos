# Odigos Enterprise

Here are components that replace or add to the Odigoes Open Source project.

## Odiglet

The enterprise odiglet uses the opensource odiglet as a library, and extends it by using a custom `main.go` function 
that adds the enterprise go auto instrumentation and instrumentation config capabilities.

To develop Odiglet enterprise, and test it from code, you will need:

1. GITHUB_TOKEN environment variables set to so you can pull private keyval repositories from github. You can create a personal access token in github website under setting -> developer settings -> personal access tokens. You will need to give it access to private repos. Set the environment variables in your shell, and store them in ~/.zshrc or ~/.bashrc so you don't have to set them every time you open a new shell.

2. Update the `replace` directive in `./doiglet/go.mod` to point to any git branch and commit hash you want to run and test.

for example, you can point to your own fork of odiglet open source, and use a branch commit hash from there:
```
replace github.com/odigos-io/odigos/odiglet => github.com/<YOUR-USERNAME>/odigos/odiglet <COMMIT-HASH>
```

3. Run `TAG=<CURRENT-ODIGOS-VERSION> make deploy` to build and upload the images to kind. The TAG should match the current version of odigos you are using. e.g. what you get back from `odigos version`. 

When you run `odigos install` it applies a manifest file for the odiglet daemonset. the docker image built from code should use the same image tag so it will be pulled by k8s and used in your cluster.

## Enterprise UI

The UI image compiles the Enterprise Go server with the OSS frontend and embeds
its static webapp. By default it clones the OSS commit pinned in `ui/go.mod`.
To use a local checkout, including local UI-kit links:

```sh
make build-ui-local TAG=dev
# Or select another checkout:
make build-ui TAG=dev ODIGOS_OSS_SRC=/path/to/odigos
```

To reuse a webapp already built with `yarn build` in the OSS checkout, skip the
Node.js build by passing the output directory as an additional build context:

```sh
make build-ui TAG=dev ODIGOS_OSS_SRC=../odigos \
  ODIGOS_OSS_WEBAPP_OUT=../odigos/frontend/webapp/out
```

You can also reuse the OSS Dockerfile's `builder` stage as a local image:

```sh
# From the OSS repository:
docker build --target builder -f frontend/Dockerfile -t odigos-ui-webapp:dev .
# From the Enterprise repository:
make build-ui TAG=dev ODIGOS_OSS_SRC=../odigos \
  ODIGOS_OSS_WEBAPP_IMAGE=odigos-ui-webapp:dev
```

The image must contain `/webapp/out`; the final OSS UI runtime image contains
only a compiled Go binary and cannot supply these assets. Use Docker's default
builder to access images in the local Docker image store. Keep the checkout and
webapp output/image from the same OSS revision. `GITHUB_TOKEN` is required for
private Enterprise Go dependencies. The same variables work with `deploy-ui`.

## eBPF SDKs

When testing change made to one of our eBPF SDKs, the following steps could be made:

1.  Update the directive in `./doiglet/go.mod` to point to any git branch and commit hash you want to run and test.

for example, you can point to your own branch of `ebpf-java-instrumentation`, under the `require` section
```
require (
    ...
	github.com/odigos-io/ebpf-java-instrumentation/v2 <your branch name>
    ...
)
```

Next, run `go mod tidy` inside the `odiglet` folder which should bring the latest commit hash from the provided branch

2. Use steps 1,3 from the previous section to build and load odiglet enterprise.

3. Once the loading is done, we need to change the default SDK being used by odiglet to the one we want to test.
Currently the default one is the OTel agent.
```
kubectl edit odigosconfigurations.odigos.io -n odigos-system -o yaml
```
Will open a vi editor, look for the `defaultSDKs` section and for the language you want to check.
For example, changing:
```   
java:
    sdkTier: community
    sdkType: native
```
to
```
java:
    sdkTier: enterprise
    sdkType: ebpf
```
Will cause a restart of the instrumented Java applications with the eBPF SDK.

# Development

## Go workspace setup

Similar to [the main OSS repo](https://github.com/odigos-io/odigos/blob/main/DEVELOPMENT.md#go-workspace-setup),
the use of different Go modules as dependencies makes Go Workspaces essential for a clean IDE environment.

The modules currently used are:

* `github.com/odigos-io/enterprise-go-instrumentation`
* `github.com/odigos-io/ebpf-java-instrumentation/v2`
* `github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension/v2`
* `github.com/odigos-io/ebpf-nodejs-instrumentation`
* `github.com/odigos-io/mysql-instrumentation`
* `github.com/odigos-io/runtime-detector`

Go Workspaces lets you refer to local copies of these repos as their dependencies. Set up a Go Workspace
by running:

```
go work init
go work use -r .
```

Then edit the `go.work` file to refer to the relative local folder for the dependency modules, for example:

```
go 1.24.4

use (
  ../enterprise-go-instrumentation
  ../ebpf-java-instrumentation
  ../ebpf-java-instrumentation/otel_agent_extension
  ../ebpf-nodejs-instrumentation
  ../mysql-instrumentation
  ../runtime-detector
  ./central-backend
  ./central-proxy
  ./distros
  ./instrumentor
  ./odiglet
  ./odiglet/licenses/github.com/hashicorp/go-version
)

replace github.com/odigos-io/odigos/common => ../odigos/common
```

This example also sets a `replace` for `github.com/odigos-io/odigos/common`. This is because this repo already defines
`replace` directives for this module, so redeclaring it in the `go.work` file overrides it.

You can usually tell if you have a dependency problem that can be resolved by Go Workspace settings if you see the following
types of errors:

```
pattern bpf_arm64_bpfel.o: no matching files found
```

```
packages.Load error: err: exit status 1: stderr: go: conflicting replacements for github.com/odigos-io/odigos/common:
	github.com/odigos-io/odigos/common@v1.0.201
	github.com/odigos-io/odigos/common@v1.0.201-0.20250625140527-d258bae8905d
use "go work edit -replace github.com/odigos-io/odigos/common=[override]" to resolve
go list
```

Or any error around Go imports or `go.mod`. Go Workspaces allow you to set up your own dev environment
however you want, so these settings may take some trial and error to get perfect.

## Updating OSS Dependencies

This repository depends on various Odigos Open Source components. There are two main ways to update these dependencies:

### Using Make Targets (Recommended for Development)

The repository includes make targets in `deps.mk` for updating dependencies:

#### Update All OSS Dependencies
```bash
# Update all OSS dependencies to a specific tag (e.g., v1.5.0)
make update-odigos-oss-mod-deps-all OSS_TAG=v1.5.0

# Update all OSS dependencies to main branch (default)
make update-odigos-oss-mod-deps-all
```

#### Update the Collector Components

The collector is built by `ocb` from `collector/builder-config.yaml`, not by a `go.mod`, so it
is re-pinned by its own target (which `update-odigos-oss-mod-deps-all` already calls):

```bash
make update-collector-oss-deps OSS_TAG=v1.5.0
```

This rewrites the pinned version of every `github.com/odigos-io/odigos/collector/...` component
in the manifest and regenerates `collector/odigosotelcol/`. Commit the manifest and the
generated distribution together — CI regenerates and fails on a diff. The target refuses to
write anything if a component moved, if a module declares a path other than the one it is
replaced under, or if OSS builds a component this manifest is missing; the error says which.

Note these are Odigos' own collector components. Upstream OpenTelemetry versions arrive
transitively through them and are bumped in
[odigos-io/odigos](https://github.com/odigos-io/odigos/blob/main/DEVELOPMENT.md#updating-opentelemetry-dependencies),
not here.

### Using CI Workflows

#### Automatic Dependency Sync (Enterprise Dependencies Update)
The `enterprise-dependencies-update.yaml` workflow automatically updates all dependencies to the latest main branch when triggered. This is typically used when OSS dependencies are updated and need to be synced to the enterprise repository.

#### Release Dependencies Update
The `create-release-pr.yaml` workflow updates all dependencies to a specific release tag when creating a release PR. This is used during the release process to ensure enterprise components use the correct OSS version.
