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
replace github.com/keyval-dev/odigos/odiglet => github.com/<YOUR-USERNAME>/odigos/odiglet <COMMIT-HASH>
```

3. Run `TAG=<CURRENT-ODIGOS-VERSION> make deploy` to build and upload the images to kind. The TAG should match the current version of odigos you are using. e.g. what you get back from `odigos version`. 

When you run `odigos install` it applied a manifest file for the odiglet daemonset. the docker image built from code should use the same image tag so it will be pulled by k8s and used in your cluster.
