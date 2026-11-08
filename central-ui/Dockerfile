# 1️⃣ Build Stage: Compile Next.js App
FROM node:24.22.2-alpine AS builder

# Set working directory inside the container
WORKDIR /app

# Copy package.json and yarn.lock for dependency installation
COPY package.json yarn.lock .npmrc ./
COPY .yalc* ./.yalc/

# This allows us to deploy the UI to local clusters with the yalc-linked version of the UI kit.
# The optional npmrc secret holds the GitHub Packages token for the @odigos-io scope (never copied into a layer).
RUN --mount=type=secret,id=npmrc,target=/root/.npmrc,required=false \
    if [ -d .yalc ] && [ "$(ls -A .yalc 2>/dev/null)" ]; then \
      echo "Using yalc"; \
      npm install -g yalc; \
      yarn install; \
    else \
      echo "Using frozen lockfile"; \
      yarn install --frozen-lockfile; \
    fi

# Copy application source code
COPY . .

# Build the Next.js app
RUN yarn build
ARG RHEL=false
RUN if [ "$RHEL" = "true" ] ; then \
        mkdir -p /app/licenses && \
        npx license-checker --json > ./licenses/licenses.json && \
        echo "=== License file preview ===" && \
        head -n 20 ./licenses/licenses.json && \
        echo "=== End of license preview ==="; \
    fi

######## RHEL Image ########
FROM registry.access.redhat.com/ubi9/ubi-micro:latest AS rhel

ARG VERSION
ARG RELEASE
ARG SUMMARY
ARG DESCRIPTION

LABEL name="Central UI"
LABEL vendor="Odigos"
LABEL maintainer="Odigos"
LABEL version=$VERSION
LABEL release=$RELEASE
LABEL summary=$SUMMARY
LABEL description=$DESCRIPTION

WORKDIR /app

COPY --from=builder /app/package.json /app/yarn.lock ./
COPY --from=builder /app/.next ./.next
COPY --from=builder /app/public ./public
COPY --from=builder /app/node_modules ./node_modules
COPY --from=builder /app/licenses /licenses

EXPOSE 3000
USER 65532:65532

CMD ["yarn", "start"]

# 2️⃣ Runtime Stage: Serve with Next.js Built-in Server
FROM node:24.22.2-alpine

# Upgrade all Alpine packages to pick up security fixes (e.g. zlib CVE-2026-27171).
RUN apk upgrade --no-cache

# Remove bundled npm to eliminate CVEs in its dependencies.
# We only use yarn for "yarn start" in this image.
RUN rm -rf /usr/local/lib/node_modules/npm

# Set working directory
WORKDIR /app

# Copy only the built Next.js app
COPY --from=builder /app/package.json /app/yarn.lock ./
COPY --from=builder /app/.next ./.next
COPY --from=builder /app/public ./public
COPY --from=builder /app/node_modules ./node_modules

# Expose the frontend port
EXPOSE 3000

# Start Next.js in production mode
CMD ["yarn", "start"]
