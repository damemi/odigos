#!/usr/bin/env bash
# Migration runner for the connector Postgres schemas. Waits for the database to accept connections,
# then applies the bundled goose migrations. Run by the Helm migration Job (a post-install/post-upgrade
# hook), never by connector pods — connectors verify the schema version and fail fast on mismatch.
#
# Required env:
#   DATABASE_URL  postgres connection URL, e.g. postgres://user:pass@host:5432/db?sslmode=disable
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL is required}"
MIGRATIONS_DIR="${MIGRATIONS_DIR:-/migrations}"

echo "connector-postgres: waiting for database to become ready..."
for _ in $(seq 1 60); do
  if pg_isready -d "$DATABASE_URL" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [ "${ready:-0}" != "1" ]; then
  echo "connector-postgres: database did not become ready in time" >&2
  exit 1
fi

echo "connector-postgres: applying migrations from ${MIGRATIONS_DIR}..."
goose -dir "${MIGRATIONS_DIR}" postgres "$DATABASE_URL" up
echo "connector-postgres: migrations applied"
