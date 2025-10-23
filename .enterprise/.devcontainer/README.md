# Devcontainer for Odigos-enterprise
This devcontainer is used to run the enterprise extensions of Odigos.

Please make sure you run the deployment as described in the Odigos repo. Refer to [here](https://github.com/odigos/odigos/blob/main/.devcontainer/README.md) for more details.

## Prerequisites
> You have a `.env.example` file to get you started; Copy it to a `.env` file and fill in the values.
- `.env` file with the following variables:
  - `GITHUB_TOKEN`: Your GitHub token with access to `github.com/odigos-io/*` repos
  - `GOPRIVATE`: `github.com/odigos-io/*`

## Setup
Once the devcontainer is built, it will populate the in-container GOPRIVATE and GITHUB_TOKEN environment variables from the `.env` file, and in addition connect to your local `kind` cluster.

## Known tricks and time-wasters
1. If you've recently rotated your GitHub token, you need to rebuild the devcontainer to pick up the new token. You can try to export it etc, but the easiest way is to rebuild the container. (believe me, I've tried). In addition, run the deploy/build commands with `NO_CACHE=--no-cache` to avoid caching issues.
1. If you've recreated your kind cluster or that the cluster was not present during the devcontainer's startup, you need to run the script `.devcontainer/connect-kind.sh` to connect the container to your local kind cluster.

## Deployments and builds
It is critical to figure how you've deployed the Odigos classic version, as the enterprise version is an extension of it.

If you ran `make cli-install ODIGOS_CLI_VERSION=x.y.z` from the classic Odigos repo, the tag you've supplied there should be the same tag you use here.

For example, if you've created the classic Odigos deployment with `make cli-install ODIGOS_CLI_VERSION=1.2.3`, and then configured Odigos to be enterprise mode, you should run `make deploy TAG=1.2.x` in this repo such that the enterprise images will be built and introduced to the cluster.

### Deploy commands
The below commands will build and deploy the enterprise components to your cluster.
Deploy all at once:
```sh
    make deploy TAG=1.2.3
    # More granular deploy commands:
    # deploy-odiglet
    # deploy-odiglet-with-agents
    # deploy-instrumentor
    # deploy-agents
```
### Building commands
You can also build the images without deploying them to your cluster.
Build all at once:
```sh
    make build-images TAG=1.2.3
    # More granular build commands:
    # build-odiglet
    # build-odiglet-with-otel_agents
    # build-instrumentor
    # build-agents
    # build-images
    # build-images-with-agents
    # build-images-rhel
```