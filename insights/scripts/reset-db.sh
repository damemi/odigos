#!/usr/bin/env bash
#
# Reset the Odigos Insights service and its bundled Postgres database from scratch.
#
# It drops every table in the insights database, re-applies the schema
# migrations straight from source (internal/db/postgres/migrate/migrations),
# records the applied version so the Helm migrate Job stays a no-op, and rolls
# the service. Nothing survives: baselines, observations, anomalies,
# transactions and the seeded policies are all recreated fresh.
#
# Usage:
#   insights/scripts/reset-db.sh
#
# Overridable via env:
#   NS               k8s namespace                (default: odigos-system)
#   INSIGHTS_DEPLOY  insights deployment name     (default: odigos-insights)
#   PG_DEPLOY        postgres deployment name     (default: odigos-insights-db)
#   DB               postgres database/user       (default: odigos_insights)
set -euo pipefail

NS="${NS:-odigos-system}"
INSIGHTS_DEPLOY="${INSIGHTS_DEPLOY:-odigos-insights}"
PG_DEPLOY="${PG_DEPLOY:-odigos-insights-db}"
DB="${DB:-odigos_insights}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MIGRATIONS_DIR="$SCRIPT_DIR/../internal/db/postgres/migrate/migrations"

# Run whatever SQL arrives on stdin inside the Postgres pod as the database
# owner. psql connects over the local socket, so no password is needed.
pg() {
	kubectl exec -i -n "$NS" "deploy/$PG_DEPLOY" -- \
		psql -v ON_ERROR_STOP=1 -q -U "$DB" -d "$DB" "$@"
}

echo ">> [1/5] Scaling $INSIGHTS_DEPLOY to 0 so nothing writes during the reset"
was="$(kubectl get deploy "$INSIGHTS_DEPLOY" -n "$NS" -o jsonpath='{.spec.replicas}' 2>/dev/null || echo 1)"
{ [ -z "$was" ] || [ "$was" -lt 1 ] 2>/dev/null; } && was=1
kubectl scale deploy "$INSIGHTS_DEPLOY" -n "$NS" --replicas=0 >/dev/null
kubectl rollout status deploy/"$INSIGHTS_DEPLOY" -n "$NS" --timeout=90s >/dev/null

echo ">> [2/5] Dropping every table in '$DB'"
pg <<'SQL'
DO $$
DECLARE t text;
BEGIN
	FOR t IN SELECT tablename FROM pg_tables WHERE schemaname = current_schema() LOOP
		RAISE NOTICE 'drop %', t;
		EXECUTE format('DROP TABLE IF EXISTS %I CASCADE', t);
	END LOOP;
END $$;
SQL

echo ">> [3/5] Applying migrations from source"
for f in "$MIGRATIONS_DIR"/*.up.sql; do
	echo "   - apply $(basename "$f")"
	pg -1 <"$f"
done

echo ">> [4/5] Recording applied version so the Helm migrate Job stays a no-op"
latest="$(for f in "$MIGRATIONS_DIR"/*.up.sql; do basename "$f"; done | sed -E 's/^0*([0-9]+)_.*/\1/' | sort -n | tail -1)"
pg <<SQL
CREATE TABLE IF NOT EXISTS schema_migrations (version bigint NOT NULL PRIMARY KEY, dirty boolean NOT NULL);
TRUNCATE schema_migrations;
INSERT INTO schema_migrations (version, dirty) VALUES ($latest, false);
SQL
echo "   - marked version $latest, dirty false"

echo ">> [5/5] Restoring $INSIGHTS_DEPLOY to $was replica(s)"
kubectl scale deploy "$INSIGHTS_DEPLOY" -n "$NS" --replicas="$was" >/dev/null
kubectl rollout status deploy/"$INSIGHTS_DEPLOY" -n "$NS" --timeout=120s

echo ">> Fresh table row counts:"
pg -c "SELECT relname AS table, n_live_tup AS rows FROM pg_stat_user_tables ORDER BY relname"

echo ">> Done. Insights + database reset from scratch."
