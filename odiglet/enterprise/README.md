# Odiglet Enterprise

## Setup

Enterprise odiglet uses a private repo from github: `"github.com/keyval-dev/enterprise-go-instrumentation"`

To make it work locally, use the following commands:

```sh
export GOPRIVATE=github.com/keyval-dev/enterprise-go-instrumentation
```

I also had to [add the following](https://go.dev/doc/faq#git_https) to my `~/.gitconfig`:

```sh
[url "ssh://git@github.com/"]
        insteadOf = https://github.com/
```

Shell shortcut (copy and paste):
```sh
git config --global url."ssh://git@github.com/".insteadOf "https://github.com/"
```

Now you can run `go mod tidy`/`go build ./cmd/enterprise` in `odiglet` directory and it should work.

### Docker Build

1. Start the SSH Agent:
Ensure that your SSH agent is running and has the necessary keys added. You can check with ssh-add -l. If your key is not listed, add it with `ssh-add ~/.ssh/id_ed25519`.

2. Build the Image with SSH Agent Forwarding:
Build your Docker image using the --ssh flag to forward the SSH agent:

The command I use is:
```sh
export TAG=v1.0.0
DOCKER_BUILDKIT=1 docker build --ssh default -t keyval/odigos-odiglet:$TAG . -f odiglet/Dockerfile && kind load docker-image keyval/odigos-odiglet:$TAG
```
