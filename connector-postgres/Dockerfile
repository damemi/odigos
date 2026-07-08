# connector-postgres bundles PostgreSQL with the goose migration engine and the ordered connector
# migrations. One image, two roles:
#   1. The PostgreSQL server   — run with the default `postgres` entrypoint (the chart's DB Deployment).
#   2. The migration runner    — run `run-migrations.sh`, which waits for the DB and applies migrations
#                                via `goose ... up` (the chart's Helm-hook migration Job).
# Connectors never migrate: each runtime verifies connector_platform.schema_versions at startup and
# fails fast on mismatch.
FROM golang:1.25 AS goose
# Static goose binary (no cgo) so it drops cleanly into the postgres:16 (Debian) image below.
# Pinned for reproducible builds; goose v3.27.x requires Go >= 1.25.7.
RUN CGO_ENABLED=0 go install github.com/pressly/goose/v3/cmd/goose@v3.27.2

FROM postgres:16

# goose CLI for versioned migrations (single-file up/down, multi-schema).
COPY --from=goose /go/bin/goose /usr/local/bin/goose

# Ordered migration set spanning connector_platform + connector_aws (and future provider schemas).
COPY migrations /migrations
COPY scripts/run-migrations.sh /usr/local/bin/run-migrations.sh
RUN chmod +x /usr/local/bin/run-migrations.sh

# Default behavior is the stock postgres server. The migration Job overrides the command with
# run-migrations.sh.
