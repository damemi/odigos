# Custom Keycloak Image

This directory contains a custom Keycloak Docker image that extends the official Keycloak image with health check endpoints enabled.

This custom image enables health and metrics endpoints at **build time**, which is required for `--optimized` mode to work properly.

## Build & Push

Currently, this image is built and pushed manually to our GCP registry.

```bash
# Build and Push the image
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  --push \
  -t us-central1-docker.pkg.dev/odigos-cloud/components/keycloak:24.0.3 \
  .
```

## Features Enabled

- ✅ `/health/live` and `/health/ready` endpoints
- ✅ Metrics endpoints for monitoring
