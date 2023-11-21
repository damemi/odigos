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

Now you can run `go mod tidy` in `odiglet` directory and it should work.

### Docker Build

