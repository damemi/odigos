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

## eBPF SDKs

When testing change made to one of our eBPF SDKs, the following steps could be made:

1.  Update the directive in `./doiglet/go.mod` to point to any git branch and commit hash you want to run and test.

for example, you can point to your own branch of `ebpf-java-instrumentation`, under the `require` section
```
require (
    ...
	github.com/odigos-io/ebpf-java-instrumentation <your branch name>
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
* `github.com/odigos-io/ebpf-java-instrumentation`
* `github.com/odigos-io/ebpf-java-instrumentation/otel_agent_extension`
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
  ./odigosauth
  ./onprem-token-generator
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
