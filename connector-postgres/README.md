# connector-postgres

PostgreSQL image for the Odigos cloud-connectors feature, bundling the [goose](https://github.com/pressly/goose)
migration engine and the ordered connector migrations. PostgreSQL is the single durable store for
connector state (schema-per-provider); see the cloud-connectors design.

## One image, two roles

1. **Database server** — run with the default `postgres` entrypoint (the chart's PostgreSQL
   Deployment). Based on `postgres:16`.
2. **Migration runner** — run `run-migrations.sh`, which waits for the database and applies the
   bundled migrations with `goose ... up` (the chart's Helm post-install/post-upgrade Job).

Connector runtimes **never** migrate: each verifies `connector_platform.schema_versions` at startup
and fails fast on mismatch.

## Layout

```
Dockerfile                                  # postgres:16 + goose + /migrations + run-migrations.sh
scripts/run-migrations.sh                   # wait-for-db then `goose ... up`
migrations/
  0001_connector_platform.sql               # connector_platform schema + version marker
  0002_connector_aws_intent.sql             # sources + destinations (instrumentation intent)
  0003_connector_aws_workload_inventory.sql # lambda_functions + ecs_tasks (workload domain)
  0004_connector_aws_service_inventory.sql  # ecs_clusters (service domain)
  0005_connector_aws_discovery_state.sql    # discovery cursors + connector_aws version marker
  0006_connector_platform_drain.sql         # runtime_heartbeat drain columns
```

Each migration is a single file with a `-- +goose Up` section (applied on `goose up`) and a
`-- +goose Down` section (applied on `goose down` / `down-to`). goose tracks applied versions in its own
`goose_db_version` table — separate from the app-level `connector_platform.schema_versions` markers the
runtimes verify.

## Adding a migration

1. Add `NNNN_name.sql` under `migrations/` (versioned, ordered) with both a `-- +goose Up` and a
   `-- +goose Down` section. The Down section must revert the Up (drop objects in reverse creation order;
   roll back any `schema_versions` change). If it finalizes a schema, insert the matching
   `connector_platform.schema_versions` row and bump `ExpectedSchemaVersions` in
   `connectors/postgres/schema.go`.
2. Validate locally: `make validate-db-migrations` (checks the up/down annotations and version ordering).
   CI (`.github/workflows/connector-postgres-migrations.yml`) validates and runs a full
   up → down → up round trip against a real Postgres, failing the PR on a broken revert.

Greenfield migrations should be **CREATE-only** DDL. Avoid ALTER / backfill migrations in the same PR
that introduces a schema — squash the final table shape into the initial migration instead.

## Build & test locally

```bash
make build-connector-postgres TAG=dev            # from the repo root
docker run -d --rm --name pg -e POSTGRES_PASSWORD=odigos -e POSTGRES_DB=odigos_central \
  -p 5432:5432 registry.odigos.io/odigos-enterprise-connector-postgres:dev
docker run --rm -e DATABASE_URL="postgres://postgres:odigos@host.docker.internal:5432/odigos_central?sslmode=disable" \
  registry.odigos.io/odigos-enterprise-connector-postgres:dev run-migrations.sh
```

Re-running the migration runner is idempotent (goose tracks applied revisions in `goose_db_version`).
To roll back, run the migration binary with a down command against the same `DATABASE_URL`, e.g.
`goose -dir /migrations postgres "$DATABASE_URL" down` (one step) or `down-to 0` (revert everything).
