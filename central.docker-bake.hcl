# Bake targets for the Odigos Central images. Run from the repo root:
#
#   depot bake -f central.docker-bake.hcl                    # all three, linux/amd64
#   depot bake -f central.docker-bake.hcl central-ui         # one image
#   PLATFORMS=linux/amd64,linux/arm64 depot bake -f central.docker-bake.hcl
#
# central-ui installs the private @odigos-io/ui-kit with NPMRC (path to an .npmrc, default
# ~/.npmrc), as in the Makefile. Bake reads the NPMRC file up front, so it must exist for any target.
#
# BUILDKIT_CACHE_MOUNT_NS isolates the Dockerfiles' --mount=type=cache volumes per
# target on Depot's shared builders.

variable "ORG" {
  default = "registry.odigos.io"
}

variable "TAG" {
  default = "dev"
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
  targets = ["central-backend", "central-proxy", "central-ui"]
}

target "_central" {
  platforms  = split(",", PLATFORMS)
  provenance = false
}

target "central-backend" {
  inherits   = ["_central"]
  context    = "."
  dockerfile = "central-backend/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "central-backend"
  }
  tags = ["${ORG}/odigos-central-backend:${TAG}"]
}

target "central-proxy" {
  inherits   = ["_central"]
  context    = "."
  dockerfile = "central-proxy/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "central-proxy"
  }
  tags = ["${ORG}/odigos-central-proxy:${TAG}"]
}

target "central-ui" {
  inherits   = ["_central"]
  context    = "central-ui"
  dockerfile = "Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "central-ui"
  }
  secret = [
    {
      type = "file"
      id   = "npmrc"
      src  = NPMRC
    }
  ]
  tags = ["${ORG}/odigos-central-ui:${TAG}"]
}
