# Bake targets for the Odigos Kubernetes platform images built on Depot. Run from the repo root:
#
#   depot bake -f platform-k8s.docker-bake.hcl                    # all images, linux/amd64
#   depot bake -f platform-k8s.docker-bake.hcl collector          # one image
#   PLATFORMS=linux/amd64,linux/arm64 depot bake -f platform-k8s.docker-bake.hcl
#
# odiglet downloads private instrumentation repos with the GITHUB_TOKEN environment variable,
# passed as the github_token secret.
#
# ui installs the private @odigos-io/ui-kit with NPMRC (path to an .npmrc, default ~/.npmrc), as in
# the Makefile. Bake reads the NPMRC file up front, so it must exist for any target.
#
# BUILDKIT_CACHE_MOUNT_NS isolates the Dockerfiles' --mount=type=cache volumes per
# target on Depot's shared builders.

variable "ORG" {
  default = "registry.odigos.io"
}

variable "TAG" {
  default = "dev"
}

# The version the operator and the CLI package their embedded Helm chart with; must be valid semver.
variable "ODIGOS_VERSION" {
  default = "v0.0.0-dev"
}

# Stamped into the CLI binary. Static by default so they do not bust the layer cache.
variable "SHORT_COMMIT" {
  default = "dev"
}

variable "DATE" {
  default = "1970-01-01T00:00:00Z"
}

# Comma-separated, e.g. linux/amd64,linux/arm64.
variable "PLATFORMS" {
  default = "linux/amd64"
}

variable "HOME" {
  default = ""
}

variable "NPMRC" {
  default = "${HOME}/.npmrc"
}

group "default" {
  targets = ["instrumentor", "insights", "collector", "operator", "odiglet", "agents", "ui", "cli"]
}

target "_platform-k8s" {
  platforms  = split(",", PLATFORMS)
  provenance = false
  context    = "."
}

target "instrumentor" {
  inherits   = ["_platform-k8s"]
  dockerfile = "instrumentor/enterprise/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "instrumentor"
  }
  tags = ["${ORG}/odigos-enterprise-instrumentor:${TAG}"]
}

target "insights" {
  inherits   = ["_platform-k8s"]
  dockerfile = "insights/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "insights"
  }
  tags = ["${ORG}/odigos-enterprise-insights:${TAG}"]
}

target "collector" {
  inherits   = ["_platform-k8s"]
  dockerfile = "collector/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "collector"
  }
  tags = ["${ORG}/odigos-enterprise-collector:${TAG}"]
}

target "operator" {
  inherits   = ["_platform-k8s"]
  dockerfile = "operator/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "operator"
    ODIGOS_VERSION          = ODIGOS_VERSION
  }
  tags = ["${ORG}/odigos-operator:${TAG}"]
}

target "odiglet" {
  inherits   = ["_platform-k8s"]
  dockerfile = "odiglet/enterprise/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "odiglet"
  }
  secret = [
    {
      type = "env"
      id   = "github_token"
      env  = "GITHUB_TOKEN"
    }
  ]
  tags = ["${ORG}/odigos-enterprise-odiglet:${TAG}"]
}

target "agents" {
  inherits   = ["_platform-k8s"]
  dockerfile = "odiglet/enterprise/Dockerfile"
  target     = "agents"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "agents"
  }
  tags = ["${ORG}/odigos-enterprise-agents:${TAG}"]
}

# The -extended variants also package legacy .NET and Ruby 2.7, for odiglet.extended.
target "odiglet-extended" {
  inherits = ["odiglet"]
  args = {
    EXTENDED = "true"
  }
  tags = ["${ORG}/odigos-enterprise-odiglet:${TAG}-extended"]
}

target "agents-extended" {
  inherits = ["agents"]
  args = {
    EXTENDED = "true"
  }
  tags = ["${ORG}/odigos-enterprise-agents:${TAG}-extended"]
}

target "ui" {
  inherits   = ["_platform-k8s"]
  dockerfile = "ui/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "ui"
  }
  secret = [
    {
      type = "file"
      id   = "npmrc"
      src  = NPMRC
    }
  ]
  tags = ["${ORG}/odigos-enterprise-ui:${TAG}"]
}

target "cli" {
  inherits   = ["_platform-k8s"]
  dockerfile = "cli/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "cli"
    VERSION                 = ODIGOS_VERSION
    CHART_VERSION           = ODIGOS_VERSION
    SHORT_COMMIT            = SHORT_COMMIT
    DATE                    = DATE
  }
  tags = ["${ORG}/odigos-cli:${TAG}"]
}
