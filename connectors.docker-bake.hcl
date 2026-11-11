# Bake targets for the cloud connector images. Run from the repo root:
#
#   GITHUB_TOKEN=... depot bake -f connectors.docker-bake.hcl                    # all images, linux/amd64
#   GITHUB_TOKEN=... depot bake -f connectors.docker-bake.hcl connector-aws      # one image
#   PLATFORMS=linux/amd64,linux/arm64 depot bake -f connectors.docker-bake.hcl
#
# The Go connector images download the private agents-api module with the GITHUB_TOKEN environment
# variable, passed as the github_token secret, as in central.mk. connector-postgres needs no token.
#
# connector-aws stages the Lambda layers from the LAMBDA_AGENTS_IMAGE default pinned in its Dockerfile.
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

group "default" {
  targets = [
    "connector-runtime",
    "connector-opamp-gateway",
    "connector-aws",
    "connector-azure",
    "connector-gcp",
    "connector-mulesoft",
    "connector-postgres",
  ]
}

target "_connector" {
  platforms  = split(",", PLATFORMS)
  provenance = false
}

# The Go connectors build from the repo root and read the private agents-api module.
target "_go-connector" {
  inherits = ["_connector"]
  context  = "."
  secret = [
    {
      type = "env"
      id   = "github_token"
      env  = "GITHUB_TOKEN"
    }
  ]
}

target "connector-runtime" {
  inherits   = ["_go-connector"]
  dockerfile = "connector-runtime/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "connector-runtime"
  }
  tags = ["${ORG}/odigos-enterprise-connector-runtime:${TAG}"]
}

target "connector-opamp-gateway" {
  inherits   = ["_go-connector"]
  dockerfile = "connector-opamp-gateway/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "connector-opamp-gateway"
  }
  tags = ["${ORG}/odigos-enterprise-connector-opamp-gateway:${TAG}"]
}

target "connector-aws" {
  inherits   = ["_go-connector"]
  dockerfile = "connectors/providers/aws/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "connector-aws"
  }
  tags = ["${ORG}/odigos-enterprise-connector-aws:${TAG}"]
}

target "connector-azure" {
  inherits   = ["_go-connector"]
  dockerfile = "connectors/providers/azure/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "connector-azure"
  }
  tags = ["${ORG}/odigos-enterprise-connector-azure:${TAG}"]
}

target "connector-gcp" {
  inherits   = ["_go-connector"]
  dockerfile = "connectors/providers/gcp/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "connector-gcp"
  }
  tags = ["${ORG}/odigos-enterprise-connector-gcp:${TAG}"]
}

target "connector-mulesoft" {
  inherits   = ["_go-connector"]
  dockerfile = "connectors/providers/mulesoft/Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "connector-mulesoft"
  }
  tags = ["${ORG}/odigos-enterprise-connector-mulesoft:${TAG}"]
}

target "connector-postgres" {
  inherits   = ["_connector"]
  context    = "connector-postgres"
  dockerfile = "Dockerfile"
  args = {
    BUILDKIT_CACHE_MOUNT_NS = "connector-postgres"
  }
  tags = ["${ORG}/odigos-enterprise-connector-postgres:${TAG}"]
}
