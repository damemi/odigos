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