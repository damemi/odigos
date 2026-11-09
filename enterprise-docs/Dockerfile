FROM node:22-bookworm-slim AS docs
RUN npm install --global pnpm@11.19.0
WORKDIR /src/docs
COPY enterprise-docs/package.json enterprise-docs/pnpm-lock.yaml enterprise-docs/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY enterprise-docs/ ./
RUN pnpm check && pnpm build

FROM golang:1.26.8 AS server
WORKDIR /src
# enterprise-docs/server builds odigosauth's docs-server, which it replaces (with common) by local path.
COPY common/ common/
COPY odigosauth/ odigosauth/
COPY enterprise-docs/server/go.mod enterprise-docs/server/go.sum enterprise-docs/server/
WORKDIR /src/enterprise-docs/server
RUN go mod download
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /docs-server github.com/odigos-io/odigos/odigosauth/cmd/docs-server

FROM gcr.io/distroless/static:nonroot
# Lambda assigns its own user, which cannot enter distroless /home/nonroot.
WORKDIR /
COPY --from=public.ecr.aws/awsguru/aws-lambda-adapter:1.0.1 /lambda-adapter /opt/extensions/lambda-adapter
COPY --from=server /docs-server /docs-server
COPY --from=docs /src/docs/build /content
ENV DOCS_CONTENT_DIR=/content \
    LISTEN_ADDR=:8080 \
    AWS_LWA_PORT=8080 \
    AWS_LWA_READINESS_CHECK_PATH=/healthz \
    AWS_LWA_READINESS_CHECK_HEALTHY_STATUS=200 \
    AWS_LWA_INVOKE_MODE=response_stream
EXPOSE 8080
ENTRYPOINT ["/docs-server"]
