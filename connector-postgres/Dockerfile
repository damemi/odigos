# connector-postgres bundles PostgreSQL with the goose migration engine and the ordered connector
# migrations. One image, two roles:
#   1. The PostgreSQL server   — run with the default `postgres` entrypoint (the chart's DB Deployment).
#   2. The migration runner    — run `run-migrations.sh`, which waits for the DB and applies migrations
#                                via `goose ... up` (the chart's Helm-hook migration Job).
# Connectors never migrate: each runtime verifies connector_platform.schema_versions at startup and
# fails fast on mismatch.
FROM golang:1.26.8 AS goose
# Static goose binary (no cgo) so it drops cleanly into the postgres:16 (Debian) image below.
# Pinned for reproducible builds; goose v3.28.x requires Go >= 1.26.
# Built via a throwaway module (not `go install`) so transitive deps like x/crypto can be pinned.
ARG GOOSE_VERSION=v3.28.0
ARG X_CRYPTO_VERSION=v0.57.0
WORKDIR /build
RUN go mod init goose-build \
 && go get github.com/pressly/goose/v3/cmd/goose@${GOOSE_VERSION} golang.org/x/crypto@${X_CRYPTO_VERSION} \
 && CGO_ENABLED=0 go build -trimpath -o /go/bin/goose github.com/pressly/goose/v3/cmd/goose

# postgres:16 ships upstream's prebuilt gosu, built with an EOL Go that upstream won't rebuild, so its
# stdlib CVEs never clear. Rebuild it from source with a current Go and upstream's static release flags.
# Pinned to gosu master (Go code unchanged since the 1.19 tag).
FROM golang:1.26.8 AS gosu
ARG GOSU_VERSION=v0.0.0-20260606051551-40506998e34a
RUN CGO_ENABLED=0 go install -trimpath -ldflags='-d -w' github.com/tianon/gosu@${GOSU_VERSION}

FROM postgres:16

# Overwrite the base image's gosu (used by docker-entrypoint.sh to drop to the postgres user).
COPY --from=gosu /go/bin/gosu /usr/local/bin/gosu

# goose CLI for versioned migrations (single-file up/down, multi-schema).
COPY --from=goose /go/bin/goose /usr/local/bin/goose

# Ordered migration set spanning connector_platform + connector_aws (and future provider schemas).
COPY migrations /migrations
COPY scripts/run-migrations.sh /usr/local/bin/run-migrations.sh
RUN chmod +x /usr/local/bin/run-migrations.sh

# Default behavior is the stock postgres server. The migration Job overrides the command with
# run-migrations.sh.
