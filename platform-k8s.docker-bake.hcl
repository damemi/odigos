# Bake targets for the Odigos Kubernetes platform images built on Depot. Run from the repo root:
#
#   depot bake -f platform-k8s.docker-bake.hcl                    # all images, linux/amd64
#   depot bake -f platform-k8s.docker-bake.hcl collector          # one image
#   PLATFORMS=linux/amd64,linux/arm64 depot bake -f platform-k8s.docker-bake.hcl
#
# BUILDKIT_CACHE_MOUNT_NS isolates the Dockerfiles' --mount=type=cache volumes per
# target on Depot's shared builders.

variable "ORG" {
  default = "registry.odigos.io"
}

variable "TAG" {
  default = "dev"
}

# The version the operator packages its embedded Helm chart with; must be valid semver.
variable "ODIGOS_VERSION" {
  default = "v0.0.0-dev"
}

# Comma-separated, e.g. linux/amd64,linux/arm64.
variable "PLATFORMS" {
  default = "linux/amd64"
}

group "default" {
  targets = ["instrumentor", "insights", "collector", "operator"]
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
  dockerfile = "collector/enterprise/Dockerfile"
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
